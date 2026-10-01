package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

func TestInterruptedUsageRetainedUntilExplicitOfflineSettlement(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	r, err := openRuntime(ctx, cfg, logger)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	account := domain.Account{ID: "synthetic-account", Kind: domain.AccountChatGPT, Status: domain.AccountActive, Email: "synthetic@example.invalid", CreatedAt: now}
	if err := r.data.store.SaveAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	cipher, err := r.data.vault.Encrypt([]byte("synthetic"))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.data.store.SaveAccountCredential(ctx, domain.AccountCredential{AccountID: account.ID, AccessTokenEncrypted: cipher, RefreshTokenEncrypted: cipher, IDTokenEncrypted: cipher}); err != nil {
		t.Fatal(err)
	}
	_, err = r.data.store.ReserveUsage(ctx, domain.ReservationRequest{ID: "pending", APIKeyID: domain.LocalProxyKeyID, AccountID: account.ID, Model: "gpt-6-sol", Now: now, Budget: domain.UsageAmount{InputTokens: 10}})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.close(); err != nil {
		t.Fatal(err)
	}
	r, err = openRuntime(ctx, cfg, logger)
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := r.data.store.GetReservation(ctx, "pending")
	if err != nil || !reservation.NeedsReconciliation || reservation.Status != "reserved" {
		t.Fatal("restart silently discarded interrupted accounting")
	}
	if n, err := r.data.store.ReleaseStaleReservations(ctx, now.Add(time.Hour)); err != nil || n != 0 {
		t.Fatal("unknown usage automatically released")
	}
	var output bytes.Buffer
	if err := reconcileUsage(ctx, cfg, &output); err == nil {
		t.Fatal("reconciliation opened the running server database")
	}
	r.close()
	if err := reconcileUsage(ctx, cfg, &output); err != nil || !strings.Contains(output.String(), `"id":"pending"`) {
		t.Fatal("offline reconciliation list failed")
	}
	path := filepath.Join(t.TempDir(), "settlement.json")
	if err := os.WriteFile(path, []byte(`{"status":"success","usage":{"inputTokens":7,"outputTokens":3,"costMicrodollars":123}}`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg.reservation, cfg.settlement = "pending", path
	output.Reset()
	if err := reconcileUsage(ctx, cfg, &output); err != nil || !strings.Contains(output.String(), `"applied":true`) {
		t.Fatalf("confirmed usage not applied: %v", err)
	}
	output.Reset()
	if err := reconcileUsage(ctx, cfg, &output); err != nil || !strings.Contains(output.String(), "alreadySettled") {
		t.Fatal("repeat reconciliation was not idempotent")
	}
	data, err := openData(ctx, cfg.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer data.close()
	totals, err := data.store.UsageTotals(ctx, domain.LocalProxyKeyID, "")
	if err != nil || totals.RequestCount != 1 || totals.Usage.InputTokens != 7 || totals.Usage.CostMicrodollars != 123 {
		t.Fatalf("reconciliation lost or duplicated usage: %+v %v", totals, err)
	}
}

func TestUsageReconciliationRequiresExplicitValidResolution(t *testing.T) {
	t.Setenv("CODEX_LB_DATA_DIR", t.TempDir())
	for _, args := range [][]string{{"--release"}, {"--reservation", "r"}, {"--reservation", "r", "--release", "--settlement", "/tmp/x"}, {"--reservation", "r", "--release", "--after", "x"}} {
		if _, err := parseConfig("reconcile-usage", args, io.Discard); err == nil {
			t.Fatalf("invalid options accepted: %v", args)
		}
	}
	path := filepath.Join(t.TempDir(), "input.json")
	for _, body := range []string{
		`{"status":"success"}`,
		`{"status":"success","usage":{"inputTokens":-1}}`,
		`{"status":"success","accountId":"other","usage":{}}`,
		`{"status":"success","usage":{},"password":"not-supported"}`,
		`{"status":"success","usage":{}}` + strings.Repeat(" ", 65536) + `{}`,
	} {
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readUsageSettlement(path, domain.Reservation{ID: "r", AccountID: "owner"}); err == nil {
			t.Fatal("invalid, oversized or cross-owner settlement accepted")
		}
	}
}
