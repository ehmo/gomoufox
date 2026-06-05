package camoufoxcfg

// OS represents the operating system persona Camoufox will impersonate for
// fingerprint generation. OSRandom leaves the choice to BrowserForge.
type OS string

const (
	OSWindows OS = "windows"
	OSMacOS   OS = "macos"
	OSLinux   OS = "linux"
	OSRandom  OS = ""
)

// HeadlessMode controls whether the browser runs visibly.
type HeadlessMode int

const (
	HeadlessTrue HeadlessMode = iota
	HeadlessFalse
	HeadlessVirtual
)
