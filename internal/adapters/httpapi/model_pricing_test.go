package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"codex-lb/internal/adapters/credentials"
	"codex-lb/internal/adapters/sqlite"
	"codex-lb/internal/application"
	"codex-lb/internal/domain/pricing"
)

func TestModelPricingAdminRoutes(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := sqlite.Open(filepath.Join(dir, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	vault, err := credentials.Open(filepath.Join(dir, "key"), true)
	if err != nil {
		t.Fatal(err)
	}
	server := New(store, vault, Config{}, nil)
	admin := http.NewServeMux()
	NewModelPricingHandler(store).RegisterAdminRoutes(admin)
	handler := server.requireAdmin(admin)
	request := func(method, path, token string, body []byte) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://localhost"+path, bytes.NewReader(body))
		if body != nil {
			r.Header.Set("Content-Type", "application/json")
		}
		if token != "" {
			r.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if got := request("GET", "/api/model-prices", "", nil).Code; got != 401 {
		t.Fatalf("unauthenticated catalog: %d", got)
	}
	token, err := server.auth.SetupPassword(ctx, "synthetic-password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	listed := request("GET", "/api/model-prices", token, nil)
	var catalog application.ModelPriceList
	if listed.Code != 200 || json.Unmarshal(listed.Body.Bytes(), &catalog) != nil || len(catalog.Prices) == 0 {
		t.Fatalf("builtin pricing catalog: %d %s", listed.Code, listed.Body.String())
	}
	price := pricing.Price{Standard: pricing.Rates{Input: 3000000, Cached: 300000, Output: 12000000},
		PriorityMultiplierMilli: 2000, Long: &pricing.Rates{Input: 6000000, Cached: 600000, Output: 18000000}, Threshold: 272000}
	encoded, err := json.Marshal(struct {
		Price pricing.Price `json:"price"`
	}{price})
	if err != nil {
		t.Fatal(err)
	}
	saved := request("PUT", "/api/model-prices/gpt-6-sol", token, encoded)
	var entry application.ModelPriceEntry
	if saved.Code != 200 || json.Unmarshal(saved.Body.Bytes(), &entry) != nil ||
		entry.Model != "gpt-6-sol" || entry.Source != "custom" || !entry.HasBuiltin ||
		entry.Price.Standard.Input != price.Standard.Input || entry.Reprice == nil || !entry.Reprice.Applied {
		t.Fatalf("save pricing response: %d %s", saved.Code, saved.Body.String())
	}
	listed = request("GET", "/api/model-prices/", token, nil)
	if listed.Code != 200 || json.Unmarshal(listed.Body.Bytes(), &catalog) != nil {
		t.Fatalf("list after save: %d %s", listed.Code, listed.Body.String())
	}
	found := false
	for _, item := range catalog.Prices {
		if item.Model == "gpt-6-sol" {
			found = item.Source == "custom" && item.HasBuiltin && item.Price.Standard.Input == price.Standard.Input
		}
	}
	if !found {
		t.Fatalf("custom override missing from merged catalog: %+v", catalog)
	}
	if got := request("PUT", "/api/model-prices/gpt-7*", token, encoded).Code; got != 422 {
		t.Fatalf("wildcard model ID accepted: %d", got)
	}
	for _, malformed := range []string{`{}`, `{"price":null}`, `{"price":{}}`, `{"price":{"standard":{}}}`,
		`{"price":{"standard":{"inputMicrodollarsPerMillion":1,"outputMicrodollarsPerMillion":2}}}`,
		`{"price":{"standard":{"inputMicrodollarsPerMillion":1,"cachedMicrodollarsPerMillion":null,"outputMicrodollarsPerMillion":2}}}`} {
		if got := request("PUT", "/api/model-prices/gpt-6-sol", token, []byte(malformed)).Code; got != 400 && got != 422 {
			t.Fatalf("missing tariff fields were accepted: %d", got)
		}
	}
	if current, ok, err := store.GetModelPriceOverride(ctx, "gpt-6-sol"); err != nil || !ok || current.Standard != price.Standard {
		t.Fatalf("invalid payload erased the previous tariff: %+v, %v", current, err)
	}
	removed := request("DELETE", "/api/model-prices/gpt-6-sol", token, nil)
	var deletion struct {
		Reprice application.ModelRepriceSummary `json:"reprice"`
	}
	if removed.Code != 200 || json.Unmarshal(removed.Body.Bytes(), &deletion) != nil || !deletion.Reprice.Applied {
		t.Fatalf("delete override: %d %s", removed.Code, removed.Body.String())
	}
	listed = request("GET", "/api/model-prices", token, nil)
	if listed.Code != 200 || json.Unmarshal(listed.Body.Bytes(), &catalog) != nil {
		t.Fatalf("list after delete: %d %s", listed.Code, listed.Body.String())
	}
	found = false
	for _, item := range catalog.Prices {
		if item.Model == "gpt-6-sol" {
			found = item.Source == "builtin" && item.HasBuiltin
		}
	}
	if !found {
		t.Fatalf("builtin tariff not restored after delete: %+v", catalog)
	}
}
