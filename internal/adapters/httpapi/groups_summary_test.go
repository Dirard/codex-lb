package httpapi_test

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"regexp"
	"testing"
)

// GET /api/groups/summary returns the overview's group rows in the same order
// with 30-day spend, templates, members and keys, the pool row and the
// accounts, compressed with Server-Timing behind an administrator session
// (R40, R48, R50).
func TestGroupsSummaryRoute(t *testing.T) {
	server := newOverviewServer(t, nil)
	seedOverviewRoutes(t, server)
	anonymous := *server
	anonymous.cookie = nil
	if got := anonymous.do("GET", "/api/groups/summary", nil, nil).Code; got != http.StatusUnauthorized {
		t.Fatalf("anonymous groups summary %d", got)
	}
	bad := server.do("GET", "/api/groups/summary?timezone=Mars/Olympus", nil, nil)
	if bad.Code != http.StatusBadRequest || !bytes.Contains(bad.Body.Bytes(), []byte("invalid_timezone")) {
		t.Fatalf("unknown zone %d %s", bad.Code, bad.Body.String())
	}
	result := server.do("GET", "/api/groups/summary/?timezone=Europe/Prague", nil, nil)
	if result.Code != 200 {
		t.Fatalf("groups summary %d %s", result.Code, result.Body.String())
	}
	if timing := result.Header().Get("Server-Timing"); !regexp.MustCompile(`^app;dur=\d+\.\d$`).MatchString(timing) {
		t.Fatalf("Server-Timing %q", timing)
	}
	var summary struct {
		Timezone string `json:"timezone"`
		Shell    struct {
			Ready bool `json:"ready"`
		} `json:"shell"`
		Pool struct {
			ID            *string  `json:"id"`
			SpendTodayUSD float64  `json:"spendTodayUsd"`
			Spend30dUSD   *float64 `json:"spend30dUsd"`
			Keys          struct {
				Total int `json:"total"`
			} `json:"keys"`
		} `json:"pool"`
		Groups []map[string]json.RawMessage `json:"groups"`
		// Accounts list the non-deleted accounts for the member picker.
		Accounts []struct {
			ID     string `json:"id"`
			Health string `json:"health"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal(result.Body.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Timezone != "Europe/Prague" || !summary.Shell.Ready || summary.Pool.ID != nil || summary.Pool.SpendTodayUSD != 6 ||
		summary.Pool.Spend30dUSD == nil || *summary.Pool.Spend30dUSD != 6 || summary.Pool.Keys.Total != 2 || len(summary.Accounts) != 2 {
		t.Fatalf("summary header %+v", summary)
	}

	overview := server.do("GET", "/api/overview?timezone=Europe/Prague", nil, nil)
	var rows struct {
		Groups []map[string]json.RawMessage `json:"groups"`
	}
	if overview.Code != 200 || json.Unmarshal(overview.Body.Bytes(), &rows) != nil || len(rows.Groups) != len(summary.Groups) {
		t.Fatalf("overview %d: %d rows, summary %d", overview.Code, len(rows.Groups), len(summary.Groups))
	}
	for i, row := range summary.Groups {
		for field, value := range rows.Groups[i] {
			if !reflect.DeepEqual(row[field], value) {
				t.Fatalf("row %d field %s: summary %s, overview %s", i, field, row[field], value)
			}
		}
	}
	var team struct {
		Name        string   `json:"name"`
		Spend30dUSD *float64 `json:"spend30dUsd"`
		AccountIDs  []string `json:"accountIds"`
		Limits      []any    `json:"limits"`
		TopKeys7d   []struct {
			ID          string   `json:"id"`
			Spend30dUSD *float64 `json:"spend30dUsd"`
		} `json:"topKeys7d"`
	}
	body, err := json.Marshal(summary.Groups[1])
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, &team); err != nil {
		t.Fatal(err)
	}
	if team.Name != "Team" || team.Spend30dUSD == nil || *team.Spend30dUSD != 1 || !reflect.DeepEqual(team.AccountIDs, []string{"acct-a", "acct-r"}) ||
		team.Limits == nil || len(team.TopKeys7d) != 1 || team.TopKeys7d[0].ID != "key-team" || *team.TopKeys7d[0].Spend30dUSD != 1 {
		t.Fatalf("team row %s", body)
	}

	gzipped := server.do("GET", "/api/groups/summary", nil, func(r *http.Request) { r.Header.Set("Accept-Encoding", "gzip") })
	if gzipped.Header().Get("Content-Encoding") != "gzip" || gzipped.Header().Get("Server-Timing") == "" {
		t.Fatalf("groups summary must be compressed with Server-Timing: %v", gzipped.Header())
	}
	reader, err := gzip.NewReader(gzipped.Body)
	if err != nil {
		t.Fatal(err)
	}
	if body, err := io.ReadAll(reader); err != nil || !json.Valid(body) {
		t.Fatalf("gzip body %v", err)
	}
}
