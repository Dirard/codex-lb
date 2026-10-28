package httpapi

import (
	"bufio"
	"compress/gzip"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// minCompressedJSONBytes is the smallest JSON body worth a gzip frame; below
// it the headers and CPU cost exceed the saving.
const minCompressedJSONBytes = 1024

var gzipWriters = sync.Pool{New: func() any {
	writer, _ := gzip.NewWriterLevel(io.Discard, gzip.DefaultCompression)
	return writer
}}

// compressJSON gzips JSON responses of the authenticated dashboard API. It is
// mounted only on the admin mux, so proxy routes (/v1, /backend-api,
// /api/codex), authentication, server-sent events and WebSocket upgrades
// never pass through it. Non-JSON, small and HEAD responses are untouched.
func compressJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead || !acceptsGzip(r.Header.Values("Accept-Encoding")) {
			next.ServeHTTP(w, r)
			return
		}
		cw := &compressWriter{ResponseWriter: w}
		defer cw.finish()
		next.ServeHTTP(cw, r)
	})
}

// acceptsGzip reports whether the request lists gzip (or `*`) with a
// non-zero quality and does not refuse it explicitly.
func acceptsGzip(values []string) bool {
	gzipQuality, wildcard := -1.0, -1.0
	for _, part := range strings.Split(strings.Join(values, ","), ",") {
		token, params, _ := strings.Cut(part, ";")
		quality := 1.0
		for _, param := range strings.Split(params, ";") {
			if key, value, ok := strings.Cut(param, "="); ok && strings.EqualFold(strings.TrimSpace(key), "q") {
				parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
				if err != nil || parsed < 0 || parsed > 1 {
					parsed = 0
				}
				quality = parsed
			}
		}
		switch strings.ToLower(strings.TrimSpace(token)) {
		case "gzip", "x-gzip":
			gzipQuality = quality
		case "*":
			wildcard = quality
		}
	}
	if gzipQuality >= 0 {
		return gzipQuality > 0
	}
	return wildcard > 0
}

type compressState int

const (
	statePending     compressState = iota // nothing written yet
	stateBuffering                        // eligible JSON, body below the threshold so far
	stateGzip                             // committed to a gzip body
	statePassthrough                      // committed to the unmodified response
)

// compressWriter decides per response: it buffers an eligible JSON body until
// it reaches minCompressedJSONBytes, then commits to gzip; anything else is
// passed through unchanged.
type compressWriter struct {
	http.ResponseWriter
	state  compressState
	status int
	buffer []byte
	gzip   *gzip.Writer
}

func (w *compressWriter) WriteHeader(status int) {
	if w.state != statePending {
		return
	}
	if status >= 100 && status < 200 {
		w.ResponseWriter.WriteHeader(status)
		return
	}
	w.status = status
	if compressibleResponse(w.Header(), status) {
		w.Header().Add("Vary", "Accept-Encoding")
		w.state = stateBuffering
		return
	}
	w.state = statePassthrough
	w.ResponseWriter.WriteHeader(status)
}

func (w *compressWriter) Write(data []byte) (int, error) {
	if w.state == statePending {
		w.WriteHeader(http.StatusOK)
	}
	switch w.state {
	case stateBuffering:
		if len(w.buffer)+len(data) < minCompressedJSONBytes {
			w.buffer = append(w.buffer, data...)
			return len(data), nil
		}
		if err := w.startGzip(); err != nil {
			return 0, err
		}
		return w.gzip.Write(data)
	case stateGzip:
		return w.gzip.Write(data)
	default:
		return w.ResponseWriter.Write(data)
	}
}

func (w *compressWriter) startGzip() error {
	header := w.Header()
	header.Set("Content-Encoding", "gzip")
	header.Del("Content-Length")
	w.ResponseWriter.WriteHeader(w.status)
	w.gzip = gzipWriters.Get().(*gzip.Writer)
	w.gzip.Reset(w.ResponseWriter)
	w.state = stateGzip
	buffered := w.buffer
	w.buffer = nil
	_, err := w.gzip.Write(buffered)
	return err
}

// flushBuffered commits a still-small eligible body uncompressed.
func (w *compressWriter) flushBuffered() error {
	w.state = statePassthrough
	w.ResponseWriter.WriteHeader(w.status)
	buffered := w.buffer
	w.buffer = nil
	if len(buffered) == 0 {
		return nil
	}
	_, err := w.ResponseWriter.Write(buffered)
	return err
}

// Flush sends everything written so far. An eligible body that is still
// below the threshold commits to gzip, because the handler is streaming.
func (w *compressWriter) Flush() {
	_ = w.FlushError()
}

func (w *compressWriter) FlushError() error {
	if w.state == statePending {
		w.WriteHeader(http.StatusOK)
	}
	switch w.state {
	case stateBuffering:
		if err := w.startGzip(); err != nil {
			return err
		}
		fallthrough
	case stateGzip:
		if err := w.gzip.Flush(); err != nil {
			return err
		}
	}
	return http.NewResponseController(w.ResponseWriter).Flush()
}

// Hijack is only possible before anything has been written, so the caller
// takes over an untouched connection.
func (w *compressWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if w.state != statePending {
		return nil, nil, errors.New("httpapi: hijack after a compressed response started")
	}
	w.state = statePassthrough
	return http.NewResponseController(w.ResponseWriter).Hijack()
}

func (w *compressWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *compressWriter) finish() {
	switch w.state {
	case stateBuffering:
		_ = w.flushBuffered()
	case stateGzip:
		_ = w.gzip.Close()
		w.gzip.Reset(io.Discard)
		gzipWriters.Put(w.gzip)
		w.gzip = nil
	}
}

func compressibleResponse(header http.Header, status int) bool {
	if status == http.StatusNoContent || status == http.StatusNotModified || status < 200 {
		return false
	}
	if header.Get("Content-Encoding") != "" || header.Get("Content-Range") != "" {
		return false
	}
	media, _, err := mime.ParseMediaType(header.Get("Content-Type"))
	if err != nil {
		return false
	}
	return media == "application/json" || strings.HasSuffix(media, "+json")
}
