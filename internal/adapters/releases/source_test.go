package releases

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"codex-lb/internal/domain"
)

type stubResponse struct {
	status int
	header map[string]string
	body   io.Reader
}

type stubRequest struct {
	url           string
	authorization string
	cookie        string
}

type stubTransport struct {
	mu        sync.Mutex
	responses map[string]stubResponse
	requests  []stubRequest
}

func (t *stubTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.requests = append(t.requests, stubRequest{
		url:           req.URL.String(),
		authorization: req.Header.Get("Authorization"),
		cookie:        req.Header.Get("Cookie"),
	})
	response, ok := t.responses[req.URL.String()]
	if !ok {
		return nil, fmt.Errorf("unexpected request %s", req.URL)
	}
	header := http.Header{}
	for key, value := range response.header {
		header.Set(key, value)
	}
	return &http.Response{
		Status:     fmt.Sprintf("%d %s", response.status, http.StatusText(response.status)),
		StatusCode: response.status,
		Header:     header,
		Body:       io.NopCloser(response.body),
		Request:    req,
	}, nil
}

func (t *stubTransport) recorded() []stubRequest {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]stubRequest(nil), t.requests...)
}

type repeatReader struct {
	byteValue byte
	remaining int64
}

func (r *repeatReader) Read(p []byte) (int, error) {
	if r.remaining <= 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	for i := range p {
		p[i] = r.byteValue
	}
	r.remaining -= int64(len(p))
	return len(p), nil
}

func newTestSource(transport *stubTransport) *Source {
	return New(&http.Client{Transport: transport}, "linux", "amd64")
}

func testRelease(tag string) domain.RuntimeRelease {
	return New(nil, "linux", "amd64").expectedRelease(tag)
}

func releasesJSON(releases ...string) io.Reader {
	return strings.NewReader("[" + strings.Join(releases, ",") + "]")
}

func releaseItem(tag string, draft, prerelease bool, assets ...string) string {
	return fmt.Sprintf(`{"tag_name":%q,"draft":%t,"prerelease":%t,"html_url":"https://evil.example/%s","assets":[%s]}`,
		tag, draft, prerelease, tag, strings.Join(assets, ","))
}

func assetJSON(name, url string) string {
	return fmt.Sprintf(`{"name":%q,"browser_download_url":%q}`, name, url)
}

func platformArchive(tag, goos, goarch string) string {
	name := "codex-lb-" + tag + "-" + goos + "-" + goarch + ".tar.gz"
	return assetJSON(name, "https://github.com/Dirard/codex-lb/releases/download/"+tag+"/"+name)
}

func platformChecksums(tag string) string {
	return assetJSON(checksumName, "https://github.com/Dirard/codex-lb/releases/download/"+tag+"/"+checksumName)
}

func TestLatestSelectsHighestCompleteStableRelease(t *testing.T) {
	transport := &stubTransport{responses: map[string]stubResponse{
		releaseAPIURL: {
			status: http.StatusOK,
			header: map[string]string{"Content-Type": "application/json"},
			body: releasesJSON(
				releaseItem("go-v1.2.0", false, false, platformArchive("go-v1.2.0", "linux", "amd64"), platformChecksums("go-v1.2.0")),
				releaseItem("go-v1.4.0", false, false, platformArchive("go-v1.4.0", "linux", "amd64")),
				releaseItem("go-v1.3.0", false, true, platformArchive("go-v1.3.0", "linux", "amd64"), platformChecksums("go-v1.3.0")),
				releaseItem("go-v1.5.0", true, false, platformArchive("go-v1.5.0", "linux", "amd64"), platformChecksums("go-v1.5.0")),
				releaseItem("v9.9.9", false, false, platformArchive("v9.9.9", "linux", "amd64"), platformChecksums("v9.9.9")),
				releaseItem("go-v1.1.0", false, false, platformArchive("go-v1.1.0", "linux", "arm64"), platformChecksums("go-v1.1.0")),
				releaseItem("go-v1.2.8", false, false,
					assetJSON("codex-lb-go-v1.2.8-linux-amd64.tar.gz", "https://evil.example/codex-lb-go-v1.2.8-linux-amd64.tar.gz"),
					platformChecksums("go-v1.2.8")),
				releaseItem("go-v1.2.9", false, false,
					platformArchive("go-v1.2.9", "linux", "amd64"),
					platformArchive("go-v1.2.9", "linux", "amd64"),
					platformChecksums("go-v1.2.9")),
			),
		},
	}}
	release, err := newTestSource(transport).Latest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if expected := testRelease("go-v1.2.0"); release != expected {
		t.Fatalf("latest = %+v; want %+v", release, expected)
	}
	requests := transport.recorded()
	if len(requests) != 1 || requests[0].authorization != "" || requests[0].cookie != "" {
		t.Fatalf("unexpected metadata requests: %+v", requests)
	}
}

