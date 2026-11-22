package upstream

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

const (
	// antigravitySkipSignature is the documented placeholder for a Gemini
	// function call whose thought signature is unknown.
	antigravitySkipSignature  = "skip_thought_signature_validator"
	antigravityUserAgentField = "antigravity"
	antigravityRequestType    = "agent"
	// antigravityClaudeMinBudget is the smallest Claude thinking budget.
	antigravityClaudeMinBudget = 1024
)

// antigravityEnvelope is the Cloud Code v1internal generateContent body; the
// field order follows the native client.
type antigravityEnvelope struct {
	Project     string                     `json:"project"`
	RequestID   string                     `json:"requestId"`
	Model       string                     `json:"model"`
	UserAgent   string                     `json:"userAgent"`
	RequestType string                     `json:"requestType"`
	Request     antigravityGenerateRequest `json:"request"`
}

type antigravityGenerateRequest struct {
	Contents          []antigravityContent         `json:"contents"`
	SystemInstruction *antigravityContent          `json:"systemInstruction,omitempty"`
	Tools             []antigravityTool            `json:"tools,omitempty"`
	ToolConfig        *antigravityToolConfig       `json:"toolConfig,omitempty"`
	GenerationConfig  *antigravityGenerationConfig `json:"generationConfig,omitempty"`
	SessionID         string                       `json:"sessionId"`
}

type antigravityContent struct {
	Role  string            `json:"role"`
	Parts []antigravityPart `json:"parts"`
}

// antigravityPart is one content part in both directions. Text is never sent
// empty: Google refuses an empty part with 400.
type antigravityPart struct {
	Text             string                       `json:"text,omitempty"`
	Thought          bool                         `json:"thought,omitempty"`
	InlineData       *antigravityInlineData       `json:"inlineData,omitempty"`
	FunctionCall     *antigravityFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *antigravityFunctionResponse `json:"functionResponse,omitempty"`
	ThoughtSignature string                       `json:"thoughtSignature,omitempty"`
}

type antigravityInlineData struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

type antigravityFunctionCall struct {
	ID   string          `json:"id,omitempty"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args,omitempty"`
}

type antigravityFunctionResponse struct {
	ID       string                `json:"id,omitempty"`
	Name     string                `json:"name"`
	Response antigravityToolOutput `json:"response"`
}

type antigravityToolOutput struct {
	Output string `json:"output"`
}

type antigravityTool struct {
	FunctionDeclarations []antigravityFunctionDeclaration `json:"functionDeclarations"`
}

type antigravityFunctionDeclaration struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type antigravityToolConfig struct {
	FunctionCallingConfig antigravityFunctionCallingConfig `json:"functionCallingConfig"`
}

type antigravityFunctionCallingConfig struct {
	Mode                 string   `json:"mode"`
	AllowedFunctionNames []string `json:"allowedFunctionNames,omitempty"`
}

type antigravityGenerationConfig struct {
	MaxOutputTokens *uint64                    `json:"maxOutputTokens,omitempty"`
	Temperature     *float64                   `json:"temperature,omitempty"`
	ThinkingConfig  *antigravityThinkingConfig `json:"thinkingConfig,omitempty"`
}

type antigravityThinkingConfig struct {
	IncludeThoughts bool   `json:"includeThoughts"`
	ThinkingLevel   string `json:"thinkingLevel,omitempty"`
	ThinkingBudget  *int   `json:"thinkingBudget,omitempty"`
}

// antigravityPlan is one translated Cloud Code call: the encoded envelope,
// the Chat messages it was built from (the continuation's history) and the
// known thought signatures it replays, which the next continuation keeps.
type antigravityPlan struct {
	body       []byte
	messages   []ChatMessage
	signatures map[string]string
	// history hashes the request contents; the answer's text turn signature
	// is stored under history.turnKey(text).
	history turnHash
	claude  bool
}

