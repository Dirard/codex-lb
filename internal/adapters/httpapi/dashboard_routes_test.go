package httpapi

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"codex-lb/internal/application"
)

// dashboardMockHandlers is the dashboard's MSW handler list; every mocked /api
// route must exist in this router or the dashboard tests exercise a contract
// the Go server does not serve.
var dashboardMockHandlers = filepath.Join("..", "..", "..", "web", "src", "test", "mocks", "handlers.ts")

var mswRoute = regexp.MustCompile(`http\.(get|post|put|patch|delete)\(\s*"([^"]+)"`)

// normalizeRoute maps MSW (`:id`) and ServeMux (`{id}`, `{id...}`, `{$}`)
// path syntax onto one comparable form.
func normalizeRoute(path string) string {
	path = strings.ReplaceAll(path, "{$}", "")
	segments := strings.Split(path, "/")
	for i, segment := range segments {
		if strings.HasPrefix(segment, ":") || strings.HasPrefix(segment, "{") && strings.HasSuffix(segment, "}") {
			segments[i] = "{}"
		}
	}
	return strings.Join(segments, "/")
}

// samplePath replaces MSW parameters with a concrete segment for routing.
func samplePath(path string) string {
	segments := strings.Split(path, "/")
	for i, segment := range segments {
		if strings.HasPrefix(segment, ":") {
			segments[i] = "sample-" + strings.TrimPrefix(segment, ":")
		}
	}
	return strings.Join(segments, "/")
}

func TestDashboardMocksMatchGoRoutes(t *testing.T) {
	source, err := os.ReadFile(dashboardMockHandlers)
	if err != nil {
		t.Fatalf("read dashboard mocks: %v", err)
	}
	server := New(nil, nil, Config{}, nil)
	// Mount every optional route group the runtime wires in production;
	// only route registration is exercised, never the services.
	server.accounts = &application.AccountsService{}
	server.usage = &application.AccountUsageService{}
	server.automations = &application.AutomationsService{}
	server.warmups = &application.WarmupService{}
	server.catalog = &application.ModelCatalogService{}
	server.modelPricing = NewModelPricingHandler(nil)
	server.archives = &application.ErrorArchives{}
	server.firewall = &application.Firewall{}
	server.capacity = &RuntimeCapacitySource{}
	server.backups = &application.BackupService{}
	outer := http.NewServeMux()
	server.registerAuth(outer)
	server.registerKeyReportSession(outer)
	admin := server.adminRoutes()

	matches := mswRoute.FindAllStringSubmatch(string(source), -1)
	if len(matches) < 50 {
		t.Fatalf("found only %d MSW handlers in %s; the parser no longer matches the mock file", len(matches), dashboardMockHandlers)
	}
	checked := 0
	for _, match := range matches {
		method, path := strings.ToUpper(match[1]), match[2]
		if !strings.HasPrefix(path, "/api/") {
			continue // /health and /health/ready are wired by cmd/codex-lb.
		}
		checked++
		req := httptest.NewRequest(method, "http://localhost"+samplePath(path), nil)
		_, pattern := outer.Handler(req)
		if pattern == "" {
			_, pattern = admin.Handler(req)
		}
		goMethod, goPath, found := strings.Cut(pattern, " ")
		if !found {
			goMethod, goPath = "", pattern
		}
		if goMethod != method || normalizeRoute(goPath) != normalizeRoute(path) {
			t.Errorf("MSW mocks %s %s, but the Go router resolves it to %q", method, path, pattern)
		}
	}
	if checked == 0 {
		t.Fatal("no /api MSW handlers were checked")
	}
}
