package upstream

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
)

func (a *HTTPAdapter) beginStream(ctx context.Context, target Target, endpoint string, body []byte) (*http.Response, Result, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, Result{}, &Error{Code: ErrorCodeInvalidConfiguration, Message: "invalid upstream endpoint: " + err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if target.Credential != "" {
		req.Header.Set("Authorization", "Bearer "+target.Credential)
	}
	copyAllowedTargetHeaders(req.Header, target.Headers)
	response, err := a.client.Do(req)
	if err != nil {
		return nil, Result{}, wrapContext(ctx, &Error{Code: ErrorCodeConnection, Message: err.Error()})
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		payload, readErr := io.ReadAll(io.LimitReader(response.Body, maxErrorBodyBytes+1))
		response.Body.Close()
		if readErr != nil {
			result, _ := rejectedResult(response.StatusCode, payload, target.Capabilities.Protocol)
			return nil, result, wrapContext(ctx, &Error{Code: ErrorCodeConnection, Message: "upstream rejection body read failed"})
		}
		if int64(len(payload)) > maxErrorBodyBytes {
			return nil, Result{}, &Error{Code: ErrorCodeConnection, Message: "upstream error body exceeds bounded read limit"}
		}
		result, failure := rejectedHTTPResult(response.StatusCode, payload, target.Capabilities.Protocol)
		return nil, result, failure
	}
	contentType := strings.TrimSpace(strings.ToLower(response.Header.Get("Content-Type")))
	missingAllowed := contentType == "" && target.Capabilities.AllowMissingSSEContentType
	if !missingAllowed && !strings.HasPrefix(contentType, "text/event-stream") {
		response.Body.Close()
		return nil, Result{}, &Error{Code: ErrorCodeInvalidStream, Message: "upstream stream Content-Type is not text/event-stream"}
	}
	return response, Result{}, nil
}
