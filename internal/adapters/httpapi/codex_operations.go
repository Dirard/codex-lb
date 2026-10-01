package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/netip"
	"strings"

	"github.com/coder/websocket"

	"codex-lb/internal/application"
)

type CodexOperationsHandler struct {
	store ProxyRepository
	ops   *application.CodexOperations
}

func NewCodexOperationsHandler(store ProxyRepository, operations *application.CodexOperations) *CodexOperationsHandler {
	return &CodexOperationsHandler{store: store, ops: operations}
}

func RegisterCodexOperationRoutes(mux *http.ServeMux, store ProxyRepository, operations *application.CodexOperations) {
	NewCodexOperationsHandler(store, operations).RegisterRoutes(mux)
}

// RegisterRoutes attaches all owned ancillary routes to the runtime mux.
func (h *CodexOperationsHandler) RegisterRoutes(mux *http.ServeMux) {
	h.registerPublicWarmupRoutes(mux)
	for _, path := range []string{"/backend-api/codex/responses/compact", "/backend-api/codex/responses/compact/{$}", "/v1/responses/compact", "/v1/responses/compact/{$}"} {
		mux.HandleFunc("POST "+path, h.compact)
	}
	control := map[string][]string{
		"thread/goal/get": {http.MethodGet, http.MethodPost}, "thread/goal/set": {http.MethodPost},
		"thread/goal/clear": {http.MethodPost}, "analytics-events/events": {http.MethodPost},
		"memories/trace_summarize": {http.MethodPost}, "safety/arc": {http.MethodPost},
		"alpha/search": {http.MethodPost}, "agent-identities/jwks": {http.MethodGet},
	}
	for path, methods := range control {
		for _, method := range methods {
			mux.HandleFunc(method+" /backend-api/codex/"+path, h.control)
			mux.HandleFunc(method+" /backend-api/codex/"+path+"/{$}", h.control)
		}
	}
	mux.HandleFunc("POST /backend-api/files", h.createFile)
	mux.HandleFunc("POST /backend-api/files/{file_id}/uploaded", h.finalizeFile)
	mux.HandleFunc("POST /backend-api/transcribe", h.transcribeBackend)
	mux.HandleFunc("POST /backend-api/transcribe/{$}", h.transcribeBackend)
	mux.HandleFunc("POST /v1/audio/transcriptions", h.transcribeV1)
	mux.HandleFunc("POST /v1/audio/transcriptions/{$}", h.transcribeV1)
	mux.HandleFunc("POST /backend-api/codex/realtime/calls", h.createRealtimeCall)
	mux.HandleFunc("POST /backend-api/codex/realtime/calls/{$}", h.createRealtimeCall)
	mux.HandleFunc("GET /backend-api/codex/{call_id}", h.realtimeLive)
	mux.HandleFunc("GET /v1/live/{call_id}", h.realtimeLive)
	mux.HandleFunc("GET /v1/realtime", h.realtimeLive)
	mux.HandleFunc("GET /v1/realtime/{$}", h.realtimeLive)
}

func (h *CodexOperationsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	mux.ServeHTTP(w, r)
}

func (h *CodexOperationsHandler) authenticate(r *http.Request) (string, error) {
	key, err := authenticateProxyKey(r, h.store)
	if err != nil {
		return "", err
	}
	return key.ID, nil
}

func (h *CodexOperationsHandler) compact(w http.ResponseWriter, r *http.Request) {
	keyID, err := h.authenticate(r)
	if err != nil {
		writeProxyError(w, err)
		return
	}
	body, err := readOperationJSON(w, r, 32<<20)
	if err != nil {
		writeProxyError(w, err)
		return
	}
	result, err := h.ops.CompactWithOptions(r.Context(), operationOptions(r, keyID), body)
	if err == nil && !result.Failed && strings.HasPrefix(r.URL.Path, "/backend-api/codex/") {
		result.Body = normalizeCodexCompactOutput(result.Body)
	}
	h.writeResult(w, r, result, err, true)
}

