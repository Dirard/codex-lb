package egress

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strconv"
	"testing"
	"time"
)

func guardedClient(guard *Guard, proxy func(*http.Request) (*url.URL, error)) *http.Client {
	dialer := &net.Dialer{Timeout: time.Second}
	return &http.Client{
		Transport: &http.Transport{DialContext: guard.DialContext(dialer.DialContext), Proxy: guard.Proxy(proxy)},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func get(ctx context.Context, client *http.Client, target string) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	return client.Do(request)
}

func TestRestrictedConnectionsReachOnlyPublicAddresses(t *testing.T) {
	reached := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { reached++ }))
	defer upstream.Close()
	port := upstream.Listener.Addr().(*net.TCPAddr).Port
	guard := New(false, nil)
	// A public-looking name that resolves to loopback (DNS rebinding).
	guard.lookup = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("203.0.113.10"), netip.MustParseAddr("127.0.0.1")}, nil
	}
	client := guardedClient(guard, nil)
	restricted := Restrict(context.Background())
	for _, target := range []string{upstream.URL, "http://source.example:" + strconv.Itoa(port) + "/v1"} {
		response, err := get(restricted, client, target)
		if err == nil {
			response.Body.Close()
		}
		if !errors.Is(err, ErrForbiddenDestination) {
			t.Fatalf("%s: restricted request was not refused: %v", target, err)
		}
	}
	if reached != 0 {
		t.Fatal("refused destination received a connection")
	}
	// Unmarked traffic (subscription, OAuth) keeps its existing path.
	response, err := get(context.Background(), client, upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	// The explicit opt-in allows self-hosted private sources.
	response, err = get(restricted, guardedClient(New(true, nil), nil), upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if reached != 2 {
		t.Fatalf("reached = %d", reached)
	}
}

func TestRestrictedConnectionsDialTheCheckedAddress(t *testing.T) {
	var dialed []string
	guard := New(false, nil)
	guard.lookup = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("203.0.113.10")}, nil
	}
	dial := guard.DialContext(func(_ context.Context, _, address string) (net.Conn, error) {
		dialed = append(dialed, address)
		return nil, errors.New("synthetic dial failure")
	})
	if _, err := dial(Restrict(context.Background()), "tcp", "source.example:443"); err == nil {
		t.Fatal("synthetic dial succeeded")
	}
	if len(dialed) != 1 || dialed[0] != "203.0.113.10:443" {
		t.Fatalf("dialed %v; want the checked IP, not a second resolution", dialed)
	}
}

func TestRestrictedRequestsThroughAnEnvironmentProxyCheckTheDestination(t *testing.T) {
	proxied := 0
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { proxied++ }))
	defer proxyServer.Close()
	proxyURL, _ := url.Parse(proxyServer.URL)
	guard := New(false, nil)
	guard.lookup = func(_ context.Context, host string) ([]netip.Addr, error) {
		if host == "internal.example" {
			return []netip.Addr{netip.MustParseAddr("10.0.0.5")}, nil
		}
		return []netip.Addr{netip.MustParseAddr("203.0.113.10")}, nil
	}
	client := guardedClient(guard, http.ProxyURL(proxyURL))
	restricted := Restrict(context.Background())
	if response, err := get(restricted, client, "http://internal.example/v1"); err == nil || !errors.Is(err, ErrForbiddenDestination) {
		if response != nil {
			response.Body.Close()
		}
		t.Fatalf("private destination handed to the proxy: %v", err)
	}
	response, err := get(restricted, client, "http://public.example/v1")
	if err != nil {
		t.Fatalf("operator proxy not dialable for a public destination: %v", err)
	}
	response.Body.Close()
	if proxied != 1 {
		t.Fatalf("proxied = %d", proxied)
	}
}
