package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"

	"codex-lb/internal/adapters/upstream"
	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

const maxOperationResponseBytes = 32 << 20

var _ application.CodexOperationProvider = (*Adapter)(nil)

func (a *Adapter) Control(ctx context.Context, target application.CodexOperationTarget, request application.CodexControlRequest) (application.CodexOperationResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	endpoint := a.chatGPTBaseURL() + "/" + strings.Trim(request.Path, "/")
	if query := encodeQuery(request.Query); query != "" {
		endpoint += "?" + query
	}
	if request.Path == "realtime/calls" {
		if err := request.RealtimeHeaders.Validate(); err != nil {
			return application.CodexOperationResult{}, err
		}
		return a.chatGPTBody(ctx, target, request.Method, endpoint, request.Body, request.ContentType, realtimeProtocolHeaders(request.RealtimeHeaders), nil)
	}
	return a.chatGPTOperation(ctx, target, request.Method, endpoint, request.Body, request.ContentType)
}

func (a *Adapter) CreateFile(ctx context.Context, target application.CodexOperationTarget, body json.RawMessage) (application.CodexOperationResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return a.chatGPTOperation(ctx, target, http.MethodPost, a.fileBaseURL()+"/files", body, "application/json")
}

func (a *Adapter) FinalizeFile(ctx context.Context, target application.CodexOperationTarget, fileID string) (application.CodexOperationResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	endpoint := a.fileBaseURL() + "/files/" + url.PathEscape(fileID) + "/uploaded"
	deadline := time.Now().Add(30 * time.Second)
	for {
		result, err := a.chatGPTOperation(ctx, target, http.MethodPost, endpoint, json.RawMessage("{}"), "application/json")
		if err != nil || result.Failed {
			return result, err
		}
		var payload struct {
			Status string `json:"status"`
		}
		if json.Unmarshal(result.Body, &payload) != nil || payload.Status != "retry" || time.Now().After(deadline) {
			return result, nil
		}
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func (a *Adapter) Transcribe(ctx context.Context, target application.CodexOperationTarget, request application.CodexTranscriptionRequest) (application.CodexOperationResult, error) {
	if target.Account.Kind == domain.AccountExternal {
		source, credential, err := a.externalTarget(ctx, target.Account)
		if err != nil {
			return application.CodexOperationResult{}, err
		}
		if !source.Enabled || !source.Audio {
			return application.CodexOperationResult{}, providerFailure("audio_transcription_unsupported", 0, false)
		}
		sourceModel, ok := source.Model(request.Model)
		if !ok {
			return application.CodexOperationResult{}, providerFailure("model_source_model_unavailable", 0, false)
		}
		ctx, release, err := a.beginSource(ctx, source)
		if err != nil {
			return application.CodexOperationResult{}, err
		}
		defer release()
		ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
		defer cancel()
		body, contentType, err := transcriptionForm(request, true)
		if err != nil {
			return application.CodexOperationResult{}, err
		}
		headers := http.Header{}
		headers.Set("Authorization", "Bearer "+credential)
		result, err := a.operation(ctx, http.MethodPost, strings.TrimSuffix(source.BaseURL, "/")+"/audio/transcriptions", body, contentType, headers, nil)
		result, err = transcriptionResult(result, err)
		known := result.UsageKnown
		if sourceModel.AudioPerMinute != nil {
			known = result.AudioSecondsKnown
		}
		if err == nil && !known && (!result.Failed || reportedOperationBilling(result.Body)) {
			return result, providerFailure("usage_unavailable", 502, true)
		}
		return result, err
	}

	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	body, contentType, err := transcriptionForm(request, false)
	if err != nil {
		return application.CodexOperationResult{}, err
	}
	result, err := a.chatGPTBody(ctx, target, http.MethodPost, a.fileBaseURL()+"/transcribe", body, contentType, nil, nil)
	result, err = transcriptionResult(result, err)
	if err == nil && !result.Failed && !result.UsageKnown {
		// Subscription voice returns text without token billing; only a valid
		// successful transcript establishes that zero-token result.
		result.UsageKnown = true
	}
	return result, err
}

func (a *Adapter) chatGPTOperation(ctx context.Context, target application.CodexOperationTarget, method, endpoint string, body []byte, contentType string) (application.CodexOperationResult, error) {
	return a.chatGPTBody(ctx, target, method, endpoint, body, contentType, nil, nil)
}

func (a *Adapter) chatGPTBody(ctx context.Context, target application.CodexOperationTarget, method, endpoint string, body []byte, contentType string, extra http.Header, readSuccess func(*http.Response) (application.CodexOperationResult, error)) (application.CodexOperationResult, error) {
	credential, err := a.credentialForAccount(ctx, target.Account)
	if err != nil {
		return application.CodexOperationResult{}, providerFailure("chatgpt_credential_unavailable", 0, false)
	}
	token, err := a.chatgpt.AccessToken(ctx, target.Account, credential)
	if err != nil {
		return application.CodexOperationResult{}, providerFailure("chatgpt_token_unavailable", 0, false)
	}
	headers, err := a.chatGPTHeaders(ctx)
	if err != nil {
		return application.CodexOperationResult{}, err
	}
	headers.Set("Authorization", "Bearer "+token)
	if target.Account.ChatGPTAccountID != "" {
		headers.Set("ChatGPT-Account-ID", target.Account.ChatGPTAccountID)
	}
	if target.SessionID != "" {
		headers.Set("Session_id", target.SessionID)
	}
	if target.TurnState != "" {
		headers.Set("X-Codex-Turn-State", target.TurnState)
	}
	for name, values := range extra {
		for _, value := range values {
			headers.Add(name, value)
		}
	}
	result, err := a.operation(ctx, method, endpoint, body, contentType, headers, readSuccess)
	if err != nil || result.Status != http.StatusUnauthorized || !unchargedOperation(result) {
		return result, err
	}
	refreshed, refreshErr := a.chatgpt.ForceRefresh(ctx, target.Account, token)
	if refreshErr != nil {
		return result, providerFailure("chatgpt_token_unavailable", 0, false)
	}
	headers.Set("Authorization", "Bearer "+refreshed)
	return a.operation(ctx, method, endpoint, body, contentType, headers, readSuccess)
}

func (a *Adapter) operation(ctx context.Context, method, endpoint string, body []byte, contentType string, headers http.Header, readSuccess func(*http.Response) (application.CodexOperationResult, error)) (application.CodexOperationResult, error) {
	request, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return application.CodexOperationResult{}, providerFailure("invalid_upstream_endpoint", 0, false)
	}
	for name, values := range headers {
		for _, value := range values {
			request.Header.Add(name, value)
		}
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	if request.Header.Get("Accept") == "" {
		request.Header.Set("Accept", "*/*")
	}
	response, err := a.config.HTTPClient.Do(request)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return application.CodexOperationResult{}, err
		}
		return application.CodexOperationResult{}, providerFailure("upstream_unavailable", 502, true)
	}
	defer response.Body.Close()
	reader := readOperationResponse
	if readSuccess != nil && response.StatusCode >= 200 && response.StatusCode < 300 {
		reader = readSuccess
	}
	result, err := reader(response)
	if credential := headers.Get("Authorization"); strings.HasPrefix(credential, "Bearer ") {
		token := strings.TrimPrefix(credential, "Bearer ")
		result.Body = redactCredential(result.Body, token)
		result.ErrorCode = string(redactCredential([]byte(result.ErrorCode), token))
	}
	if err != nil && ctx.Err() != nil {
		err = ctx.Err()
	}
	return result, err
}

