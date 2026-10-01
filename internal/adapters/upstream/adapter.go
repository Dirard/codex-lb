package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const maxErrorBodyBytes = 1 << 20

type Adapter interface {
	Execute(ctx context.Context, target Target, request Request) (Result, error)
	OpenStream(ctx context.Context, target Target, request Request, emit func(Event) error) (Result, error)
}

type HTTPAdapter struct {
	client   *http.Client
	store    *ContinuationStore
	sessions websocketSessions
}

func (a *HTTPAdapter) Close() error {
	a.sessions.close()
	a.client.CloseIdleConnections()
	return nil
}

func New(client *http.Client, continuations *ContinuationStore) *HTTPAdapter {
	if client == nil {
		client = &http.Client{}
	}
	// A provider redirect must not carry an account token or replay a request
	// body to a different endpoint, even when called outside the server runtime.
	ownedClient := *client
	ownedClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	client = &ownedClient
	if continuations == nil {
		continuations = NewContinuationStore()
	}
	return &HTTPAdapter{client: client, store: continuations}
}

func (a *HTTPAdapter) Execute(ctx context.Context, target Target, request Request) (Result, error) {
	prepared, err := prepareTarget(target)
	if err != nil {
		return Result{}, err
	}
	req, items, catalog, err := prepareRequest(request, prepared)
	if err != nil {
		return Result{}, err
	}
	if req.Stream {
		return Result{}, &Error{Code: ErrorCodeInvalidRequest, Message: "streaming requests must use OpenStream"}
	}
	if prepared.Capabilities.Protocol == ProtocolResponses {
		if prepared.Capabilities.StreamTransport == TransportWebSocket {
			return Result{}, &Error{Code: ErrorCodeInvalidRequest, Message: "WebSocket transport is stream-only"}
		}
		return a.executeResponses(ctx, prepared, req, catalog)
	}
	return a.executeChat(ctx, prepared, req, items, catalog)
}

func (a *HTTPAdapter) OpenStream(ctx context.Context, target Target, request Request, emit func(Event) error) (Result, error) {
	if emit == nil {
		return Result{}, &Error{Code: ErrorCodeInvalidRequest, Message: "stream callback is required"}
	}
	prepared, err := prepareTarget(target)
	if err != nil {
		return Result{}, err
	}
	req, items, catalog, err := prepareRequest(request, prepared)
	if err != nil {
		return Result{}, err
	}
	if !req.Stream {
		return Result{}, &Error{Code: ErrorCodeInvalidRequest, Message: "OpenStream requires stream=true"}
	}
	if prepared.Capabilities.Protocol == ProtocolResponses {
		if prepared.Capabilities.StreamTransport == TransportWebSocket {
			return a.streamResponsesWebSocket(ctx, prepared, req, catalog, emit)
		}
		return a.streamResponses(ctx, prepared, req, catalog, emit)
	}
	return a.streamChat(ctx, prepared, req, items, catalog, emit)
}

func prepareTarget(target Target) (Target, error) {
	capabilities, err := target.Capabilities.Normalized()
	if err != nil {
		return target, err
	}
	target.Capabilities = capabilities
	if target.ProviderID == "" || target.AccountID == "" || target.KeyID == "" {
		return target, &Error{Code: ErrorCodeInvalidConfiguration, Message: "provider, account, and key identifiers are required for state isolation"}
	}
	parsed, err := url.Parse(target.BaseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
		return target, &Error{Code: ErrorCodeInvalidConfiguration, Message: "base URL must be an absolute HTTP URL without query or fragment"}
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return target, &Error{Code: ErrorCodeInvalidConfiguration, Message: "base URL must use HTTP or HTTPS"}
	}
	return target, nil
}

func prepareRequest(request Request, target Target) (responsesWireRequest, []responsesItem, toolCatalog, error) {
	req, err := decodeResponsesRequest(request.Body, target.Capabilities.MaxRequestBodyBytes)
	if err != nil {
		return req, nil, toolCatalog{}, err
	}
	items, err := req.items()
	if err != nil {
		return req, nil, toolCatalog{}, err
	}
	if err := validateRequestCapabilities(req, items, target.Capabilities); err != nil {
		return req, nil, toolCatalog{}, err
	}
	if target.Capabilities.Protocol == ProtocolResponses {
		// Responses forwards Raw; parsed payloads were needed only for validation.
		// Do not carry their full copies into a potentially long HTTP/WS call.
		req.Input, req.Tools, req.System, req.Instructions = nil, nil, "", ""
		return req, nil, toolCatalog{}, nil
	}
	catalog, err := newToolCatalog(req, target.Capabilities)
	if err != nil {
		return req, nil, toolCatalog{}, err
	}
	return req, items, catalog, nil
}

