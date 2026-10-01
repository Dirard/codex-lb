package chatgpt

import (
	"context"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"time"

	"codex-lb/internal/application"
)

type callbackListener struct {
	listener net.Listener
	server   *http.Server
}

func ListenCallback(address string, callback application.OAuthCallback) (io.Closer, error) {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /auth/callback", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		result := callback(ctx, r.URL.String())
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Frame-Options", "DENY")
		if result.Status == "success" {
			fmt.Fprint(w, "<html><body><h1>Login complete</h1><p>Return to the dashboard.</p></body></html>")
			return
		}
		message := "Authorization failed."
		if result.ErrorMessage != nil {
			message = *result.ErrorMessage
		}
		fmt.Fprintf(w, "<html><body><h1>Login failed</h1><p>%s</p></body></html>", html.EscapeString(message))
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 35 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	go func() { _ = server.Serve(listener) }()
	return &callbackListener{listener, server}, nil
}

func (c *callbackListener) Close() error {
	c.server.SetKeepAlivesEnabled(false)
	return c.listener.Close()
}
