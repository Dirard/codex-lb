package webui

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"testing/fstest"
)

// Synthetic stand-ins for the Vite precompression output; their content is
// arbitrary because the server never recompresses or decodes variants.
const (
	scriptBody  = "console.log('codex-lb dashboard entry');"
	brotliBody  = "BROTLI-BYTES"
	indexBody   = "<!doctype html><title>codex-lb</title>"
	indexBrotli = "INDEX-BROTLI"
)

func gzipBytes(t *testing.T, data string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(data)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func encodedFS(t *testing.T) fstest.MapFS {
	return fstest.MapFS{
		"index.html":             {Data: []byte(indexBody)},
		"index.html.br":          {Data: []byte(indexBrotli)},
		"index.html.gz":          {Data: gzipBytes(t, indexBody)},
		"assets/app-HASH.js":     {Data: []byte(scriptBody)},
		"assets/app-HASH.js.br":  {Data: []byte(brotliBody)},
		"assets/app-HASH.js.gz":  {Data: gzipBytes(t, scriptBody)},
		"assets/only-gz.css":     {Data: []byte("body{color:red}")},
		"assets/only-gz.css.gz":  {Data: gzipBytes(t, "body{color:red}")},
		"assets/font-HASH.woff2": {Data: []byte("wOF2")},
	}
}

func get(t *testing.T, handler http.Handler, target string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestNegotiatesPrecompressedVariants(t *testing.T) {
	handler := New(encodedFS(t))
	gz := string(gzipBytes(t, scriptBody))

	cases := []struct {
		name, accept, encoding, body string
	}{
		{"brotli preferred", "gzip, deflate, br", "br", brotliBody},
		{"plain-HTTP browsers get gzip", "gzip, deflate", "gzip", gz},
		{"brotli refused with q=0", "br;q=0, gzip", "gzip", gz},
		{"higher gzip quality wins", "br;q=0.5, gzip;q=0.9", "gzip", gz},
		{"wildcard allows brotli", "*", "br", brotliBody},
		{"wildcard excluded coding", "br;q=0, *;q=0.1", "gzip", gz},
		{"identity only", "identity", "", scriptBody},
		{"no header", "", "", scriptBody},
		{"malformed quality is refusal", "br;q=abc, gzip;q=2", "", scriptBody},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			headers := map[string]string{}
			if tc.accept != "" {
				headers["Accept-Encoding"] = tc.accept
			}
			rec := get(t, handler, "/assets/app-HASH.js", headers)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if got := rec.Header().Get("Content-Encoding"); got != tc.encoding {
				t.Fatalf("content-encoding = %q, want %q", got, tc.encoding)
			}
			if got := rec.Body.String(); got != tc.body {
				t.Fatalf("body = %q, want %q", got, tc.body)
			}
			if got := rec.Header().Get("Content-Length"); got != strconv.Itoa(len(tc.body)) {
				t.Fatalf("content-length = %q, want %d", got, len(tc.body))
			}
			if got := rec.Header().Get("Content-Type"); got != "text/javascript; charset=utf-8" {
				t.Fatalf("content-type = %q, want the original script type", got)
			}
			wantRanges := "bytes"
			if tc.encoding != "" {
				wantRanges = "none"
			}
			if got := rec.Header().Get("Accept-Ranges"); got != wantRanges {
				t.Fatalf("accept-ranges = %q, want %q", got, wantRanges)
			}
			if got := rec.Header().Get("Vary"); got != "Accept-Encoding" {
				t.Fatalf("vary = %q, want Accept-Encoding", got)
			}
			if got := rec.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
				t.Fatalf("cache-control = %q, want immutable", got)
			}
		})
	}
}

func TestGzipOnlyAssetAndUncompressibleAsset(t *testing.T) {
	handler := New(encodedFS(t))

	rec := get(t, handler, "/assets/only-gz.css", map[string]string{"Accept-Encoding": "br, gzip"})
	if rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("gzip-only asset: content-encoding = %q, want gzip", rec.Header().Get("Content-Encoding"))
	}
	if rec.Header().Get("Content-Type") != "text/css; charset=utf-8" {
		t.Fatalf("gzip-only asset: content-type = %q", rec.Header().Get("Content-Type"))
	}

	rec = get(t, handler, "/assets/font-HASH.woff2", map[string]string{"Accept-Encoding": "br, gzip"})
	if rec.Header().Get("Content-Encoding") != "" || rec.Header().Get("Vary") != "" {
		t.Fatalf("font without variants: encoding = %q, vary = %q", rec.Header().Get("Content-Encoding"), rec.Header().Get("Vary"))
	}
	if rec.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" || rec.Header().Get("ETag") == "" {
		t.Fatalf("hashed font must be immutable with an ETag: %v", rec.Header())
	}
}

