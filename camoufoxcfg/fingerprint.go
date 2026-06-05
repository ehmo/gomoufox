package camoufoxcfg

// FingerprintOverride allows low-level BrowserForge config overrides.
type FingerprintOverride map[string]any

// WebGLConfig sets WebGL vendor and renderer strings.
type WebGLConfig struct {
	Vendor   string
	Renderer string
}
