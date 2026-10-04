package chatgpt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"codex-lb/internal/application"
)

func TestFetchUsageParsesWindowsAndSendsHeaders(t *testing.T) {
	var observedAuth, observedAccount string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observedAuth = r.Header.Get("Authorization")
		observedAccount = r.Header.Get("chatgpt-account-id")
		if !strings.HasPrefix(r.URL.Path, "/backend-api/wham/usage") {
			t.Fatalf("path = %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"plan_type": "team", "workspace_id": "org-1", "seat_type": "Owner-Admin",
			"rate_limit": map[string]any{
				"allowed":          true,
				"limit_reached":    false,
				"primary_window":   map[string]any{"used_percent": 42.5, "reset_at": 1900000000, "limit_window_seconds": 300},
				"secondary_window": map[string]any{"used_percent": 250.0},
			},
			"credits":                  map[string]any{"has_credits": true, "unlimited": false, "balance": "12.5"},
			"rate_limit_reset_credits": map[string]any{"available_count": 2},
			"additional_rate_limits": []map[string]any{{
				"limit_name": "Copilot code", "metered_feature": "code",
				"rate_limit": map[string]any{"primary_window": map[string]any{"used_percent": 10}},
			}},
		})
	}))
	defer server.Close()
	client := NewUsageClient(UsageConfig{BaseURL: server.URL, HTTPClient: server.Client()})
	snapshot, err := client.FetchUsage(context.Background(), "token-1", "chatgpt-1")
	if err != nil {
		t.Fatal(err)
	}
	if observedAuth != "Bearer token-1" || observedAccount != "chatgpt-1" {
		t.Fatalf("headers = %q %q", observedAuth, observedAccount)
	}
	if snapshot.PlanType != "team" || snapshot.WorkspaceID != "org-1" || snapshot.SeatType != "Owner-Admin" {
		t.Fatalf("snapshot metadata = %+v", snapshot)
	}
	if snapshot.Primary == nil || *snapshot.Primary.UsedPercent != 42.5 || snapshot.Primary.ResetAt == nil || snapshot.Primary.ResetAt.Unix() != 1900000000 || *snapshot.Primary.WindowMinutes != 5 {
		t.Fatalf("primary = %+v", snapshot.Primary)
	}
	if snapshot.Secondary == nil || *snapshot.Secondary.UsedPercent != 100 {
		t.Fatalf("secondary = %+v", snapshot.Secondary)
	}
	if snapshot.Credits.Has == nil || !*snapshot.Credits.Has || snapshot.ResetCreditCount == nil || *snapshot.ResetCreditCount != 2 {
		t.Fatalf("credits = %+v reset %+v", snapshot.Credits, snapshot.ResetCreditCount)
	}
	if snapshot.RateLimitAllowed == nil || !*snapshot.RateLimitAllowed || snapshot.RateLimitReached == nil || *snapshot.RateLimitReached {
		t.Fatalf("rate-limit permission = %+v", snapshot)
	}
	if len(snapshot.AdditionalQuotas) != 1 || snapshot.AdditionalQuotas[0].LimitName != "Copilot code" || snapshot.AdditionalQuotas[0].Primary == nil {
		t.Fatalf("additional = %+v", snapshot.AdditionalQuotas)
	}
}

func TestFetchUsagePreservesOptionalRateLimitPermission(t *testing.T) {
	for _, test := range []struct {
		name                     string
		body                     string
		wantAllowed, wantReached string
	}{
		{"denied", `{"rate_limit":{"allowed":false,"limit_reached":true}}`, "false", "true"},
		{"missing", `{"rate_limit":{}}`, "", ""},
		{"absent", `{}`, "", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			client := NewUsageClient(UsageConfig{BaseURL: server.URL, HTTPClient: server.Client()})
			snapshot, err := client.FetchUsage(context.Background(), "token", "account")
			if err != nil {
				t.Fatal(err)
			}
			check := func(name, want string, value *bool) {
				t.Helper()
				switch want {
				case "":
					if value != nil {
						t.Fatalf("%s = %v, want absent", name, *value)
					}
				default:
					if value == nil || fmt.Sprint(*value) != want {
						t.Fatalf("%s = %v, want %s", name, value, want)
					}
				}
			}
			check("allowed", test.wantAllowed, snapshot.RateLimitAllowed)
			check("limit reached", test.wantReached, snapshot.RateLimitReached)
		})
	}
}

func TestAdditionalQuotaFieldPresenceSurvivesMapping(t *testing.T) {
	if usage := (usagePayload{}).snapshot(); usage.AdditionalQuotas != nil {
		t.Fatal("omitted additional_rate_limits became an explicit clear")
	}
	if usage := (usagePayload{AdditionalRateLimits: []additionalRateLimitPayload{}}).snapshot(); usage.AdditionalQuotas == nil {
		t.Fatal("explicit empty additional_rate_limits was lost")
	}
}

