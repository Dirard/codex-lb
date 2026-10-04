// Package releases discovers and downloads verified runtime release
// artifacts from the fixed GitHub origin Dirard/codex-lb.
package releases

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

const (
	releaseAPIURL  = "https://api.github.com/repos/Dirard/codex-lb/releases?per_page=100"
	releaseBaseURL = "https://github.com/Dirard/codex-lb/releases"
	archivePrefix  = "codex-lb-"
	archiveSuffix  = ".tar.gz"
	checksumName   = "SHA256SUMS"
	binaryName     = "codex-lb"
	updaterAgent   = "codex-lb-runtime-updater"

	metadataLimit   = 2 << 20
	checksumLimit   = 64 << 10
	archiveLimit    = 128 << 20
	binaryLimit     = 256 << 20
	auxiliaryLimit  = 64 << 10
	metadataTimeout = 30 * time.Second
	checksumTimeout = 30 * time.Second
	archiveTimeout  = 10 * time.Minute

	metadataAccept = "application/vnd.github+json"
	binaryAccept   = "application/octet-stream"
)

var allowedRedirectHosts = map[string]struct{}{
	"github.com":                           {},
	"release-assets.githubusercontent.com": {},
	"objects.githubusercontent.com":        {},
}

// Source implements application.RuntimeReleaseSource against GitHub releases.
// It accepts no credentials, never executes release contents and downloads
// only from the fixed origin and its GitHub asset hosts.
type Source struct {
	client *http.Client
	goos   string
	goarch string
}

var _ application.RuntimeReleaseSource = (*Source)(nil)

// New returns a Source for goos/goarch archives. A nil client becomes a
// credential-free default client with GitHub-only HTTPS redirect checks.
func New(client *http.Client, goos, goarch string) *Source {
	if client == nil {
		client = &http.Client{}
	}
	owned := *client
	owned.Jar = nil
	owned.CheckRedirect = checkRedirect
	return &Source{client: &owned, goos: goos, goarch: goarch}
}

func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 || req.URL.User != nil {
		return fmt.Errorf("release redirect is excessive or contains credentials")
	}
	for _, header := range []string{"Authorization", "Cookie", "Proxy-Authorization", "Www-Authenticate"} {
		req.Header.Del(header)
	}
	if req.URL.Scheme != "https" {
		return fmt.Errorf("release redirect uses non-HTTPS URL %q", req.URL.Redacted())
	}
	host := req.URL.Hostname()
	if _, ok := allowedRedirectHosts[host]; !ok {
		return fmt.Errorf("release redirect uses untrusted host %q", host)
	}
	if port := req.URL.Port(); port != "" && port != "443" {
		return fmt.Errorf("release redirect uses nonstandard port %q", port)
	}
	return nil
}

func (s *Source) validatePlatform() error {
	if s.goos == "" || s.goarch == "" {
		return fmt.Errorf("runtime release platform is not configured")
	}
	return nil
}

func (s *Source) expectedRelease(tag string) domain.RuntimeRelease {
	archiveName := archivePrefix + tag + "-" + s.goos + "-" + s.goarch + archiveSuffix
	downloadBase := releaseBaseURL + "/download/" + tag + "/"
	return domain.RuntimeRelease{
		Version:     tag,
		ArchiveName: archiveName,
		ArchiveURL:  downloadBase + archiveName,
		ChecksumURL: downloadBase + checksumName,
		ReleaseURL:  releaseBaseURL + "/tag/" + tag,
	}
}

func (s *Source) validateRelease(release domain.RuntimeRelease) (domain.RuntimeRelease, error) {
	if err := s.validatePlatform(); err != nil {
		return domain.RuntimeRelease{}, err
	}
	if _, ok := domain.ParseRuntimeVersion(release.Version); !ok {
		return domain.RuntimeRelease{}, fmt.Errorf("unsupported runtime release version %q", release.Version)
	}
	expected := s.expectedRelease(release.Version)
	if release != expected {
		return domain.RuntimeRelease{}, fmt.Errorf("runtime release descriptor does not match the fixed release origin")
	}
	return expected, nil
}

func newReleaseRequest(ctx context.Context, rawURL, accept string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", updaterAgent)
	return req, nil
}

func (s *Source) fetchBytes(ctx context.Context, rawURL, accept string, limit int64, description string) ([]byte, error) {
	req, err := newReleaseRequest(ctx, rawURL, accept)
	if err != nil {
		return nil, fmt.Errorf("invalid %s request: %w", description, err)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s download failed: %w", description, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s download returned HTTP %d", description, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("invalid %s response: %w", description, err)
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("%s exceeds %d bytes", description, limit)
	}
	return body, nil
}
