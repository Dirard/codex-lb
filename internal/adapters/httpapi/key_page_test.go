package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

// The administrator key page needs an administrator session, answers 404 for
// unknown and internal keys, carries the shell status, cost by account,
// lifetime totals, assigned accounts and the full request rows, and sends
// Server-Timing; its recent requests page by cursor like the request log's
// offset pages and filter errors only (R20, R23, R48, R50).
func TestAdminKeyPageRoutes(t *testing.T) {
	f := newKeyPageFixture(t)
	ctx := context.Background()
	token, err := f.server.auth.SetupPassword(ctx, "test", "", "synthetic-password", false, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	admin := &http.Cookie{Name: sessionCookie, Value: token}
	get := func(path string, cookie *http.Cookie) (int, string, http.Header) {
		t.Helper()
		r := httptestRequest("GET", path, cookie)
		r.Header.Set("Accept-Encoding", "identity")
		w := serve(f.handler, r)
		return w.Code, w.Body.String(), w.Header()
	}
	for _, path := range []string{"/api/api-keys/a/page", "/api/api-keys/a/requests"} {
		if code, _, _ := get(path, nil); code != 401 {
			t.Fatalf("anonymous %s: %d", path, code)
		}
	}
	for _, id := range []string{"missing", domain.WarmupKeyID, domain.LocalProxyKeyID} {
		for _, path := range []string{"/api/api-keys/" + id + "/page", "/api/api-keys/" + id + "/requests"} {
			if code, _, _ := get(path, admin); code != 404 {
				t.Fatalf("%s: %d", path, code)
			}
		}
	}
	if code, body, _ := get("/api/api-keys/a/page?api_key_id=b", admin); code != 400 || !strings.Contains(body, "invalid_report_filters") {
		t.Fatalf("unknown parameter %d %s", code, body)
	}
	code, body, header := get("/api/api-keys/a/page/?timezone=Asia/Kolkata", admin)
	if code != 200 || !regexp.MustCompile(`^app;dur=\d+\.\d$`).MatchString(header.Get("Server-Timing")) {
		t.Fatalf("admin page %d %v %s", code, header, body)
	}
	var page domain.KeyPageResponse
	if err := json.Unmarshal([]byte(body), &page); err != nil {
		t.Fatal(err)
	}
	if page.Scope != "admin" || page.Shell == nil || page.Lifetime == nil || page.Lifetime.RequestCount != 3 || page.ByAccount == nil ||
		len(*page.ByAccount) != 1 || *(*page.ByAccount)[0].Label != "shared@example.invalid" || page.Key.AssignedAccounts == nil ||
		(*page.Key.AssignedAccounts)[0].Label != "shared@example.invalid" || page.Period.Timezone != "Asia/Kolkata" || !page.Period.IncludesToday ||
		page.Summary.Requests != 3 || len(page.Daily) != 7 || len(page.ByModel) != 1 || page.ByModel[0].Model != "model-a" ||
		page.RecentRequests[0].ClientIP == nil || page.RecentRequests[0].ConversationID == nil || page.RecentRequests[0].UserAgent == nil ||
		page.Pool == nil || page.Pool.Scope != "group" || page.Group == nil || len(page.Group.Keys) != 1 {
		t.Fatalf("admin page %s", body)
	}
	// Cursor pages equal the request log's offset pages of the key.
	cursor := ""
	for offset := 0; offset < 4; offset++ {
		path := "/api/api-keys/a/requests?limit=1"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		code, body, _ := get(path, admin)
		var items domain.KeyRequestsResponse
		if code != 200 || json.Unmarshal([]byte(body), &items) != nil {
			t.Fatalf("%s: %d %s", path, code, body)
		}
		code, logBody, _ := get("/api/request-logs?apiKeyId=a&limit=1&offset="+strconv.Itoa(offset), admin)
		var log domain.RequestLogsResponse
		if code != 200 || json.Unmarshal([]byte(logBody), &log) != nil {
			t.Fatalf("request log %d %s", code, logBody)
		}
		if len(items.Items) != len(log.Requests) || len(items.Items) == 1 && items.Items[0].RequestID != log.Requests[0].RequestID {
			t.Fatalf("offset %d: %s vs %s", offset, body, logBody)
		}
		if items.NextCursor == nil {
			if offset != 2 {
				t.Fatalf("paging ended at %d", offset)
			}
			break
		}
		cursor = *items.NextCursor
	}
	code, body, _ = get("/api/api-keys/a/requests?errors_only=true", admin)
	var failed domain.KeyRequestsResponse
	if code != 200 || json.Unmarshal([]byte(body), &failed) != nil || len(failed.Items) != 1 || failed.Items[0].Status != "error" ||
		failed.Items[0].AccountID == nil || failed.NextCursor != nil {
		t.Fatalf("errors only %d %s", code, body)
	}
	// Admin JSON is compressed like every administrator response.
	r := httptestRequest("GET", "/api/api-keys/a/page", admin)
	r.Header.Set("Accept-Encoding", "gzip")
	if w := serve(f.handler, r); w.Code != 200 || w.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("admin page encoding %d %v", w.Code, w.Header())
	}
}
