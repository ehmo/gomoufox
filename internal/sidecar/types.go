package sidecar

import (
	"time"

	"github.com/ehmo/gomoufox/internal/policy"
)

const (
	RequiredCamoufox       = "0.4.11"
	RequiredPlaywright     = "1.57.0"
	RequiredPlaywrightJSON = "1.57.0-beta-1764944708000"
	RequiredPip            = "26.1.2"
	PlaywrightGoVersion    = "v0.5700.1"
	CamoufoxBinaryVersion  = "v135.0.1-beta.24"
)

const (
	RuntimePython     = "python"
	RuntimeNodeDirect = "node-direct"
)

type State int

const (
	StateIdle State = iota
	StateStarting
	StateReady
	StateShuttingDown
	StateDead
)

type Config struct {
	PythonBin       string
	VenvDir         string
	Runtime         string
	ConnectTimeout  time.Duration
	Headless        int
	Persistent      bool
	UserDataDir     string
	DirectNetwork   bool
	Proxy           *ProxyConfig
	LaunchProxy     *ProxyConfig
	Policy          policy.Config
	GeoIP           bool
	Humanize        *float64
	OS              string
	Locale          []string
	BlockImages     bool
	BlockWebRTC     bool
	BlockWebGL      bool
	Addons          []string
	Window          *Size
	Screen          *Size
	WebGL           *WebGLConfig
	FirefoxPrefs    map[string]any
	BrowserArgs     []string
	CustomFontsOnly bool
	FFVersion       int
	CamoufoxDebug   bool
	Fonts           []string
	Fingerprint     map[string]any
	MainWorldEval   bool
	EnableCache     bool
	DisableCOOP     bool
	ExtraEnv        []string
}

type ProxyConfig struct {
	Server   string
	Username string
	Password string
}

type Size struct {
	Width  int
	Height int
}

type WebGLConfig struct {
	Vendor   string
	Renderer string
}

type Info struct {
	PID                int
	CamoufoxVersion    string
	PlaywrightVersion  string
	WSEndpointRedacted string
	Runtime            string
}

type InstallOptions struct {
	PythonBin       string
	VenvDir         string
	Runtime         string
	CamoufoxVersion string
	SkipBinaryFetch bool
	CamoufoxPath    string
	Verbose         bool
	ForceReinstall  bool
}
