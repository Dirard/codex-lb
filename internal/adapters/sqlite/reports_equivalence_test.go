package sqlite

import (
	"context"
	"fmt"
	"math/rand/v2"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

// The single-pass QueryReports must return the same response as the former
// per-day statements for mixed raw/folded data, filters and time zones,
// including daylight-saving and fractional-offset days; costs are integer
// sums and medians histogram approximations (design §1.3, §1.5).
func TestQueryReportsMatchesPerDayReference(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	random := rand.New(rand.NewPCG(7, 11))
	for i, alias := range []string{"alpha", "", "gamma"} {
		id := fmt.Sprintf("acct-%d", i)
		if err := s.SaveAccount(ctx, domain.Account{ID: id, Kind: domain.AccountChatGPT, Email: id + "@example.invalid",
			Alias: alias, PlanType: "plus", Status: domain.AccountActive, CreatedAt: fixedTime}); err != nil {
			t.Fatal(err)
		}
	}
	base := time.Date(2026, 2, 20, 0, 0, 0, 0, time.UTC)
	span := int64(80 * 24 * time.Hour / time.Millisecond)
	accounts := []any{"acct-0", "acct-1", "acct-2", nil, "", "\x1fodd"}
	statuses := []string{"success", "completed", "cancelled", "error", "quota"}
	agents := []any{"codex", "sdk", "", nil}
	conversations := []any{"conv-a", " conv-a ", "conv-b", "", "  ", nil}
	kinds := []string{"normal", "normal", "normal", "warmup", "compact"}
	for i := range 4000 {
		at := millis(base) + random.Int64N(span)
		var ttft any
		if random.IntN(5) > 0 {
			ttft = random.Int64N(900)
		}
		var legacy any
		if i%97 == 0 {
			legacy = fmt.Sprintf("legacy-%d", i)
		}
		if _, err := s.db.ExecContext(ctx, `INSERT INTO usage_events(request_id,api_key_id,account_id,model,status,request_kind,
 requested_at,conversation_id,useragent_group,input_tokens,output_tokens,cached_input_tokens,reasoning_tokens,
 reasoning_tokens_known,cost_microdollars,queue_latency_ms,total_latency_ms,latency_first_token_ms,legacy_request_id)
 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			fmt.Sprintf("req-%05d", i), fmt.Sprintf("key-%d", i%3), accounts[random.IntN(len(accounts))],
			fmt.Sprintf("model-%d", random.IntN(4)), statuses[random.IntN(len(statuses))], kinds[random.IntN(len(kinds))],
			at, conversations[random.IntN(len(conversations))], agents[random.IntN(len(agents))],
			random.Int64N(5000), random.Int64N(800), random.Int64N(6000), random.Int64N(300), random.IntN(2),
			random.Int64N(90000), random.Int64N(60)-5, random.Int64N(4000), ttft, legacy); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 600 {
		bucket := base.Unix() + random.Int64N(span/1000)/3600*3600
		if _, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO legacy_hourly_usage(bucket_epoch,account_id,api_key_id,model,
 service_tier,request_kind,is_deleted,request_count,error_count,cancelled_count,input_tokens,output_tokens,
 reasoning_tokens,output_or_reasoning_tokens,cached_input_tokens,cached_input_tokens_clamped,cost_usd,cost_count,
 reasoning_known_count) VALUES(?,?,?,?,'',?,0,?,?,?,?,?,?,?,?,?,?,1,?)`,
			bucket, []string{"acct-0", "\x1f", "acct-9", "\x1f\x1fodd"}[i%4], fmt.Sprintf("key-%d", i%3),
			fmt.Sprintf("model-%d", i%5), []string{"normal", "warmup"}[i%7/6], 1+random.Int64N(20), random.Int64N(3),
			random.Int64N(2), random.Int64N(9000), random.Int64N(900), random.Int64N(90), random.Int64N(900),
			random.Int64N(9000), random.Int64N(9000), float64(random.Int64N(5000000))/1e6, random.Int64N(3)); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO legacy_conversation_hourly(bucket_epoch,conversation_id,account_id,is_deleted,request_count)
 VALUES(?,?,?,0,1)`, bucket, fmt.Sprintf("conv-%d", i%40), []string{"acct-0", "\x1f"}[i%2]); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO legacy_import_state(id,source_sha256,key_fingerprint,folded_through,
 hourly_folded_through,imported_at,sidecar_path) VALUES(1,'x','y',?,?,?,'')`,
		millis(base.AddDate(0, 0, 20)), millis(base.AddDate(0, 0, 20)), millis(base)); err != nil {
		t.Fatal(err)
	}
	filters := []domain.ReportFilter{
		{},
		{AccountIDs: []string{"acct-0", "\x1fodd"}},
		{Model: "model-1"},
		{UserAgentGroup: "codex"},
		{UserAgentGroup: "Missing User-Agent"},
		{APIKeyIDs: []string{"key-1"}},
	}
	windows := []struct {
		zone       string
		start, end string
	}{
		{"UTC", "2026-02-20", "2026-05-10"},
		{"America/New_York", "2026-03-01", "2026-03-15"},
		{"Europe/Moscow", "2026-04-01", "2026-04-30"},
		{"Asia/Kathmandu", "2026-03-10", "2026-03-20"},
		{"Australia/Lord_Howe", "2026-04-01", "2026-04-10"},
	}
	for _, window := range windows {
		loc, err := time.LoadLocation(window.zone)
		if err != nil {
			t.Fatal(err)
		}
		startDay, _ := time.ParseInLocation("2006-01-02", window.start, loc)
		endDay, _ := time.ParseInLocation("2006-01-02", window.end, loc)
		days := int(endDay.Sub(startDay).Hours()/24) + 1
		for _, filter := range filters {
			filter.Start, filter.End, filter.Location = startDay.UTC(), endDay.AddDate(0, 0, 1).UTC(), loc
			filter.PreviousStart = startDay.AddDate(0, 0, -days).UTC()
			want, err := s.referenceQueryReports(ctx, filter)
			if err != nil {
				t.Fatal(err)
			}
			got, err := s.QueryReports(ctx, filter)
			if err != nil {
				t.Fatal(err)
			}
			if window.zone == "UTC" && filter.AccountIDs == nil && filter.Model == "" && filter.UserAgentGroup == "" && filter.APIKeyIDs == nil &&
				(len(got.Daily) != 80 || got.Summary.TotalRequests == 0 || got.Summary.TotalConversations == 0 ||
					got.Daily[40].MedianTTFTMS == 0 || got.Daily[40].MedianTPS == 0 || len(got.ByAccount) < 4 ||
					got.ByUserAgent[len(got.ByUserAgent)-1].Requests == 0) {
				t.Fatalf("fixture does not exercise the report: %+v", got.Summary)
			}
			assertReportNearReference(t, fmt.Sprintf("%s %+v", window.zone, filter), got, want)
		}
	}
}
