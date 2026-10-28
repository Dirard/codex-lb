// Package webui embeds the built admin SPA and serves it safely from the
// single Go binary. API routes are never rewritten to index.html.
package webui

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strconv"
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

// encoding is a precompressed representation written by the Vite build next
// to the original file (`<name><suffix>`), in server preference order.
type encoding struct {
	token  string
	suffix string
}

var encodings = []encoding{{"br", ".br"}, {"gzip", ".gz"}}

// asset is the immutable serving metadata of one file in the build output.
type asset struct {
	etag     string
	variants map[string]string // content-coding token -> ETag of that variant
}

type server struct {
	filesystem fs.FS
	assets     map[string]asset
}

// New serves filesystem as the SPA root. Reserved API prefixes return 404
// instead of the SPA shell, hashed assets are cached immutably, index.html is
// revalidated on every load, and precompressed variants are negotiated per
// request. The filesystem must not change after New returns.
func New(filesystem fs.FS) http.Handler {
	s := &server{filesystem: filesystem, assets: indexAssets(filesystem)}
	return http.HandlerFunc(s.serveHTTP)
}

func (s *server) serveHTTP(w http.ResponseWriter, r *http.Request) {
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
		s.serveIndex(w, r)
		return
	}
	trimmed := strings.TrimPrefix(name, "/")
	if _, ok := s.assets[trimmed]; ok {
		s.serveFile(w, r, trimmed)
		return
	}
	if isVariantName(trimmed) {
		// Precompressed siblings are an internal representation, not URLs.
		http.NotFound(w, r)
		return
	}
	switch _, err := fs.Stat(s.filesystem, trimmed); {
	case err == nil:
		// A directory of the build output is not a client route.
		http.NotFound(w, r)
		return
	case !errors.Is(err, fs.ErrNotExist):
		http.Error(w, "admin UI lookup failed", http.StatusInternalServerError)
		return
	}
	// Only extensionless client routes fall back to the SPA shell; a missing
	// asset must stay a 404 instead of returning HTML with status 200.
	if strings.Contains(path.Base(name), ".") {
		http.NotFound(w, r)
		return
	}
	s.serveIndex(w, r)
}

func reservedPath(name string) bool {
	for _, prefix := range []string{"/api", "/v1", "/backend-api", "/health", "/metrics", "/debug"} {
		if name == prefix || strings.HasPrefix(name, prefix+"/") {
			return true
		}
	}
	return false
}

func (s *server) serveIndex(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.assets["index.html"]; !ok {
		http.Error(w, "admin UI assets are not built; run `npm run build` in web/", http.StatusServiceUnavailable)
		return
	}
	s.serveFile(w, r, "index.html")
}

// serveFile negotiates the representation of a known asset and delegates
// conditional requests, HEAD and ranges to http.ServeContent.
func (s *server) serveFile(w http.ResponseWriter, r *http.Request, name string) {
	meta := s.assets[name]
	header := w.Header()
	switch {
	case strings.HasPrefix(name, "assets/"):
		// Vite emits content-hashed filenames; safe to cache forever.
		header.Set("Cache-Control", "public, max-age=31536000, immutable")
	case name == "index.html":
		header.Set("Cache-Control", "no-cache")
	}
	contentType := mime.TypeByExtension(path.Ext(name))
	if contentType != "" {
		header.Set("Content-Type", contentType)
	}
	served, etag := name, meta.etag
	// A variant is only safe when the type comes from the original name;
	// sniffing compressed bytes would mislabel the response.
	if len(meta.variants) > 0 && contentType != "" {
		header.Add("Vary", "Accept-Encoding")
		// Ranges always address the identity representation, so a client
		// resuming a download never receives a slice of compressed bytes.
		if r.Header.Get("Range") == "" {
			if coding := negotiateEncoding(r.Header.Values("Accept-Encoding"), meta.variants); coding != "" {
				served, etag = name+encodingSuffix(coding), meta.variants[coding]
				header.Set("Content-Encoding", coding)
			}
		}
	}
	header.Set("ETag", etag)
	file, err := s.filesystem.Open(served)
	if err != nil {
		header.Del("Content-Encoding")
		http.Error(w, "admin UI lookup failed", http.StatusInternalServerError)
		return
	}
	defer file.Close()
	seeker, ok := file.(io.ReadSeeker)
	if !ok {
		header.Del("Content-Encoding")
		http.Error(w, "admin UI file is not seekable", http.StatusInternalServerError)
		return
	}
	if header.Get("Content-Encoding") == "" {
		http.ServeContent(w, r, name, time.Time{}, seeker)
		return
	}
	// ServeContent omits Content-Length once Content-Encoding is set and
	// advertises byte ranges, which would address the encoded bytes.
	size, err := seeker.Seek(0, io.SeekEnd)
	if err == nil {
		_, err = seeker.Seek(0, io.SeekStart)
	}
	if err != nil {
		header.Del("Content-Encoding")
		http.Error(w, "admin UI file is not seekable", http.StatusInternalServerError)
		return
	}
	header.Set("Content-Length", strconv.FormatInt(size, 10))
	http.ServeContent(encodedWriter{w}, r, name, time.Time{}, seeker)
}

