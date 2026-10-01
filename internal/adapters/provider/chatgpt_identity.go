package provider

import (
	"context"
	"net/http"

	"codex-lb/internal/domain"
)

// chatGPTHeaders snapshots the live client version once per operation so its
// transport headers cannot observe different settings revisions.
func (a *Adapter) chatGPTHeaders(ctx context.Context) (http.Header, error) {
	version := a.config.CodexVersion
	if a.config.ResolveClientVersion != nil {
		var err error
		version, err = a.config.ResolveClientVersion(ctx)
		if err != nil {
			return nil, providerFailure("codex_client_version_unavailable", 0, false)
		}
	}
	if !domain.ValidCodexClientVersion(version) {
		return nil, providerFailure("codex_client_version_unavailable", 0, false)
	}
	headers := http.Header{}
	headers.Set("Originator", "codex_cli_rs")
	headers.Set("Version", version)
	headers.Set("User-Agent", "codex_cli_rs/"+version)
	return headers, nil
}
