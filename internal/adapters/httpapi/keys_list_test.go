package httpapi_test

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

type keysPageBody struct {
	Shell struct {
		Ready bool `json:"ready"`
	} `json:"shell"`
	Total  int `json:"total"`
	Offset int `json:"offset"`
	Limit  int `json:"limit"`
	Rows   []struct {
		ID            string   `json:"id"`
		GroupID       *string  `json:"groupId"`
		GroupName     *string  `json:"groupName"`
		Status        string   `json:"status"`
		SpendTodayUSD float64  `json:"spendTodayUsd"`
		Spend7dUSD    *float64 `json:"spend7dUsd"`
		MaxLimit      *struct {
			Percent float64 `json:"percent"`
		} `json:"maxLimit"`
		LimitsSummary []struct {
			Type     string `json:"type"`
			MaxValue int64  `json:"maxValue"`
		} `json:"limitsSummary"`
	} `json:"rows"`
}

func (s *overviewServer) keysPage(query string) keysPageBody {
	s.t.Helper()
	result := s.do("GET", "/api/keys?"+query, nil, nil)
	if result.Code != http.StatusOK {
		s.t.Fatalf("keys %s: %d %s", query, result.Code, result.Body)
	}
	var page keysPageBody
	if err := json.Unmarshal(result.Body.Bytes(), &page); err != nil {
		s.t.Fatal(err)
	}
	return page
}

func rowIDs(page keysPageBody) []string {
	ids := make([]string, 0, len(page.Rows))
	for _, row := range page.Rows {
		ids = append(ids, row.ID)
	}
	return ids
}

// seedManyKeys adds n keys named <name> 000 … with the given group.
func seedManyKeys(t *testing.T, server *overviewServer, name string, n int, group *string) []string {
	t.Helper()
	now := time.Now().UTC()
	ids := make([]string, n)
	for i := range n {
		ids[i] = fmt.Sprintf("key-%s-%03d", strings.ToLower(name), i)
		if err := server.store.SaveAPIKey(context.Background(), domain.APIKey{ID: ids[i], Name: fmt.Sprintf("%s %03d", name, i),
			KeyHash: hashHex(ids[i]), KeyPrefix: fmt.Sprintf("sk-clb-%s%03d", strings.ToLower(name[:1]), i), GroupID: group, IsActive: true, CreatedAt: now,
			AccountAssignmentScopeEnabled: group != nil}, now); err != nil {
			t.Fatal(err)
		}
	}
	return ids
}

// GET /api/keys lists the current keys with spend, group and limits, filters
// and pages on the server with the total before paging, is compressed, sends
// Server-Timing, requires an administrator and refuses unknown, repeated and
// invalid parameters (R33, R34, R48, R50).
func TestKeysListRoute(t *testing.T) {
	server := newOverviewServer(t, nil)
	seedOverviewRoutes(t, server)
	anonymous := *server
	anonymous.cookie = nil
	for _, path := range []string{"/api/keys", "/api/keys/ids"} {
		if got := anonymous.do("GET", path, nil, nil).Code; got != http.StatusUnauthorized {
			t.Fatalf("anonymous %s %d", path, got)
		}
	}
	result := server.do("GET", "/api/keys?timezone=Europe/Prague", nil, func(r *http.Request) { r.Header.Set("Accept-Encoding", "gzip") })
	if result.Code != 200 || result.Header().Get("Content-Encoding") != "gzip" || !strings.HasPrefix(result.Header().Get("Server-Timing"), "app;dur=") {
		t.Fatalf("keys %d %v", result.Code, result.Header())
	}
	reader, err := gzip.NewReader(result.Body)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	var page keysPageBody
	if err := json.Unmarshal(body, &page); err != nil {
		t.Fatal(err)
	}
	if page.Total != 2 || !slices.Equal(rowIDs(page), []string{"key-solo", "key-team"}) || page.Limit != 50 || !page.Shell.Ready {
		t.Fatalf("all keys %s", body)
	}
	solo, team := page.Rows[0], page.Rows[1]
	if solo.SpendTodayUSD != 2 || solo.Spend7dUSD == nil || *solo.Spend7dUSD != 2 || len(solo.LimitsSummary) != 1 ||
		solo.LimitsSummary[0].MaxValue != 100 || solo.MaxLimit == nil || solo.MaxLimit.Percent != 10 || solo.GroupID != nil {
		t.Fatalf("solo row %+v", solo)
	}
	if team.GroupName == nil || *team.GroupName != "Team" || team.Status != "active" || team.SpendTodayUSD != 1 {
		t.Fatalf("team row %+v", team)
	}
	for query, want := range map[string][]string{
		"group=grp-1":          {"key-team"},
		"group=ungrouped":      {"key-solo"},
		"group=missing":        {},
		"q=SOLO":               {"key-solo"},
		"q=sk-clb-te":          {"key-team"},
		"status=disabled":      {},
		"sort=spend_today":     {"key-solo", "key-team"},
		"sort=name&order=desc": {"key-team", "key-solo"},
		"inactive_days=7":      {},
		"near_limit=1":         {},
		"expiring_days=7":      {},
	} {
		if got := rowIDs(server.keysPage(query)); !slices.Equal(got, want) {
			t.Errorf("%s: %v, want %v", query, got, want)
		}
	}
	for _, query := range []string{"selected=key-solo", "status=active&status=disabled", "limit=101", "limit=0", "offset=-1",
		"sort=spend", "order=up", "near_limit=yes", "expiring_days=0", "inactive_days=366", "status=paused", "q=" + strings.Repeat("x", 257)} {
		result := server.do("GET", "/api/keys?"+query, nil, nil)
		if result.Code != http.StatusBadRequest || !strings.Contains(result.Body.String(), "invalid_key_filters") {
			t.Errorf("%s: %d %s", query, result.Code, result.Body)
		}
	}
	if result := server.do("GET", "/api/keys?timezone=Mars/Olympus", nil, nil); result.Code != http.StatusBadRequest ||
		!strings.Contains(result.Body.String(), "invalid_timezone") {
		t.Fatalf("unknown zone %d %s", result.Code, result.Body)
	}
}

