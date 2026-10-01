package httpapi

import "codex-lb/internal/application"

func normalizeCodexCompactOutput(body []byte) []byte {
	normalized, _ := application.NormalizeCodexCompactOutput(body)
	return normalized
}
