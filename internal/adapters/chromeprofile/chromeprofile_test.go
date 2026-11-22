package chromeprofile

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bogdanfinn/fhttp/http2"
	tls_client "github.com/bogdanfinn/tls-client"
)

// seenRequest is what the stub servers answer with, so a test can assert the
// exact request the transport delivered.
type seenRequest struct {
	method string
	path   string
	header http.Header
	body   string
}

func stubServer(t *testing.T, seen *seenRequest) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seen.method, seen.path, seen.body = r.Method, r.URL.RequestURI(), string(body)
		seen.header = r.Header.Clone()
		_, _ = w.Write([]byte("upstream answer"))
	}))
	t.Cleanup(server.Close)
	return server
}

func TestRoundTripCarriesTheRequestVerbatim(t *testing.T) {
	var seen seenRequest
	server := stubServer(t, &seen)
	transport, err := NewTransport(Config{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(transport.CloseIdleConnections)

	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, server.URL+"/v1internal:generateContent",
		strings.NewReader(`{"model":"gemini-3-pro-preview"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer access-token")
	request.Header.Set("User-Agent", "antigravity/hub/2.33.0 (aidev_client; os_type=windows; arch=amd64; cl=1023160026)")

	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)

	if seen.method != http.MethodPost || seen.path != "/v1internal:generateContent" {
		t.Fatalf("request line changed: %s %s", seen.method, seen.path)
	}
	if seen.body != `{"model":"gemini-3-pro-preview"}` {
		t.Fatalf("request body changed: %s", seen.body)
	}
	for header, want := range map[string]string{
		"Content-Type": "application/json", "Authorization": "Bearer access-token",
		"User-Agent": "antigravity/hub/2.33.0 (aidev_client; os_type=windows; arch=amd64; cl=1023160026)",
	} {
		if got := seen.header.Get(header); got != want {
			t.Errorf("%s arrived as %q, want %q", header, got, want)
		}
	}
	if response.StatusCode != http.StatusOK || string(body) != "upstream answer" {
		t.Fatalf("answer changed: %d %q", response.StatusCode, body)
	}
	// The transport emulates a fingerprint, not a browser: nothing of its own
	// is added to the header set.
	for header := range seen.header {
		switch header {
		case "Content-Type", "Authorization", "User-Agent", "Accept-Encoding", "Content-Length", "Host":
		default:
			t.Errorf("transport added header %q", header)
		}
	}
}

func TestRoundTripUsesTheConfiguredProxy(t *testing.T) {
	var seen seenRequest
	upstream := stubServer(t, &seen)
	// The proxy is a real CONNECT tunnel, the shape a Google-bound request
	// sees in production.
	var tunnels atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "only CONNECT is served", http.StatusBadRequest)
			return
		}
		target, err := net.Dial("tcp", r.Host)
		if err != nil {
			http.Error(w, "tunnel target unreachable", http.StatusBadGateway)
			return
		}
		defer target.Close()
		hijacked, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "cannot hijack", http.StatusInternalServerError)
			return
		}
		conn, _, err := hijacked.Hijack()
		if err != nil {
			http.Error(w, "cannot hijack", http.StatusInternalServerError)
			return
		}
		defer conn.Close()
		if _, err := conn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
			return
		}
		tunnels.Add(1)
		done := make(chan struct{}, 2)
		go func() { _, _ = io.Copy(target, conn); done <- struct{}{} }()
		go func() { _, _ = io.Copy(conn, target); done <- struct{}{} }()
		<-done
	}))
	t.Cleanup(proxy.Close)
	proxyURL, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}

	transport, err := NewTransport(Config{Proxy: func(*http.Request) (*url.URL, error) { return proxyURL, nil }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(transport.CloseIdleConnections)

	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, upstream.URL+"/v1internal:fetchAvailableModels", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if tunnels.Load() != 1 {
		t.Fatalf("proxy opened %d tunnels, want 1", tunnels.Load())
	}
	if seen.path != "/v1internal:fetchAvailableModels" || string(body) != "upstream answer" {
		t.Fatalf("tunneled request reached %q and answered %q", seen.path, body)
	}
}

func TestRoundTripHonorsContextCancellation(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
		_, _ = w.Write([]byte("late"))
	}))
	t.Cleanup(server.Close)
	defer close(release)

	transport, err := NewTransport(Config{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(transport.CloseIdleConnections)

	ctx, cancel := context.WithCancel(context.Background())
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/v1internal:loadCodeAssist", nil)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		response, err := transport.RoundTrip(request)
		if response != nil {
			response.Body.Close()
		}
		done <- err
	}()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation error is %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation did not interrupt the request")
	}
}

func TestRoundTripSpeaksTLS(t *testing.T) {
	var seen seenRequest
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seen.method, seen.path, seen.body = r.Method, r.URL.RequestURI(), string(body)
		_, _ = w.Write([]byte("tls answer"))
	}))
	t.Cleanup(server.Close)

	// The test endpoint's certificate is self-signed, so only the test-built
	// transport skips verification; production transports verify.
	transport, err := newTransport(Config{}, tls_client.WithInsecureSkipVerify())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(transport.CloseIdleConnections)

	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, server.URL+"/v1internal:onboardUser",
		strings.NewReader(`{"tierId":"tier"}`))
	if err != nil {
		t.Fatal(err)
	}
	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatalf("TLS request failed: %v", err)
	}
	defer response.Body.Close()
	if seen.path != "/v1internal:onboardUser" || response.StatusCode != http.StatusOK {
		t.Fatalf("TLS round trip reached %q with status %d", seen.path, response.StatusCode)
	}
}

// TestChromeProfileMatchesTheManagerFingerprint pins the wire parameters to
// rquest's Chrome123 emulation: the captured Chrome ClientHello, the four
// HTTP/2 settings Chrome sends, the connection window increment and the
// pseudo-header order.
func TestChromeProfileMatchesTheManagerFingerprint(t *testing.T) {
	profile := chromeProfile()
	if id := profile.GetClientHelloId(); id.Client != "Chrome" || id.Version != "120" {
		t.Fatalf("ClientHello %s/%s is not the captured Chrome profile", id.Client, id.Version)
	}
	wantSettings := map[http2.SettingID]uint32{
		http2.SettingHeaderTableSize:   65536,
		http2.SettingEnablePush:        0,
		http2.SettingInitialWindowSize: 6291456,
		http2.SettingMaxHeaderListSize: 262144,
	}
	got := profile.GetSettings()
	if len(got) != len(wantSettings) {
		t.Fatalf("settings %v do not match %v", got, wantSettings)
	}
	for setting, value := range wantSettings {
		if got[setting] != value {
			t.Errorf("setting %d is %d, want %d", setting, got[setting], value)
		}
	}
	if flow := profile.GetConnectionFlow(); flow != 15663105 {
		t.Errorf("connection flow is %d, want 15663105", flow)
	}
	if order := profile.GetPseudoHeaderOrder(); strings.Join(order, ",") != ":method,:authority,:scheme,:path" {
		t.Errorf("pseudo-header order is %v", order)
	}
	if !profile.GetAllowHTTP() {
		t.Error("plain-HTTP endpoints are not allowed")
	}
}
