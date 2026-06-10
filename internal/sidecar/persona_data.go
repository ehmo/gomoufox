package sidecar

import "embed"

//go:embed personadata/apify/* personadata/camoufox/* personadata/licenses/*
var personaDataFS embed.FS
