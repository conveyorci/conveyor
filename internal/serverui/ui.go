package serverui

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed all:public
var EmbeddedFS embed.FS

// New returns a handler that serves the embedded UI files.
func New() http.Handler {
	// Create a filesystem that starts inside the 'public' directory
	subFS, err := fs.Sub(EmbeddedFS, "public")
	if err != nil {
		panic("CRITICAL: could not find 'public' directory in embedded filesystem: " + err.Error())
	}

	// Return a standard file server
	return http.FileServer(http.FS(subFS))
}
