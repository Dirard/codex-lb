package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

const imageRequestTimeout = 5 * time.Minute

// Images posts a validated Images body unchanged to the subscription images
// endpoint (`<ChatGPT Codex base>/images/generations` or `/images/edits`).
// The subscription decides model, size and quality; the body is not rewritten.
func (a *Adapter) Images(ctx context.Context, target application.CodexOperationTarget, request application.CodexImageRequest) (application.CodexOperationResult, error) {
	if target.Account.Kind != domain.AccountChatGPT {
		return application.CodexOperationResult{}, providerFailure("image_generation_unsupported", 0, false)
	}
	ctx, cancel := context.WithTimeout(ctx, imageRequestTimeout)
	defer cancel()
	path := "/images/generations"
	if request.Edit {
		path = "/images/edits"
	}
	result, err := a.chatGPTBody(ctx, target, http.MethodPost, a.chatGPTBaseURL()+path, request.Body, "application/json", nil, nil)
	if err != nil || result.Failed || result.Status < 200 || result.Status >= 300 {
		return result, err
	}
	if !imageResponseHasData(result.Body) {
		return result, providerFailure("invalid_upstream_response", http.StatusBadGateway, true)
	}
	result.OutputObserved = true
	return result, nil
}

// imageResponseHasData reports whether a successful Images body carries at
// least one image; Codex reads data[0].b64_json.
func imageResponseHasData(body []byte) bool {
	var response struct {
		Data []struct {
			B64JSON *string `json:"b64_json"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &response) != nil {
		return false
	}
	for _, entry := range response.Data {
		if entry.B64JSON != nil && *entry.B64JSON != "" {
			return true
		}
	}
	return false
}
