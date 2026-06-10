package gomoufox

import (
	"time"

	"github.com/ehmo/gomoufox/camoufoxcfg"
	"github.com/ehmo/gomoufox/internal/pwbridge"
)

// Option is a functional option for New.
type Option func(*launchConfig)

func WithHeadless(mode camoufoxcfg.HeadlessMode) Option {
	return func(c *launchConfig) { c.headless = mode }
}

func WithHumanize(maxDuration time.Duration) Option {
	return func(c *launchConfig) {
		v := maxDuration.Seconds()
		c.humanize = &v
	}
}

func WithGeoIP(enabled bool) Option { return func(c *launchConfig) { c.geoip = enabled } }

func WithProxy(cfg camoufoxcfg.ProxyConfig) Option {
	return func(c *launchConfig) {
		copy := cfg
		c.proxy = &copy
	}
}

func WithOS(os camoufoxcfg.OS) Option { return func(c *launchConfig) { c.os = os } }

func WithLocale(locales ...string) Option {
	return func(c *launchConfig) { c.locale = append([]string(nil), locales...) }
}

func WithBlockImages(block bool) Option { return func(c *launchConfig) { c.blockImages = block } }
func WithBlockWebRTC(block bool) Option { return func(c *launchConfig) { c.blockWebRTC = block } }
func WithBlockWebGL(block bool) Option  { return func(c *launchConfig) { c.blockWebGL = block } }

func WithPersistentContext(userDataDir string) Option {
	return func(c *launchConfig) {
		c.persistentCtx = true
		c.userDataDir = userDataDir
	}
}

// WithUnsafeDirectNetwork bypasses gomoufox's local filtering proxy for browser
// traffic. This disables URL guardrails for browser-initiated requests and
// should only be used for parity tests or fully trusted destinations.
func WithUnsafeDirectNetwork(enabled bool) Option {
	return func(c *launchConfig) { c.directNetwork = enabled }
}

func WithAllowedOrigins(origins ...string) Option {
	return func(c *launchConfig) { c.policy.AllowedOrigins = append([]string(nil), origins...) }
}

func WithAllowedHosts(hosts ...string) Option {
	return func(c *launchConfig) { c.policy.AllowedHosts = append([]string(nil), hosts...) }
}

func WithAddons(paths ...string) Option {
	return func(c *launchConfig) { c.addons = append([]string(nil), paths...) }
}

func WithWindow(w, h int) Option {
	return func(c *launchConfig) { c.window = &camoufoxcfg.WindowSize{Width: w, Height: h} }
}

func WithScreen(w, h int) Option {
	return func(c *launchConfig) { c.screen = &camoufoxcfg.ScreenConfig{Width: w, Height: h} }
}

func WithWebGL(vendor, renderer string) Option {
	return func(c *launchConfig) { c.webgl = &camoufoxcfg.WebGLConfig{Vendor: vendor, Renderer: renderer} }
}

func WithFirefoxUserPrefs(prefs camoufoxcfg.FirefoxUserPrefs) Option {
	return func(c *launchConfig) {
		c.firefoxPrefs = camoufoxcfg.FirefoxUserPrefs{}
		for k, v := range prefs {
			c.firefoxPrefs[k] = v
		}
	}
}

func WithBrowserArgs(args ...string) Option {
	return func(c *launchConfig) { c.browserArgs = append([]string(nil), args...) }
}

func WithCustomFontsOnly(enabled bool) Option {
	return func(c *launchConfig) { c.customFontsOnly = enabled }
}

func WithFirefoxVersion(version int) Option {
	return func(c *launchConfig) { c.ffVersion = version }
}

func WithCamoufoxDebug(enabled bool) Option {
	return func(c *launchConfig) { c.camoufoxDebug = enabled }
}

func WithFonts(families ...string) Option {
	return func(c *launchConfig) { c.fonts = append([]string(nil), families...) }
}

func WithFingerprintOverride(overrides camoufoxcfg.FingerprintOverride) Option {
	return func(c *launchConfig) {
		if c.fingerprint == nil {
			c.fingerprint = camoufoxcfg.FingerprintOverride{}
		}
		for k, v := range overrides {
			c.fingerprint[k] = v
		}
	}
}

func WithIdleTimeout(d time.Duration) Option { return func(c *launchConfig) { c.idleTimeout = d } }
func WithAutoInstall(auto bool) Option       { return func(c *launchConfig) { c.autoInstall = auto } }
func WithPythonBin(path string) Option       { return func(c *launchConfig) { c.pythonBin = path } }
func WithVenvDir(dir string) Option          { return func(c *launchConfig) { c.venvDir = dir } }
func WithConnectTimeout(d time.Duration) Option {
	return func(c *launchConfig) { c.connectTimeout = d }
}
func WithMainWorldEval(enabled bool) Option {
	return func(c *launchConfig) { c.mainWorldEval = enabled }
}
func WithEnableCache(enabled bool) Option { return func(c *launchConfig) { c.enableCache = enabled } }
func WithDisableCOOP(enabled bool) Option { return func(c *launchConfig) { c.disableCOOP = enabled } }
func WithExtraEnv(pairs ...string) Option {
	return func(c *launchConfig) { c.extraEnv = append([]string(nil), pairs...) }
}

// WithSidecarRuntime selects the long-lived sidecar process model.
//
// SidecarRuntimeNodeDirect is the default Go-managed runtime path.
// SidecarRuntimePython remains available as an explicit compatibility mode.
func WithSidecarRuntime(runtime SidecarRuntime) Option {
	return func(c *launchConfig) { c.sidecarRuntime = runtime }
}

// withConnector is intentionally unexported; tests and internal packages use it
// to swap playwright-go for deterministic fakes without expanding the public API.
func withConnector(connector pwbridge.Connector) Option {
	return func(c *launchConfig) { c.connector = connector }
}

func withSidecarFactory(factory func(launchConfig) (sidecarHandle, error)) Option {
	return func(c *launchConfig) { c.sidecar = factory }
}

// ContextOption is a functional option for Browser.NewContext and Browser.NewPage.
type ContextOption func(*contextConfig)

func WithViewport(width, height int) ContextOption {
	return func(c *contextConfig) { c.Viewport = &camoufoxcfg.WindowSize{Width: width, Height: height} }
}

func WithStorageState(state *StorageState) ContextOption {
	return func(c *contextConfig) { c.StorageState = state }
}

func WithContextProxy(cfg camoufoxcfg.ProxyConfig) ContextOption {
	return func(c *contextConfig) {
		copy := cfg
		c.Proxy = &copy
	}
}

func WithContextLocale(locale string) ContextOption {
	return func(c *contextConfig) { c.Locale = locale }
}

func WithTimezoneID(tz string) ContextOption {
	return func(c *contextConfig) { c.TimezoneID = tz }
}

func WithExtraHTTPHeaders(headers map[string]string) ContextOption {
	return func(c *contextConfig) {
		c.ExtraHTTPHeaders = map[string]string{}
		for k, v := range headers {
			c.ExtraHTTPHeaders[k] = v
		}
	}
}

func WithHTTPCredentials(username, password string) ContextOption {
	return func(c *contextConfig) { c.HTTPCredentials = &HTTPCredentials{Username: username, Password: password} }
}
