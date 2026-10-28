package metrics

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRegistryRendersPrometheusText(t *testing.T) {
	r := NewRegistry()
	done := r.RequestStarted()
	done("responses", "2xx", 1500*time.Millisecond)
	r.RequestStarted()("admin_api", "5xx", 20*time.Millisecond)
	inFlight := r.RequestStarted()
	r.TaskFinished("account_usage", time.Second, false, false, time.Unix(1_800_000_000, 0))
	r.TaskFinished("account_usage", time.Second, true, false, time.Unix(1_800_000_100, 0))
	r.TaskFinished("automations", time.Millisecond, false, true, time.Unix(1_800_000_000, 0))
	r.Gauge("codex_lb_generations_active", "Active generations.", func() []Sample { return []Sample{{Value: 3}} })
	r.Gauge("codex_lb_accounts", "Accounts by status.", func() []Sample {
		return []Sample{{Labels: map[string]string{"status": `quota"exceeded`}, Value: 2}}
	})
	w := httptest.NewRecorder()
	r.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/metrics", nil))
	body := w.Body.String()
	for _, want := range []string{
		`codex_lb_http_requests_total{code="2xx",group="responses"} 1`,
		`codex_lb_http_requests_total{code="5xx",group="admin_api"} 1`,
		`codex_lb_http_request_duration_seconds_bucket{group="responses",le="1"} 0`,
		`codex_lb_http_request_duration_seconds_bucket{group="responses",le="2.5"} 1`,
		`codex_lb_http_request_duration_seconds_bucket{group="responses",le="+Inf"} 1`,
		`codex_lb_http_request_duration_seconds_sum{group="responses"} 1.5`,
		`codex_lb_http_in_flight_requests 1`,
		`codex_lb_background_runs_total{result="ok",task="account_usage"} 1`,
		`codex_lb_background_runs_total{result="error",task="account_usage"} 1`,
		`codex_lb_background_runs_total{result="panic",task="automations"} 1`,
		`codex_lb_background_last_success_timestamp_seconds{task="account_usage"} 1.8e+09`,
		`codex_lb_generations_active 3`,
		`codex_lb_accounts{status="quota\"exceeded"} 2`,
		"# TYPE codex_lb_http_request_duration_seconds histogram",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in\n%s", want, body)
		}
	}
	if w.Header().Get("Content-Type") != "text/plain; version=0.0.4; charset=utf-8" {
		t.Fatalf("content type = %q", w.Header().Get("Content-Type"))
	}
	inFlight("responses", "2xx", time.Millisecond)
}
