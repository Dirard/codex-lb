// Package webui embeds the built admin SPA and serves it safely from the
// single Go binary. API routes are never rewritten to index.html.
package webui

import (
	"embed"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

//go:embed all:dist
var embedded embed.FS

// Dist returns the embedded Vite build output tree (contents of dist/).
func Dist() fs.FS {
	sub, err := fs.Sub(embedded, "dist")
	if err != nil {
		// Cannot happen: the dist tree is part of the compiled binary.
		panic(err)
	}
	return sub
}

// Handler serves the embedded SPA. Wire it after the real API mux.
func Handler() http.Handler {
	return New(Dist())
}

// New serves filesystem as the SPA root. Reserved API prefixes return 404
// instead of the SPA shell, hashed assets are cached immutably, and
// index.html is never cached.
func New(filesystem fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := path.Clean("/" + r.URL.Path)
		if strings.Contains(name, "..") || reservedPath(name) {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if name == "/" {
			serveIndex(w, r, filesystem)
			return
		}

		trimmed := strings.TrimPrefix(name, "/")
		file, err := filesystem.Open(trimmed)
		switch {
		case err == nil:
			defer file.Close()
			serveFile(w, r, file, trimmed)
		case errors.Is(err, fs.ErrNotExist):
			serveFallback(w, r, filesystem, name)
		default:
			http.Error(w, "admin UI lookup failed", http.StatusInternalServerError)
		}
	})
}

func reservedPath(name string) bool {
	for _, prefix := range []string{"/api", "/v1", "/backend-api", "/health"} {
		if name == prefix || strings.HasPrefix(name, prefix+"/") {
			return true
		}
	}
	return false
}

func serveFile(w http.ResponseWriter, r *http.Request, file fs.File, name string) {
	stat, err := file.Stat()
	if err != nil || stat.IsDir() {
		http.NotFound(w, r)
		return
	}
	seeker, ok := file.(io.ReadSeeker)
	if !ok {
		http.Error(w, "admin UI file is not seekable", http.StatusInternalServerError)
		return
	}
	switch {
	case strings.HasPrefix(name, "assets/"):
		// Vite emits content-hashed filenames; safe to cache forever.
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	case name == "index.html":
		w.Header().Set("Cache-Control", "no-store")
	}
	http.ServeContent(w, r, stat.Name(), time.Time{}, seeker)
}

func serveFallback(w http.ResponseWriter, r *http.Request, filesystem fs.FS, name string) {
	// Only extensionless client routes fall back to the SPA shell; a missing
	// asset must stay a 404 instead of returning HTML with status 200.
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if strings.Contains(path.Base(name), ".") {
		http.NotFound(w, r)
		return
	}
	serveIndex(w, r, filesystem)
}

func serveIndex(w http.ResponseWriter, r *http.Request, filesystem fs.FS) {
	file, err := filesystem.Open("index.html")
	if err != nil {
		http.Error(w, "admin UI assets are not built; run `npm run build` in web/", http.StatusServiceUnavailable)
		return
	}
	defer file.Close()
	w.Header().Set("Cache-Control", "no-store")
	serveFile(w, r, file, "index.html")
}