func TestRangeRequestsAreServedUnencoded(t *testing.T) {
	handler := New(encodedFS(t))

	rec := get(t, handler, "/assets/app-HASH.js", map[string]string{"Accept-Encoding": "br, gzip", "Range": "bytes=0-6"})
	if rec.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206", rec.Code)
	}
	if rec.Header().Get("Content-Encoding") != "" {
		t.Fatalf("range response must not be encoded, got %q", rec.Header().Get("Content-Encoding"))
	}
	if rec.Body.String() != scriptBody[:7] {
		t.Fatalf("body = %q, want the first identity bytes", rec.Body.String())
	}
	if want := "bytes 0-6/" + strconv.Itoa(len(scriptBody)); rec.Header().Get("Content-Range") != want {
		t.Fatalf("content-range = %q, want %q", rec.Header().Get("Content-Range"), want)
	}
	if rec.Header().Get("Vary") != "Accept-Encoding" {
		t.Fatal("range response of a negotiable asset must still vary on Accept-Encoding")
	}
}

func TestETagsRevalidatePerRepresentation(t *testing.T) {
	handler := New(encodedFS(t))

	identity := get(t, handler, "/", nil)
	brotli := get(t, handler, "/", map[string]string{"Accept-Encoding": "br"})
	if identity.Header().Get("Cache-Control") != "no-cache" || brotli.Header().Get("Cache-Control") != "no-cache" {
		t.Fatal("index.html must be served with no-cache")
	}
	if brotli.Body.String() != indexBrotli || brotli.Header().Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("brotli index: body = %q, type = %q", brotli.Body.String(), brotli.Header().Get("Content-Type"))
	}
	identityTag, brotliTag := identity.Header().Get("ETag"), brotli.Header().Get("ETag")
	if identityTag == "" || brotliTag == "" || identityTag == brotliTag {
		t.Fatalf("representations need distinct strong ETags: identity %q, br %q", identityTag, brotliTag)
	}
	if identityTag[0] != '"' {
		t.Fatalf("ETag %q must be strong", identityTag)
	}

	revalidated := get(t, handler, "/dashboard", map[string]string{"Accept-Encoding": "br", "If-None-Match": brotliTag})
	if revalidated.Code != http.StatusNotModified {
		t.Fatalf("matching If-None-Match: status = %d, want 304", revalidated.Code)
	}
	if revalidated.Body.Len() != 0 || revalidated.Header().Get("Content-Encoding") != "" {
		t.Fatalf("304 must carry no body or encoding: %v", revalidated.Header())
	}

	changed := get(t, handler, "/", map[string]string{"If-None-Match": brotliTag})
	if changed.Code != http.StatusOK || changed.Body.String() != indexBody {
		t.Fatalf("identity request with the brotli ETag: status = %d, body = %q", changed.Code, changed.Body.String())
	}
}

func TestVariantFilesAreNotAddressable(t *testing.T) {
	handler := New(encodedFS(t))

	for _, target := range []string{"/assets/app-HASH.js.br", "/assets/app-HASH.js.gz", "/index.html.gz"} {
		if rec := get(t, handler, target, nil); rec.Code != http.StatusNotFound {
			t.Fatalf("GET %s: status = %d, want 404", target, rec.Code)
		}
	}
	if rec := get(t, handler, "/assets", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("GET /assets directory: status = %d, want 404", rec.Code)
	}
}

func TestHeadMatchesNegotiatedGet(t *testing.T) {
	handler := New(encodedFS(t))

	req := httptest.NewRequest(http.MethodHead, "/assets/app-HASH.js", nil)
	req.Header.Set("Accept-Encoding", "br")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Fatalf("HEAD: status = %d, body length = %d", rec.Code, rec.Body.Len())
	}
	if rec.Header().Get("Content-Encoding") != "br" || rec.Header().Get("Content-Length") != strconv.Itoa(len(brotliBody)) {
		t.Fatalf("HEAD headers = %v, want brotli variant metadata", rec.Header())
	}
}
