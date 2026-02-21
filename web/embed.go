// Package web provides embedded static assets for the dashboard frontend.
//
// During development, the dist/ directory contains only a .gitkeep placeholder.
// The production build (via Makefile or Dockerfile) populates dist/ with the
// compiled Vue.js application before the Go binary is built.
package web

import "embed"

// DistFS holds the compiled frontend assets. When the dashboard is enabled,
// these files are served by the HTTP handler. The all: prefix ensures dotfiles
// (if any) are included.
//
//go:embed all:dist
var DistFS embed.FS
