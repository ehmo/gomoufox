package gomoufox

import (
	"time"

	"github.com/ehmo/gomoufox/camoufoxcfg"
	"github.com/ehmo/gomoufox/internal/policy"
	"github.com/ehmo/gomoufox/internal/pwbridge"
)

type launchConfig struct {
	headless        camoufoxcfg.HeadlessMode
	humanize        *float64
	geoip           bool
	proxy           *camoufoxcfg.ProxyConfig
	os              camoufoxcfg.OS
	locale          []string
	blockImages     bool
	blockWebRTC     bool
	blockWebGL      bool
	persistentCtx   bool
	userDataDir     string
	directNetwork   bool
	addons          []string
	window          *camoufoxcfg.WindowSize
	screen          *camoufoxcfg.ScreenConfig
	webgl           *camoufoxcfg.WebGLConfig
	firefoxPrefs    camoufoxcfg.FirefoxUserPrefs
	browserArgs     []string
	customFontsOnly bool
	ffVersion       int
	camoufoxDebug   bool
	fonts           []string
	fingerprint     camoufoxcfg.FingerprintOverride
	idleTimeout     time.Duration
	autoInstall     bool
	pythonBin       string
	venvDir         string
	connectTimeout  time.Duration
	mainWorldEval   bool
	enableCache     bool
	disableCOOP     bool
	extraEnv        []string

	connector pwbridge.Connector
	sidecar   func(launchConfig) (sidecarHandle, error)
	policy    policy.Config
}

func defaultLaunchConfig() launchConfig {
	return launchConfig{
		headless:       camoufoxcfg.HeadlessTrue,
		autoInstall:    true,
		connectTimeout: 30 * time.Second,
		policy:         policy.DefaultConfig(),
		connector:      pwbridge.RealConnector{},
		sidecar:        newSidecarManager,
	}
}

type contextConfig struct {
	Viewport         *camoufoxcfg.WindowSize
	StorageState     *StorageState
	Proxy            *camoufoxcfg.ProxyConfig
	Locale           string
	TimezoneID       string
	ExtraHTTPHeaders map[string]string
	HTTPCredentials  *HTTPCredentials
}

// HTTPCredentials stores HTTP authentication credentials for a context.
type HTTPCredentials struct {
	Username string
	Password string
}
