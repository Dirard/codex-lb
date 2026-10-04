// Package updatecontrol carries the small private updater protocol over a Unix
// socket. It never listens on a public TCP address or forwards model traffic.
package updatecontrol

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"time"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

type Client struct {
	http  *http.Client
	token string
}

func NewClient(socket, token string) *Client {
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	return &Client{http: &http.Client{Transport: transport, Timeout: 15 * time.Second}, token: token}
}
func (c *Client) Close() { c.http.CloseIdleConnections() }

func (c *Client) Call(ctx context.Context, method, path string, input, output any) error {
	var body []byte
	if input != nil {
		var err error
		body, err = json.Marshal(input)
		if err != nil {
			return err
		}
	}
	request, err := http.NewRequestWithContext(ctx, method, "http://runtime"+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return errors.New("runtime control connection unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		switch response.StatusCode {
		case http.StatusConflict:
			return domain.ErrUpdateBusy
		case http.StatusBadRequest:
			return domain.ErrUpdateTarget
		default:
			return domain.ErrUpdateUnavailable
		}
	}
	if output == nil {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
	if err != nil || len(data) > 64<<10 {
		return errors.New("invalid runtime control response")
	}
	if err = json.Unmarshal(data, output); err != nil {
		return errors.New("invalid runtime control response")
	}
	return nil
}
func (c *Client) status(ctx context.Context, method, path, version string) (domain.RuntimeUpdateStatus, error) {
	var state domain.RuntimeUpdateStatus
	var body any
	if version != "" {
		body = struct {
			Version string `json:"version"`
		}{version}
	}
	err := c.Call(ctx, method, path, body, &state)
	return state, err
}
func (c *Client) Status(ctx context.Context) (domain.RuntimeUpdateStatus, error) {
	return c.status(ctx, "GET", "/status", "")
}
func (c *Client) Check(ctx context.Context) (domain.RuntimeUpdateStatus, error) {
	return c.status(ctx, "POST", "/check", "")
}
func (c *Client) Apply(ctx context.Context, version string) (domain.RuntimeUpdateStatus, error) {
	return c.status(ctx, "POST", "/apply", version)
}
func (c *Client) Rollback(ctx context.Context, version string) (domain.RuntimeUpdateStatus, error) {
	return c.status(ctx, "POST", "/rollback", version)
}

func Protect(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if token == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		next.ServeHTTP(w, r)
	})
}

func Handler(service application.RuntimeUpdater) http.Handler {
	mux := http.NewServeMux()
	respond := func(w http.ResponseWriter, state domain.RuntimeUpdateStatus, err error) {
		w.Header().Set("Content-Type", "application/json")
		if err != nil {
			status := http.StatusServiceUnavailable
			if errors.Is(err, domain.ErrUpdateBusy) {
				status = http.StatusConflict
			}
			if errors.Is(err, domain.ErrUpdateTarget) {
				status = http.StatusBadRequest
			}
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":"runtime_update_rejected"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(state)
	}
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) { v, e := service.Status(r.Context()); respond(w, v, e) })
	mux.HandleFunc("POST /check", func(w http.ResponseWriter, r *http.Request) { v, e := service.Check(r.Context()); respond(w, v, e) })
	for _, action := range []string{"apply", "rollback"} {
		mux.HandleFunc("POST /"+action, func(w http.ResponseWriter, r *http.Request) {
			var body struct {
				Version string `json:"version"`
			}
			decoder := json.NewDecoder(r.Body)
			decoder.DisallowUnknownFields()
			if decoder.Decode(&body) != nil || body.Version == "" || len(body.Version) > 64 {
				http.Error(w, "Invalid target", http.StatusBadRequest)
				return
			}
			var extra any
			if decoder.Decode(&extra) != io.EOF {
				http.Error(w, "Invalid target", http.StatusBadRequest)
				return
			}
			var state domain.RuntimeUpdateStatus
			var err error
			if action == "apply" {
				state, err = service.Apply(r.Context(), body.Version)
			} else {
				state, err = service.Rollback(r.Context(), body.Version)
			}
			respond(w, state, err)
		})
	}
	return mux
}
