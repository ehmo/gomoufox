package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"

	"github.com/ehmo/gomoufox/internal/buildinfo"
)

type ExecutableCheck struct {
	Path         string `json:"path,omitempty"`
	ResolvedPath string `json:"resolved_path,omitempty"`
	PATHBinary   string `json:"path_binary,omitempty"`
	Version      string `json:"version"`
	Revision     string `json:"revision,omitempty"`
	Modified     bool   `json:"modified"`
	GoVersion    string `json:"go_version"`
	Platform     string `json:"platform"`
	UpdateCheck  string `json:"update_check"`
	Warning      string `json:"warning,omitempty"`
}

var doctorExecutable = os.Executable
var doctorBuildInfo = debug.ReadBuildInfo

func doctorExecutableCheck() ExecutableCheck {
	r := ExecutableCheck{Version: buildinfo.Version, GoVersion: runtime.Version(), Platform: runtime.GOOS + "/" + runtime.GOARCH, UpdateCheck: "not_performed"}
	r.Path, _ = doctorExecutable()
	if resolved, err := filepath.EvalSymlinks(r.Path); err == nil {
		r.ResolvedPath = resolved
	}
	if info, ok := doctorBuildInfo(); ok {
		r.GoVersion = info.GoVersion
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				r.Revision = setting.Value
			case "vcs.modified":
				r.Modified = setting.Value == "true"
			}
		}
	}
	r.PATHBinary, _ = doctorLookPath("gomoufox")
	if current, err := os.Stat(r.Path); err == nil && r.PATHBinary != "" {
		if other, err := os.Stat(r.PATHBinary); err == nil && !os.SameFile(current, other) {
			r.Warning = "PATH resolves gomoufox to a different file; updating that file does not replace this running executable"
		}
	}
	if r.Version == "dev" || r.Version == "" {
		if r.Warning != "" {
			r.Warning += "; "
		}
		r.Warning += "development build; release freshness is unknown"
	}
	return r
}
