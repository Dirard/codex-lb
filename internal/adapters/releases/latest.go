package releases

import (
	"context"
	"encoding/json"
	"fmt"

	"codex-lb/internal/domain"
)

type githubRelease struct {
	TagName    string        `json:"tag_name"`
	Draft      bool          `json:"draft"`
	Prerelease bool          `json:"prerelease"`
	Assets     []githubAsset `json:"assets"`
}

type githubAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// Latest returns the highest stable go-vX.Y.Z release that has exactly one
// matching platform archive and one SHA256SUMS asset at the expected GitHub
// download URLs. Drafts, prereleases, legacy tags and incomplete or
// untrusted-URL packages are ignored rather than offered.
func (s *Source) Latest(ctx context.Context) (domain.RuntimeRelease, error) {
	if err := s.validatePlatform(); err != nil {
		return domain.RuntimeRelease{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, metadataTimeout)
	defer cancel()
	body, err := s.fetchBytes(ctx, releaseAPIURL, metadataAccept, metadataLimit, "release metadata")
	if err != nil {
		return domain.RuntimeRelease{}, err
	}
	var items []githubRelease
	if err := json.Unmarshal(body, &items); err != nil {
		return domain.RuntimeRelease{}, fmt.Errorf("invalid release metadata: %w", err)
	}
	return s.selectLatest(items)
}

func (s *Source) selectLatest(items []githubRelease) (domain.RuntimeRelease, error) {
	var latest domain.RuntimeRelease
	var latestVersion [3]uint64
	found := false
	for _, item := range items {
		if item.Draft || item.Prerelease {
			continue
		}
		version, ok := domain.ParseRuntimeVersion(item.TagName)
		if !ok {
			continue
		}
		release, ok := s.completeRelease(item.TagName, item.Assets)
		if !ok {
			continue
		}
		if found && !newerVersion(version, latestVersion) {
			continue
		}
		latest, latestVersion, found = release, version, true
	}
	if !found {
		return domain.RuntimeRelease{}, fmt.Errorf("no complete stable release found for %s/%s", s.goos, s.goarch)
	}
	return latest, nil
}

func (s *Source) completeRelease(tag string, assets []githubAsset) (domain.RuntimeRelease, bool) {
	expected := s.expectedRelease(tag)
	var archiveURL, checksumURL string
	var archives, checksums int
	for _, asset := range assets {
		switch asset.Name {
		case expected.ArchiveName:
			archives++
			archiveURL = asset.BrowserDownloadURL
		case checksumName:
			checksums++
			checksumURL = asset.BrowserDownloadURL
		}
	}
	if archives != 1 || checksums != 1 {
		return domain.RuntimeRelease{}, false
	}
	if archiveURL != expected.ArchiveURL || checksumURL != expected.ChecksumURL {
		return domain.RuntimeRelease{}, false
	}
	return expected, true
}

func newerVersion(candidate, current [3]uint64) bool {
	for i := range candidate {
		if candidate[i] != current[i] {
			return candidate[i] > current[i]
		}
	}
	return false
}
