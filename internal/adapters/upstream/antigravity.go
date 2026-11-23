package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	antigravityStreamMethod   = "v1internal:streamGenerateContent?alt=sse"
	antigravityGenerateMethod = "v1internal:generateContent"
	maxAntigravityResponse    = 64 << 20
)

// antigravityFrame is one SSE frame or the generateContent answer:
// {"response":{...},"traceId":...} or {"error":{...}}. An unwrapped response
// object is accepted too.
type antigravityFrame struct {
	Response *antigravityResponse `json:"response"`
	Error    *googleRPCError      `json:"error"`
	antigravityResponse
}

type antigravityResponse struct {
	Candidates    []antigravityCandidate `json:"candidates"`
	UsageMetadata *antigravityUsage      `json:"usageMetadata"`
}

type antigravityCandidate struct {
	Content *antigravityContent `json:"content"`
}

// antigravityUsage is Gemini usageMetadata; absent counters are zero (proto
// JSON omits them), but a frame without promptTokenCount reports no usage.
type antigravityUsage struct {
	PromptTokenCount        *uint64 `json:"promptTokenCount"`
	CandidatesTokenCount    uint64  `json:"candidatesTokenCount"`
	ThoughtsTokenCount      uint64  `json:"thoughtsTokenCount"`
	CachedContentTokenCount uint64  `json:"cachedContentTokenCount"`
}

// chatUsage renders the usage as Chat Completions usage for chatStreamState:
// input = prompt, cached = cachedContent, output = candidates + thoughts,
// reasoning = thoughts.
func (u *antigravityUsage) chatUsage() json.RawMessage {
	if u == nil || u.PromptTokenCount == nil {
		return nil
	}
	output := u.CandidatesTokenCount + u.ThoughtsTokenCount
	return mustJSON(map[string]any{
		"prompt_tokens": *u.PromptTokenCount, "completion_tokens": output,
		"total_tokens":              *u.PromptTokenCount + output,
		"prompt_tokens_details":     map[string]uint64{"cached_tokens": u.CachedContentTokenCount},
		"completion_tokens_details": map[string]uint64{"reasoning_tokens": u.ThoughtsTokenCount},
	})
}

// executeAntigravity answers a non-streaming request through Cloud Code
// v1internal:generateContent with the same Responses result, continuation
// and signatures as a stream.
func (a *HTTPAdapter) executeAntigravity(ctx context.Context, target Target, req responsesWireRequest, items []responsesItem, catalog toolCatalog) (Result, error) {
	plan, err := a.planAntigravity(target, req, items, catalog)
	if err != nil {
		return Result{}, err
	}
	turn := newAntigravityTurn(target, req.Model, catalog, func(Event) error { return nil }, plan.messages)
	response, rejected, err := a.openAntigravity(ctx, target, antigravityGenerateMethod, plan.body, false)
	if err != nil {
		return rejected, err
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxAntigravityResponse+1))
	if err != nil {
		return Result{}, wrapContext(ctx, &Error{Code: ErrorCodeConnection, Message: "upstream response read failed"})
	}
	if len(payload) > maxAntigravityResponse {
		return Result{}, &Error{Code: ErrorCodeConnection, Message: "upstream response exceeds bounded read limit"}
	}
	if err := turn.pushFrame(payload); err != nil {
		return turn.chat.result(), turn.chat.fail(err)
	}
	return a.finishAntigravity(target, plan, turn, false)
}

// streamAntigravity streams a request through Cloud Code
// v1internal:streamGenerateContent and realizes the workflow step "translate
// frames to Responses events" (R5): every frame feeds the shared
// chatStreamState, so events, the final response, tool-call validation and
// the continuation match Chat Completions sources.
func (a *HTTPAdapter) streamAntigravity(ctx context.Context, target Target, req responsesWireRequest, items []responsesItem, catalog toolCatalog, emit func(Event) error) (Result, error) {
	plan, err := a.planAntigravity(target, req, items, catalog)
	if err != nil {
		return Result{}, err
	}
	turn := newAntigravityTurn(target, req.Model, catalog, emit, plan.messages)
	state := turn.chat
	response, rejected, err := a.openAntigravity(ctx, target, antigravityStreamMethod, plan.body, true)
	if err != nil {
		return rejected, state.fail(wrapContext(ctx, err))
	}
	defer response.Body.Close()
	if err := state.emitJSON("response.created", map[string]any{
		"type":     "response.created",
		"response": map[string]any{"id": state.responseID, "status": "in_progress", "model": state.model},
	}); err != nil {
		return state.result(), err
	}
	// Cloud Code sends no [DONE]: a stream that ends cleanly is complete, with
	// or without finishReason.
	err = readSSE(response.Body, func(_ string, data []byte) error {
		if len(bytes.TrimSpace(data)) == 0 {
			return nil
		}
		return turn.pushFrame(data)
	})
	if err != nil {
		return state.result(), state.fail(wrapContext(ctx, err))
	}
	return a.finishAntigravity(target, plan, turn, true)
}