func (h *CodexOperationsHandler) control(w http.ResponseWriter, r *http.Request) {
	keyID, err := h.authenticate(r)
	if err != nil {
		writeProxyError(w, err)
		return
	}
	const prefix = "/backend-api/codex/"
	path := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, prefix), "/")
	var body []byte
	if r.Method != http.MethodGet {
		body, err = readOperationBody(w, r, 2<<20)
		if err != nil {
			writeProxyError(w, err)
			return
		}
	}
	query := make([][2]string, 0, len(r.URL.Query()))
	for name, values := range r.URL.Query() {
		for _, value := range values {
			query = append(query, [2]string{name, value})
		}
	}
	result, err := h.ops.ControlWithOptions(r.Context(), operationOptions(r, keyID), application.CodexControlRequest{
		Method: r.Method, Path: path, Query: query, Body: body, ContentType: mediaType(r.Header.Get("Content-Type")),
	})
	h.writeResult(w, r, result, err, false)
}

func (h *CodexOperationsHandler) createFile(w http.ResponseWriter, r *http.Request) {
	keyID, err := h.authenticate(r)
	if err != nil {
		writeProxyError(w, err)
		return
	}
	body, err := readOperationJSON(w, r, 64<<10)
	if err != nil {
		writeProxyError(w, err)
		return
	}
	result, err := h.ops.CreateFileWithOptions(r.Context(), operationOptions(r, keyID), body)
	h.writeResult(w, r, result, err, false)
}

func (h *CodexOperationsHandler) finalizeFile(w http.ResponseWriter, r *http.Request) {
	keyID, err := h.authenticate(r)
	if err != nil {
		writeProxyError(w, err)
		return
	}
	result, err := h.ops.FinalizeFileWithOptions(r.Context(), operationOptions(r, keyID), r.PathValue("file_id"))
	h.writeResult(w, r, result, err, false)
}

func (h *CodexOperationsHandler) transcribeBackend(w http.ResponseWriter, r *http.Request) {
	keyID, err := h.authenticate(r)
	if err != nil {
		writeProxyError(w, err)
		return
	}
	request, err := parseTranscription(w, r, false)
	if err != nil {
		writeProxyError(w, err)
		return
	}
	result, err := h.ops.TranscribeWithOptions(r.Context(), operationOptions(r, keyID), request)
	h.writeResult(w, r, result, err, false)
}

func (h *CodexOperationsHandler) transcribeV1(w http.ResponseWriter, r *http.Request) {
	keyID, err := h.authenticate(r)
	if err != nil {
		writeProxyError(w, err)
		return
	}
	request, err := parseTranscription(w, r, true)
	if err != nil {
		writeProxyError(w, err)
		return
	}
	result, err := h.ops.TranscribeWithOptions(r.Context(), operationOptions(r, keyID), request)
	h.writeResult(w, r, result, err, false)
}

func (h *CodexOperationsHandler) createRealtimeCall(w http.ResponseWriter, r *http.Request) {
	key, err := authenticateBearerKey(r, h.store)
	if err != nil {
		writeProxyError(w, err)
		return
	}
	switch strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Encoding"))) {
	case "", "identity":
	default:
		writeProxyError(w, &application.ProxyError{Code: "unsupported_content_encoding", Status: 415, Message: "Unsupported request encoding"})
		return
	}
	body, err := readOperationBody(w, r, application.MaxRealtimeCallBytes)
	if err != nil {
		writeProxyError(w, err)
		return
	}
	query := make([][2]string, 0, len(r.URL.Query()))
	for name, values := range r.URL.Query() {
		for _, value := range values {
			query = append(query, [2]string{name, value})
		}
	}
	result, err := h.ops.CreateRealtimeCall(r.Context(), operationOptions(r, key.ID), application.CodexControlRequest{Body: body, ContentType: r.Header.Get("Content-Type"), Query: query, RealtimeHeaders: realtimeHeaders(r)})
	h.writeResult(w, r, result, err, false)
}

