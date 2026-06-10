package sidecar

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

var errGoLaunchPlanUnsupported = errors.New("go node-direct launch plan unsupported")

var cachePrefs = map[string]any{
	"browser.cache.disk.enable":                true,
	"browser.cache.disk.smart_size.enabled":    true,
	"browser.cache.memory.enable":              true,
	"browser.sessionhistory.max_entries":       10,
	"browser.sessionhistory.max_total_viewers": 4,
}

func buildNodeDirectSpecGo(cfg Config) (nodeDirectSpec, error) {
	if cfg.Persistent {
		return nodeDirectSpec{}, fmt.Errorf("%w: persistent contexts still require Camoufox launch_options", errGoLaunchPlanUnsupported)
	}
	if cfg.GeoIP || cfg.Humanize != nil || len(cfg.Locale) > 0 {
		return nodeDirectSpec{}, fmt.Errorf("%w: dynamic locale/geo/humanize options still require Camoufox launch_options", errGoLaunchPlanUnsupported)
	}
	root, _, err := ResolveRuntimeAssets(cfg.VenvDir)
	if err != nil {
		return nodeDirectSpec{}, err
	}
	nodejs := root.NodeJS
	launchScript := root.LaunchServerJS
	cwd := root.PlaywrightPackageDir
	executablePath, err := installedRuntimeBrowserExecutable(root)
	if err != nil {
		return nodeDirectSpec{}, fmt.Errorf("%w: locate runtime Camoufox browser executable: %v", ErrNotInstalled, err)
	}
	config := cloneStringAnyMap(cfg.Fingerprint)
	if len(config) == 0 {
		rng := rand.New(rand.NewSource(time.Now().UnixNano()))
		config, err = generatePersonaConfig(cfg, rng)
		if err != nil {
			return nodeDirectSpec{}, err
		}
		if !cfg.BlockWebGL {
			webgl, webgl2Enabled, err := sampleWebGLConfig(cfg, rng)
			if err != nil {
				return nodeDirectSpec{}, err
			}
			for key, value := range webgl {
				if _, exists := config[key]; !exists {
					config[key] = value
				}
			}
			if cfg.FirefoxPrefs == nil {
				cfg.FirefoxPrefs = map[string]any{}
			}
			if _, exists := cfg.FirefoxPrefs["webgl.enable-webgl2"]; !exists {
				cfg.FirefoxPrefs["webgl.enable-webgl2"] = webgl2Enabled
			}
			if _, exists := cfg.FirefoxPrefs["webgl.force-enabled"]; !exists {
				cfg.FirefoxPrefs["webgl.force-enabled"] = true
			}
		}
	}
	addons := append([]string(nil), cfg.Addons...)
	if ubo := defaultUBOAddonPath(executablePath); ubo != "" {
		addons = append([]string{ubo}, addons...)
	}
	if len(addons) > 0 {
		config["addons"] = addons
	}
	if cfg.MainWorldEval {
		config["allowMainWorld"] = true
	}
	if len(cfg.Fonts) > 0 {
		config["fonts"] = append([]string(nil), cfg.Fonts...)
	}
	if cfg.CustomFontsOnly {
		config["fonts:all"] = false
	}
	env, err := nodeDirectGoEnv(config, cfg.ExtraEnv)
	if err != nil {
		return nodeDirectSpec{}, err
	}
	prefs := cloneStringAnyMap(cfg.FirefoxPrefs)
	if cfg.BlockWebGL {
		prefs["webgl.disabled"] = true
	}
	if cfg.BlockImages {
		prefs["permissions.default.image"] = 2
	}
	if cfg.BlockWebRTC {
		prefs["media.peerconnection.enabled"] = false
	}
	if cfg.DisableCOOP {
		prefs["browser.tabs.remote.useCrossOriginOpenerPolicy"] = false
	}
	if cfg.EnableCache {
		for key, value := range cachePrefs {
			prefs[key] = value
		}
	}
	browserArgs := append([]string{}, cfg.BrowserArgs...)
	payload := map[string]any{
		"executablePath":   executablePath,
		"args":             browserArgs,
		"env":              env,
		"firefoxUserPrefs": prefs,
		"headless":         cfg.Headless != 1,
	}
	if cfg.LaunchProxy != nil {
		proxy := map[string]any{"server": cfg.LaunchProxy.Server}
		if cfg.LaunchProxy.Username != "" {
			proxy["username"] = cfg.LaunchProxy.Username
		}
		if cfg.LaunchProxy.Password != "" {
			proxy["password"] = cfg.LaunchProxy.Password
		}
		payload["proxy"] = proxy
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nodeDirectSpec{}, fmt.Errorf("%w: encode Go node-direct payload: %v", ErrSidecarStart, err)
	}
	spec := nodeDirectSpec{
		NodeJS:       nodejs,
		LaunchScript: launchScript,
		CWD:          cwd,
		StdinBase64:  base64.StdEncoding.EncodeToString(data),
	}
	if err := validateNodeDirectSpec(spec); err != nil {
		return nodeDirectSpec{}, err
	}
	return spec, nil
}

func nodeDirectGoEnv(config map[string]any, extraEnv []string) (map[string]any, error) {
	env := map[string]any{}
	for key, value := range browserLaunchEnv(Config{ExtraEnv: extraEnv}) {
		env[key] = value
	}
	configData, err := json.Marshal(config)
	if err != nil {
		return nil, fmt.Errorf("%w: encode Camoufox config: %v", ErrSidecarStart, err)
	}
	chunkSize := 32767
	if sidecarGOOS == "windows" {
		chunkSize = 2047
	}
	configText := string(configData)
	for i := 0; i < len(configText); i += chunkSize {
		end := i + chunkSize
		if end > len(configText) {
			end = len(configText)
		}
		env["CAMOU_CONFIG_"+strconv.Itoa((i/chunkSize)+1)] = configText[i:end]
	}
	return env, nil
}

func installedRuntimeBrowserExecutable(root RuntimeRoot) (string, error) {
	if exe, err := findBrowserExecutable(root.BrowserResourcesDir); err == nil {
		return exe, nil
	}
	return "", os.ErrNotExist
}

func nodeExecutableName() string {
	if sidecarGOOS == "windows" {
		return "node.exe"
	}
	return "node"
}

func defaultUBOAddonPath(executablePath string) string {
	addon := filepath.Join(browserResourcesDirForExecutable(executablePath), "addons", "UBO")
	if st, err := os.Stat(addon); err == nil && st.IsDir() {
		return addon
	}
	return ""
}

func browserResourcesDirForExecutable(executablePath string) string {
	dir := filepath.Dir(executablePath)
	if sidecarGOOS == "darwin" && filepath.Base(dir) == "MacOS" {
		return filepath.Join(filepath.Dir(dir), "Resources")
	}
	return filepath.Join(filepath.Dir(executablePath), "resources")
}

func cloneStringAnyMap(in map[string]any) map[string]any {
	out := map[string]any{}
	for key, value := range in {
		out[key] = value
	}
	return out
}
