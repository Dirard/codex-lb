package application_test

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"codex-lb/internal/adapters/credentials"
	"codex-lb/internal/adapters/sqlite"
	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

type refreshFunc func(context.Context, string) (application.OAuthTokens, error)

func (f refreshFunc) Refresh(ctx context.Context, token string) (application.OAuthTokens, error) {
	return f(ctx, token)
}

func tokenFixture(t *testing.T) (*sqlite.Store, *credentials.Vault, domain.Account, domain.AccountCredential) {
	t.Helper()
	dir := t.TempDir()
	store, err := sqlite.Open(filepath.Join(dir, "tokens.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	vault, err := credentials.Open(filepath.Join(dir, "key"), true)
	if err != nil {
		t.Fatal(err)
	}
	account := domain.Account{ID: "refresh-account", Kind: domain.AccountChatGPT, Provider: "openai", Email: "synthetic@example.invalid", PlanType: "plus", Status: domain.AccountActive, RoutingPolicy: "normal", CreatedAt: time.Now()}
	if err := store.SaveAccount(context.Background(), account); err != nil {
		t.Fatal(err)
	}
	access, _ := vault.Encrypt([]byte(expiringToken(time.Now().Add(-time.Hour))))
	refresh, _ := vault.Encrypt([]byte("synthetic-refresh"))
	id, _ := vault.Encrypt([]byte("synthetic-id-token"))
	credential := domain.AccountCredential{AccountID: account.ID, AccessTokenEncrypted: access, RefreshTokenEncrypted: refresh, IDTokenEncrypted: id}
	if err := store.SaveAccountCredential(context.Background(), credential); err != nil {
		t.Fatal(err)
	}
	return store, vault, account, credential
}

func expiringToken(at time.Time) string {
	return "e30." + base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, at.Unix()))) + ".synthetic"
}

func TestRefreshRotationSurvivesFirstWaiterCancellation(t *testing.T) {
	store, vault, account, credential := tokenFixture(t)
	started, finish := make(chan struct{}), make(chan struct{})
	var calls atomic.Int64
	fresh := expiringToken(time.Now().Add(time.Hour))
	service := application.NewTokenService(store, vault, refreshFunc(func(ctx context.Context, token string) (application.OAuthTokens, error) {
		if token != "synthetic-refresh" {
			t.Error("wrong refresh credential")
		}
		if calls.Add(1) == 1 {
			close(started)
		}
		select {
		case <-finish:
			return application.OAuthTokens{AccessToken: fresh, RefreshToken: "rotated-synthetic-refresh"}, nil
		case <-ctx.Done():
			return application.OAuthTokens{}, ctx.Err()
		}
	}))
	defer service.Close()
	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() { _, err := service.AccessToken(ctx, account, credential); first <- err }()
	<-started
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatalf("first waiter did not cancel: %v", err)
	}
	close(finish)
	secondCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	token, err := service.AccessToken(secondCtx, account, credential)
	if err != nil || token != fresh || calls.Load() != 1 {
		t.Fatalf("refresh duplicated or abandoned: calls=%d error=%v", calls.Load(), err)
	}
	stored, err := store.GetAccountCredential(secondCtx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := vault.Decrypt(stored.RefreshTokenEncrypted)
	if err != nil || string(plain) != "rotated-synthetic-refresh" {
		t.Fatal("rotated refresh token not persisted")
	}
	if changed, err := store.RotateAccountCredential(secondCtx, credential, credential, time.Now()); err != nil || changed {
		t.Fatal("stale token compare-and-swap overwrote fresh credentials")
	}
}

func TestExplicitLoginWinsOverOlderRefresh(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(fmt.Sprint(failure), func(t *testing.T) {
			store, vault, account, credential := tokenFixture(t)
			started, finish := make(chan struct{}), make(chan struct{})
			service := application.NewTokenService(store, vault, refreshFunc(func(ctx context.Context, _ string) (application.OAuthTokens, error) {
				close(started)
				select {
				case <-finish:
				case <-ctx.Done():
					return application.OAuthTokens{}, ctx.Err()
				}
				if failure {
					return application.OAuthTokens{}, &application.OAuthError{Code: "invalid_grant", Message: "synthetic rejection"}
				}
				return application.OAuthTokens{AccessToken: expiringToken(time.Now().Add(2 * time.Hour)), RefreshToken: "older-rotation"}, nil
			}))
			defer service.Close()
			done := make(chan error, 1)
			go func() { _, err := service.AccessToken(context.Background(), account, credential); done <- err }()
			<-started
			manual := credential
			manual.AccessTokenEncrypted, _ = vault.Encrypt([]byte(expiringToken(time.Now().Add(3 * time.Hour))))
			manual.RefreshTokenEncrypted, _ = vault.Encrypt([]byte("new-login-refresh"))
			if err := store.SaveAccountCredential(context.Background(), manual); err != nil {
				t.Fatal(err)
			}
			close(finish)
			<-done
			stored, err := store.GetAccountCredential(context.Background(), account.ID)
			if err != nil {
				t.Fatal(err)
			}
			plain, err := vault.Decrypt(stored.RefreshTokenEncrypted)
			if err != nil || string(plain) != "new-login-refresh" {
				t.Fatal("refresh overwrote a concurrent explicit login")
			}
			current, err := store.GetAccount(context.Background(), account.ID)
			if err != nil || current.Status != domain.AccountActive {
				t.Fatal("stale refresh failure disabled newer credentials")
			}
		})
	}
}