// planAntigravity loads the continuation and translates the request.
func (a *HTTPAdapter) planAntigravity(target Target, req responsesWireRequest, items []responsesItem, catalog toolCatalog) (antigravityPlan, error) {
	if strings.TrimSpace(target.Credential) == "" || strings.TrimSpace(target.AntigravityUserAgent) == "" {
		return antigravityPlan{}, &Error{Code: ErrorCodeInvalidConfiguration, Message: "Antigravity access token and User-Agent are required"}
	}
	state, err := a.loadContinuation(target, req)
	if err != nil {
		return antigravityPlan{}, err
	}
	owner := ownerOf(target)
	return buildAntigravityRequest(target, req, items, state, catalog, func(keys []string) map[string]string {
		return a.store.Signatures(owner, keys)
	})
}

// finishAntigravity validates the completed turn, saves its continuation with
// the replayed and new thought signatures, and emits the terminal events.
func (a *HTTPAdapter) finishAntigravity(target Target, plan antigravityPlan, turn *antigravityTurn, stream bool) (Result, error) {
	state := turn.chat
	if !target.Capabilities.AllowMissingUsage && !state.usageSeen {
		return state.result(), state.fail(&Error{Code: ErrorCodeMissingUsage, Message: "upstream stream omitted terminal usage"})
	}
	if err := state.validateToolCalls(); err != nil {
		return state.result(), state.fail(err)
	}
	terminal, assistant, continuation, err := state.complete(plan.messages)
	if err != nil {
		return state.result(), state.fail(err)
	}
	continuation.Signatures = make(map[string]string, len(plan.signatures)+len(turn.callSigs)+1)
	for key, signature := range plan.signatures {
		continuation.Signatures[key] = signature
	}
	for key, signature := range turn.signatures(plan.history, assistant) {
		continuation.Signatures[key] = signature
	}
	if state.usageSeen {
		if err := a.store.Save(ownerOf(target), state.responseID, continuation); err != nil {
			return terminal, state.fail(err)
		}
	}
	if stream {
		if err := state.emitTerminal(terminal); err != nil {
			return terminal, err
		}
	}
	return terminal, nil
}

// openAntigravity posts the envelope with exactly the Content-Type,
// Authorization and User-Agent headers (owner boundary: no native-only
// headers, no fingerprint emulation). A refusal before the body is
// classified and proven unexecuted.
func (a *HTTPAdapter) openAntigravity(ctx context.Context, target Target, method string, body []byte, stream bool) (*http.Response, Result, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint(target.BaseURL, method), bytes.NewReader(body))
	if err != nil {
		return nil, Result{}, &Error{Code: ErrorCodeInvalidConfiguration, Message: "invalid upstream endpoint: " + err.Error()}
	}
	request.Header = http.Header{
		"Content-Type":  {"application/json"},
		"Authorization": {"Bearer " + target.Credential},
		"User-Agent":    {target.AntigravityUserAgent},
	}
	response, err := a.client.Do(request)
	if err != nil {
		return nil, Result{}, wrapContext(ctx, &Error{Code: ErrorCodeConnection, Message: err.Error()})
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		defer response.Body.Close()
		payload, readErr := io.ReadAll(io.LimitReader(response.Body, maxErrorBodyBytes+1))
		if readErr != nil {
			return nil, Result{}, wrapContext(ctx, &Error{Code: ErrorCodeConnection, Message: "upstream rejection body read failed"})
		}
		var rpc *googleRPCError
		if len(payload) <= maxErrorBodyBytes {
			rpc = parseGoogleRPCError(payload)
		}
		failure := classifyAntigravityError(response.StatusCode, response.Header, rpc, time.Now())
		failure.RejectedBeforeExecution = true
		return nil, Result{Failed: true, ErrorCode: failure.Code}, failure
	}
	if stream {
		contentType := strings.TrimSpace(strings.ToLower(response.Header.Get("Content-Type")))
		missingAllowed := contentType == "" && target.Capabilities.AllowMissingSSEContentType
		if !missingAllowed && !strings.HasPrefix(contentType, "text/event-stream") {
			response.Body.Close()
			return nil, Result{}, &Error{Code: ErrorCodeInvalidStream, Message: "upstream stream Content-Type is not text/event-stream"}
		}
	}
	return response, Result{}, nil
}

// antigravityTurn feeds Cloud Code parts into chatStreamState as Chat stream
// deltas and keeps the turn's thought signatures.
type antigravityTurn struct {
	chat *chatStreamState
	// usedIDs are the call ids of the request history and of this turn, so a
	// missing or repeated provider id is replaced by a fresh unique one.
	usedIDs     map[string]bool
	calls       int
	firstCallID string
	callSigs    map[string]string
	turnSig     string
}

