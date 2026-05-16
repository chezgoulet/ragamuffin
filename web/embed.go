package web

import "embed"

// FS embeds the web UI static files into the binary.
// Files are served at / (index.html) and /static/*.
//
//go:embed *.html static/*
var FS embed.FS