// buildAntigravityRequest realizes the workflow step "translate to the Cloud
// Code envelope" (R4a, R6): the Responses input becomes Chat messages through
// translateToChat, and those become Gemini contents with the system
// instruction, cleaned function declarations, tool config, generation config
// and a stable session id. lookup finds thought signatures that the loaded
// continuation does not hold (a stateless replay of the full input).
func buildAntigravityRequest(target Target, req responsesWireRequest, items []responsesItem, state Continuation, catalog toolCatalog, lookup func([]string) map[string]string) (antigravityPlan, error) {
	if strings.TrimSpace(target.AntigravityProject) == "" {
		return antigravityPlan{}, &Error{Code: ErrorCodeInvalidConfiguration, Message: "Antigravity project is not configured"}
	}
	chat, messages, err := translateToChat(req, items, state, catalog, target.Capabilities, false)
	if err != nil {
		return antigravityPlan{}, err
	}
	claude := strings.HasPrefix(strings.ToLower(chat.Model), "claude-")
	system, contents, err := antigravityContents(messages)
	if err != nil {
		return antigravityPlan{}, err
	}
	turnKeys, history := antigravityTurnKeys(contents)
	known := antigravityKnownSignatures(contents, turnKeys, state.Signatures, lookup)
	antigravityApplySignatures(contents, turnKeys, known, claude)
	tools, err := antigravityTools(catalog.tools, claude)
	if err != nil {
		return antigravityPlan{}, err
	}
	envelope := antigravityEnvelope{
		Project: target.AntigravityProject, RequestID: "agent-" + newUUID(), Model: chat.Model,
		UserAgent: antigravityUserAgentField, RequestType: antigravityRequestType,
		Request: antigravityGenerateRequest{
			Contents: contents, SystemInstruction: system, Tools: tools,
			ToolConfig:       antigravityToolChoice(req.ToolChoice, tools, claude),
			GenerationConfig: antigravityGeneration(req, claude),
			SessionID:        antigravitySessionID(target, messages),
		},
	}
	body, err := encodeAntigravityJSON(envelope)
	if err != nil {
		return antigravityPlan{}, err
	}
	return antigravityPlan{body: body, messages: messages, signatures: known, history: history, claude: claude}, nil
}

// antigravityContents converts translated Chat messages into Gemini turns:
// the leading system message becomes the system instruction, user text and
// images user parts, assistant text and tool calls model parts (thought text
// is never sent back), and tool results functionResponse parts. Adjacent
// turns of one role merge; empty parts and turns are dropped. The result is
// the same whether the history came from a continuation or a full replay.
func antigravityContents(messages []ChatMessage) (*antigravityContent, []antigravityContent, error) {
	var systemParts []antigravityPart
	var contents []antigravityContent
	callNames := make(map[string]string)
	add := func(role string, parts []antigravityPart) {
		if len(parts) == 0 {
			return
		}
		if last := len(contents) - 1; last >= 0 && contents[last].Role == role {
			contents[last].Parts = append(contents[last].Parts, parts...)
			return
		}
		contents = append(contents, antigravityContent{Role: role, Parts: parts})
	}
	for i, message := range messages {
		switch message.Role {
		case "system":
			parts := antigravityTextParts(message.Content)
			if i == 0 {
				systemParts = append(systemParts, parts...)
			} else {
				add("user", parts)
			}
		case "user":
			parts, err := antigravityUserParts(message.Content)
			if err != nil {
				return nil, nil, err
			}
			add("user", parts)
		case "assistant":
			parts := antigravityTextParts(message.Content)
			for _, call := range message.ToolCalls {
				args, err := antigravityCallArgs(call.Function.Arguments)
				if err != nil {
					return nil, nil, err
				}
				callNames[call.ID] = call.Function.Name
				parts = append(parts, antigravityPart{
					FunctionCall: &antigravityFunctionCall{ID: call.ID, Name: call.Function.Name, Args: args},
				})
			}
			add("model", parts)
		case "tool":
			name, ok := callNames[message.ToolCallID]
			if !ok {
				return nil, nil, &Error{Code: ErrorCodeInvalidRequest, Message: "tool output has no matching tool call"}
			}
			output, images := antigravityToolResult(message.Content)
			parts := append([]antigravityPart{{FunctionResponse: &antigravityFunctionResponse{
				ID: message.ToolCallID, Name: name, Response: antigravityToolOutput{Output: output},
			}}}, images...)
			add("user", parts)
		default:
			return nil, nil, &Error{Code: ErrorCodeInvalidRequest, Message: fmt.Sprintf("unsupported message role %q", message.Role)}
		}
	}
	for i := range contents {
		if contents[i].Role == "user" {
			antigravityResponsesFirst(contents[i].Parts)
		}
	}
	if len(contents) == 0 {
		return nil, nil, &Error{Code: ErrorCodeInvalidRequest, Message: "the request has no non-empty input"}
	}
	var system *antigravityContent
	if len(systemParts) != 0 {
		system = &antigravityContent{Role: "user", Parts: systemParts}
	}
	return system, contents, nil
}

