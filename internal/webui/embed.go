package webui

import (
	"embed"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// The tracked .gitkeep allows Go-only tests to compile before npm is installed.
// Runtime startup still requires the real Vite output.
//
//go:embed all:dist
var assets embed.FS

func Embedded() (fs.FS, error) {
	return fs.Sub(assets, "dist")
}

func Handler(files fs.FS) (http.Handler, error) {
	index, err := fs.ReadFile(files, "index.html")
	if err != nil || len(index) == 0 {
		return nil, fmt.Errorf("WEB_ASSETS_MISSING: build the frontend with make build before starting Controller")
	}
	server := http.FileServerFS(files)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		clean := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if strings.HasPrefix(clean, ".") || strings.Contains(clean, "/.") {
			http.NotFound(w, r)
			return
		}
		if clean == "." || clean == "" || clean == "index.html" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
			if r.Method != http.MethodHead {
				_, _ = w.Write(index)
			}
			return
		}
		// No client-side router yet. Missing assets and paths are real 404s.
		server.ServeHTTP(w, r)
	}), nil
}