func TestLatestRejectsWhenNoCompleteRelease(t *testing.T) {
	for name, body := range map[string]io.Reader{
		"empty":       strings.NewReader("[]"),
		"draft only":  releasesJSON(releaseItem("go-v1.0.0", true, false)),
		"legacy only": releasesJSON(releaseItem("v1.0.0", false, false)),
	} {
		transport := &stubTransport{responses: map[string]stubResponse{
			releaseAPIURL: {status: http.StatusOK, body: body},
		}}
		if _, err := newTestSource(transport).Latest(context.Background()); err == nil || !strings.Contains(err.Error(), "no complete stable release") {
			t.Fatalf("%s: expected missing-release error, got %v", name, err)
		}
	}
}

func TestLatestRejectsHTTPError(t *testing.T) {
	transport := &stubTransport{responses: map[string]stubResponse{
		releaseAPIURL: {status: http.StatusForbidden, body: strings.NewReader(`{"message":"rate limited"}`)},
	}}
	if _, err := newTestSource(transport).Latest(context.Background()); err == nil || !strings.Contains(err.Error(), "HTTP 403") {
		t.Fatalf("expected HTTP failure, got %v", err)
	}
}

func TestLatestRejectsInvalidMetadata(t *testing.T) {
	transport := &stubTransport{responses: map[string]stubResponse{
		releaseAPIURL: {status: http.StatusOK, body: strings.NewReader("not-json")},
	}}
	if _, err := newTestSource(transport).Latest(context.Background()); err == nil || !strings.Contains(err.Error(), "invalid release metadata") {
		t.Fatalf("expected metadata failure, got %v", err)
	}
}

func TestLatestRejectsOversizedMetadata(t *testing.T) {
	transport := &stubTransport{responses: map[string]stubResponse{
		releaseAPIURL: {status: http.StatusOK, body: &repeatReader{byteValue: '{', remaining: metadataLimit + 1}},
	}}
	if _, err := newTestSource(transport).Latest(context.Background()); err == nil || !strings.Contains(err.Error(), "release metadata exceeds") {
		t.Fatalf("expected metadata size failure, got %v", err)
	}
}

func TestLatestRequiresPlatform(t *testing.T) {
	source := New(nil, "", "amd64")
	if _, err := source.Latest(context.Background()); err == nil || !strings.Contains(err.Error(), "platform is not configured") {
		t.Fatalf("expected platform failure, got %v", err)
	}
}

func TestDownloadRejectsDescriptorOutsideFixedOrigin(t *testing.T) {
	transport := &stubTransport{responses: map[string]stubResponse{}}
	tampered := testRelease("go-v1.2.0")
	tampered.ArchiveURL = "https://evil.example/archive.tar.gz"
	if _, err := newTestSource(transport).Download(context.Background(), tampered, t.TempDir()); err == nil ||
		!strings.Contains(err.Error(), "fixed release origin") {
		t.Fatalf("expected fixed-origin failure, got %v", err)
	}
	legacy := domain.RuntimeRelease{Version: "v1.2.0", ArchiveName: "codex-lb-v1.2.0-linux-amd64.tar.gz"}
	if _, err := newTestSource(transport).Download(context.Background(), legacy, t.TempDir()); err == nil ||
		!strings.Contains(err.Error(), "unsupported runtime release version") {
		t.Fatalf("expected legacy-version failure, got %v", err)
	}
	if requests := transport.recorded(); len(requests) != 0 {
		t.Fatalf("invalid descriptors reached the network: %+v", requests)
	}
}