// antigravityResponsesFirst moves functionResponse parts ahead of other user
// parts, as Claude requires tool results to open the turn.
func antigravityResponsesFirst(parts []antigravityPart) {
	responses := make([]antigravityPart, 0, len(parts))
	others := make([]antigravityPart, 0, len(parts))
	for _, part := range parts {
		if part.FunctionResponse != nil {
			responses = append(responses, part)
		} else {
			others = append(others, part)
		}
	}
	copy(parts, append(responses, others...))
}

// antigravityApplySignatures replays known thought signatures on the part
// that returned them: a turn's on its first text part, a call's on its
// functionCall. A Gemini model turn without any known signature gets the
// placeholder on each function call; a turn that carries its signature keeps
// it alone, as the native client sends it. Claude turns never get an
// invented signature.
func antigravityApplySignatures(contents []antigravityContent, turnKeys map[int]string, known map[string]string, claude bool) {
	for i := range contents {
		if contents[i].Role != "model" {
			continue
		}
		parts := contents[i].Parts
		signed := false
		textSigned := false
		for j := range parts {
			switch {
			case parts[j].FunctionCall != nil:
				parts[j].ThoughtSignature = known[antigravityCallSignatureKey(parts[j].FunctionCall.ID)]
			case parts[j].Text != "" && !textSigned:
				textSigned = true
				parts[j].ThoughtSignature = known[turnKeys[i]]
			}
			signed = signed || parts[j].ThoughtSignature != ""
		}
		if signed || claude {
			continue
		}
		for j := range parts {
			if parts[j].FunctionCall != nil {
				parts[j].ThoughtSignature = antigravitySkipSignature
			}
		}
	}
}

func antigravityTextParts(raw json.RawMessage) []antigravityPart {
	if text := antigravityContentText(raw); text != "" {
		return []antigravityPart{{Text: text}}
	}
	return nil
}

// antigravityContentText joins the text of a Chat content string or parts
// array; other part types are ignored.
func antigravityContentText(raw json.RawMessage) string {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return ""
	}
	if raw[0] == '"' {
		return chatText(raw)
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var text strings.Builder
	for _, part := range parts {
		switch part.Type {
		case "text", "input_text", "output_text":
			text.WriteString(part.Text)
		}
	}
	return text.String()
}

func antigravityUserParts(raw json.RawMessage) ([]antigravityPart, error) {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, nil
	}
	if raw[0] == '"' {
		return antigravityTextParts(raw), nil
	}
	var source []struct {
		Type     string          `json:"type"`
		Text     string          `json:"text"`
		ImageURL json.RawMessage `json:"image_url"`
	}
	if err := json.Unmarshal(raw, &source); err != nil {
		return nil, &Error{Code: ErrorCodeInvalidRequest, Message: "message content must be a string or an array of parts"}
	}
	parts := make([]antigravityPart, 0, len(source))
	for _, part := range source {
		switch part.Type {
		case "text", "input_text", "output_text":
			if part.Text != "" {
				parts = append(parts, antigravityPart{Text: part.Text})
			}
		case "image_url", "input_image":
			url := antigravityImageURL(part.ImageURL)
			image, err := antigravityInlineImage(url)
			if err != nil {
				return nil, err
			}
			parts = append(parts, antigravityPart{InlineData: image})
		default:
			return nil, unsupported(fmt.Sprintf("content part type %q is not supported by Antigravity sources", part.Type))
		}
	}
	return parts, nil
}

