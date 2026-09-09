package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	gomoufox "github.com/ehmo/gomoufox"
	"github.com/ehmo/gomoufox/internal/sidecar"
)

type RuntimeRecovery struct {
	Reason  string   `json:"reason"`
	Hint    string   `json:"hint"`
	Command []string `json:"command,omitempty"`
}

type InstallResult struct {
	OK            bool             `json:"ok"`
	Executable    ExecutableCheck  `json:"executable"`
	Runtime       string           `json:"runtime"`
	CacheDir      string           `json:"cache_dir"`
	FetchDisabled bool             `json:"fetch_disabled"`
	Recovery      *RuntimeRecovery `json:"recovery,omitempty"`
	DoctorCommand []string         `json:"doctor_command"`
}

func installResult(req InstallRequest, err error) InstallResult {
	runtimeName, dir := req.Runtime, req.Dir
	if runtimeName == "" {
		runtimeName = sidecar.RuntimeNodeDirect
	}
	if dir == "" {
		dir = sidecar.DefaultCacheDir()
	}
	if absolute, err := filepath.Abs(dir); err == nil {
		dir = absolute
	}
	r := InstallResult{OK: err == nil, Executable: doctorExecutableCheck(), Runtime: runtimeName, CacheDir: dir, FetchDisabled: os.Getenv("GOMOUFOX_SKIP_FETCH") != ""}
	r.DoctorCommand = runtimeCommand(r.Executable.Path, "doctor", dir, runtimeName, false)
	r.DoctorCommand = append(r.DoctorCommand, "--json")
	if err != nil {
		recovery := runtimeRecovery(installationFailureReason(err), r.Executable.Path, dir, runtimeName)
		if req.Python != "" && len(recovery.Command) != 0 {
			recovery.Command = append(recovery.Command, "--python", req.Python)
		}
		if req.Force && len(recovery.Command) != 0 && !containsArgument(recovery.Command, "--force") {
			recovery.Command = append(recovery.Command, "--force")
		}
		r.Recovery = &recovery
	}
	return r
}

func runtimeCommand(executable, command, dir, runtimeName string, force bool) []string {
	if executable == "" {
		executable = "gomoufox"
	}
	args := []string{executable, command, "--dir", dir, "--runtime", runtimeName}
	if force {
		args = append(args, "--force")
	}
	return args
}

func containsArgument(args []string, value string) bool {
	for _, arg := range args {
		if arg == value {
			return true
		}
	}
	return false
}

func doctorRecovery(report DoctorReport) []RuntimeRecovery {
	checks := []Check{report.RuntimeAssets, report.Venv, report.Python, report.CamoufoxPkg, report.Node, report.Playwright, report.CamoufoxBin}
	reason := ""
	for _, check := range checks {
		if checkSet(check) && !check.OK && check.Status != "" {
			reason = check.Status
			if reason != "unavailable" {
				break
			}
		}
	}
	if reason == "" {
		return nil
	}
	r := runtimeRecovery(reason, report.Executable.Path, report.CacheDir, report.Runtime)
	if os.Getenv("GOMOUFOX_SKIP_FETCH") != "" {
		r.Hint += " GOMOUFOX_SKIP_FETCH is set; a missing download requires enabling fetch before retrying."
	}
	if os.Getenv(sidecar.EnvCamoufoxPath) != "" {
		r.Hint += " GOMOUFOX_CAMOUFOX_PATH selects an offline source; that source must match the expected browser pin."
	}
	return []RuntimeRecovery{r}
}

func runtimeRecovery(reason, executable, dir, runtimeName string) RuntimeRecovery {
	r := RuntimeRecovery{Reason: reason}
	force, retry := false, true
	switch reason {
	case "stale_launcher":
		r.Hint = "Reinstall the launcher using this executable and cache; then rerun doctor."
	case "corrupt_asset", "invalid_manifest", "integrity_mismatch":
		r.Hint = "Replace the affected cache assets from verified pinned sources, then rerun doctor. Do not edit checksums or enable a trust override to repair corruption."
		force = true
	case "version_mismatch":
		r.Hint = "Install the versions pinned by this executable into the selected cache. Updating another binary on PATH does not update this running process."
		force = true
	case "missing_manifest", "missing_asset", "missing_environment", "incomplete_install", "unavailable":
		r.Hint = "Complete installation for this executable, cache and runtime, then rerun doctor."
	case "fetch_disabled":
		r.Hint = "Required runtime assets are unavailable while fetch is disabled. Enable fetch before retrying, or provide the verified pinned offline assets."
	case "download_failed":
		r.Hint = "A pinned runtime download failed. Check network access and upstream asset availability before retrying the same installation; do not substitute an unverified version."
	case "missing_python":
		r.Hint = "The Python runtime needs Python 3.9 or newer. Install it or select its executable with --python, then retry."
	case "dependency_install_failed":
		r.Hint = "The locked Python dependency installation failed. Check the reported package-index or interpreter error before retrying; keep the hash-locked requirements."
	case "permission_denied":
		r.Hint, retry = "The selected cache cannot be accessed. Check its ownership and permissions, or select a writable cache with --dir.", false
	case "unsupported_platform", "unsupported_runtime":
		r.Hint, retry = "This executable has no supported pinned runtime for the selection. Use a supported platform and runtime; reinstalling this cache cannot add upstream platform support.", false
	case "timeout", "canceled":
		r.Hint, retry = "Installation stopped before completion. Inspect the selected cache with the doctor command before retrying.", false
	default:
		r.Hint, retry = "Inspect the selected cache with the doctor command and resolve the reported installation error before retrying.", false
	}
	if retry {
		r.Command = runtimeCommand(executable, "install", dir, runtimeName, force)
	}
	return r
}

func installationFailureReason(err error) string {
	message := strings.ToLower(err.Error())
	switch {
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, gomoufox.ErrTimeout):
		return "timeout"
	case errors.Is(err, os.ErrPermission), strings.Contains(message, "permission denied"):
		return "permission_denied"
	case strings.Contains(message, "fetch is disabled"):
		return "fetch_disabled"
	case strings.Contains(message, "unsupported for"), strings.Contains(message, "no pinned playwright node.js asset for"):
		return "unsupported_platform"
	case strings.Contains(message, "python 3.9+ not found"):
		return "missing_python"
	case strings.Contains(message, "checksum"), strings.Contains(message, "failed manifest verification"):
		return "integrity_mismatch"
	case strings.Contains(message, "download"):
		return "download_failed"
	case strings.Contains(message, "pip install"), strings.Contains(message, "upgrade pip"):
		return "dependency_install_failed"
	case errors.Is(err, gomoufox.ErrVersionMismatch):
		return "version_mismatch"
	case errors.Is(err, gomoufox.ErrNotInstalled):
		return "unavailable"
	default:
		return "installation_failed"
	}
}

// POSIX single quotes keep paths and shell metacharacters literal. JSON responses
// expose argv directly for clients on every platform.
func displayCommand(args []string) string {
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = "'" + strings.ReplaceAll(arg, "'", "'\"'\"'") + "'"
	}
	return strings.Join(quoted, " ")
}
