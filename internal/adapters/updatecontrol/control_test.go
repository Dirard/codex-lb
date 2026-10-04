package updatecontrol

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"codex-lb/internal/domain"
)

type controlFixture struct{ calls int }

func (s *controlFixture) Status(context.Context) (domain.RuntimeUpdateStatus, error) {
	return domain.RuntimeUpdateStatus{CurrentVersion: "go-v1.0.0", Supported: true, Phase: "idle"}, nil
}
func (s *controlFixture) Check(ctx context.Context) (domain.RuntimeUpdateStatus, error) {
	s.calls++
	return s.Status(ctx)
}
func (s *controlFixture) Apply(ctx context.Context, _ string) (domain.RuntimeUpdateStatus, error) {
	return s.Check(ctx)
}
func (s *controlFixture) Rollback(ctx context.Context, _ string) (domain.RuntimeUpdateStatus, error) {
	return s.Check(ctx)
}

func TestPrivateUpdateControlRejectsUnauthorizedAndMalformedActions(t *testing.T) {
	const token = "synthetic-private-control"
	for _, tc := range []struct {
		name, auth, body string
		status           int
	}{
		{"missing token", "", `{"version":"go-v1.0.1"}`, 401},
		{"wrong token", "Bearer wrong", `{"version":"go-v1.0.1"}`, 401},
		{"unknown field", "Bearer " + token, `{"version":"go-v1.0.1","url":"https://example.test"}`, 400},
		{"extra object", "Bearer " + token, `{"version":"go-v1.0.1"}{}`, 400},
		{"oversized version", "Bearer " + token, `{"version":"` + strings.Repeat("a", 5000) + `"}`, 400},
		{"valid", "Bearer " + token, `{"version":"go-v1.0.1"}`, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := &controlFixture{}
			request := httptest.NewRequest(http.MethodPost, "/apply", strings.NewReader(tc.body))
			request.Header.Set("Authorization", tc.auth)
			response := httptest.NewRecorder()
			Protect(token, Handler(service)).ServeHTTP(response, request)
			if response.Code != tc.status || (service.calls != 0) != (tc.status == 200) {
				t.Fatalf("status=%d calls=%d", response.Code, service.calls)
			}
		})
	}
}

func TestPrivateUpdateClientUsesUnixSocketAndToken(t *testing.T) {
	// Keep the Unix path below the platform socket-path limit.
	socket := filepath.Join(t.TempDir(), "control.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	server := &http.Server{Handler: Protect("synthetic-token", Handler(&controlFixture{}))}
	done := make(chan struct{})
	go func() { defer close(done); _ = server.Serve(listener) }()
	defer func() { _ = server.Close(); <-done }()
	client := NewClient(socket, "synthetic-token")
	defer client.Close()
	status, err := client.Status(context.Background())
	if err != nil || status.CurrentVersion != "go-v1.0.0" {
		t.Fatalf("status failed: %v", err)
	}
	unauthorized := NewClient(socket, "wrong-token")
	defer unauthorized.Close()
	if _, err := unauthorized.Status(context.Background()); err == nil {
		t.Fatal("private status accepted the wrong token")
	}
}