func newAntigravityTurn(target Target, model string, catalog toolCatalog, emit func(Event) error, history []ChatMessage) *antigravityTurn {
	used := make(map[string]bool)
	for _, message := range history {
		for _, call := range message.ToolCalls {
			used[call.ID] = true
		}
	}
	return &antigravityTurn{
		chat: &chatStreamState{
			target: target, model: model, catalog: catalog, emit: emit,
			responseID: newID("resp_"), messageID: newID("msg_"), reasoningID: newID("rs_"),
			toolCalls: make(map[int]*chatToolCallState), outputIndexes: make(map[string]int),
		},
		usedIDs: used, callSigs: make(map[string]string),
	}
}

// pushFrame decodes one frame; an error frame is classified like an HTTP
// refusal, but after the request reached Google it is never proven
// unexecuted.
func (t *antigravityTurn) pushFrame(data []byte) error {
	var frame antigravityFrame
	if err := decodeExactJSON(data, &frame); err != nil {
		return err
	}
	if frame.Error != nil {
		failure := classifyAntigravityError(frame.Error.Code, nil, frame.Error, time.Now())
		t.chat.errorCode = failure.Code
		return failure
	}
	response := frame.antigravityResponse
	if frame.Response != nil {
		response = *frame.Response
	}
	if len(response.Candidates) > 1 {
		return &Error{Code: ErrorCodeInvalidStream, Message: "only one upstream candidate is supported"}
	}
	if len(response.Candidates) == 1 && response.Candidates[0].Content != nil {
		for _, part := range response.Candidates[0].Content.Parts {
			if err := t.pushPart(part); err != nil {
				return err
			}
		}
	}
	if usage := response.UsageMetadata.chatUsage(); usage != nil {
		return t.chat.push(chatStreamChunk{Usage: usage})
	}
	return nil
}

// pushPart converts one part: thought text to reasoning, text to output text,
// a function call to one complete Chat tool call delta.
func (t *antigravityTurn) pushPart(part antigravityPart) error {
	if call := part.FunctionCall; call != nil {
		id := call.ID
		if id == "" || t.usedIDs[id] {
			id = newID("call_")
		}
		t.usedIDs[id] = true
		if t.firstCallID == "" {
			t.firstCallID = id
		}
		if part.ThoughtSignature != "" {
			t.callSigs[id] = part.ThoughtSignature
		}
		arguments, err := antigravityResultArguments(call.Args)
		if err != nil {
			return err
		}
		index := t.calls
		t.calls++
		return t.chat.push(chatStreamChunk{Choices: []chatStreamChoice{{Delta: chatStreamDelta{ToolCalls: []chatStreamToolCall{{
			Index: &index, ID: id, Function: &chatStreamToolCallFunction{Name: call.Name, Arguments: arguments},
		}}}}}})
	}
	if part.ThoughtSignature != "" && t.turnSig == "" {
		t.turnSig = part.ThoughtSignature
	}
	if part.Text == "" {
		return nil
	}
	delta := chatStreamDelta{Content: part.Text}
	if part.Thought {
		delta = chatStreamDelta{ReasoningContent: part.Text}
	}
	return t.chat.push(chatStreamChunk{Choices: []chatStreamChoice{{Delta: delta}}})
}

// antigravityResultArguments renders returned function call args (an object
// or absent) as a Chat arguments string.
func antigravityResultArguments(args json.RawMessage) (string, error) {
	trimmed := bytes.TrimSpace(args)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return "{}", nil
	}
	if trimmed[0] != '{' {
		return "", &Error{Code: ErrorCodeInvalidToolCall, Message: "upstream function call args are not a JSON object"}
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, trimmed); err != nil {
		return "", &Error{Code: ErrorCodeInvalidToolCall, Message: "upstream function call args are not valid JSON"}
	}
	return compact.String(), nil
}

// signatures are the turn's thought signatures for the continuation (R6): a
// function call's by its id; a non-call signature on the turn's text, or on
// its first call when the turn has no text.
func (t *antigravityTurn) signatures(history turnHash, assistant ChatMessage) map[string]string {
	out := make(map[string]string, len(t.callSigs)+1)
	for id, signature := range t.callSigs {
		out[antigravityCallSignatureKey(id)] = signature
	}
	if t.turnSig == "" {
		return out
	}
	if text := chatText(assistant.Content); text != "" {
		out[history.turnKey(text)] = t.turnSig
	} else if key := antigravityCallSignatureKey(t.firstCallID); t.firstCallID != "" && out[key] == "" {
		out[key] = t.turnSig
	}
	return out
}
