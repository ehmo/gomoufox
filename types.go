package gomoufox

type StorageState struct {
	Cookies []Cookie `json:"cookies"`
	Origins []Origin `json:"origins"`
}

type Origin struct {
	Origin       string    `json:"origin"`
	LocalStorage []LSEntry `json:"localStorage"`
}

type LSEntry struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type Cookie struct {
	Name     string  `json:"name"`
	Value    string  `json:"value"`
	Domain   string  `json:"domain"`
	Path     string  `json:"path"`
	Expires  float64 `json:"expires"`
	HTTPOnly bool    `json:"httpOnly"`
	Secure   bool    `json:"secure"`
	SameSite string  `json:"sameSite"`
}

type SidecarInfo struct {
	PID                int
	CamoufoxVersion    string
	PlaywrightVersion  string
	WSEndpointRedacted string
	Runtime            SidecarRuntime
}

// SidecarRuntime selects the long-lived sidecar process model.
type SidecarRuntime string

const (
	// SidecarRuntimePython uses Camoufox's Python server wrapper.
	SidecarRuntimePython SidecarRuntime = "python"
	// SidecarRuntimeNodeDirect asks Python to generate Camoufox launch options,
	// then runs the Playwright Node server directly as the long-lived sidecar.
	SidecarRuntimeNodeDirect SidecarRuntime = "node-direct"
)