// encodedWriter marks a precompressed representation as not range-addressable;
// range requests are always answered from the identity file instead.
type encodedWriter struct{ http.ResponseWriter }

func (w encodedWriter) WriteHeader(status int) {
	w.Header().Set("Accept-Ranges", "none")
	w.ResponseWriter.WriteHeader(status)
}

// indexAssets records every regular file with its strong ETag and the
// precompressed variants that the build wrote next to it.
func indexAssets(filesystem fs.FS) map[string]asset {
	etags := map[string]string{}
	_ = fs.WalkDir(filesystem, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || !entry.Type().IsRegular() {
			return nil
		}
		if etag, ok := contentETag(filesystem, name); ok {
			etags[name] = etag
		}
		return nil
	})
	assets := make(map[string]asset, len(etags))
	for name, etag := range etags {
		if isVariantName(name) {
			continue
		}
		meta := asset{etag: etag}
		for _, enc := range encodings {
			if variantETag, ok := etags[name+enc.suffix]; ok {
				if meta.variants == nil {
					meta.variants = map[string]string{}
				}
				meta.variants[enc.token] = variantETag
			}
		}
		assets[name] = meta
	}
	return assets
}

func contentETag(filesystem fs.FS, name string) (string, bool) {
	data, err := fs.ReadFile(filesystem, name)
	if err != nil {
		return "", false
	}
	sum := sha256.Sum256(data)
	return `"` + hex.EncodeToString(sum[:12]) + `"`, true
}

func isVariantName(name string) bool {
	for _, enc := range encodings {
		if strings.HasSuffix(name, enc.suffix) {
			return true
		}
	}
	return false
}

func encodingSuffix(token string) string {
	for _, enc := range encodings {
		if enc.token == token {
			return enc.suffix
		}
	}
	return ""
}

// negotiateEncoding returns the most preferred available content-coding the
// Accept-Encoding header allows with a non-zero quality (RFC 9110 §12.5.3).
// Equal qualities resolve in server order (brotli, then gzip); an empty
// result selects the identity representation.
func negotiateEncoding(values []string, available map[string]string) string {
	if len(values) == 0 {
		return ""
	}
	explicit := map[string]float64{}
	wildcard := -1.0
	for _, part := range strings.Split(strings.Join(values, ","), ",") {
		token, params, _ := strings.Cut(part, ";")
		token = strings.ToLower(strings.TrimSpace(token))
		if token == "" {
			continue
		}
		quality := 1.0
		for _, param := range strings.Split(params, ";") {
			key, value, found := strings.Cut(param, "=")
			if found && strings.EqualFold(strings.TrimSpace(key), "q") {
				parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
				if err != nil || parsed < 0 || parsed > 1 {
					parsed = 0
				}
				quality = parsed
			}
		}
		if token == "*" {
			wildcard = quality
			continue
		}
		explicit[token] = quality
	}
	best, bestQuality := "", 0.0
	for _, enc := range encodings {
		if _, ok := available[enc.token]; !ok {
			continue
		}
		quality, listed := explicit[enc.token]
		if !listed {
			quality = max(wildcard, 0)
		}
		if quality > bestQuality {
			best, bestQuality = enc.token, quality
		}
	}
	return best
}
