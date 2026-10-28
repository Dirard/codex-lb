package httpapi

import (
	"net/http"
	"testing"

	"codex-lb/internal/application"
)

type discardResponseWriter struct{ header http.Header }

func (w *discardResponseWriter) Header() http.Header         { return w.header }
func (w *discardResponseWriter) Write(p []byte) (int, error) { return len(p), nil }
func (w *discardResponseWriter) WriteHeader(int)             {}

func BenchmarkWriteSSE(b *testing.B) {
	w := &discardResponseWriter{header: http.Header{}}
	event := application.ResponseEvent{Type: "response.output_text.delta", Data: []byte(`{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"token 1 ","sequence_number":17,"obfuscation":"abcdEFGH"}`)}
	var buffer []byte
	b.ReportAllocs()
	for b.Loop() {
		frame, err := appendSSE(buffer[:0], event)
		if err != nil {
			b.Fatal(err)
		}
		buffer = frame
		_, _ = w.Write(frame)
	}
}