func TestFetchUsageClassifiesLoneWeeklyWindow(t *testing.T) {
	for _, test := range []struct {
		name, primary, secondary, want string
	}{
		{"weekly", `{"used_percent":41,"limit_window_seconds":604800,"reset_at":1900000000}`, `null`, "secondary"},
		{"monthly", `{"used_percent":41,"limit_window_seconds":2592000}`, `null`, "monthly"},
		{"five_hour", `{"used_percent":41,"limit_window_seconds":18000}`, `null`, "primary"},
		{"unknown_duration", `{"used_percent":41}`, `null`, "primary"},
		{"not_exact_week", `{"used_percent":41,"limit_window_seconds":604801}`, `null`, "primary"},
		{"dual", `{"used_percent":41,"limit_window_seconds":18000}`, `{"used_percent":12,"limit_window_seconds":604800}`, "dual"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`{"rate_limit":{"primary_window":` + test.primary + `,"secondary_window":` + test.secondary + `}}`))
			}))
			defer server.Close()
			client := NewUsageClient(UsageConfig{BaseURL: server.URL, HTTPClient: server.Client()})
			snapshot, err := client.FetchUsage(context.Background(), "synthetic", "synthetic-account")
			if err != nil {
				t.Fatal(err)
			}
			windows := map[string]*application.UsageWindow{"primary": snapshot.Primary, "secondary": snapshot.Secondary, "monthly": snapshot.Monthly}
			if test.want == "dual" {
				if snapshot.Primary == nil || snapshot.Secondary == nil || snapshot.Monthly != nil || *snapshot.Secondary.UsedPercent != 12 {
					t.Fatal("dual windows were not preserved")
				}
				return
			}
			for name, window := range windows {
				if name != test.want && window != nil || name == test.want && (window == nil || window.UsedPercent == nil || *window.UsedPercent != 41) {
					t.Fatalf("incorrect %s window for %s", name, test.name)
				}
			}
			if test.name == "weekly" && (snapshot.Secondary.WindowMinutes == nil || *snapshot.Secondary.WindowMinutes != 10080 || snapshot.Secondary.ResetAt == nil || snapshot.Secondary.ResetAt.Unix() != 1900000000) {
				t.Fatal("weekly observation values were changed")
			}
		})
	}
}

func TestFetchUsageOmitsSyntheticAccountHeaderAndMapsErrors(t *testing.T) {
	var observedAccount string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observedAccount = r.Header.Get("chatgpt-account-id")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "TokenExpired", "message": "token expired"}})
	}))
	defer server.Close()
	client := NewUsageClient(UsageConfig{BaseURL: server.URL, HTTPClient: server.Client()})
	_, err := client.FetchUsage(context.Background(), "token-1", "email_abc")
	var usageErr *application.UsageError
	if !errors.As(err, &usageErr) || usageErr.Code != "tokenexpired" || usageErr.Message != "token expired" || usageErr.Status != 403 {
		t.Fatalf("err = %v", err)
	}
	if observedAccount != "" {
		t.Fatalf("synthetic account header sent: %q", observedAccount)
	}
}

func TestFetchResetCreditsAndConsume(t *testing.T) {
	var consumeBody map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/backend-api/wham/rate-limit-reset-credits":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"available_count": 1,
				"credits": []map[string]any{{
					"id": "credit-1", "status": "available", "reset_type": "primary",
					"expires_at": "2026-01-02T03:04:05Z", "title": "Bonus",
				}},
			})
		case "/backend-api/wham/rate-limit-reset-credits/consume":
			_ = json.NewDecoder(r.Body).Decode(&consumeBody)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": "reset", "windows_reset": 2,
				"credit": map[string]any{"id": "credit-1", "status": "redeemed", "redeemed_at": "2026-01-01T00:00:00Z"},
			})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	client := NewUsageClient(UsageConfig{BaseURL: server.URL, HTTPClient: server.Client()})
	credits, err := client.FetchResetCredits(context.Background(), "token-1", "chatgpt-1")
	if err != nil || credits.AvailableCount != 1 || len(credits.Credits) != 1 {
		t.Fatalf("credits = %+v err %v", credits, err)
	}
	if credits.Credits[0].ExpiresAt == nil || !credits.Credits[0].ExpiresAt.Equal(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Fatalf("expires = %+v", credits.Credits[0].ExpiresAt)
	}
	result, err := client.ConsumeResetCredit(context.Background(), "token-1", "chatgpt-1", "credit-1", "redeem-1")
	if err != nil || result.Code != "reset" || result.WindowsReset != 2 || result.RedeemedAt == nil {
		t.Fatalf("consume = %+v err %v", result, err)
	}
	if consumeBody["credit_id"] != "credit-1" || consumeBody["redeem_request_id"] != "redeem-1" {
		t.Fatalf("consume body = %+v", consumeBody)
	}
	consumeBody = nil
	if _, err := client.ConsumeResetCredit(context.Background(), "token-1", "chatgpt-1", "", "legacy-request"); err != nil {
		t.Fatal(err)
	}
	if len(consumeBody) != 1 || consumeBody["redeem_request_id"] != "legacy-request" {
		t.Fatalf("server-selected reset must send only redeem_request_id: %+v", consumeBody)
	}
}

func TestConsumeRejectsUnknownCode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": "surprise", "windows_reset": 1})
	}))
	defer server.Close()
	client := NewUsageClient(UsageConfig{BaseURL: server.URL, HTTPClient: server.Client()})
	if _, err := client.ConsumeResetCredit(context.Background(), "t", "a", "c", "r"); err == nil {
		t.Fatal("unknown consume code accepted")
	}
}

func TestFetchResetCreditsRejectsMalformedSnapshot(t *testing.T) {
	for _, body := range []string{`null`, `{}`, `{"available_count":null,"credits":[]}`, `{"available_count":0,"credits":null}`, `{"available_count":-1,"credits":[]}`, `{"available_count":1,"credits":[{"status":"available"}]}`} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()
			client := NewUsageClient(UsageConfig{BaseURL: server.URL, HTTPClient: server.Client()})
			if _, err := client.FetchResetCredits(context.Background(), "synthetic", "acct"); err == nil {
				t.Fatal("malformed snapshot accepted as authoritative credits")
			}
		})
	}
}