func readOperationResponse(response *http.Response) (application.CodexOperationResult, error) {
	payload, readErr := io.ReadAll(io.LimitReader(response.Body, maxOperationResponseBytes+1))
	if readErr != nil {
		return application.CodexOperationResult{}, providerFailure("upstream_unavailable", 502, true)
	}
	if len(payload) > maxOperationResponseBytes {
		return application.CodexOperationResult{}, providerFailure("upstream_response_too_large", 502, true)
	}
	return operationResult(response, payload), nil
}

func operationResult(response *http.Response, payload []byte) application.CodexOperationResult {
	result := application.CodexOperationResult{
		Status: response.StatusCode, Body: payload, ContentType: response.Header.Get("Content-Type"),
		Headers: map[string][]string(response.Header.Clone()),
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		result.Failed = true
		result.ErrorCode = upstreamHTTPCode(response.StatusCode, payload)
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(payload, &object) == nil && object != nil {
		result.OutputObserved = compactHasOutput(payload)
		result.UsageReported = reportedOperationBilling(payload)
		if len(object["error"]) != 0 && string(object["error"]) != "null" {
			result.Failed, result.ErrorCode = true, upstreamHTTPCode(response.StatusCode, payload)
		}
		usage, valid := operationUsage(object)
		result.Usage = usage
		result.UsageKnown = valid && nativeChatUsageKnown(object)
		tier, tierValid := operationServiceTier(object["service_tier"])
		if tierValid {
			result.ServiceTier = tier
		}
		if !valid || !tierValid {
			result.UsageKnown = false
			result.Failed = true
			if result.ErrorCode == "" {
				result.ErrorCode = "invalid_upstream_usage"
			}
			result.Status = http.StatusBadGateway
			return result
		}
	}
	return result
}

func transcriptionResult(result application.CodexOperationResult, err error) (application.CodexOperationResult, error) {
	if err != nil {
		return result, err
	}
	var object struct {
		Text     *string  `json:"text"`
		Duration *float64 `json:"duration"`
		Usage    *struct {
			Seconds  *float64 `json:"seconds"`
			Duration *float64 `json:"duration"`
		} `json:"usage"`
	}
	if json.Unmarshal(result.Body, &object) != nil || !result.Failed && object.Text == nil {
		if result.Failed && !reportedOperationBilling(result.Body) {
			return result, nil
		}
		return result, providerFailure("invalid_upstream_response", 502, true)
	}
	durations := []*float64{object.Duration}
	if object.Usage != nil {
		durations = append(durations, object.Usage.Seconds, object.Usage.Duration)
	}
	for _, duration := range durations {
		if duration == nil {
			continue
		}
		if *duration < 0 || math.IsNaN(*duration) || math.IsInf(*duration, 0) {
			result.AudioSecondsKnown = false
			return result, providerFailure("invalid_upstream_usage", 502, true)
		}
		if !result.AudioSecondsKnown {
			result.AudioSeconds, result.AudioSecondsKnown = *duration, true
		}
	}
	return result, nil
}

func reportedOperationBilling(body []byte) bool {
	var object map[string]json.RawMessage
	if json.Unmarshal(body, &object) != nil {
		return false
	}
	return nativeUsagePresent(object) || len(object["duration"]) > 0 && string(object["duration"]) != "null"
}

func unchargedOperation(result application.CodexOperationResult) bool {
	if result.OutputObserved {
		return false
	}
	if !reportedOperationBilling(result.Body) {
		return true
	}
	var object map[string]json.RawMessage
	_ = json.Unmarshal(result.Body, &object)
	// Duration-bearing failures are not token-only authentication rejections.
	return result.UsageKnown && result.Usage.InputTokens == 0 && result.Usage.OutputTokens == 0 &&
		(len(object["duration"]) == 0 || string(object["duration"]) == "null")
}

func transcriptionForm(request application.CodexTranscriptionRequest, includeModel bool) ([]byte, string, error) {
	if len(request.Audio) == 0 || len(request.Audio) > 25_000_000 {
		return nil, "", providerFailure("invalid_audio_size", 0, false)
	}
	buffer := &bytes.Buffer{}
	form := multipart.NewWriter(buffer)
	filename := strings.TrimSpace(request.Filename)
	if filename == "" {
		filename = "audio.wav"
	}
	contentType := strings.TrimSpace(request.ContentType)
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	file, err := form.CreateFormFile("file", filename)
	if err != nil {
		return nil, "", providerFailure("invalid_audio_form", 0, false)
	}
	if _, err = file.Write(request.Audio); err != nil {
		return nil, "", providerFailure("invalid_audio_form", 0, false)
	}
	if includeModel {
		_ = form.WriteField("model", request.Model)
	}
	if request.Prompt != "" {
		_ = form.WriteField("prompt", request.Prompt)
	}
	for _, field := range request.Fields {
		if field[0] == "" || field[0] == "file" || field[0] == "model" || field[0] == "prompt" {
			continue
		}
		_ = form.WriteField(field[0], field[1])
	}
	if err = form.Close(); err != nil {
		return nil, "", providerFailure("invalid_audio_form", 0, false)
	}
	return buffer.Bytes(), form.FormDataContentType(), nil
}

func (a *Adapter) externalTarget(ctx context.Context, account domain.Account) (domain.ModelSource, string, error) {
	source, err := a.store.GetModelSource(ctx, account.ID)
	if err != nil {
		return domain.ModelSource{}, "", providerFailure("model_source_unavailable", 0, false)
	}
	credential, err := a.credentialForAccount(ctx, account)
	if err != nil {
		return domain.ModelSource{}, "", providerFailure("model_source_credential_unavailable", 0, false)
	}
	key, err := a.plainCredential(credential.ExternalKeyEncrypted)
	if err != nil {
		return domain.ModelSource{}, "", err
	}
	return source, key, nil
}

func upstreamHTTPCode(status int, body []byte) string {
	var payload struct {
		Error struct {
			Code string `json:"code"`
			Type string `json:"type"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &payload)
	code := payload.Error.Code
	if code == "" {
		code = payload.Error.Type
	}
	if code == "" {
		code = fmt.Sprintf("http_%d", status)
	}
	if status == http.StatusTooManyRequests {
		switch strings.ToLower(code) {
		case "1308", "insufficient_quota", "usage_limit_reached", "usage_limit_exceeded":
			return upstream.ErrorCodeInsufficientQuota
		default:
			return upstream.ErrorCodeRateLimited
		}
	}
	return code
}

func encodeQuery(values [][2]string) string {
	result := &strings.Builder{}
	for _, pair := range values {
		if result.Len() != 0 {
			result.WriteByte('&')
		}
		result.WriteString(url.QueryEscape(pair[0]) + "=" + url.QueryEscape(pair[1]))
	}
	return result.String()
}

func (a *Adapter) chatGPTBaseURL() string { return strings.TrimSuffix(a.config.ChatGPTBaseURL, "/") }
func (a *Adapter) fileBaseURL() string    { return strings.TrimSuffix(a.chatGPTBaseURL(), "/codex") }
