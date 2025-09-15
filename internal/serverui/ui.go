package serverui

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed all:public
var embeddedFS embed.FS

// New returns an http.Handler that serves the embedded UI.
func New() http.Handler {
	subFS, err := fs.Sub(embeddedFS, "public")
	if err != nil {
		panic("could not find public directory in embedded filesystem: " + err.Error())
	}

	return http.FileServer(http.FS(subFS))
}
