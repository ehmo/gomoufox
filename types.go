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
}