// Paging returns the requested slice with the total before paging and an
// empty page past the end; the matching ids follow the list order, refuse
// paging parameters and stop at 1 000 (R33).
func TestKeysListPagingAndIDsRoutes(t *testing.T) {
	server := newOverviewServer(t, nil)
	seedOverviewRoutes(t, server)
	ids := seedManyKeys(t, server, "Bulk", 120, nil)
	page := server.keysPage("q=bulk&offset=50&limit=25")
	if page.Total != 120 || page.Offset != 50 || page.Limit != 25 || !slices.Equal(rowIDs(page), ids[50:75]) {
		t.Fatalf("second page total %d offset %d limit %d rows %v", page.Total, page.Offset, page.Limit, rowIDs(page))
	}
	if page := server.keysPage("q=bulk&offset=200"); page.Total != 120 || len(page.Rows) != 0 {
		t.Fatalf("past the end %+v", page)
	}
	result := server.do("GET", "/api/keys/ids?q=bulk&order=desc", nil, nil)
	var matching struct {
		IDs   []string `json:"ids"`
		Total int      `json:"total"`
	}
	if result.Code != 200 || json.Unmarshal(result.Body.Bytes(), &matching) != nil || matching.Total != 120 || len(matching.IDs) != 120 ||
		matching.IDs[0] != ids[119] || !strings.HasPrefix(result.Header().Get("Server-Timing"), "app;dur=") {
		t.Fatalf("ids %d %s", result.Code, result.Body)
	}
	if result := server.do("GET", "/api/keys/ids?offset=10", nil, nil); result.Code != http.StatusBadRequest {
		t.Fatalf("ids with offset %d", result.Code)
	}
}

type bulkResultBody struct {
	Results []struct {
		ID   string `json:"id"`
		OK   bool   `json:"ok"`
		Code string `json:"code"`
	} `json:"results"`
}

func (s *overviewServer) bulk(body any) (int, bulkResultBody, string) {
	s.t.Helper()
	result := s.do("POST", "/api/keys/bulk", body, nil)
	var decoded bulkResultBody
	if result.Code == http.StatusOK {
		if err := json.Unmarshal(result.Body.Bytes(), &decoded); err != nil {
			s.t.Fatal(err)
		}
	}
	return result.Code, decoded, result.Body.String()
}

func bulkCodes(body bulkResultBody) []string {
	out := make([]string, 0, len(body.Results))
	for _, result := range body.Results {
		if result.OK {
			out = append(out, "ok")
		} else {
			out = append(out, result.Code)
		}
	}
	return out
}

