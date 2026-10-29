package httpapi_test

import (
	"compress/gzip"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

// seedKeyUsageReports adds, on top of the overview fixture, an ungrouped key
// whose name looks like a spreadsheet formula, another ungrouped key and
// settled events of three keys spread over ten days, half of them folded into
// the rollups.
func seedKeyUsageReports(t *testing.T, server *overviewServer) {
	t.Helper()
	ctx := context.Background()
	store := server.store
	now := time.Now().UTC()
	for _, key := range []domain.APIKey{
		{ID: "key-formula", Name: "=SUM(A1)", KeyHash: hashHex("formula"), KeyPrefix: "sk-clb-formula", IsActive: true, CreatedAt: now},
		{ID: "key-wide", Name: "Wide", KeyHash: hashHex("wide"), KeyPrefix: "sk-clb-wide", IsActive: true, CreatedAt: now},
	} {
		if err := store.SaveAPIKey(ctx, key, now); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 40 {
		key := []string{"key-team", "key-wide", "key-formula"}[i%3]
		id := fmt.Sprintf("req-report-%02d", i)
		usage := domain.UsageAmount{InputTokens: int64(100 + i), OutputTokens: int64(10 + i), CachedInputTokens: int64(i),
			CostMicrodollars: int64(1234567 + i*1001)}
		at := now.Add(-time.Duration(i*6+1) * time.Hour)
		status := "success"
		if i%7 == 0 {
			status = "error"
		}
		if _, err := store.ReserveUsage(ctx, domain.ReservationRequest{ID: id, APIKeyID: key, AccountID: "acct-a", Model: "gpt-test",
			Budget: usage, Now: at}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.SettleUsage(ctx, id, domain.UsageSettlement{Status: "finalized", Event: domain.UsageEvent{RequestID: id,
			APIKeyID: key, AccountID: "acct-a", Model: "gpt-test", RequestKind: "normal", Status: status, RequestedAt: at, Usage: usage}}); err != nil {
			t.Fatal(err)
		}
		if i == 20 {
			if _, err := store.FoldUsageRollups(ctx, 1000, 1000); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func decodeKeyUsageReport(t *testing.T, body []byte) domain.KeyUsageReport {
	t.Helper()
	var report domain.KeyUsageReport
	if err := json.Unmarshal(body, &report); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	return report
}

// GET /api/reports/keys resolves groups by current membership, explicit keys
// and the ungrouped keys without deleted keys, is compressed, sends
// Server-Timing, requires an administrator and validates every parameter
// (R30, R48, R50).
func TestKeyUsageReportRoute(t *testing.T) {
	server := newOverviewServer(t, nil)
	seedOverviewRoutes(t, server)
	seedKeyUsageReports(t, server)
	period := "start_date=" + time.Now().UTC().AddDate(0, 0, -11).Format(time.DateOnly) + "&timezone=UTC"
	result := server.do("GET", "/api/reports/keys?"+period, nil, func(r *http.Request) { r.Header.Set("Accept-Encoding", "gzip") })
	if result.Code != 200 || result.Header().Get("Content-Encoding") != "gzip" || !strings.HasPrefix(result.Header().Get("Server-Timing"), "app;dur=") {
		t.Fatalf("report %d %v", result.Code, result.Header())
	}
	reader, err := gzip.NewReader(result.Body)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	report := decodeKeyUsageReport(t, body)
	if !report.Selection.AllKeys || report.Selection.ResolvedKeyCount != 4 || report.Summary.Requests != 42 || report.ActiveKeys != 4 ||
		len(report.Daily) != 12 || report.Period.Days != 12 || report.SeriesBy != domain.SeriesGroup || len(report.Groups) != 2 {
		t.Fatalf("all keys report %+v", report)
	}
	for _, key := range report.Keys {
		if key.ID == "key-gone" {
			t.Fatal("a deleted key is reported")
		}
	}
	for query, want := range map[string][]string{
		"group_id=grp-1":                       {"key-team"},
		"ungrouped=1":                          {"key-formula", "key-solo", "key-wide"},
		"api_key_id=key-solo&api_key_id=other": {"key-solo"},
		"group_id=grp-1&api_key_id=key-solo":   {"key-solo", "key-team"},
		"group_id=missing":                     {},
	} {
		result := server.do("GET", "/api/reports/keys?"+period+"&"+query, nil, nil)
		if result.Code != 200 {
			t.Fatalf("%s: %d %s", query, result.Code, result.Body)
		}
		report := decodeKeyUsageReport(t, result.Body.Bytes())
		var ids []string
		for _, key := range report.Keys {
			ids = append(ids, key.ID)
		}
		slices.Sort(ids)
		if strings.Join(ids, ",") != strings.Join(want, ",") || report.Selection.AllKeys {
			t.Errorf("%s: keys %v, want %v", query, ids, want)
		}
	}
	for query, code := range map[string]string{
		"color=red":   "invalid_report_filters",
		"ungrouped=2": "invalid_report_filters",
		"api_key_id=": "invalid_report_filters",
		"start_date=2026-01-01&start_date=2026-01-02": "invalid_report_filters",
		"timezone=Mars/Base":                          "invalid_timezone",
		"start_date=2026-02-02&end_date=2026-02-01":   "invalid_report_date_range",
		"start_date=2024-10-30&end_date=2026-10-31":   "invalid_report_date_range",
		"end_date=31.10.2026":                         "invalid_report_date_range",
	} {
		for _, path := range []string{"/api/reports/keys?", "/api/reports/keys/export.csv?"} {
			result := server.do("GET", path+query, nil, nil)
			var envelope struct {
				Error struct{ Code string } `json:"error"`
			}
			if result.Code != 400 || json.Unmarshal(result.Body.Bytes(), &envelope) != nil || envelope.Error.Code != code {
				t.Errorf("%s%s: %d %s, want 400 %s", path, query, result.Code, result.Body, code)
			}
		}
	}
	anonymous := *server
	anonymous.cookie = nil
	for _, path := range []string{"/api/reports/keys", "/api/reports/keys/export.csv"} {
		if result := anonymous.do("GET", path, nil, nil); result.Code != 401 {
			t.Errorf("anonymous %s: %d", path, result.Code)
		}
	}
}

// The per-day export has the documented header, one row per day and key with
// requests, formula-safe text cells, and sums equal to the JSON report for the
// same parameters (R31).
func TestKeyUsageExportSumsEqualTheReport(t *testing.T) {
	server := newOverviewServer(t, nil)
	seedOverviewRoutes(t, server)
	seedKeyUsageReports(t, server)
	for _, query := range []string{"timezone=Europe/Prague", "timezone=Asia/Kolkata&ungrouped=1", "group_id=grp-1&api_key_id=key-formula"} {
		query += "&start_date=" + time.Now().UTC().AddDate(0, 0, -12).Format(time.DateOnly)
		result := server.do("GET", "/api/reports/keys?"+query, nil, nil)
		if result.Code != 200 {
			t.Fatalf("%s: %d %s", query, result.Code, result.Body)
		}
		report := decodeKeyUsageReport(t, result.Body.Bytes())
		result = server.do("GET", "/api/reports/keys/export.csv?"+query, nil, func(r *http.Request) { r.Header.Set("Accept-Encoding", "gzip") })
		disposition := result.Header().Get("Content-Disposition")
		if result.Code != 200 || result.Header().Get("Content-Type") != "text/csv; charset=utf-8" ||
			disposition != `attachment; filename="key-usage-`+report.Period.Start+`_`+report.Period.End+`.csv"` ||
			!strings.HasPrefix(result.Header().Get("Server-Timing"), "app;dur=") {
			t.Fatalf("%s: export %d %v", query, result.Code, result.Header())
		}
		records, err := csv.NewReader(result.Body).ReadAll()
		if err != nil {
			t.Fatal(err)
		}
		if strings.Join(records[0], ",") != "date,group_id,group_name,key_id,key_name,requests,input_tokens,output_tokens,cached_input_tokens,reasoning_tokens,cost_usd,errors,cancelled" {
			t.Fatalf("header %v", records[0])
		}
		var sums [7]int64
		var cost float64
		seen := map[string]bool{}
		for _, record := range records[1:] {
			if record[4] == "=SUM(A1)" || record[4] != "'=SUM(A1)" && record[3] == "key-formula" {
				t.Fatalf("formula cell %q", record[4])
			}
			if seen[record[0]+record[3]] {
				t.Fatalf("duplicate row %v", record)
			}
			seen[record[0]+record[3]] = true
			for i, column := range []int{5, 6, 7, 8, 11, 12} {
				value, err := strconv.ParseInt(record[column], 10, 64)
				if err != nil {
					t.Fatal(err)
				}
				sums[i] += value
			}
			if record[5] == "0" {
				t.Fatalf("row without requests %v", record)
			}
			value, err := strconv.ParseFloat(record[10], 64)
			if err != nil || !strings.Contains(record[10], ".") || len(strings.Split(record[10], ".")[1]) != 6 {
				t.Fatalf("cost %q", record[10])
			}
			cost += value
		}
		s := report.Summary
		if sums != [7]int64{s.Requests, s.InputTokens, s.OutputTokens, s.CachedInputTokens, s.Errors, s.Cancelled, 0} ||
			math.Abs(cost-s.CostUSD) > 1e-6*float64(len(records)) || s.Requests == 0 {
			t.Fatalf("%s: CSV sums %v cost %v, report %+v", query, sums, cost, s)
		}
	}
}
