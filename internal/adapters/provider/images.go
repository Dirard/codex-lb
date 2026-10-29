package provider

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

const imageRequestTimeout = 5 * time.Minute

var errImageStreamTooLarge = errors.New("image stream event exceeds the bound")

// Images posts a validated Images body unchanged to the subscription images
// endpoint (`<ChatGPT Codex base>/images/generations` or `/images/edits`).
// The subscription decides model, size and quality; the body is not rewritten.
// A streaming request whose answer is an event stream is relayed through
// request.Emit; any other answer is read as JSON.
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
	var extra http.Header
	var readSuccess func(*http.Response) (application.CodexOperationResult, error)
	streamed, completed := false, false
	if request.Stream && request.Emit != nil {
		extra = http.Header{"Accept": {"text/event-stream"}}
		readSuccess = func(response *http.Response) (application.CodexOperationResult, error) {
			if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(response.Header.Get("Content-Type"))), "text/event-stream") {
				return readOperationResponse(response)
			}
			streamed = true
			return relayImageStream(response, request.Emit, &completed)
		}
	}
	result, err := a.chatGPTBody(ctx, target, http.MethodPost, a.chatGPTBaseURL()+path, request.Body, "application/json", extra, readSuccess)
	if err != nil || result.Failed || result.Status < 200 || result.Status >= 300 {
		return result, err
	}
	if streamed {
		if !completed {
			return result, providerFailure("invalid_upstream_response", http.StatusBadGateway, true)
		}
		return result, nil
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

// relayImageStream emits each upstream server-sent event as it arrives and
// takes usage from the completed event. Anything emitted makes the attempt
// visible, so it is never replayed on another account.
func relayImageStream(response *http.Response, emit func(application.CodexImageEvent) error, completed *bool) (application.CodexOperationResult, error) {
	result := application.CodexOperationResult{
		Status: response.StatusCode, ContentType: response.Header.Get("Content-Type"),
		Headers: map[string][]string(response.Header.Clone()),
	}
	credential := ""
	if response.Request != nil {
		credential = strings.TrimPrefix(response.Request.Header.Get("Authorization"), "Bearer ")
	}
	reader := bufio.NewReaderSize(response.Body, 64<<10)
	name := ""
	var data []byte
	dispatch := func() error {
		if name == "" && len(data) == 0 {
			return nil
		}
		event := application.CodexImageEvent{Name: name, Data: redactCredential(data, credential)}
		name, data = "", nil
		inspectImageEvent(&result, event, completed)
		result.OutputObserved = true
		return emit(event)
	}
	for {
		line, readErr := readStreamLine(reader, maxOperationResponseBytes)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			if errors.Is(readErr, errImageStreamTooLarge) {
				return result, providerFailure("upstream_response_too_large", http.StatusBadGateway, true)
			}
			return result, providerFailure("upstream_unavailable", http.StatusBadGateway, true)
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case line == "":
			if err := dispatch(); err != nil {
				return result, err
			}
		case strings.HasPrefix(line, ":"):
		case strings.HasPrefix(line, "event:"):
			name = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			value := strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " ")
			if len(data)+len(value)+1 > maxOperationResponseBytes {
				return result, providerFailure("upstream_response_too_large", http.StatusBadGateway, true)
			}
			if len(data) != 0 {
				data = append(data, '\n')
			}
			data = append(data, value...)
		}
		if errors.Is(readErr, io.EOF) {
			return result, dispatch()
		}
	}
}

// readStreamLine reads one line, refusing lines longer than limit: a completed
// event carries the whole base64 image on one data line.
func readStreamLine(reader *bufio.Reader, limit int) (string, error) {
	var line []byte
	for {
		chunk, err := reader.ReadSlice('\n')
		if len(line)+len(chunk) > limit {
			return "", errImageStreamTooLarge
		}
		line = append(line, chunk...)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return string(line), err
	}
}

func inspectImageEvent(result *application.CodexOperationResult, event application.CodexImageEvent, completed *bool) {
	var object map[string]json.RawMessage
	if json.Unmarshal(event.Data, &object) != nil || object == nil {
		return
	}
	var kind string
	_ = json.Unmarshal(object["type"], &kind)
	if kind == "" {
		kind = event.Name
	}
	switch {
	case kind == "error" || strings.HasSuffix(kind, ".failed"):
		result.Failed = true
		result.ErrorCode = upstreamHTTPCode(http.StatusBadGateway, event.Data)
	case strings.HasSuffix(kind, ".completed"):
		var image string
		if json.Unmarshal(object["b64_json"], &image) == nil && image != "" {
			*completed = true
		}
		if usage, valid := operationUsage(object); valid && nativeChatUsageKnown(object) {
			result.Usage, result.UsageKnown = usage, true
		}
		if nativeUsagePresent(object) {
			result.UsageReported = true
		}
	}
}
