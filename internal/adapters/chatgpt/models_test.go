package chatgpt

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"codex-lb/internal/domain"
)

func TestModelCatalogClientPreservesRawFields(t *testing.T) {
	var gotPath, authorization, accountID, originator, userAgent, accept string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, authorization, accountID = r.URL.String(), r.Header.Get("Authorization"), r.Header.Get("Chatgpt-Account-ID")
		originator, userAgent, accept = r.Header.Get("Originator"), r.Header.Get("User-Agent"), r.Header.Get("Accept")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[{"slug":"gpt-live","model_messages":{"keep":{"nested":true}},"base_instructions":"prompt","future":{"x":1},"truncation_policy":{"mode":"tokens","limit":10},"experimental_supported_tools":[]}]}`))
	}))
	defer server.Close()
	client := NewModelCatalogClient(server.URL, "0.144.1", server.Client())
	models, err := client.Fetch(context.Background(), domain.Account{ID: "acct", ChatGPTAccountID: "chat-acct"}, "token")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/models?client_version=0.144.1" || authorization != "Bearer token" || accountID != "chat-acct" ||
		originator != "codex_cli_rs" || userAgent != "codex_cli_rs/0.144.1" || accept != "*/*" {
		t.Fatalf("catalog request identity mismatch: path=%q auth=%q account=%q originator=%q ua=%q accept=%q",
			gotPath, authorization, accountID, originator, userAgent, accept)
	}
	if len(models) != 1 || string(models[0].Raw["model_messages"]) != `{"keep":{"nested":true}}` ||
		models[0].BaseInstructions != "prompt" || string(models[0].Raw["future"]) != `{"x":1}` {
		t.Fatalf("raw catalog fields were not preserved: %#v", models)
	}
}

func TestModelCatalogClientDefaultVersionIncludesLuna(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("client_version") != "0.156.0" || r.Header.Get("User-Agent") != "codex_cli_rs/0.156.0" {
			_, _ = w.Write([]byte(`{"models":[{"slug":"gpt-5.6-luna"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"models":[{"slug":"gpt-6-luna"}]}`))
	}))
	defer server.Close()
	client := NewModelCatalogClient(server.URL, "", server.Client())
	models, err := client.Fetch(context.Background(), domain.Account{ID: "acct"}, "token")
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].Slug != "gpt-6-luna" {
		t.Fatalf("default catalog identity did not expose Luna: %+v", models)
	}
}

func TestModelCatalogClientReturnsTyped401(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	client := NewModelCatalogClient(server.URL, "0.144.0", server.Client())
	_, err := client.Fetch(context.Background(), domain.Account{ID: "acct"}, "token")
	var fetchErr *domain.CatalogFetchError
	if err == nil || !errors.As(err, &fetchErr) {
		t.Fatal("expected an error")
	}
	if !fetchErr.AuthRejected() {
		t.Fatalf("401 was not classified: %+v", fetchErr)
	}
}

func TestModelCatalogClientReadsVersionPerFetchAndFailsClosed(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		version := r.URL.Query().Get("client_version")
		if r.Header.Get("User-Agent") != "codex_cli_rs/"+version {
			t.Error("catalog query and header used different versions")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[{"slug":"` + version + `"}]}`))
	}))
	defer server.Close()
	client := NewModelCatalogClient(server.URL, "9.9.9", server.Client())
	version, sourceErr := "0.156.0", error(nil)
	client.ResolveClientVersion = func(context.Context) (string, error) { return version, sourceErr }
	for _, next := range []string{"0.156.0", "0.157.0-beta.1"} {
		version = next
		models, err := client.Fetch(context.Background(), domain.Account{ID: "acct"}, "token")
		if err != nil || len(models) != 1 || models[0].Slug != next {
			t.Fatalf("live catalog version was not applied: %+v %v", models, err)
		}
	}
	for _, invalid := range []string{"", "0.157.0\r\nX-Injected: yes", "0.157.0"} {
		version = invalid
		if invalid == "0.157.0" {
			sourceErr = errors.New("settings unavailable")
		}
		if _, err := client.Fetch(context.Background(), domain.Account{ID: "acct"}, "token"); err == nil {
			t.Fatal("invalid/unavailable setting used a stale version")
		}
	}
	if calls.Load() != 2 {
		t.Fatal("failed version resolution dispatched an upstream request")
	}
}

func TestModelCatalogClientDoesNotFollowRedirect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://example.invalid/models", http.StatusFound)
	}))
	defer server.Close()
	client := NewModelCatalogClient(server.URL, "0.144.0", &http.Client{})
	_, err := client.Fetch(context.Background(), domain.Account{ID: "acct"}, "token")
	var fetchErr *domain.CatalogFetchError
	if !errors.As(err, &fetchErr) || fetchErr.Status != http.StatusFound {
		t.Fatalf("redirect was followed or lost: %+v", err)
	}
}
