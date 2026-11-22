package application_test

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"codex-lb/internal/adapters/credentials"
	"codex-lb/internal/adapters/sqlite"
	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

// fakeGoogle is the Antigravity upstream port in memory. Refresh calls can be
// held on a channel to observe single-flight behavior.
type fakeGoogle struct {
	mu        sync.Mutex
	email     string
	project   string
	models    []application.AntigravityModel
	refreshes atomic.Int32
	entered   chan struct{}
	hold      chan struct{}
	rotate    string
	refuse    bool
	issued    int
	exchanged []string
}

func (g *fakeGoogle) AuthorizationURL(state, challenge string) string {
	return "https://accounts.example.test/auth?" + url.Values{"state": {state}, "code_challenge": {challenge}}.Encode()
}

func (g *fakeGoogle) ExchangeCode(_ context.Context, code, verifier string) (application.AntigravityTokenSet, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if code == "refused" {
		return application.AntigravityTokenSet{}, application.ErrAntigravityGrantRefused
	}
	g.exchanged = append(g.exchanged, code+"|"+verifier)
	g.issued++
	return application.AntigravityTokenSet{Access: fmt.Sprintf("access-%d", g.issued), Refresh: "refresh-" + code, ExpiresIn: time.Hour}, nil
}

func (g *fakeGoogle) Refresh(_ context.Context, refresh string) (application.AntigravityTokenSet, error) {
	g.refreshes.Add(1)
	if g.entered != nil {
		g.entered <- struct{}{}
	}
	if g.hold != nil {
		<-g.hold
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.refuse {
		return application.AntigravityTokenSet{}, application.ErrAntigravityGrantRefused
	}
	g.issued++
	return application.AntigravityTokenSet{Access: fmt.Sprintf("access-%d-from-%s", g.issued, refresh), Refresh: g.rotate, ExpiresIn: time.Hour}, nil
}

func (g *fakeGoogle) UserEmail(context.Context, string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.email, nil
}

func (g *fakeGoogle) LoadCodeAssist(context.Context, string) (application.AntigravityCodeAssist, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return application.AntigravityCodeAssist{Project: g.project, Tier: "g1-pro-tier", OnboardTier: "standard-tier"}, nil
}

func (g *fakeGoogle) OnboardUser(context.Context, string, string) (string, error) {
	return "onboarded-project", nil
}

func (g *fakeGoogle) AvailableModels(context.Context, string, string) ([]application.AntigravityModel, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]application.AntigravityModel(nil), g.models...), nil
}

type antigravityFixture struct {
	store   *sqlite.Store
	vault   application.SecretCipher
	google  *fakeGoogle
	service *application.AntigravityService
	now     time.Time
}

