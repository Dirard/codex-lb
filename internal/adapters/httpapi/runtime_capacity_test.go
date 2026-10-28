package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

func TestRuntimeCapacityRouteReportsPlanAndUsageToAdministrators(t *testing.T) {
	server, _, _ := reportSessionServer(t)
	plan := domain.PlanCapacity(domain.HostResources{CPUs: 6, MemoryBytes: 4 << 30, MemorySource: domain.MemorySourceCgroupV2, FileDescriptors: 65536},
		domain.CapacityOverrides{MaxClientWebSockets: 9000})
	server.ConfigureRuntimeCapacity(RuntimeCapacitySource{Plan: plan, Usage: func() CapacityUsage {
		return CapacityUsage{ActiveGenerations: 3, QueuedGenerations: 1, ClientWebSockets: 42, ResidentBytes: 123 << 20}
	}})
	handler := server.Handler(nil, nil)
	if got := runtimeUpdateRequest(handler, "GET", "/api/runtime/capacity", "").Code; got != http.StatusUnauthorized {
		t.Fatalf("anonymous capacity read = %d", got)
	}
	reportLogin := reportSessionRequest(handler, "POST", "/api/key-reports/session", "synthetic-a", nil)
	if reportLogin.Code != http.StatusOK || len(reportLogin.Result().Cookies()) != 1 {
		t.Fatalf("report session setup failed: %d", reportLogin.Code)
	}
	if got := runtimeUpdateRequest(handler, "GET", "/api/runtime/capacity", "", reportLogin.Result().Cookies()[0]).Code; got != http.StatusUnauthorized {
		t.Fatalf("key-report capacity read = %d", got)
	}
	adminToken, err := server.auth.SetupPassword(context.Background(), "test", "", "synthetic-password", false, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/runtime/capacity", "/api/runtime/capacity/"} {
		w := runtimeUpdateRequest(handler, "GET", path, "", &http.Cookie{Name: sessionCookie, Value: adminToken})
		if w.Code != http.StatusOK {
			t.Fatalf("%s = %d %s", path, w.Code, w.Body.String())
		}
		var view runtimeCapacityView
		if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
			t.Fatal(err)
		}
		if view.Host.CPUs != 6 || view.Host.MemorySource != domain.MemorySourceCgroupV2 || view.Limits.MaxGenerations != 2048 ||
			view.Limits.ClientWebSockets != 9000 || view.Limits.SQLiteReaders != 16 || len(view.Overridden) != 1 || view.Overridden[0] != "max_client_websockets" {
			t.Fatalf("view = %+v", view)
		}
		if view.Usage.ActiveGenerations != 3 || view.Usage.QueuedGenerations != 1 || view.Usage.ClientWebSockets != 42 ||
			view.Usage.ResidentBytes != 123<<20 || view.Usage.GoHeapBytes <= 0 || view.Usage.Goroutines <= 0 {
			t.Fatalf("usage = %+v", view.Usage)
		}
	}
}

func TestProxyHandlerLimitsSizeIngressBudgets(t *testing.T) {
	proxy := application.NewProxy(nil, nil, nil, application.ProxyConfig{MaxStreams: 4, MaxQueued: 2})
	handler := NewProxyHandlerWithLimits(nil, proxy, nil, ProxyHandlerLimits{BodyReaders: 7, ClientWebSockets: 9})
	if open, limit := handler.ClientWebSockets(); open != 0 || limit != 9 || cap(handler.reading) != 7 || cap(handler.websocketBodies) != 12 {
		t.Fatalf("budgets = %d/%d readers=%d bodies=%d", open, limit, cap(handler.reading), cap(handler.websocketBodies))
	}
	if active, queued := proxy.AdmissionUsage(); active != 0 || queued != 0 {
		t.Fatalf("idle usage = %d/%d", active, queued)
	}
}