func (a *HTTPAdapter) executeResponses(ctx context.Context, target Target, req responsesWireRequest, catalog toolCatalog) (Result, error) {
	_ = catalog
	body, err := passthroughBody(req, target.Capabilities)
	if err != nil {
		return Result{}, err
	}
	responseBody, status, err := a.do(ctx, target, http.MethodPost, endpoint(target.BaseURL, "responses"), body, "application/json")
	if err != nil {
		return Result{}, err
	}
	if status != http.StatusOK {
		return rejectedHTTPResult(status, responseBody, target.Capabilities.Protocol)
	}
	var payload struct {
		ID          string          `json:"id"`
		Status      string          `json:"status"`
		Error       *wireError      `json:"error"`
		WireUsage   json.RawMessage `json:"usage"`
		ServiceTier string          `json:"service_tier"`
	}
	if err := decodeExactJSON(responseBody, &payload); err != nil {
		return Result{}, err
	}
	if payload.ID == "" {
		return Result{}, &Error{Code: ErrorCodeInvalidStream, Message: "upstream Responses payload omitted id"}
	}
	result := Result{ResponseID: payload.ID, Response: responseBody, ServiceTier: payload.ServiceTier}
	result.readUsage(payload.WireUsage, ProtocolResponses)
	if payload.Status == "failed" || payload.Status == "incomplete" || payload.Error != nil {
		result.Failed = true
		if payload.Error != nil {
			result.ErrorCode = wireErrorCode(payload.Error)
			result.ErrorMessage = payload.Error.Message
		}
	}
	if result.UsageReported && !result.UsageKnown {
		return result, &Error{Code: ErrorCodeInvalidStream, Message: "Responses payload contains partial usage"}
	}
	if !result.UsageKnown && !result.Failed && !target.Capabilities.AllowMissingUsage {
		return result, &Error{Code: ErrorCodeMissingUsage, Message: "Responses payload omitted valid terminal usage"}
	}
	return result, nil
}

func (a *HTTPAdapter) executeChat(ctx context.Context, target Target, req responsesWireRequest, items []responsesItem, catalog toolCatalog) (Result, error) {
	state, err := a.loadContinuation(target, req)
	if err != nil {
		return Result{}, err
	}
	chat, requestMessages, err := translateToChat(req, items, state, catalog, target.Capabilities, false)
	if err != nil {
		return Result{}, err
	}
	body, err := mapRequestBody(chat, target.Capabilities)
	if err != nil {
		return Result{}, err
	}
	responseBody, status, err := a.do(ctx, target, http.MethodPost, endpoint(target.BaseURL, "chat/completions"), body, "application/json")
	if err != nil {
		return Result{}, err
	}
	if status != http.StatusOK {
		return rejectedHTTPResult(status, responseBody, target.Capabilities.Protocol)
	}
	var chatResponse chatWireResponse
	if err := decodeExactJSON(responseBody, &chatResponse); err != nil {
		return Result{}, err
	}
	responseID := newID("resp_")
	result, assistant, err := responsesResponseFromChat(responseID, req.Model, chatResponse, catalog, !target.Capabilities.AllowMissingUsage)
	if err != nil {
		result.Response = responseBody
		return result, err
	}
	if result.UsageKnown {
		if err := a.store.Save(ownerOf(target), responseID, continuationFromTurn(requestMessages, assistant, responseID)); err != nil {
			return result, err
		}
	}
	result.ResponseID = responseID
	return result, nil
}

func rejectedResult(status int, body []byte, protocol Protocol) (Result, error) {
	failure := upstreamHTTPError(status, body)
	result := Result{Response: body, Failed: true, ErrorCode: failure.Code}
	var rejected struct {
		Usage json.RawMessage `json:"usage"`
	}
	if json.Unmarshal(body, &rejected) == nil {
		result.readUsage(rejected.Usage, protocol)
	}
	return result, failure
}

// Stream error events share rejectedResult, but an HTTP validation rejection
// proves the request was not executed. Never infer this from a status alone.
func rejectedHTTPResult(status int, body []byte, protocol Protocol) (Result, error) {
	result, err := rejectedResult(status, body, protocol)
	if status != http.StatusBadRequest && status != http.StatusUnprocessableEntity {
		return result, err
	}
	var object map[string]json.RawMessage
	var rejection struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(body, &object) != nil || json.Unmarshal(object["error"], &rejection) != nil || rejection.Type != "invalid_request_error" {
		return result, err
	}
	for _, field := range []string{"usage", "duration", "response", "id"} {
		if raw := bytes.TrimSpace(object[field]); len(raw) != 0 && !bytes.Equal(raw, []byte("null")) {
			return result, err
		}
	}
	for _, field := range []string{"output", "choices"} {
		var items []json.RawMessage
		if raw := object[field]; len(raw) != 0 && (json.Unmarshal(raw, &items) != nil || len(items) != 0) {
			return result, err
		}
	}
	err.(*Error).RejectedBeforeExecution = true
	return result, err
}

