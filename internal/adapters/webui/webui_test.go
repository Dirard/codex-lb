package webui

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func testFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html":          &fstest.MapFile{Data: []byte("<!doctype html><title>codex-lb</title>")},
		"favicon.svg":         &fstest.MapFile{Data: []byte("<svg/>")},
		"fonts/app.woff2":     &fstest.MapFile{Data: []byte("font")},
		"assets/app-HASH.js":  &fstest.MapFile{Data: []byte("console.log(1)")},
		"assets/app-HASH.css": &fstest.MapFile{Data: []byte("body{}")},
	}
}

func do(t *testing.T, handler http.Handler, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestServesIndexAndSPAfallback(t *testing.T) {
	handler := New(testFS())

	for _, target := range []string{"/", "/dashboard", "/accounts/acc-1/settings"} {
		rec := do(t, handler, http.MethodGet, target)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: status = %d, want 200", target, rec.Code)
		}
		if got := rec.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
			t.Fatalf("GET %s: content-type = %q, want text/html", target, got)
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Fatalf("GET %s: cache-control = %q, want no-store", target, got)
		}
		if rec.Body.String() != "<!doctype html><title>codex-lb</title>" {
			t.Fatalf("GET %s: body = %q, want index.html", target, rec.Body.String())
		}
	}
}

func TestAPIPathsNeverBecomeIndex(t *testing.T) {
	handler := New(testFS())

	for _, target := range []string{
		"/api/accounts", "/api/", "/api", "/v1/responses", "/backend-api/codex/responses", "/health", "/health/ready",
	} {
		rec := do(t, handler, http.MethodGet, target)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("GET %s: status = %d, want 404", target, rec.Code)
		}
		if rec.Body.String() == "<!doctype html><title>codex-lb</title>" {
			t.Fatalf("GET %s: returned index.html", target)
		}
	}
}

func TestPathTraversalRejected(t *testing.T) {
	handler := New(testFS())

	for _, target := range []string{"/../secret", "/%2e%2e/secret", "/..%2f..%2fetc/passwd", "/assets/..%2findex.html"} {
		rec := do(t, handler, http.MethodGet, target)
		// path.Clean neutralizes dot segments; the response may only ever be
		// the SPA shell or a miss, never a file outside dist.
		spa := rec.Code == http.StatusOK && rec.Body.String() == "<!doctype html><title>codex-lb</title>"
		miss := rec.Code == http.StatusNotFound || rec.Code == http.StatusSeeOther || rec.Code == http.StatusMovedPermanently
		if !spa && !miss {
			t.Fatalf("GET %s: status = %d, body = %q; escaped dist", target, rec.Code, rec.Body.String())
		}
	}
}

func TestHashedAssetsImmutableOtherFilesNot(t *testing.T) {
	handler := New(testFS())

	cases := []struct {
		target string
		cache  string
		ctype  string
	}{
		{"/assets/app-HASH.js", "public, max-age=31536000, immutable", "text/javascript; charset=utf-8"},
		{"/assets/app-HASH.css", "public, max-age=31536000, immutable", "text/css; charset=utf-8"},
		{"/favicon.svg", "", "image/svg+xml"},
		{"/fonts/app.woff2", "", "font/woff2"},
	}
	for _, tc := range cases {
		rec := do(t, handler, http.MethodGet, tc.target)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: status = %d, want 200", tc.target, rec.Code)
		}
		if got := rec.Header().Get("Cache-Control"); got != tc.cache {
			t.Fatalf("GET %s: cache-control = %q, want %q", tc.target, got, tc.cache)
		}
		if got := rec.Header().Get("Content-Type"); got != tc.ctype {
			t.Fatalf("GET %s: content-type = %q, want %q", tc.target, got, tc.ctype)
		}
	}
}

func TestMissingAssetStays404AndMethodsRestricted(t *testing.T) {
	handler := New(testFS())

	if rec := do(t, handler, http.MethodGet, "/assets/missing.js"); rec.Code != http.StatusNotFound {
		t.Fatalf("missing asset: status = %d, want 404", rec.Code)
	}
	if rec := do(t, handler, http.MethodPost, "/dashboard"); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST client route: status = %d, want 405", rec.Code)
	}
	if rec := do(t, handler, http.MethodHead, "/dashboard"); rec.Code != http.StatusOK {
		t.Fatalf("HEAD client route: status = %d, want 200", rec.Code)
	}
}

func TestEmbeddedDistServesIndex(t *testing.T) {
	rec := do(t, Handler(), http.MethodGet, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("embedded handler status = %d, want 200", rec.Code)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("embedded index.html must not be cached")
	}
}