func (h *CodexOperationsHandler) realtimeLive(w http.ResponseWriter, r *http.Request) {
	key, err := authenticateBearerKey(r, h.store)
	if err != nil {
		writeProxyError(w, err)
		return
	}
	options := operationOptions(r, key.ID)
	options.Transport = "websocket"
	request := application.CodexRealtimeRequest{CallID: r.PathValue("call_id"), Query: realtimeQuery(r), Protocol: application.CodexRealtimeLive, Headers: realtimeHeaders(r)}
	ids := r.URL.Query()["call_id"]
	if request.CallID == "" && len(ids) == 1 {
		request.CallID, request.Protocol = ids[0], application.CodexRealtimeLegacy
	} else if len(ids) != 0 {
		writeProxyError(w, &application.ProxyError{Code: "invalid_realtime_call_id", Status: 400, Message: "Provide exactly one realtime call selector"})
		return
	}
	if err := application.ValidateCodexRealtimeRequest(request); err != nil {
		writeProxyError(w, err)
		return
	}
	target, err := h.ops.AuthorizeRealtime(r.Context(), options, request.CallID)
	if err != nil {
		writeProxyError(w, err)
		return
	}
	connection, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	connection.SetReadLimit(application.MaxRealtimeMessageBytes)
	defer connection.CloseNow()
	if err := h.ops.Realtime(r.Context(), options, target, request, websocketConnection{connection}); err != nil {
		_ = connection.Close(websocket.StatusInternalError, "realtime proxy failed")
		return
	}
}

func (h *CodexOperationsHandler) writeResult(w http.ResponseWriter, r *http.Request, result application.CodexOperationResult, err error, compact bool) {
	if err != nil {
		writeProxyError(w, err)
		return
	}
	if result.ContentType == "" {
		result.ContentType = "application/json"
	}
	if result.Failed && result.Status >= 200 && result.Status < 300 {
		writeProxyError(w, &application.ProviderFailure{Code: result.ErrorCode, Status: http.StatusBadGateway, Dispatched: true})
		return
	}
	if compact {
		w.Header().Set("Cache-Control", "no-store")
	}
	for _, name := range []string{"Cache-Control", "ETag", "Last-Modified", "Location", "Openai-Processing-Ms", "Request-Id", "X-Request-Id"} {
		if value := resultHeaderValue(result.Headers, name); value != "" {
			w.Header().Set(name, value)
		}
	}
	w.Header().Set("Content-Type", result.ContentType)
	status := result.Status
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	_, _ = w.Write(result.Body)
	_ = r
}

func operationOptions(r *http.Request, keyID string) application.CodexOperationOptions {
	clientIP := ""
	if address, err := netip.ParseAddrPort(r.RemoteAddr); err == nil {
		clientIP = address.Addr().Unmap().String()
	}
	identity := responseOptions(r, keyID)
	return application.CodexOperationOptions{
		KeyID: keyID, SessionID: identity.SessionID, TurnState: identity.TurnState,
		ThreadID: identity.ThreadID, ClientAffinity: identity.ClientAffinity,
		ConversationID: r.Header.Get("X-Codex-Conversation-Id"), UserAgent: r.UserAgent(),
		UserAgentGroup: r.Header.Get("X-Codex-User-Agent-Group"), ClientIP: clientIP, Transport: "http",
	}
}

func resultHeaderValue(headers map[string][]string, name string) string {
	for key, values := range headers {
		if strings.EqualFold(key, name) && len(values) != 0 {
			return values[0]
		}
	}
	return ""
}

func realtimeQuery(r *http.Request) [][2]string {
	values := r.URL.Query()
	query := make([][2]string, 0, len(values))
	for name, items := range values {
		if name == "call_id" {
			continue
		}
		for _, value := range items {
			query = append(query, [2]string{name, value})
		}
	}
	return query
}

func realtimeHeaders(r *http.Request) application.CodexRealtimeHeaders {
	return application.CodexRealtimeHeaders{Alpha: strings.Join(r.Header.Values("OpenAI-Alpha"), ", "), Beta: strings.Join(r.Header.Values("OpenAI-Beta"), ", ")}
}

type websocketConnection struct {
	connection *websocket.Conn
}

func (c websocketConnection) Read(ctx context.Context) (application.CodexRealtimeMessage, error) {
	messageType, reader, err := c.connection.Reader(ctx)
	var data []byte
	if err == nil {
		data, err = io.ReadAll(io.LimitReader(reader, application.MaxRealtimeMessageBytes+1))
	}
	if err != nil {
		return application.CodexRealtimeMessage{}, err
	}
	if len(data) > application.MaxRealtimeMessageBytes {
		return application.CodexRealtimeMessage{}, &application.ProxyError{Code: "realtime_message_too_large", Status: 1009, Message: "Realtime message exceeds limit"}
	}
	return application.CodexRealtimeMessage{Binary: messageType == websocket.MessageBinary, Data: data}, nil
}

