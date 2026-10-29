package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand/v2"
	"reflect"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

// seedAccountViewExtras gives every account of the dashboard scale fixture
// the rows only the accounts list reads: token ciphertexts, an additional
// quota, purchased credits, lifetime totals and, for every fifth account, a
// quota refusal.
func seedAccountViewExtras(t testing.TB, s *Store, accounts int) {
	t.Helper()
	now := millis(dashboardNow)
	for a := range accounts {
		id := fmt.Sprintf("acct-%d", a)
		for _, statement := range []struct {
			query string
			args  []any
		}{
			{`INSERT INTO account_credentials(account_id,access_token_encrypted,refresh_token_encrypted,id_token_encrypted,external_key_encrypted)
 VALUES(?,?,?,?,?)`, []any{id, []byte("access-" + id), []byte("refresh-" + id), []byte("id-" + id), []byte("never-read")}},
			{`INSERT INTO account_additional_quotas(account_id,quota_key,window,limit_name,metered_feature,used_percent,reset_at,window_minutes,observed_at)
 VALUES(?,'codex_spark','primary','codex_spark','codex',?,?,300,?)`, []any{id, float64(a % 90), now + 3600000, now}},
			{`INSERT INTO account_usage_credits(account_id,credits_has,credits_unlimited,credits_balance,observed_at) VALUES(?,1,0,?,?)`,
				[]any{id, float64(a) + 0.5, now}},
			{`INSERT INTO usage_totals(scope,scope_id,request_count,failed_count,input_tokens,output_tokens,cached_input_tokens,
 reasoning_tokens,cost_microdollars) VALUES('account',?,?,1,?,?,?,1,?)`, []any{id, 10 + a, 1000 * a, 100 * a, 2000 * a, 1000000 * a}},
		} {
			if _, err := s.db.Exec(statement.query, statement.args...); err != nil {
				t.Fatal(err)
			}
		}
		if a%5 == 0 {
			if _, err := s.db.Exec(`UPDATE accounts SET status='quota_exceeded' WHERE id=?`, id); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec(`INSERT INTO account_quota_block_evidence(account_id,blocked_at) VALUES(?,?)`, id, now-600000); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// The accounts list executes the same number of statements for 9 accounts as
// for 100, with every per-account row kind present (R43).
func TestListAccountViewsStatementCountIsIndependentOfScale(t *testing.T) {
	var counts []int64
	for _, accounts := range []int{9, 100} {
		s, counter := countingStore(t)
		seedDashboardScale(t, s, dashboardScale{keys: 30, groups: 5, accounts: accounts, days: 2, eventsPerKeyHour: 1})
		seedAccountViewExtras(t, s, accounts)
		before := counter.count()
		views, err := s.ListAccountViews(context.Background(), dashboardNow)
		if err != nil {
			t.Fatal(err)
		}
		counts = append(counts, counter.count()-before)
		if !views.RollupsUsable || len(views.Accounts) != accounts {
			t.Fatalf("%d accounts: usable %v, %d views", accounts, views.RollupsUsable, len(views.Accounts))
		}
		var requests int64
		for _, view := range views.Accounts {
			if view.Credential == nil || len(view.AdditionalQuotas) != 1 || view.Credits == nil || view.Totals.RequestCount == 0 {
				t.Fatalf("%s misses rows: %+v", view.Account.ID, view)
			}
			if view.Credential.ExternalKeyEncrypted != nil {
				t.Fatalf("%s: the external key was read", view.Account.ID)
			}
			requests += view.Usage7d.Requests
		}
		if requests == 0 {
			t.Fatalf("%d accounts: no usage in the last 7 days", accounts)
		}
	}
	t.Logf("accounts list: %d statements at 9 and 100 accounts", counts[1])
	if counts[0] != counts[1] || counts[0] == 0 {
		t.Fatalf("%d statements for 9 accounts, %d for 100", counts[0], counts[1])
	}
}

// Every per-account field of the batch read equals what the per-account
// reads return: quotas, credits, additional quotas, the refusal of a
// quota-exceeded account, memberships, lifetime totals and credentials;
// deleted accounts are left out (R43).
func TestListAccountViewsMatchesThePerAccountReads(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	seedDashboardScale(t, s, dashboardScale{keys: 10, groups: 4, accounts: 12, days: 1, eventsPerKeyHour: 1})
	seedAccountViewExtras(t, s, 12)
	for _, statement := range []string{
		`DELETE FROM account_credentials WHERE account_id='acct-2'`,
		`DELETE FROM account_usage_credits WHERE account_id='acct-3'`,
		`DELETE FROM account_additional_quotas WHERE account_id='acct-4'`,
		`DELETE FROM usage_totals WHERE scope='account' AND scope_id='acct-6'`,
		`UPDATE usage_totals SET cached_input_tokens=input_tokens+5 WHERE scope='account' AND scope_id='acct-7'`,
		`UPDATE account_credentials SET access_token_encrypted=NULL WHERE account_id='acct-8'`,
		`UPDATE accounts SET status='deactivated',deactivation_reason='deleted' WHERE id='acct-9'`,
	} {
		if _, err := s.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	views, err := s.ListAccountViews(ctx, dashboardNow)
	if err != nil {
		t.Fatal(err)
	}
	accounts, err := s.ListAccounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(views.Accounts) != len(accounts) || len(accounts) != 11 {
		t.Fatalf("%d views for %d accounts", len(views.Accounts), len(accounts))
	}
	refusals := 0
	for i, view := range views.Accounts {
		account := accounts[i]
		if !reflect.DeepEqual(view.Account, account) {
			t.Fatalf("account %d: %+v, want %+v", i, view.Account, account)
		}
		quotas, err := s.ListAccountQuota(ctx, account.ID)
		if err != nil {
			t.Fatal(err)
		}
		credits, err := s.LoadAccountCreditStatus(ctx, account.ID)
		if err != nil {
			t.Fatal(err)
		}
		additional, err := s.ListAccountAdditionalQuotas(ctx, account.ID)
		if err != nil {
			t.Fatal(err)
		}
		totals, err := s.UsageTotals(ctx, "", account.ID)
		if err != nil {
			t.Fatal(err)
		}
		var refusal *time.Time
		if account.Status == domain.AccountQuotaExceeded {
			if refusal, err = s.LoadAccountQuotaRefusalAt(ctx, account.ID); err != nil {
				t.Fatal(err)
			}
			refusals++
		}
		var credential *domain.AccountCredential
		if stored, err := s.GetAccountCredential(ctx, account.ID); err == nil {
			stored.ExternalKeyEncrypted = nil
			credential = &stored
		} else if !errors.Is(err, ErrNotFound) {
			t.Fatal(err)
		}
		groups := []string{}
		rows, err := s.db.Query(`SELECT group_id FROM account_group_accounts WHERE account_id=? ORDER BY group_id`, account.ID)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var group string
			if err := rows.Scan(&group); err != nil {
				t.Fatal(err)
			}
			groups = append(groups, group)
		}
		if err := closeRows(rows); err != nil {
			t.Fatal(err)
		}
		for name, pair := range map[string][2]any{
			"quotas": {view.Quotas, quotas}, "credits": {view.Credits, credits}, "additional": {view.AdditionalQuotas, additional},
			"totals": {view.Totals, totals}, "refusal": {view.RefusalAt, refusal}, "credential": {view.Credential, credential},
			"groups": {view.GroupIDs, groups},
		} {
			if !reflect.DeepEqual(pair[0], pair[1]) {
				t.Errorf("%s %s: batch %+v, per account %+v", account.ID, name, pair[0], pair[1])
			}
		}
	}
	if refusals < 2 {
		t.Fatalf("fixture has %d quota-exceeded accounts", refusals)
	}
}

// accountViewsCase reads the accounts list at now through one path.
func accountViewsCase(now time.Time) rollupReadCase {
	return rollupReadCase{
		name: fmt.Sprintf("accounts list %s", now.Format(time.RFC3339Nano)),
		rollup: func(ctx context.Context, r *rollupRead) (any, error) {
			views, err := listAccountViewsTx(ctx, r.tx)
			if err != nil {
				return nil, err
			}
			sums := newAccountUsageSums(views, now)
			err = r.accountUsage(ctx, sums)
			return sums.result(views, true), err
		},
		oracle: func(ctx context.Context, tx *sql.Tx) (any, error) {
			views, err := listAccountViewsTx(ctx, tx)
			if err != nil {
				return nil, err
			}
			sums := newAccountUsageSums(views, now)
			err = sums.readEvents(ctx, tx)
			return sums.result(views, true), err
		},
	}
}

// Every account's last 7 days from the rollups equal the raw events and
// folded legacy hours of the window in every fold state, with an escaped
// account id, windows on and off the hour and a window across the import
// watermark (R43, R50).
func TestAccountViewsUsageMatchesRawEventsInEveryFoldState(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	random := rand.New(rand.NewPCG(81, 82))
	seedRollupEquivalence(t, s, random)
	if err := s.SaveAccount(ctx, domain.Account{ID: "\x1fodd", Kind: domain.AccountChatGPT, Email: "odd@example.invalid",
		PlanType: "plus", Status: domain.AccountActive, CreatedAt: fixedTime}); err != nil {
		t.Fatal(err)
	}
	instants := []time.Time{rollupSpringBase.Add(5 * 24 * time.Hour), rollupSpringBase.Add(9*24*time.Hour + 17*time.Minute),
		rollupAutumnBase.Add(20 * 24 * time.Hour)}
	for range 5 {
		instants = append(instants, rollupRandomInstant(random))
	}
	var cases []rollupReadCase
	for _, now := range instants {
		cases = append(cases, accountViewsCase(now))
	}
	foldRollups(t, s, 1000)
	assertRollupReadsMatchRaw(t, s, "(a) folded", cases)
	// Sanity, before the mutations delete acct-0 and acct-1: the window
	// sees usage of several accounts, the escaped one included.
	views, err := s.ListAccountViews(ctx, instants[0])
	if err != nil {
		t.Fatal(err)
	}
	active := 0
	for _, view := range views.Accounts {
		if view.Usage7d.Requests > 0 {
			active++
		}
	}
	if !views.RollupsUsable || active < 3 {
		t.Fatalf("fixture does not exercise the window: usable %v, %d accounts with usage", views.RollupsUsable, active)
	}
	var events int
	if err := s.db.QueryRow(`SELECT count(*) FROM usage_events`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := s.RebuildUsageRollups(ctx); err != nil {
		t.Fatal(err)
	}
	foldRollupsTo(t, s, 1+random.IntN(events-1))
	assertRollupReadsMatchRaw(t, s, "(b) cursor", cases)
	foldRollups(t, s, 1000)
	for round := range 2 {
		mutateRollupEquivalence(t, s, random, round)
		assertRollupReadsMatchRaw(t, s, fmt.Sprintf("(c) journal after round %d", round), cases)
		foldRollups(t, s, 1000)
		assertRollupReadsMatchRaw(t, s, fmt.Sprintf("(d) applied round %d", round), cases)
	}
}

// benchmarkAccountViews is the accounts list of BenchmarkDashboardPages: 100
// accounts with every per-account row kind, the last 7 days of 300 keys'
// events from the rollups (R49).
func benchmarkAccountViews(b *testing.B, s *Store) {
	seedAccountViewExtras(b, s, 100)
	b.Run("accounts_list", func(b *testing.B) {
		for range b.N {
			views, err := s.ListAccountViews(context.Background(), dashboardNow)
			if err != nil || len(views.Accounts) != 100 || !views.RollupsUsable {
				b.Fatalf("%d views, usable %v: %v", len(views.Accounts), views.RollupsUsable, err)
			}
		}
	})
}
