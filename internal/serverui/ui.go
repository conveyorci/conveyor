package serverui

import (
	"embed"
	"io/fs"
	"log"
	"net/http"
	"strings"
)

//go:embed all:public
var embeddedFS embed.FS

func getPublicFS() fs.FS {
	subFS, err := fs.Sub(embeddedFS, "public")
	if err != nil {
		panic("CRITICAL: could not find 'public' directory in embedded filesystem: " + err.Error())
	}
	return subFS
}

// New creates a new http.Handler that serves the UI and handles 404s.
func New() http.Handler {
	publicFS := getPublicFS()

	// debugging
	log.Println("--- Embedded UI Filesystem Contents ---")
	err := fs.WalkDir(publicFS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		log.Printf("Found embedded file: %s\n", path)
		return nil
	})
	if err != nil {
		log.Printf("ERROR walking embedded FS: %v", err)
	}
	log.Println("------------------------------------")

	fileServer := http.FileServer(http.FS(publicFS))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqPath := r.URL.Path
		lookupPath := strings.TrimPrefix(reqPath, "/")

		if lookupPath == "" {
			lookupPath = "index.html"
		}

		if strings.HasSuffix(lookupPath, "/") {
			lookupPath = lookupPath + "index.html"
		}

		f, err := publicFS.Open(lookupPath)
		if err != nil {
			log.Printf("DEBUG: file not found for path '%s' (lookup: '%s'), serving 404.", r.URL.Path, lookupPath)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusNotFound)

			notFoundPage, _ := fs.ReadFile(publicFS, "404.html")
			w.Write(notFoundPage)
			return
		}
		f.Close()

		fileServer.ServeHTTP(w, r)
	})
}