func TestDelayedUnauthorizedDoesNotRotateNewTokenAgain(t *testing.T) {
	store, vault, account, credential := tokenFixture(t)
	rejected, err := vault.Decrypt(credential.AccessTokenEncrypted)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	fresh := expiringToken(time.Now().Add(time.Hour))
	service := application.NewTokenService(store, vault, refreshFunc(func(context.Context, string) (application.OAuthTokens, error) {
		calls.Add(1)
		return application.OAuthTokens{AccessToken: fresh, RefreshToken: "new-refresh"}, nil
	}))
	defer service.Close()
	for range 3 {
		got, err := service.ForceRefresh(context.Background(), account, string(rejected))
		if err != nil || got != fresh {
			t.Fatalf("refresh failed: %v", err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("delayed 401 rotated token %d times", calls.Load())
	}
}

func TestStaleRefreshCannotReadOrRotateReimportedCredential(t *testing.T) {
	store, vault, account, credential := tokenFixture(t)
	started, finish := make(chan struct{}), make(chan struct{})
	var calls atomic.Int64
	oldToken := expiringToken(time.Now().Add(time.Hour))
	newToken := expiringToken(time.Now().Add(2 * time.Hour))
	service := application.NewTokenService(store, vault, refreshFunc(func(ctx context.Context, token string) (application.OAuthTokens, error) {
		if calls.Add(1) == 1 {
			if token != "synthetic-refresh" {
				t.Error("stale refresh read a newer credential")
			}
			close(started)
			<-finish
			return application.OAuthTokens{AccessToken: oldToken, RefreshToken: "older-rotation"}, nil
		}
		if token != "new-refresh" {
			t.Errorf("new incarnation used %q", token)
		}
		return application.OAuthTokens{AccessToken: newToken, RefreshToken: "new-rotation"}, nil
	}))
	defer service.Close()

	oldDone := make(chan error, 1)
	go func() { _, err := service.AccessToken(context.Background(), account, credential); oldDone <- err }()
	<-started
	if err := store.DeleteAccount(context.Background(), account.ID, false); err != nil {
		t.Fatal(err)
	}
	reimported := credential
	reimported.AccessTokenEncrypted, _ = vault.Encrypt([]byte(expiringToken(time.Now().Add(-time.Hour))))
	reimported.RefreshTokenEncrypted, _ = vault.Encrypt([]byte("new-refresh"))
	reimported.IDTokenEncrypted, _ = vault.Encrypt([]byte("new-id"))
	if err := store.SaveAccountIdentity(context.Background(), account, reimported); err != nil {
		t.Fatal(err)
	}
	currentAccount, err := store.GetAccount(context.Background(), account.ID)
	if err != nil || currentAccount.Generation != 1 {
		t.Fatalf("reimport = %+v %v", currentAccount, err)
	}
	currentCredential, err := store.GetAccountCredential(context.Background(), account.ID)
	if err != nil || currentCredential.Generation != 1 {
		t.Fatalf("reimport credential = %+v %v", currentCredential, err)
	}
	close(finish)
	if err := <-oldDone; !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stale refresh outcome = %v", err)
	}
	token, err := service.AccessToken(context.Background(), currentAccount, currentCredential)
	if err != nil || token != newToken || calls.Load() != 2 {
		t.Fatalf("new incarnation refresh token=%q calls=%d err=%v", token, calls.Load(), err)
	}
	stored, err := store.GetAccountCredential(context.Background(), account.ID)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := vault.Decrypt(stored.RefreshTokenEncrypted)
	if err != nil || string(plain) != "new-rotation" {
		t.Fatal("new incarnation rotation was not persisted")
	}
}