func antigravityImageURL(raw json.RawMessage) string {
	var url string
	if json.Unmarshal(raw, &url) == nil {
		return url
	}
	var object struct {
		URL string `json:"url"`
	}
	_ = json.Unmarshal(raw, &object)
	return object.URL
}

// antigravityInlineImage turns a base64 data: URL into inline data. Remote
// URLs are refused rather than fetched.
func antigravityInlineImage(url string) (*antigravityInlineData, error) {
	if len(url) < 5 || !strings.EqualFold(url[:5], "data:") {
		return nil, unsupported("Antigravity sources accept images only as base64 data: URLs")
	}
	meta, data, ok := strings.Cut(url[5:], ",")
	if !ok || data == "" {
		return nil, &Error{Code: ErrorCodeInvalidRequest, Message: "image data: URL has no data"}
	}
	if !strings.HasSuffix(strings.ToLower(meta), ";base64") {
		return nil, unsupported("Antigravity sources accept only base64-encoded image data: URLs")
	}
	mime, _, _ := strings.Cut(meta, ";")
	if mime = strings.TrimSpace(mime); mime == "" {
		return nil, &Error{Code: ErrorCodeInvalidRequest, Message: "image data: URL has no media type"}
	}
	return &antigravityInlineData{MimeType: mime, Data: data}, nil
}

// antigravityCallArgs parses replayed tool call arguments into the JSON
// object Gemini expects, unchanged; empty arguments become {}.
func antigravityCallArgs(arguments string) (json.RawMessage, error) {
	trimmed := strings.TrimSpace(arguments)
	if trimmed == "" {
		return json.RawMessage(`{}`), nil
	}
	var object map[string]json.RawMessage
	if json.Unmarshal([]byte(trimmed), &object) != nil || object == nil {
		return nil, &Error{Code: ErrorCodeInvalidRequest, Message: "replayed tool call arguments must be a JSON object"}
	}
	return json.RawMessage(trimmed), nil
}

// antigravityToolResult renders a tool output as functionResponse text. A
// Codex multimodal output (an array of input_text and input_image items)
// keeps its text there and sends its data: URL images as inline parts beside
// the response; a remote image URL stays a text reference.
func antigravityToolResult(raw json.RawMessage) (string, []antigravityPart) {
	text := chatText(raw)
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "[") {
		return text, nil
	}
	var items []struct {
		Type     string          `json:"type"`
		Text     string          `json:"text"`
		ImageURL json.RawMessage `json:"image_url"`
	}
	if json.Unmarshal([]byte(trimmed), &items) != nil || len(items) == 0 {
		return text, nil
	}
	var lines []string
	var images []antigravityPart
	for _, item := range items {
		switch item.Type {
		case "input_text", "output_text", "text":
			lines = append(lines, item.Text)
		case "input_image", "image_url":
			url := antigravityImageURL(item.ImageURL)
			if image, err := antigravityInlineImage(url); err == nil {
				images = append(images, antigravityPart{InlineData: image})
			} else if strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://") {
				lines = append(lines, "[image: "+url+"]")
			}
		default:
			return text, nil
		}
	}
	return strings.Join(lines, "\n"), images
}

// antigravityTools turns the Chat-format tool catalog into one Gemini tool of
// function declarations with cleaned parameter schemas.
func antigravityTools(catalog []json.RawMessage, claude bool) ([]antigravityTool, error) {
	if len(catalog) == 0 {
		return nil, nil
	}
	declarations := make([]antigravityFunctionDeclaration, 0, len(catalog))
	for _, raw := range catalog {
		var tool struct {
			Function struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				Parameters  json.RawMessage `json:"parameters"`
			} `json:"function"`
		}
		if err := json.Unmarshal(raw, &tool); err != nil || tool.Function.Name == "" {
			return nil, &Error{Code: ErrorCodeInvalidRequest, Message: "invalid tool declaration"}
		}
		parameters, err := cleanAntigravitySchema(tool.Function.Parameters, claude)
		if err != nil {
			return nil, err
		}
		declarations = append(declarations, antigravityFunctionDeclaration{
			Name: tool.Function.Name, Description: tool.Function.Description, Parameters: parameters,
		})
	}
	return []antigravityTool{{FunctionDeclarations: declarations}}, nil
}

