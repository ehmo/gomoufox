package sidecar

import (
	"embed"
	"io/fs"
)

//go:embed personadata/apify/fingerprint-network-definition.json
//go:embed personadata/camoufox/fonts.json personadata/camoufox/webgl-data.json
var embeddedPersonaDataFS embed.FS

var personaDataFS fs.ReadFileFS = embeddedPersonaDataFS
