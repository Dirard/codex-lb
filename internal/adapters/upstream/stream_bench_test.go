package upstream

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"testing"
)

type memoryStreamTransport struct{ body []byte }

func (t memoryStreamTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Body != nil {
		_, _ = io.Copy(io.Discard, request.Body)
		_ = request.Body.Close()
	}
	return &http.Response{StatusCode: http.StatusOK, ProtoMajor: 2, Header: http.Header{"Content-Type": {"text/event-stream"}},
		Body: io.NopCloser(bytes.NewReader(t.body)), Request: request}, nil
}

func benchmarkSSEStream(events int) []byte {
	var buffer bytes.Buffer
	buffer.WriteString("event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"status\":\"in_progress\"}}\n\n")
	for i := range events {
		fmt.Fprintf(&buffer, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_1\",\"output_index\":0,\"content_index\":0,\"delta\":\"token %d \",\"sequence_number\":%d,\"obfuscation\":\"abcdEFGH\"}\n\n", i, i)
	}
	buffer.WriteString("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n")
	return buffer.Bytes()
}

// BenchmarkStreamResponsesSSE measures the per-event cost of the upstream
// Responses SSE path (parser, event decode and hand-off) without network I/O.
func BenchmarkStreamResponsesSSE(b *testing.B) {
	const events = 1000
	adapter := New(&http.Client{Transport: memoryStreamTransport{benchmarkSSEStream(events)}}, nil)
	target := testTarget("https://upstream.invalid", ResponsesCapabilities())
	request := Request{Body: []byte(`{"model":"m","input":"hi","stream":true}`)}
	var retained []Event
	b.ReportAllocs()
	for b.Loop() {
		retained = retained[:0]
		if _, err := adapter.OpenStream(context.Background(), target, request, func(event Event) error {
			retained = append(retained, event)
			return nil
		}); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*(events+2)), "ns/event")
}

func BenchmarkReadSSE(b *testing.B) {
	const events = 1000
	stream := benchmarkSSEStream(events)
	b.ReportAllocs()
	for b.Loop() {
		if err := readSSE(bytes.NewReader(stream), func(string, []byte) error { return nil }); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*(events+2)), "ns/event")
}