// antigravityToolChoice maps tool_choice: required → ANY, none → NONE, a
// named function → ANY limited to it; otherwise Claude gets VALIDATED and
// Gemini the backend default.
func antigravityToolChoice(raw json.RawMessage, tools []antigravityTool, claude bool) *antigravityToolConfig {
	if len(tools) == 0 {
		return nil
	}
	config := func(mode string, names ...string) *antigravityToolConfig {
		return &antigravityToolConfig{FunctionCallingConfig: antigravityFunctionCallingConfig{Mode: mode, AllowedFunctionNames: names}}
	}
	var choice string
	if json.Unmarshal(raw, &choice) == nil {
		switch strings.ToLower(strings.TrimSpace(choice)) {
		case "required":
			return config("ANY")
		case "none":
			return config("NONE")
		}
	} else {
		var named struct {
			Type string `json:"type"`
			Name string `json:"name"`
		}
		if json.Unmarshal(raw, &named) == nil && named.Type == "function" {
			for _, declaration := range tools[0].FunctionDeclarations {
				if declaration.Name == named.Name {
					return config("ANY", named.Name)
				}
			}
		}
	}
	if claude {
		return config("VALIDATED")
	}
	return nil
}

var (
	antigravityThinkingLevels = map[string]string{
		"minimal": "minimal", "low": "low", "medium": "medium", "high": "high",
		"xhigh": "xhigh", "max": "xhigh", "ultra": "xhigh",
	}
	antigravityClaudeBudgets = map[string]int{
		"minimal": 1024, "low": 1024, "medium": 4096, "high": 16384,
		"xhigh": 32768, "max": 32768, "ultra": 32768,
	}
)

// antigravityGeneration maps max_output_tokens, temperature and
// reasoning.effort into generationConfig.
func antigravityGeneration(req responsesWireRequest, claude bool) *antigravityGenerationConfig {
	effort := ""
	if req.Reasoning != nil {
		effort = strings.ToLower(strings.TrimSpace(req.Reasoning.Effort))
	}
	config := &antigravityGenerationConfig{
		MaxOutputTokens: req.MaxOutputTokens, Temperature: req.Temperature,
		ThinkingConfig: antigravityThinking(effort, req.MaxOutputTokens, claude),
	}
	if config.MaxOutputTokens == nil && config.Temperature == nil && config.ThinkingConfig == nil {
		return nil
	}
	return config
}

// antigravityThinking maps an effort to a Gemini thinkingLevel or a Claude
// thinkingBudget kept below maxOutputTokens (thinking is dropped when that
// leaves less than the Claude minimum). Effort none sends no thinking config.
func antigravityThinking(effort string, maxOutput *uint64, claude bool) *antigravityThinkingConfig {
	switch {
	case effort == "none":
		return nil
	case claude:
		budget, ok := antigravityClaudeBudgets[effort]
		if !ok {
			return &antigravityThinkingConfig{IncludeThoughts: true}
		}
		if maxOutput != nil && *maxOutput <= uint64(budget) {
			budget = int(*maxOutput) - 1
		}
		if budget < antigravityClaudeMinBudget {
			return nil
		}
		return &antigravityThinkingConfig{IncludeThoughts: true, ThinkingBudget: &budget}
	default:
		return &antigravityThinkingConfig{IncludeThoughts: true, ThinkingLevel: antigravityThinkingLevels[effort]}
	}
}

