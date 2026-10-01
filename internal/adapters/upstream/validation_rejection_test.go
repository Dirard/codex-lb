package upstream

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const validationRejection = `{"error":{"type":"invalid_request_error","code":"unknown_parameter","message":"Unknown parameter"}}`

func TestValidationRejectionIsClassifiedOnlyAtHTTPBoundary(t *testing.T) {
	for _, mode := range []string{"responses", "responses_sse", "chat", "chat_sse", "websocket_handshake", "error_event"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode == "error_event" {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, "data: "+`{"type":"error","status":400,"error":{"type":"invalid_request_error","code":"unknown_parameter"}}`+"\n\n")
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(400)
				fmt.Fprint(w, validationRejection)
			}))
			defer server.Close()
			capabilities := ResponsesCapabilities()
			if strings.HasPrefix(mode, "chat") {
				capabilities = ChatCompletionsCapabilities()
			}
			if mode == "websocket_handshake" {
				capabilities.StreamTransport = TransportWebSocket
			}
			adapter := New(server.Client(), nil)
			defer adapter.Close()
			stream := mode != "responses" && mode != "chat"
			request := Request{Body: []byte(fmt.Sprintf(`{"model":"m","input":"hi","stream":%t}`, stream))}
			var err error
			if stream {
				_, err = adapter.OpenStream(context.Background(), testTarget(server.URL, capabilities), request, func(Event) error { return nil })
			} else {
				_, err = adapter.Execute(context.Background(), testTarget(server.URL, capabilities), request)
			}
			var failure *Error
			if !errors.As(err, &failure) || failure.RejectedBeforeExecution != (mode != "error_event") {
				t.Fatalf("incorrect HTTP classification: %+v", err)
			}
		})
	}
}

func TestValidationRejectionDoesNotForgiveOutputOrBilling(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		want       bool
	}{
		{"validation", validationRejection, 400, true},
		{"unprocessable", validationRejection, 422, true},
		{"server", validationRejection, 500, false},
		{"status alone", `{"detail":"Unsupported parameter"}`, 400, false},
		{"malformed", validationRejection + `broken`, 400, false},
		{"different type", `{"error":{"type":"server_error","code":"unknown_parameter"}}`, 400, false},
		{"usage", strings.TrimSuffix(validationRejection, "}") + `,"usage":{"input_tokens":3}}`, 400, false},
		{"duration", strings.TrimSuffix(validationRejection, "}") + `,"duration":3}`, 400, false},
		{"output", strings.TrimSuffix(validationRejection, "}") + `,"output":[{"type":"message","text":"started"}]}`, 400, false},
		{"choices", strings.TrimSuffix(validationRejection, "}") + `,"choices":[{}]}`, 400, false},
		{"nested response", strings.TrimSuffix(validationRejection, "}") + `,"response":{"id":"started"}}`, 400, false},
		{"response identity", strings.TrimSuffix(validationRejection, "}") + `,"id":"started"}`, 400, false},
		{"empty arrays", strings.TrimSuffix(validationRejection, "}") + `,"output":[],"choices":[],"usage":null}`, 400, true},
		{"malformed output", strings.TrimSuffix(validationRejection, "}") + `,"output":{}}`, 400, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := rejectedHTTPResult(tc.status, []byte(tc.body), ProtocolResponses)
			var failure *Error
			if !errors.As(err, &failure) || failure.RejectedBeforeExecution != tc.want {
				t.Fatalf("classification=%+v want=%v", failure, tc.want)
			}
		})
	}
}

type failingRejectionBody struct{ io.Reader }

func (b failingRejectionBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	if err == io.EOF {
		return n, io.ErrUnexpectedEOF
	}
	return n, err
}

type rejectionTransport struct{ body string }

func (t rejectionTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: 400, Header: http.Header{}, Body: io.NopCloser(failingRejectionBody{strings.NewReader(t.body)})}, nil
}

func TestTruncatedHTTPRejectionIsNotDefinitive(t *testing.T) {
	for _, billing := range []string{"", `,"usage":{"input_tokens":2,"output_tokens":1}`} {
		body := strings.TrimSuffix(validationRejection, "}") + billing + "}"
		adapter := New(&http.Client{Transport: rejectionTransport{body}}, nil)
		defer adapter.Close()
		result, err := adapter.OpenStream(context.Background(), testTarget("http://synthetic.invalid", ResponsesCapabilities()), Request{Body: []byte(`{"model":"m","input":"hi","stream":true}`)}, func(Event) error { return nil })
		var failure *Error
		if !errors.As(err, &failure) || failure.RejectedBeforeExecution || failure.Code != ErrorCodeConnection || result.UsageKnown != (billing != "") {
			t.Fatalf("truncated rejection classification/billing changed: %+v %v", result, err)
		}
	}
}
