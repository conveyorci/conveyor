package main

import (
	"embed"
	"io/fs"
	"log"
	"net/http"
	"os"
)

//go:embed all:/public
var embeddedFiles embed.FS

// getStaticFS returns a filesystem that can be served.
func getStaticFS() http.FileSystem {
	if _, err := os.Stat("public"); err == nil {
		log.Println("Serving static files from local 'public' directory (development mode).")
		return http.Dir("public")
	}

	log.Println("Serving static files from embedded filesystem (production mode).")
	subFS, err := fs.Sub(embeddedFiles, "public")
	if err != nil {
		panic(err)
	}
	return http.FS(subFS)
}
