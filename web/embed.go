//go:build client && !server

// Package webui embeds the built Plan Forge UI. The bundle is produced by
// `npm --prefix web run build` into web/dist; the directory is ignored by Git,
// so every client-tag Go build must run the web build first. A go:embed pattern
// cannot reach outside its own package, which is why this file lives in web/.
//
// cmd/client reaches this package only through internal/app.
package webui

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var assets embed.FS

// Assets returns the UI rooted at its index.html.
func Assets() fs.FS {
	sub, err := fs.Sub(assets, "dist")
	if err != nil {
		// fs.Sub only fails for an invalid path; "dist" is a constant, so this
		// cannot happen at run time.
		panic("webui: embedded dist directory is invalid: " + err.Error())
	}
	return sub
}
