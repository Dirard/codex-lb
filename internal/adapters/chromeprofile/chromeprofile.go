// Package chromeprofile sends requests with the wire fingerprint of Chrome
// 123: the TLS ClientHello, HTTP/2 settings, connection window flow and
// pseudo-header order Antigravity-Manager presents on its Google upstream
// calls (rquest Emulation::Chrome123). Chrome 120 through 123 emit the same
// ClientHello and HTTP/2 fingerprint, and real Chrome randomizes its
// extension order, so the captured Chrome 120 profile with random extension
// order produces the same bytes on the wire. Only the transport is emulated:
// headers are the caller's own, this package adds, rewrites and orders
// nothing (owner decision 2026-11-22; prompts and protocol stay untouched).
package chromeprofile

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/http"
	"net/url"
	"slices"
	"sync"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"
	tls_client "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
)

// maxProxyClients bounds the cached per-proxy clients; the proxy function is
// environment-based, so anything above this is a misuse, and the cache
// restarts rather than growing without bound.
const maxProxyClients = 4

// requestTimeout bounds one whole request, the CONNECT handshake included,
// the way Antigravity-Manager bounds its upstream calls (7200 s). The
// library's zero timeout means an instantly expired dial deadline, not
// "no timeout", so the bound is finite on purpose.
const requestTimeout = 2 * time.Hour

// Config sets the transport's dial and proxy behavior; both fields are
// optional. The runtime passes the egress guard for both, so restricted
// destinations stay blocked at dial time and proxied destinations keep their
// name check.
type Config struct {
	DialContext func(ctx context.Context, network, address string) (net.Conn, error)
	Proxy       func(*http.Request) (*url.URL, error)
}

// Transport is an http.RoundTripper over tls-client with the Chrome profile.
type Transport struct {
	config Config
	mu     sync.Mutex
	// clients caches one tls-client per proxy endpoint ("" is direct).
	clients map[string]tls_client.HttpClient
}

var _ http.RoundTripper = (*Transport)(nil)

// NewTransport builds the Chrome-profile transport. It fails only when the
// underlying client cannot be built, which a fixed profile never does.
func NewTransport(config Config) (*Transport, error) {
	return newTransport(config)
}

// newTransport also accepts options the package's own tests need (an
// insecure test endpoint); production callers pass none.
func newTransport(config Config, extraOptions ...tls_client.HttpClientOption) (*Transport, error) {
	transport := &Transport{config: config, clients: make(map[string]tls_client.HttpClient)}
	_, err := transport.client("", extraOptions...)
	return transport, err
}

// client returns the tls-client for one proxy endpoint, building it on first
// use. The options reproduce rquest's Chrome123 setup: the captured Chrome
// profile, randomized TLS extension order, the manager's request bound and
// redirects left to the caller's client.
func (t *Transport) client(proxyEndpoint string, extraOptions ...tls_client.HttpClientOption) (tls_client.HttpClient, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if client, ok := t.clients[proxyEndpoint]; ok {
		return client, nil
	}
	options := []tls_client.HttpClientOption{
		tls_client.WithTimeout(int(requestTimeout.Seconds())),
		tls_client.WithClientProfile(chromeProfile()),
		tls_client.WithRandomTLSExtensionOrder(),
		tls_client.WithNotFollowRedirects(),
	}
	if t.config.DialContext != nil {
		options = append(options, tls_client.WithDialContext(t.config.DialContext))
	}
	if proxyEndpoint != "" {
		options = append(options, tls_client.WithProxyUrl(proxyEndpoint))
	}
	options = append(options, extraOptions...)
	client, err := tls_client.NewHttpClient(tls_client.NewNoopLogger(), options...)
	if err != nil {
		return nil, err
	}
	if len(t.clients) >= maxProxyClients {
		for _, cached := range t.clients {
			cached.CloseIdleConnections()
		}
		clear(t.clients)
	}
	t.clients[proxyEndpoint] = client
	return client, nil
}

// RoundTrip sends one request through the Chrome-profile transport. The
// request travels verbatim: method, URL, body and headers, no additions.
func (t *Transport) RoundTrip(request *http.Request) (*http.Response, error) {
	proxyEndpoint := ""
	if t.config.Proxy != nil {
		proxy, err := t.config.Proxy(request)
		if err != nil {
			return nil, err
		}
		if proxy != nil {
			proxyEndpoint = proxy.String()
		}
	}
	client, err := t.client(proxyEndpoint)
	if err != nil {
		return nil, err
	}
	converted, err := fhttp.NewRequestWithContext(request.Context(), request.Method, request.URL.String(), request.Body)
	if err != nil {
		return nil, errors.New("invalid request for the chrome transport")
	}
	converted.ContentLength = request.ContentLength
	for _, key := range slices.Sorted(maps.Keys(request.Header)) {
		for _, value := range request.Header[key] {
			converted.Header.Add(key, value)
		}
	}
	response, err := client.Do(converted)
	if err != nil {
		// A canceled or deadline context is the caller's own outcome.
		if ctxErr := request.Context().Err(); ctxErr != nil {
			return nil, ctxErr
		}
		// url.Error is the standard library's shape and quotes no headers;
		// anything else may quote the proxy endpoint and is replaced.
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			return nil, urlErr
		}
		return nil, fmt.Errorf("chrome transport cannot reach %s", request.URL.Host)
	}
	return &http.Response{
		Status:           response.Status,
		StatusCode:       response.StatusCode,
		Proto:            response.Proto,
		ProtoMajor:       response.ProtoMajor,
		ProtoMinor:       response.ProtoMinor,
		Header:           http.Header(response.Header),
		Body:             response.Body,
		ContentLength:    response.ContentLength,
		TransferEncoding: response.TransferEncoding,
		Trailer:          http.Header(response.Trailer),
		Request:          request,
	}, nil
}

// CloseIdleConnections drops the pooled connections of every cached client.
func (t *Transport) CloseIdleConnections() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, client := range t.clients {
		client.CloseIdleConnections()
	}
}

// chromeProfile returns the captured Chrome profile used by rquest's
// Chrome123 emulation. It is the library's Chrome_120 capture — the same
// wire bytes as Chrome 123 — rebuilt with allowHTTP so plain-HTTP endpoints
// (loopback stubs, self-hosted sources) still work; a fingerprint only
// exists on a TLS connection.
func chromeProfile() profiles.ClientProfile {
	base := profiles.Chrome_120
	return profiles.NewClientProfile(base.GetClientHelloId(), base.GetSettings(), base.GetSettingsOrder(),
		base.GetPseudoHeaderOrder(), base.GetConnectionFlow(), base.GetPriorities(), base.GetHeaderPriority(),
		base.GetStreamID(), true, base.GetHttp3Settings(), base.GetHttp3SettingsOrder(), base.GetHttp3PriorityParam(),
		base.GetHttp3PseudoHeaderOrder(), base.GetHttp3SendGreaseFrames())
}
