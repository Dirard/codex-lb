package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestImportLegacyPurchasedCreditsAndAdditionalQuotaSurviveRestart(t *testing.T) {
	ctx := context.Background()
	source, vault, _ := legacyFixture(t)
	legacy, err := sql.Open("sqlite", source)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`ALTER TABLE accounts ADD COLUMN blocked_at INTEGER;`,
		`UPDATE accounts SET blocked_at=1800000000 WHERE id='acct-a';`,
		`ALTER TABLE usage_history ADD COLUMN credits_has INTEGER;`,
		`ALTER TABLE usage_history ADD COLUMN credits_unlimited INTEGER;`,
		`ALTER TABLE usage_history ADD COLUMN credits_balance REAL;`,
		`UPDATE usage_history SET credits_has=1,credits_unlimited=0,credits_balance=12.5 WHERE id=1;`,
		`INSERT INTO usage_history VALUES(2,'acct-a','primary',30,1800000000,300,'2026-09-25 00:00:00.000000',NULL,NULL,NULL);`,
		`CREATE TABLE additional_usage_history(id INTEGER PRIMARY KEY,account_id TEXT,quota_key TEXT,
 limit_name TEXT,metered_feature TEXT,window TEXT,used_percent REAL,reset_at INTEGER,window_minutes INTEGER,recorded_at TEXT);`,
		`INSERT INTO additional_usage_history VALUES(1,'acct-a','codex_other','codex_other','codex_bengalfox','primary',20,1800000000,300,'2026-09-25 00:00:00.000000');`,
		`INSERT INTO additional_usage_history VALUES(2,'acct-a','codex_spark','Spark','codex_bengalfox','secondary',40,1800000000,10080,'2026-09-25 00:00:00.000000');`,
		`ALTER TABLE dashboard_settings ADD COLUMN additional_quota_routing_policies_json TEXT NOT NULL DEFAULT '{}';`,
		`UPDATE dashboard_settings SET additional_quota_routing_policies_json='{"codex_other":"preserve"}';`,
	} {
		if _, err := legacy.ExecContext(ctx, statement); err != nil {
			legacy.Close()
			t.Fatal(err)
		}
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "go.sqlite")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ImportLegacySnapshot(ctx, source, vault); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	credits, err := store.LoadAccountCreditStatus(ctx, "acct-a")
	if err != nil || credits == nil || !credits.Usable() || credits.Balance == nil || *credits.Balance != 12.5 {
		t.Fatalf("imported newest explicit credits = %+v %v", credits, err)
	}
	blockedAt, err := store.LoadAccountQuotaRefusalAt(ctx, "acct-a")
	if err != nil || blockedAt == nil || !blockedAt.Equal(time.Unix(1800000000, 0).UTC()) {
		t.Fatalf("imported provider cooldown marker = %+v %v", blockedAt, err)
	}
	additional, err := store.ListAccountAdditionalQuotas(ctx, "acct-a")
	if err != nil || len(additional) != 2 || additional[0].QuotaKey != "codex_spark" || additional[1].QuotaKey != "codex_spark" ||
		additional[0].ObservedAt.IsZero() || additional[1].ResetAt == nil {
		t.Fatalf("imported additional quota = %+v %v", additional, err)
	}
	settings, err := store.LoadSettings(ctx)
	if err != nil || settings.AdditionalQuotaRoutingPolicies["codex_spark"] != "preserve" {
		t.Fatalf("imported routing policy = %+v %v", settings.AdditionalQuotaRoutingPolicies, err)
	}
}