// antigravitySessionID is the conversation's stable Cloud Code session id: a
// negative decimal int64 from SHA-256 of the account id and the client's
// session id, or of the account id and the first user text without one.
func antigravitySessionID(target Target, messages []ChatMessage) string {
	seed := target.SessionID
	if seed == "" {
		for _, message := range messages {
			if message.Role != "user" {
				continue
			}
			if text := antigravityContentText(message.Content); strings.TrimSpace(text) != "" {
				seed = text
				break
			}
		}
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte(target.AccountID))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(seed))
	value := int64(binary.BigEndian.Uint64(hash.Sum(nil)[:8]) & math.MaxInt64)
	if value == 0 {
		value = 1
	}
	return strconv.FormatInt(-value, 10)
}

// antigravityCallSignatureKey and antigravityTurnSignatureKey name stored
// thought signatures: a function call's by its call id, a text turn's by the
// hash of the contents before it and its text (turnHash.turnKey).
func antigravityCallSignatureKey(callID string) string { return "call:" + callID }

func antigravityTurnSignatureKey(key uint64) string {
	return "turn:" + strconv.FormatUint(key, 16)
}

// antigravityTurnKeys returns the turn signature key of every model turn
// with text, hashed over the contents before it (signatures excluded), and
// the hash of all contents, under which the answer's turn is stored.
func antigravityTurnKeys(contents []antigravityContent) (map[int]string, turnHash) {
	keys := make(map[int]string)
	prior := newTurnHash()
	for i, content := range contents {
		if content.Role == "model" {
			for _, part := range content.Parts {
				if part.FunctionCall == nil && part.Text != "" {
					keys[i] = prior.turnKey(part.Text)
					break
				}
			}
		}
		prior = prior.content(content)
	}
	return keys, prior
}

// turnHash is a running FNV-1a hash of Gemini contents.
type turnHash uint64

func newTurnHash() turnHash { return 14695981039346656037 }

func (h turnHash) write(data string) turnHash {
	for i := 0; i < len(data); i++ {
		h ^= turnHash(data[i])
		h *= 1099511628211
	}
	return h
}

func (h turnHash) field(data string) turnHash { return h.write(data).write("\x00") }

// content hashes one turn without its thought signatures.
func (h turnHash) content(content antigravityContent) turnHash {
	h = h.field(content.Role)
	for _, part := range content.Parts {
		switch {
		case part.FunctionCall != nil:
			h = h.field("call").field(part.FunctionCall.ID).field(part.FunctionCall.Name).field(string(part.FunctionCall.Args))
		case part.FunctionResponse != nil:
			h = h.field("response").field(part.FunctionResponse.ID).field(part.FunctionResponse.Name).field(part.FunctionResponse.Response.Output)
		case part.InlineData != nil:
			h = h.field("data").field(part.InlineData.MimeType).field(part.InlineData.Data)
		default:
			h = h.field("text").field(part.Text)
		}
	}
	return h.field("end")
}

// turnKey is the signature key of a model turn with text after these
// contents.
func (h turnHash) turnKey(text string) string {
	return antigravityTurnSignatureKey(uint64(h.field("model").field(text)))
}

// antigravityKnownSignatures collects the stored signatures of the request's
// model turns: first from the loaded continuation, then through lookup.
func antigravityKnownSignatures(contents []antigravityContent, turnKeys map[int]string, stored map[string]string, lookup func([]string) map[string]string) map[string]string {
	known := make(map[string]string)
	var missing []string
	want := func(key string) {
		if signature, ok := stored[key]; ok {
			known[key] = signature
		} else {
			missing = append(missing, key)
		}
	}
	for i, content := range contents {
		if content.Role != "model" {
			continue
		}
		if key, ok := turnKeys[i]; ok {
			want(key)
		}
		for _, part := range content.Parts {
			if part.FunctionCall != nil {
				want(antigravityCallSignatureKey(part.FunctionCall.ID))
			}
		}
	}
	if len(missing) != 0 && lookup != nil {
		for key, signature := range lookup(missing) {
			known[key] = signature
		}
	}
	return known
}

// newUUID returns a random RFC 4122 version 4 UUID.
func newUUID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		panic(errors.New("upstream: secure request ID generation failed: " + err.Error()))
	}
	raw[6] = raw[6]&0x0f | 0x40
	raw[8] = raw[8]&0x3f | 0x80
	encoded := hex.EncodeToString(raw[:])
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:]
}
