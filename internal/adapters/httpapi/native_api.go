package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

type NativeAPIHandler struct {
	store   ProxyRepository
	service *application.NativeAPIService
	trusted []netip.Prefix
}

func NewNativeAPIHandler(store ProxyRepository, service *application.NativeAPIService, trusted []netip.Prefix) *NativeAPIHandler {
	return &NativeAPIHandler{store: store, service: service, trusted: trusted}
}

func RegisterNativeAPIRoutes(mux *http.ServeMux, store ProxyRepository, service *application.NativeAPIService, trusted []netip.Prefix) {
	NewNativeAPIHandler(store, service, trusted).RegisterRoutes(mux)
}

func (h *NativeAPIHandler) RegisterRoutes(mux *http.ServeMux) {
	for _, path := range []string{"/v1/chat/completions", "/v1/chat/completions/{$}", "/v1/embeddings", "/v1/embeddings/{$}"} {
		switch {
		case strings.Contains(path, "chat"):
			mux.HandleFunc("POST "+path, h.chat)
		default:
			mux.HandleFunc("POST "+path, h.embeddings)
		}
	}
}

func (h *NativeAPIHandler) authenticate(r *http.Request) (domain.APIKey, error) {
	return authenticateProxyKey(r, h.store)
}

func (h *NativeAPIHandler) chat(w http.ResponseWriter, r *http.Request) {
	key, err := h.authenticate(r)
	if err != nil {
		writeProxyError(w, err)
		return
	}
	body, err := readProxyBody(w, r)
	if err != nil {
		writeProxyError(w, err)
		return
	}
	var shape struct {
		Stream bool `json:"stream"`
	}
	if json.Unmarshal(body, &shape) != nil {
		writeProxyError(w, &application.ProxyError{Code: "invalid_request", Status: 400, Message: "Invalid Chat Completions request"})
		return
	}
	options := h.options(r, key.ID)
	if shape.Stream {
		h.chatStream(w, r, options, body)
		return
	}
	result, err := h.service.Chat(r.Context(), key.ID, options, body, nil)
	if err != nil {
		writeProxyError(w, err)
		return
	}
	if result.Failed {
		writeNativeFailure(w, result)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(result.Response)
}

func (h *NativeAPIHandler) embeddings(w http.ResponseWriter, r *http.Request) {
	key, err := h.authenticate(r)
	if err != nil {
		writeProxyError(w, err)
		return
	}
	body, err := readProxyBody(w, r)
	if err != nil {
		writeProxyError(w, err)
		return
	}
	result, err := h.service.Embeddings(r.Context(), key.ID, h.options(r, key.ID), body)
	if err != nil {
		writeProxyError(w, err)
		return
	}
	if result.Failed || result.Status < 200 || result.Status >= 300 {
		writeNativeFailure(w, result)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(result.Response)
}

func (h *NativeAPIHandler) options(r *http.Request, keyID string) application.ResponseOptions {
	options := responseOptions(r, keyID)
	options.Transport = "http"
	options.UserAgent = strings.TrimSpace(r.UserAgent())
	if identity := ResolveIdentity(r, h.trusted); identity.IP.IsValid() {
		options.ClientIP = identity.IP.String()
	}
	if fields := strings.Fields(options.UserAgent); len(fields) != 0 {
		options.UserAgentGroup, _, _ = strings.Cut(fields[0], "/")
	}
	return options
}

func (h *NativeAPIHandler) chatStream(w http.ResponseWriter, r *http.Request, options application.ResponseOptions, body json.RawMessage) {
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	controller := http.NewResponseController(w)
	var streamOptions struct {
		IncludeUsage bool `json:"include_usage"`
	}
	var streamOptionsRoot struct {
		StreamOptions json.RawMessage `json:"stream_options"`
	}
	_ = json.Unmarshal(body, &streamOptionsRoot)
	_ = json.Unmarshal(streamOptionsRoot.StreamOptions, &streamOptions)
	translator := newChatStreamTranslator(streamOptions.IncludeUsage)
	started := false
	var writeMu sync.Mutex
	var pendingTerminal []json.RawMessage
	pendingBytes := 0
	terminalStarted := false
	start := func() {
		if started {
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache, no-store")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		started = true
	}
	writeChunk := func(chunk json.RawMessage) error {
		start()
		writeMu.Lock()
		_ = controller.SetWriteDeadline(timeNowAddWriteDeadline())
		_, err := fmt.Fprintf(w, "data: %s\n\n", chunk)
		flushErr := controller.Flush()
		writeMu.Unlock()
		if err != nil {
			return err
		}
		return flushErr
	}
	queueTerminal := func(chunk json.RawMessage) error {
		if len(pendingTerminal) >= 16 || pendingBytes+len(chunk) > 64<<10 {
			return errors.New("native terminal output exceeded bound")
		}
		pendingTerminal = append(pendingTerminal, append(json.RawMessage(nil), chunk...))
		pendingBytes += len(chunk)
		return nil
	}
	send := func(event application.NativeAPIEvent) error {
		if event.Wire == "chat" {
			if event.Type == "chat.done" || nativeChatTerminalChunk(event.Data) {
				terminalStarted = true
			}
			if terminalStarted {
				return queueTerminal(event.Data)
			}
			return writeChunk(event.Data)
		}
		if event.Type == "response.completed" {
			terminalStarted = true
		}
		for _, chunk := range translator.translate(event) {
			if terminalStarted {
				if err := queueTerminal(chunk); err != nil {
					return err
				}
			} else if err := writeChunk(chunk); err != nil {
				return err
			}
		}
		return nil
	}
	type nativeOutcome struct {
		result application.NativeAPIResult
		err    error
	}
	done := make(chan nativeOutcome, 1)
	go func() {
		var outcome nativeOutcome
		defer func() {
			if recover() != nil {
				outcome.err = errors.New("native chat worker failed")
			}
			done <- outcome
		}()
		result, err := h.service.Chat(ctx, options.KeyID, options, body, send)
		if err != nil {
			outcome.err = err
			return
		}
		outcome.result = result
	}()
	for {
		select {
		case <-ctx.Done():
			cancel()
			<-done
			return
		case outcome := <-done:
			err := outcome.err
			cancel()
			if err == nil {
				if outcome.result.Failed {
					if started {
						_ = writeChunk(nativeFailedError(outcome.result))
					} else {
						writeNativeFailure(w, outcome.result)
					}
					return
				}
				for _, chunk := range pendingTerminal {
					if writeChunk(chunk) != nil {
						return
					}
				}
				return
			}
			if !started {
				writeProxyError(w, err)
				return
			}
			_ = writeChunk(nativeStreamError(err))
			return
		}
	}
}

func nativeChatTerminalChunk(data json.RawMessage) bool {
	var chunk struct {
		Choices []struct {
			FinishReason *string `json:"finish_reason"`
		} `json:"choices"`
	}
	if json.Unmarshal(data, &chunk) != nil {
		return false
	}
	for _, choice := range chunk.Choices {
		if choice.FinishReason != nil && *choice.FinishReason != "" {
			return true
		}
	}
	return false
}

func writeNativeFailure(w http.ResponseWriter, result application.NativeAPIResult) {
	status := result.Status
	if status < 400 || status > 599 {
		status = http.StatusBadGateway
	}
	var payload struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(result.Response, &payload) == nil && len(payload.Error) != 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(result.Response)
		return
	}
	code := result.ErrorCode
	if code == "" {
		code = "upstream_error"
	}
	writeError(w, status, code, "Upstream request failed")
}

func nativeStreamError(err error) json.RawMessage {
	_, code, message := proxyError(err)
	return mustNativeJSON(map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func nativeFailedError(result application.NativeAPIResult) json.RawMessage {
	var payload struct {
		Response struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		} `json:"response"`
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(result.Response, &payload)
	code := payload.Response.Error.Code
	if code == "" {
		code = payload.Error.Code
	}
	if code == "" {
		code = result.ErrorCode
	}
	if code == "" {
		code = "upstream_error"
	}
	return mustNativeJSON(map[string]any{"error": map[string]string{"code": code, "message": "Upstream request failed"}})
}

func mustNativeJSON(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`{"error":{"code":"internal_error","message":"encoding failed"}}`)
	}
	return encoded
}

type chatStreamTranslator struct {
	id, model    string
	created      bool
	toolIndexes  map[string]int
	nextTool     int
	includeUsage bool
}

func newChatStreamTranslator(includeUsage bool) *chatStreamTranslator {
	return &chatStreamTranslator{toolIndexes: make(map[string]int), includeUsage: includeUsage}
}

func (t *chatStreamTranslator) translate(event application.NativeAPIEvent) []json.RawMessage {
	var payload map[string]json.RawMessage
	if json.Unmarshal(event.Data, &payload) != nil {
		return nil
	}
	switch event.Type {
	case "response.created":
		var created struct {
			Response struct {
				ID    string `json:"id"`
				Model string `json:"model"`
			} `json:"response"`
		}
		_ = json.Unmarshal(event.Data, &created)
		t.id, t.model, t.created = created.Response.ID, created.Response.Model, true
		return []json.RawMessage{t.chunk(map[string]any{"role": "assistant"}, nil)}
	case "response.output_text.delta":
		var delta struct {
			Delta string `json:"delta"`
		}
		_ = json.Unmarshal(event.Data, &delta)
		return []json.RawMessage{t.chunk(map[string]any{"content": delta.Delta}, nil)}
	case "response.reasoning_text.delta":
		var delta struct {
			Delta string `json:"delta"`
		}
		_ = json.Unmarshal(event.Data, &delta)
		return []json.RawMessage{t.chunk(map[string]any{"reasoning_content": delta.Delta}, nil)}
	case "response.output_item.added":
		var item struct {
			OutputIndex int `json:"output_index"`
			Item        struct {
				ID     string `json:"id"`
				Type   string `json:"type"`
				CallID string `json:"call_id"`
				Name   string `json:"name"`
			} `json:"item"`
		}
		if json.Unmarshal(event.Data, &item) != nil || item.Item.Type != "function_call" {
			return nil
		}
		index := t.toolIndex(fmt.Sprintf("%d", item.OutputIndex))
		if item.Item.ID != "" {
			t.toolIndexes[item.Item.ID] = index
		}
		return []json.RawMessage{t.chunk(map[string]any{"tool_calls": []any{map[string]any{
			"index": index, "id": item.Item.CallID, "type": "function",
			"function": map[string]any{"name": item.Item.Name, "arguments": ""},
		}}}, nil)}
	case "response.function_call_arguments.delta":
		var item struct {
			ItemID      string `json:"item_id"`
			OutputIndex int    `json:"output_index"`
			Delta       string `json:"delta"`
		}
		_ = json.Unmarshal(event.Data, &item)
		key := item.ItemID
		if key == "" {
			key = fmt.Sprintf("%d", item.OutputIndex)
		}
		return []json.RawMessage{t.chunk(map[string]any{"tool_calls": []any{map[string]any{
			"index": t.toolIndex(key), "function": map[string]any{"arguments": item.Delta},
		}}}, nil)}
	case "response.completed":
		finish := "stop"
		if t.nextTool != 0 {
			finish = "tool_calls"
		}
		var completed struct {
			Response struct {
				Usage struct {
					Input        int64 `json:"input_tokens"`
					Output       int64 `json:"output_tokens"`
					InputDetails struct {
						Cached int64 `json:"cached_tokens"`
					} `json:"input_tokens_details"`
					OutputDetails struct {
						Reasoning int64 `json:"reasoning_tokens"`
					} `json:"output_tokens_details"`
				} `json:"usage"`
			} `json:"response"`
		}
		_ = json.Unmarshal(event.Data, &completed)
		result := []json.RawMessage{t.chunk(map[string]any{}, &finish)}
		if t.includeUsage {
			usage := completed.Response.Usage
			counts := domain.UsageAmount{InputTokens: usage.Input, OutputTokens: usage.Output, CachedInputTokens: usage.InputDetails.Cached, ReasoningTokens: usage.OutputDetails.Reasoning}
			result = append(result, mustNativeJSON(map[string]any{"id": t.id, "object": "chat.completion.chunk", "usage": chatUsage(counts), "choices": []any{}}))
		}
		result = append(result, json.RawMessage("[DONE]"))
		return result
	default:
		return nil
	}
}

func (t *chatStreamTranslator) toolIndex(key string) int {
	if index, ok := t.toolIndexes[key]; ok {
		return index
	}
	index := t.nextTool
	t.nextTool++
	t.toolIndexes[key] = index
	return index
}

func (t *chatStreamTranslator) chunk(delta map[string]any, finish *string) json.RawMessage {
	choice := map[string]any{"index": 0, "delta": delta}
	if finish != nil {
		choice["finish_reason"] = *finish
	}
	return mustNativeJSON(map[string]any{
		"id": t.id, "object": "chat.completion.chunk", "choices": []any{choice},
	})
}

func chatUsage(usage domain.UsageAmount) map[string]any {
	return map[string]any{
		"prompt_tokens": usage.InputTokens, "completion_tokens": usage.OutputTokens,
		"total_tokens":              usage.InputTokens + usage.OutputTokens,
		"prompt_tokens_details":     map[string]int64{"cached_tokens": usage.CachedInputTokens},
		"completion_tokens_details": map[string]int64{"reasoning_tokens": usage.ReasoningTokens},
	}
}

func timeNowAddWriteDeadline() time.Time { return time.Now().Add(30 * time.Second) }