func newAntigravityFixture(t *testing.T) *antigravityFixture {
	t.Helper()
	dir := t.TempDir()
	store, err := sqlite.Open(filepath.Join(dir, "antigravity.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	vault, err := credentials.Open(filepath.Join(dir, "key"), true)
	if err != nil {
		t.Fatal(err)
	}
	google := &fakeGoogle{email: "agent@example.test", project: "aicode-consumers", models: []application.AntigravityModel{
		{ID: "gemini-3.8-flash-high", DisplayName: "Gemini 3.8 Flash", ContextWindow: 1048576, MaxOutputTokens: 65536, Images: true, Thinking: true},
		{ID: "gpt-oss-120b-medium", DisplayName: "GPT-OSS", ContextWindow: 114000, MaxOutputTokens: 32768},
		{ID: "claude-sonnet-4-6", DisplayName: "Claude Sonnet 4.6", Disabled: true},
	}}
	f := &antigravityFixture{store: store, vault: vault, google: google, service: application.NewAntigravityService(store, vault, google),
		now: time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)}
	f.service.ConfigureClock(func() time.Time { return f.now })
	return f
}

func (f *antigravityFixture) signIn(t *testing.T, code, sourceID string) domain.ModelSource {
	t.Helper()
	start, err := f.service.StartSignIn(context.Background(), sourceID)
	if err != nil {
		t.Fatal(err)
	}
	source, err := f.service.CompleteSignIn(context.Background(), start.FlowID, code)
	if err != nil {
		t.Fatalf("complete sign-in: %v", err)
	}
	return source
}

func (f *antigravityFixture) refreshToken(t *testing.T, sourceID string) string {
	t.Helper()
	credential, err := f.store.GetAccountCredential(context.Background(), sourceID)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := f.vault.Decrypt(credential.ExternalKeyEncrypted)
	if err != nil {
		t.Fatal(err)
	}
	return string(plain)
}

func TestAntigravitySignInCreatesAStickyFreeSource(t *testing.T) {
	f := newAntigravityFixture(t)
	start, err := f.service.StartSignIn(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if start.ExpiresAt != f.now.Add(10*time.Minute) || start.FlowID == "" {
		t.Fatalf("start: %+v", start)
	}
	parsed, _ := url.Parse(start.AuthorizationURL)
	state := parsed.Query().Get("state")
	if state == "" || parsed.Query().Get("code_challenge") == "" {
		t.Fatalf("authorization URL lacks state or PKCE: %s", start.AuthorizationURL)
	}
	// The whole callback URL may be pasted instead of the bare code.
	source, err := f.service.CompleteSignIn(context.Background(), start.FlowID, " https://antigravity.google/oauth-callback?state="+state+"&code=4%2Fpasted ")
	if err != nil {
		t.Fatal(err)
	}
	if len(f.google.exchanged) != 1 || f.google.exchanged[0][:9] != "4/pasted|" {
		t.Fatalf("exchanged: %v", f.google.exchanged)
	}
	info := source.ProviderConfig.Antigravity
	if source.Kind != domain.ModelSourceAntigravity || source.Name != "agent@example.test" || source.BaseURL != domain.AntigravityBaseURL ||
		!source.Chat || !source.Responses || !source.Enabled || !source.StickyOnly || source.AccountStatus != domain.AccountActive ||
		info == nil || info.Email != "agent@example.test" || info.Project != "aicode-consumers" || info.Tier != "g1-pro-tier" || info.ActionURL != "" {
		t.Fatalf("source: %+v %+v", source, info)
	}
	if len(source.Models) != 2 {
		t.Fatalf("a disabled model was added: %+v", source.Models)
	}
	flash := source.Models[0]
	if flash.Model != "gemini-3.8-flash-high" || flash.DisplayName != "Gemini 3.8 Flash" || flash.ContextWindow != 1048576 || flash.MaxOutputTokens != 65536 ||
		!flash.Vision || !flash.Tools || !flash.Streaming || !flash.Enabled ||
		flash.InputPerMillion == nil || *flash.InputPerMillion != 0 || flash.CachedPerMillion == nil || *flash.CachedPerMillion != 0 ||
		flash.OutputPerMillion == nil || *flash.OutputPerMillion != 0 ||
		flash.RawMetadataJSON != `{"default_reasoning_level":"high","supported_reasoning_levels":["low","medium","high"],"supports_reasoning":true}` {
		t.Fatalf("flash: %+v", flash)
	}
	if oss := source.Models[1]; oss.Vision || oss.RawMetadataJSON != "" {
		t.Fatalf("a model without images or thinking claims them: %+v", oss)
	}
	price, err := application.ModelSourcePrice(flash)
	if err != nil || price.Standard.Input != 0 || price.Standard.Output != 0 {
		t.Fatalf("price: %+v %v", price, err)
	}
	if f.refreshToken(t, source.ID) != "refresh-4/pasted" {
		t.Fatal("the credential does not hold the refresh token")
	}
	credential, _ := f.store.GetAccountCredential(context.Background(), source.ID)
	if string(credential.ExternalKeyEncrypted) == "refresh-4/pasted" {
		t.Fatal("the refresh token is stored in plain text")
	}
	account, err := f.store.GetAccount(context.Background(), source.ID)
	if err != nil || account.RequiresEgressDecision || account.Kind != domain.AccountExternal || !account.StickyOnly {
		t.Fatalf("account: %+v %v", account, err)
	}
	// The sign-in's access token is used at once, without a refresh.
	token, _, project, err := f.service.AntigravityAccessToken(context.Background(), source.ID, -1)
	if err != nil || token != "access-1" || project != "aicode-consumers" || f.google.refreshes.Load() != 0 {
		t.Fatalf("first token: %q %q %v after %d refreshes", token, project, err, f.google.refreshes.Load())
	}
}

func TestAntigravitySignInFlowsAreSingleUseAndExpire(t *testing.T) {
	f := newAntigravityFixture(t)
	ctx := context.Background()
	start, err := f.service.StartSignIn(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.CompleteSignIn(ctx, start.FlowID, "refused"); proxyCode(err) != "antigravity_code_rejected" {
		t.Fatalf("refused code: %v", err)
	}
	if _, err := f.service.CompleteSignIn(ctx, start.FlowID, "good"); proxyCode(err) != "antigravity_sign_in_not_found" {
		t.Fatalf("a used flow completed again: %v", err)
	}
	expired, _ := f.service.StartSignIn(ctx, "")
	f.now = f.now.Add(10 * time.Minute)
	if _, err := f.service.CompleteSignIn(ctx, expired.FlowID, "good"); proxyCode(err) != "antigravity_sign_in_expired" {
		t.Fatalf("an expired flow completed: %v", err)
	}
	mismatch, _ := f.service.StartSignIn(ctx, "")
	if _, err := f.service.CompleteSignIn(ctx, mismatch.FlowID, "https://antigravity.google/oauth-callback?code=x&state=other"); proxyCode(err) != "antigravity_state_mismatch" {
		t.Fatalf("a foreign state was accepted: %v", err)
	}
	for i := 0; i < 8; i++ {
		if _, err := f.service.StartSignIn(ctx, ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.service.StartSignIn(ctx, ""); proxyCode(err) != "antigravity_sign_in_limit" {
		t.Fatalf("pending sign-ins are unbounded: %v", err)
	}
	if len(f.google.exchanged) != 0 {
		t.Fatalf("codes were exchanged: %v", f.google.exchanged)
	}
	sources, _ := f.store.ListModelSources(ctx)
	if len(sources) != 0 {
		t.Fatalf("a failed sign-in saved a source: %+v", sources)
	}
}

func TestAntigravityReauthorizationKeepsTheSource(t *testing.T) {
	f := newAntigravityFixture(t)
	ctx := context.Background()
	source := f.signIn(t, "first", "")
	price := 1.5
	source.Models[0].InputPerMillion = &price
	source.Models[1].Enabled = false
	source.TimeoutSeconds = 90
	if err := f.store.SaveModelSource(ctx, source, nil); err != nil {
		t.Fatal(err)
	}
	if err := f.service.ReportAntigravityRefusal(ctx, source.ID, "account_validation_required", "https://accounts.example.test/verify"); err != nil {
		t.Fatal(err)
	}
	f.google.mu.Lock()
	f.google.project, f.google.email = "aicode-consumers-2", "Agent@Example.test"
	f.google.mu.Unlock()
	again := f.signIn(t, "second", source.ID)
	if again.ID != source.ID || again.TimeoutSeconds != 90 || len(again.Models) != 2 || *again.Models[0].InputPerMillion != 1.5 || again.Models[1].Enabled ||
		again.AccountStatus != domain.AccountActive || again.ProviderConfig.Antigravity.ActionURL != "" ||
		again.ProviderConfig.Antigravity.Project != "aicode-consumers-2" || !again.StickyOnly {
		t.Fatalf("re-authorized source: %+v %+v", again, again.ProviderConfig.Antigravity)
	}
	if f.refreshToken(t, source.ID) != "refresh-second" {
		t.Fatal("re-authorization kept the old refresh token")
	}
	sources, _ := f.store.ListModelSources(ctx)
	if len(sources) != 1 {
		t.Fatalf("re-authorization added a source: %d", len(sources))
	}
	// Another account cannot re-authorize this source.
	f.google.mu.Lock()
	f.google.email = "someone-else@example.test"
	f.google.mu.Unlock()
	start, _ := f.service.StartSignIn(ctx, source.ID)
	if _, err := f.service.CompleteSignIn(ctx, start.FlowID, "third"); proxyCode(err) != "antigravity_account_mismatch" {
		t.Fatalf("a foreign account re-authorized the source: %v", err)
	}
	if f.refreshToken(t, source.ID) != "refresh-second" {
		t.Fatal("a mismatched sign-in replaced the credential")
	}
	if _, err := f.service.StartSignIn(ctx, "src_missing"); proxyCode(err) != "antigravity_source_not_found" {
		t.Fatalf("re-authorization of an unknown source: %v", err)
	}
}

func TestAntigravityTokenRefreshIsSingleFlightAndRotates(t *testing.T) {
	f := newAntigravityFixture(t)
	ctx := context.Background()
	source := f.signIn(t, "first", "")
	first, version, _, err := f.service.AntigravityAccessToken(ctx, source.ID, -1)
	if err != nil {
		t.Fatal(err)
	}
	// Five minutes before expiry the token is renewed, by one refresh for
	// every concurrent caller, and a rotated refresh token is stored.
	f.now = f.now.Add(56 * time.Minute)
	const callers = 8
	f.google.entered = make(chan struct{}, callers)
	f.google.hold = make(chan struct{})
	f.google.rotate = "rotated-refresh"
	type answer struct {
		token   string
		version int64
		err     error
	}
	answers := make(chan answer, callers)
	for i := 0; i < callers; i++ {
		go func() {
			token, version, _, err := f.service.AntigravityAccessToken(ctx, source.ID, -1)
			answers <- answer{token, version, err}
		}()
	}
	<-f.google.entered
	close(f.google.hold)
	var renewed answer
	for i := 0; i < callers; i++ {
		renewed = <-answers
		if renewed.err != nil || renewed.token == first || renewed.version <= version {
			t.Fatalf("renewed token: %+v (first %q v%d)", renewed, first, version)
		}
	}
	if got := f.google.refreshes.Load(); got != 1 {
		t.Fatalf("%d refreshes for concurrent callers", got)
	}
	if f.refreshToken(t, source.ID) != "rotated-refresh" {
		t.Fatal("the rotated refresh token was not stored")
	}
	f.google.entered, f.google.hold, f.google.rotate = nil, nil, ""
	// A token Google rejected is replaced once; a stale rejection is not.
	again, againVersion, _, err := f.service.AntigravityAccessToken(ctx, source.ID, renewed.version)
	if err != nil || again == renewed.token || f.google.refreshes.Load() != 2 {
		t.Fatalf("rejected token: %q %v after %d refreshes", again, err, f.google.refreshes.Load())
	}
	same, _, _, err := f.service.AntigravityAccessToken(ctx, source.ID, renewed.version)
	if err != nil || same != again || f.google.refreshes.Load() != 2 {
		t.Fatalf("a stale rejection refreshed again: %q %v", same, err)
	}
	if againVersion <= renewed.version {
		t.Fatalf("versions do not grow: %d after %d", againVersion, renewed.version)
	}
}

func TestAntigravityRefusedRefreshNeedsANewSignIn(t *testing.T) {
	f := newAntigravityFixture(t)
	ctx := context.Background()
	source := f.signIn(t, "first", "")
	f.google.refuse = true
	if _, _, _, err := f.service.AntigravityAccessToken(ctx, source.ID, 1); !errors.Is(err, application.ErrAntigravityAccountUnavailable) {
		t.Fatalf("refused refresh: %v", err)
	}
	stored, err := f.store.GetModelSource(ctx, source.ID)
	if err != nil || stored.AccountStatus != domain.AccountReauthRequired {
		t.Fatalf("account after invalid_grant: %+v %v", stored.AccountStatus, err)
	}
	refreshes := f.google.refreshes.Load()
	if _, _, _, err := f.service.AntigravityAccessToken(ctx, source.ID, -1); !errors.Is(err, application.ErrAntigravityAccountUnavailable) || f.google.refreshes.Load() != refreshes {
		t.Fatalf("an account needing a sign-in still refreshed: %v", err)
	}
	if _, err := f.service.RefreshModels(ctx, source.ID); proxyCode(err) != "antigravity_reauth_required" {
		t.Fatalf("models of an account needing a sign-in: %v", err)
	}
}

func TestAntigravityRefusalsChangeTheAccount(t *testing.T) {
	f := newAntigravityFixture(t)
	ctx := context.Background()
	source := f.signIn(t, "first", "")
	if err := f.service.ReportAntigravityRefusal(ctx, source.ID, "account_validation_required", "javascript:alert(1)"); err != nil {
		t.Fatal(err)
	}
	stored, _ := f.store.GetModelSource(ctx, source.ID)
	if stored.AccountStatus != domain.AccountReauthRequired || stored.ProviderConfig.Antigravity.ActionURL != "" {
		t.Fatalf("validation with an unsafe link: %s %+v", stored.AccountStatus, stored.ProviderConfig.Antigravity)
	}
	if err := f.service.ReportAntigravityRefusal(ctx, source.ID, "account_suspended", "https://forms.gle/appeal"); err != nil {
		t.Fatal(err)
	}
	stored, _ = f.store.GetModelSource(ctx, source.ID)
	if stored.AccountStatus != domain.AccountDeactivated || stored.DeactivationReason != "tos_violation" ||
		stored.ProviderConfig.Antigravity.ActionURL != "https://forms.gle/appeal" || stored.ProviderConfig.Antigravity.Email != "agent@example.test" {
		t.Fatalf("suspension: %s %s %+v", stored.AccountStatus, stored.DeactivationReason, stored.ProviderConfig.Antigravity)
	}
	// A later verification request cannot lift the suspension, and an
	// unrelated code changes nothing.
	if err := f.service.ReportAntigravityRefusal(ctx, source.ID, "account_validation_required", "https://accounts.example.test/verify"); err != nil {
		t.Fatal(err)
	}
	if err := f.service.ReportAntigravityRefusal(ctx, source.ID, "rate_limit_exceeded", ""); err != nil {
		t.Fatal(err)
	}
	stored, _ = f.store.GetModelSource(ctx, source.ID)
	if stored.AccountStatus != domain.AccountDeactivated || stored.ProviderConfig.Antigravity.ActionURL != "https://forms.gle/appeal" {
		t.Fatalf("suspension was overwritten: %s %+v", stored.AccountStatus, stored.ProviderConfig.Antigravity)
	}
	if _, _, _, err := f.service.AntigravityAccessToken(ctx, source.ID, -1); !errors.Is(err, application.ErrAntigravityAccountUnavailable) {
		t.Fatalf("a suspended account lent a token: %v", err)
	}
	account, _ := f.store.GetAccount(ctx, source.ID)
	if account.Status != domain.AccountDeactivated || account.DeactivationReason != "tos_violation" {
		t.Fatalf("account row: %+v", account)
	}
	// A new sign-in (after an appeal) returns the account to the pool.
	again := f.signIn(t, "after-appeal", source.ID)
	if again.AccountStatus != domain.AccountActive || again.DeactivationReason != "" || again.ProviderConfig.Antigravity.ActionURL != "" {
		t.Fatalf("after a new sign-in: %s %s %+v", again.AccountStatus, again.DeactivationReason, again.ProviderConfig.Antigravity)
	}
}

func TestAntigravityRefreshModelsMergesTheListing(t *testing.T) {
	f := newAntigravityFixture(t)
	ctx := context.Background()
	source := f.signIn(t, "first", "")
	price := 2.0
	source.Models[0].OutputPerMillion = &price
	if err := f.store.SaveModelSource(ctx, source, nil); err != nil {
		t.Fatal(err)
	}
	f.google.mu.Lock()
	f.google.models = []application.AntigravityModel{
		{ID: "gemini-3.8-flash-high", DisplayName: "Gemini 3.8 Flash", ContextWindow: 1048576, Images: true, Thinking: true},
		{ID: "gemini-4-neon-xhigh", DisplayName: "Gemini 4 Neon", ContextWindow: 1048576, MaxOutputTokens: 65536, Thinking: true},
		{ID: "claude-sonnet-4-6", Disabled: true},
	}
	f.google.mu.Unlock()
	refreshed, err := f.service.RefreshModels(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]domain.ModelSourceModel{}
	for _, model := range refreshed.Models {
		byID[model.Model] = model
	}
	flash, oss, neon := byID["gemini-3.8-flash-high"], byID["gpt-oss-120b-medium"], byID["gemini-4-neon-xhigh"]
	if len(refreshed.Models) != 3 || !flash.Enabled || flash.OutputPerMillion == nil || *flash.OutputPerMillion != 2 ||
		oss.Model == "" || oss.Enabled || !neon.Enabled || neon.InputPerMillion == nil || *neon.InputPerMillion != 0 ||
		neon.RawMetadataJSON != `{"default_reasoning_level":"high","supported_reasoning_levels":["low","medium","high","xhigh"],"supports_reasoning":true}` {
		t.Fatalf("merged models: %+v", refreshed.Models)
	}
	if _, ok := byID["claude-sonnet-4-6"]; ok {
		t.Fatal("a disabled model was added")
	}
	if _, err := f.service.RefreshModels(ctx, "src_missing"); proxyCode(err) != "antigravity_source_not_found" {
		t.Fatalf("refresh of an unknown source: %v", err)
	}
}

func TestAntigravitySourcesAreChangedOnlyBySignIn(t *testing.T) {
	f := newAntigravityFixture(t)
	ctx := context.Background()
	source := f.signIn(t, "first", "")
	sources := application.NewModelSourceService(f.store, f.vault)
	if _, err := sources.Create(ctx, application.ModelSourceCreateRequest{Name: "manual", Kind: domain.ModelSourceAntigravity, BaseURL: domain.AntigravityBaseURL,
		ProviderConfig: &domain.ModelSourceProviderConfig{Antigravity: &domain.AntigravityAccountInfo{Email: "x@example.test", Project: "p"}},
		Models:         []application.ModelSourceModelInput{{Model: "m"}}}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("an Antigravity source was created without a sign-in: %v", err)
	}
	for _, key := range []application.NullableString{{Set: true}, {Set: true, Value: new(string)}} {
		if _, err := sources.Update(ctx, source.ID, application.ModelSourceUpdateRequest{APIKey: key}); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("an API key edit replaced the refresh token: %v", err)
		}
	}
	name := "Renamed"
	updated, err := sources.Update(ctx, source.ID, application.ModelSourceUpdateRequest{Name: &name,
		ProviderConfig: &domain.ModelSourceProviderConfig{Antigravity: &domain.AntigravityAccountInfo{Email: "intruder@example.test", Project: "other"}}})
	if err != nil || updated.Name != "Renamed" || updated.ProviderConfig.Antigravity.Email != "agent@example.test" || updated.ProviderConfig.Antigravity.Project != "aicode-consumers" {
		t.Fatalf("update: %+v %v", updated, err)
	}
	if f.refreshToken(t, source.ID) != "refresh-first" {
		t.Fatal("an edit changed the credential")
	}
}