func (a *HTTPAdapter) loadContinuation(target Target, req responsesWireRequest) (Continuation, error) {
	if req.PreviousResponseID == "" {
		return Continuation{}, nil
	}
	state, found, err := a.store.Load(ownerOf(target), req.PreviousResponseID)
	if err != nil {
		return Continuation{}, err
	}
	if !found {
		return Continuation{}, &Error{
			Code:    ErrorCodeContinuationNotFound,
			Message: "previous_response_id is not available in this provider/account/key namespace; resend the full conversation without it",
		}
	}
	return state, nil
}

func (a *HTTPAdapter) do(ctx context.Context, target Target, method string, endpoint string, body []byte, contentType string) ([]byte, int, error) {
	httpReq, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, 0, &Error{Code: ErrorCodeInvalidConfiguration, Message: "invalid upstream endpoint: " + err.Error()}
	}
	httpReq.Header.Set("Content-Type", contentType)
	httpReq.Header.Set("Accept", "application/json")
	if target.Credential != "" {
		httpReq.Header.Set("Authorization", "Bearer "+target.Credential)
	}
	copyAllowedTargetHeaders(httpReq.Header, target.Headers)
	response, err := a.client.Do(httpReq)
	if err != nil {
		return nil, 0, wrapContext(ctx, &Error{Code: ErrorCodeConnection, Message: err.Error()})
	}
	defer response.Body.Close()
	limit := int64(maxErrorBodyBytes)
	if response.StatusCode < 400 {
		limit = 64 << 20
	}
	payload, readErr := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if readErr != nil {
		return nil, response.StatusCode, wrapContext(ctx, &Error{Code: ErrorCodeConnection, Message: readErr.Error()})
	}
	if int64(len(payload)) > limit {
		return nil, response.StatusCode, &Error{Code: ErrorCodeConnection, Message: "upstream response exceeds bounded read limit"}
	}
	return payload, response.StatusCode, nil
}

func decodeExactJSON(body []byte, value any) error {
	if !json.Valid(body) {
		return &Error{Code: ErrorCodeInvalidStream, Message: "upstream returned invalid JSON"}
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(value); err != nil {
		return &Error{Code: ErrorCodeInvalidStream, Message: "invalid upstream JSON: " + err.Error()}
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return &Error{Code: ErrorCodeInvalidStream, Message: "upstream JSON contains trailing data"}
	}
	return nil
}

func passthroughBody(req responsesWireRequest, cap Capabilities) (json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(req.Raw, &object); err != nil || object == nil {
		return nil, &Error{Code: ErrorCodeInvalidConfiguration, Message: "could not reshape Responses request"}
	}
	mapped := cap.ModelMappings[req.Model]
	if mapped != "" {
		object["model"] = mustJSON(mapped)
	}
	for _, field := range cap.DropBodyFields {
		delete(object, field)
	}
	for field, value := range cap.ExtraBody {
		object[field] = value
	}
	if NormalizeWireReasoning(object) || mapped != "" || len(cap.DropBodyFields) != 0 || len(cap.ExtraBody) != 0 {
		return mustJSON(object), nil
	}
	return req.Raw, nil
}

func endpoint(base string, path string) string {
	return strings.TrimSuffix(base, "/") + "/" + path
}

func copyAllowedTargetHeaders(dst http.Header, src http.Header) {
	for name, values := range src {
		if !allowedTargetHeader(name) {
			continue
		}
		for _, value := range values {
			dst.Add(name, value)
		}
	}
}

func allowedTargetHeader(name string) bool {
	lower := strings.ToLower(name)
	return lower == "chatgpt-account-id" || lower == "session_id" ||
		strings.HasPrefix(lower, "codex-") ||
		strings.HasPrefix(lower, "x-codex-") ||
		// Exact first-party Codex fingerprint headers; the provider constructs
		// these and never copies arbitrary inbound client headers.
		lower == "originator" || lower == "version" || lower == "user-agent" ||
		lower == "x-openai-internal-codex-responses-lite" || lower == "x-openai-subagent"
}

func wrapContext(ctx context.Context, err error) error {
	if ctx.Err() == nil {
		return err
	}
	return fmt.Errorf("%w: %w", ctx.Err(), err)
}

func isContextError(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
