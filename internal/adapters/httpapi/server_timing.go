package httpapi

import (
	"net/http"
	"strconv"
	"time"
)

// serverTiming adds `Server-Timing: app;dur=<ms>` to dashboard page
// endpoints: the time from the request until the response header is
// written. Proxy routes never pass through it.
func serverTiming(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		timed := &timingWriter{ResponseWriter: w, start: time.Now()}
		next.ServeHTTP(timed, r)
	})
}

type timingWriter struct {
	http.ResponseWriter
	start   time.Time
	stamped bool
}

func (w *timingWriter) stamp() {
	if w.stamped {
		return
	}
	w.stamped = true
	elapsed := float64(time.Since(w.start).Microseconds()) / 1000
	w.Header().Set("Server-Timing", "app;dur="+strconv.FormatFloat(elapsed, 'f', 1, 64))
}

func (w *timingWriter) WriteHeader(status int) {
	w.stamp()
	w.ResponseWriter.WriteHeader(status)
}

func (w *timingWriter) Write(data []byte) (int, error) {
	w.stamp()
	return w.ResponseWriter.Write(data)
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (w *timingWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