func (c websocketConnection) Write(ctx context.Context, message application.CodexRealtimeMessage) error {
	messageType := websocket.MessageText
	if message.Binary {
		messageType = websocket.MessageBinary
	}
	return c.connection.Write(ctx, messageType, message.Data)
}

func (c websocketConnection) Close(status, reason string) error {
	switch status {
	case "1000":
		return c.connection.Close(websocket.StatusNormalClosure, reason)
	case "1009":
		return c.connection.Close(websocket.StatusMessageTooBig, reason)
	case "1011":
		return c.connection.Close(websocket.StatusInternalError, reason)
	default:
		return c.connection.Close(websocket.StatusInternalError, reason)
	}
}

func readOperationJSON(w http.ResponseWriter, r *http.Request, maximum int64) (json.RawMessage, error) {
	if mediaType(r.Header.Get("Content-Type")) != "application/json" {
		return nil, &application.ProxyError{Code: "invalid_content_type", Status: 415, Message: "Content-Type must be application/json"}
	}
	body, err := readOperationBody(w, r, maximum)
	if err != nil {
		return nil, err
	}
	if !json.Valid(body) {
		return nil, &application.ProxyError{Code: "invalid_request", Status: 400, Message: "Invalid JSON request"}
	}
	return body, nil
}

func readOperationBody(w http.ResponseWriter, r *http.Request, maximum int64) ([]byte, error) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maximum))
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) || len(body) > int(maximum) {
		return nil, &application.ProxyError{Code: "request_too_large", Status: 413, Message: "Request body exceeds size limit"}
	}
	if err != nil {
		return nil, &application.ProxyError{Code: "invalid_request_body", Status: 400, Message: "Could not decode request body"}
	}
	return body, nil
}

func parseTranscription(w http.ResponseWriter, r *http.Request, requireModel bool) (application.CodexTranscriptionRequest, error) {
	mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" || params["boundary"] == "" {
		return application.CodexTranscriptionRequest{}, &application.ProxyError{Code: "invalid_content_type", Status: 415, Message: "Content-Type must be multipart/form-data"}
	}
	switch strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Encoding"))) {
	case "", "identity":
	default:
		return application.CodexTranscriptionRequest{}, &application.ProxyError{Code: "unsupported_content_encoding", Status: 415, Message: "Unsupported request encoding"}
	}
	reader := multipart.NewReader(http.MaxBytesReader(w, r.Body, 32<<20), params["boundary"])
	var result application.CodexTranscriptionRequest
	files := 0
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return application.CodexTranscriptionRequest{}, &application.ProxyError{Code: "invalid_request", Status: 400, Message: "Invalid multipart request"}
		}
		name := part.FormName()
		if name == "file" {
			files++
			if files > 1 {
				return application.CodexTranscriptionRequest{}, &application.ProxyError{Code: "invalid_request", Status: 400, Message: "Only one audio file is allowed"}
			}
			result.Filename = part.FileName()
			result.ContentType = part.Header.Get("Content-Type")
			result.Audio, err = io.ReadAll(io.LimitReader(part, 25_000_001))
			if err != nil || len(result.Audio) > 25_000_000 {
				return application.CodexTranscriptionRequest{}, &application.ProxyError{Code: "audio_too_large", Status: 413, Message: "Audio file exceeds size limit"}
			}
			part.Close()
			continue
		}
		value, err := io.ReadAll(io.LimitReader(part, 65537))
		part.Close()
		if err != nil || len(value) > 65536 {
			return application.CodexTranscriptionRequest{}, &application.ProxyError{Code: "invalid_request", Status: 400, Message: "Invalid transcription field"}
		}
		switch name {
		case "model":
			result.Model = string(value)
		case "prompt":
			result.Prompt = string(value)
		}
		if name != "file" {
			result.Fields = append(result.Fields, [2]string{name, string(value)})
		}
	}
	if files != 1 || len(result.Audio) == 0 || requireModel && strings.TrimSpace(result.Model) == "" {
		return application.CodexTranscriptionRequest{}, &application.ProxyError{Code: "invalid_request", Status: 400, Message: "Audio file and model are required"}
	}
	return result, nil
}

func mediaType(value string) string {
	kind, _, err := mime.ParseMediaType(value)
	if err != nil {
		return ""
	}
	return kind
}
