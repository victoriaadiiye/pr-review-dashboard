package httpserver

import (
	"embed"
	"io/fs"
)

//go:embed all:web
var webFS embed.FS

// Assets returns the embedded built dashboard, rooted at the web/ dir.
func Assets() fs.FS {
	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		panic(err) // build-time guarantee: web/ is embedded
	}
	return sub
}