// POST /api/keys/bulk moves keys with the group's limits, keeping the keys
// before and after a failing id changed and reporting it in place; refuses
// own limits for grouped keys; reports unknown, deleted and internal ids;
// disables in jobs; validates the body; and requires an administrator (R36–R38).
func TestKeysBulkRoute(t *testing.T) {
	server := newOverviewServer(t, nil)
	seedOverviewRoutes(t, server)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := server.store.SaveGroup(ctx, domain.AccountGroup{ID: "grp-2", Name: "Limited", AccountIDs: []string{"acct-a"},
		Limits: []domain.LimitRule{{Type: domain.LimitCostUSD, Window: domain.WindowDaily, MaxValue: 7_000_000}}}, now); err != nil {
		t.Fatal(err)
	}
	ids := seedManyKeys(t, server, "Bulk", 5, nil)
	if err := server.store.DeleteAPIKey(ctx, ids[2]); err != nil {
		t.Fatal(err)
	}
	code, body, raw := server.bulk(map[string]any{"ids": ids, "action": "move_group", "groupId": "grp-2"})
	if code != 200 || !slices.Equal(bulkCodes(body), []string{"ok", "ok", "not_found", "ok", "ok"}) || body.Results[2].ID != ids[2] {
		t.Fatalf("move %d %s", code, raw)
	}
	for _, i := range []int{0, 1, 3, 4} {
		key, err := server.store.GetAPIKey(ctx, ids[i])
		if err != nil || key.GroupID == nil || *key.GroupID != "grp-2" || !key.AccountAssignmentScopeEnabled ||
			len(key.Limits) != 1 || key.Limits[0].MaxValue != 7_000_000 {
			t.Fatalf("moved key %d: %v %+v", i, err, key)
		}
	}
	rules := []map[string]any{{"limitType": "total_tokens", "limitWindow": "weekly", "maxValue": 5000, "modelFilter": nil}}
	code, body, raw = server.bulk(map[string]any{"ids": []string{ids[0], "key-solo", domain.WarmupKeyID, "key-gone"},
		"action": "set_limits", "limits": rules})
	if code != 200 || !slices.Equal(bulkCodes(body), []string{"key_in_group", "ok", "not_found", "not_found"}) {
		t.Fatalf("set limits %d %s", code, raw)
	}
	if solo, err := server.store.GetAPIKey(ctx, "key-solo"); err != nil || len(solo.Limits) != 1 || solo.Limits[0].MaxValue != 5000 {
		t.Fatalf("solo limits %v %+v", err, solo.Limits)
	}
	code, body, raw = server.bulk(map[string]any{"ids": []string{ids[1], ids[3]}, "action": "move_group", "groupId": nil})
	if code != 200 || !slices.Equal(bulkCodes(body), []string{"ok", "ok"}) {
		t.Fatalf("ungroup %d %s", code, raw)
	}
	if page := server.keysPage("group=ungrouped&q=bulk"); !slices.Equal(rowIDs(page), []string{ids[1], ids[3]}) {
		t.Fatalf("ungrouped after the move %v", rowIDs(page))
	}
	many := seedManyKeys(t, server, "Many", 55, nil)
	code, body, raw = server.bulk(map[string]any{"ids": many, "action": "disable"})
	if codes := bulkCodes(body); code != 200 || len(codes) != 55 || slices.IndexFunc(codes, func(c string) bool { return c != "ok" }) >= 0 {
		t.Fatalf("disable %d %s", code, raw)
	}
	if page := server.keysPage("status=disabled&limit=100"); page.Total != 55 {
		t.Fatalf("%d disabled keys", page.Total)
	}
	for name, invalid := range map[string]map[string]any{
		"unknown action":       {"ids": ids, "action": "delete"},
		"no ids":               {"ids": []string{}, "action": "enable"},
		"repeated ids":         {"ids": []string{"a", "a"}, "action": "enable"},
		"group with disable":   {"ids": ids, "action": "disable", "groupId": "grp-2"},
		"move without group":   {"ids": ids, "action": "move_group"},
		"limits without rules": {"ids": ids, "action": "set_limits"},
		"limits with move":     {"ids": ids, "action": "move_group", "groupId": nil, "limits": rules},
	} {
		if code, _, raw := server.bulk(invalid); code != http.StatusBadRequest || !strings.Contains(raw, "invalid_bulk_request") {
			t.Errorf("%s: %d %s", name, code, raw)
		}
	}
	if code, _, raw := server.bulk(map[string]any{"ids": ids, "action": "set_limits",
		"limits": []map[string]any{{"limitType": "cost_usd", "limitWindow": "daily", "maxValue": 0}}}); code != http.StatusUnprocessableEntity ||
		!strings.Contains(raw, "invalid_limit") {
		t.Fatalf("invalid limits %d %s", code, raw)
	}
	if code, _, _ := server.bulk(map[string]any{"ids": ids, "action": "enable", "extra": true}); code != http.StatusBadRequest {
		t.Fatalf("unknown field %d", code)
	}
	anonymous := *server
	anonymous.cookie = nil
	if code, _, _ := anonymous.bulk(map[string]any{"ids": ids, "action": "enable"}); code != http.StatusUnauthorized {
		t.Fatalf("anonymous bulk %d", code)
	}
}
