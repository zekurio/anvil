// Package web embeds the built web UI. The frontend sources live beside this
// file and build into dist; a checkout without a build embeds only the
// placeholder, and the daemon then serves the API alone.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// Dist returns the built frontend rooted at its index.html, and whether a
// build is actually present.
func Dist() (fs.FS, bool) {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		return nil, false
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return sub, false
	}
	return sub, true
}
