package gomoufox

import (
	"context"

	"github.com/ehmo/gomoufox/internal/sidecar"
)

type sidecarHandle interface {
	Start(context.Context) (string, error)
	Stop(context.Context)
	Info() SidecarInfo
}

func newSidecarManager(cfg launchConfig) (sidecarHandle, error) {
	return sidecarAdapter{manager: sidecar.New(sidecarConfigFromLaunchConfig(cfg))}, nil
}

func sidecarConfigFromLaunchConfig(cfg launchConfig) sidecar.Config {
	sidecarCfg := sidecar.Config{
		PythonBin:       cfg.pythonBin,
		VenvDir:         cfg.venvDir,
		Runtime:         string(cfg.sidecarRuntime),
		ConnectTimeout:  cfg.connectTimeout,
		Headless:        int(cfg.headless),
		Persistent:      cfg.persistentCtx,
		UserDataDir:     cfg.userDataDir,
		DirectNetwork:   cfg.directNetwork,
		Policy:          cfg.policy,
		GeoIP:           cfg.geoip,
		Humanize:        cfg.humanize,
		OS:              string(cfg.os),
		Locale:          append([]string(nil), cfg.locale...),
		BlockImages:     cfg.blockImages,
		BlockWebRTC:     cfg.blockWebRTC,
		BlockWebGL:      cfg.blockWebGL,
		Addons:          append([]string(nil), cfg.addons...),
		BrowserArgs:     append([]string(nil), cfg.browserArgs...),
		CustomFontsOnly: cfg.customFontsOnly,
		FFVersion:       cfg.ffVersion,
		CamoufoxDebug:   cfg.camoufoxDebug,
		Fonts:           append([]string(nil), cfg.fonts...),
		MainWorldEval:   cfg.mainWorldEval,
		EnableCache:     cfg.enableCache,
		DisableCOOP:     cfg.disableCOOP,
		ExtraEnv:        append([]string(nil), cfg.extraEnv...),
	}
	if cfg.proxy != nil {
		sidecarCfg.Proxy = &sidecar.ProxyConfig{Server: cfg.proxy.Server, Username: cfg.proxy.Username, Password: cfg.proxy.Password}
	}
	if cfg.window != nil {
		sidecarCfg.Window = &sidecar.Size{Width: cfg.window.Width, Height: cfg.window.Height}
	}
	if cfg.screen != nil {
		sidecarCfg.Screen = &sidecar.Size{Width: cfg.screen.Width, Height: cfg.screen.Height}
	}
	if cfg.webgl != nil {
		sidecarCfg.WebGL = &sidecar.WebGLConfig{Vendor: cfg.webgl.Vendor, Renderer: cfg.webgl.Renderer}
	}
	if cfg.firefoxPrefs != nil {
		sidecarCfg.FirefoxPrefs = map[string]any{}
		for key, value := range cfg.firefoxPrefs {
			sidecarCfg.FirefoxPrefs[key] = value
		}
	}
	if cfg.fingerprint != nil {
		sidecarCfg.Fingerprint = map[string]any{}
		for key, value := range cfg.fingerprint {
			sidecarCfg.Fingerprint[key] = value
		}
	}
	return sidecarCfg
}

type sidecarAdapter struct {
	manager *sidecar.Manager
}

func (a sidecarAdapter) Start(ctx context.Context) (string, error) { return a.manager.Start(ctx) }
func (a sidecarAdapter) Stop(ctx context.Context)                  { a.manager.Stop(ctx) }
func (a sidecarAdapter) Info() SidecarInfo {
	info := a.manager.Info()
	return SidecarInfo{
		PID:                info.PID,
		CamoufoxVersion:    info.CamoufoxVersion,
		PlaywrightVersion:  info.PlaywrightVersion,
		WSEndpointRedacted: info.WSEndpointRedacted,
		Runtime:            SidecarRuntime(info.Runtime),
	}
}
