package sidecar

import (
	"embed"
	"io/fs"
)

//go:embed personadata/apify/* personadata/camoufox/* personadata/licenses/*
var embeddedPersonaDataFS embed.FS

var personaDataFS fs.ReadFileFS = embeddedPersonaDataFS
