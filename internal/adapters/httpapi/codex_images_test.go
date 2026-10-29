package httpapi_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"codex-lb/internal/adapters/credentials"
	"codex-lb/internal/adapters/httpapi"
	"codex-lb/internal/adapters/provider"
	"codex-lb/internal/adapters/sqlite"
	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

const (
	imageOK = `{"created":1793303698,"background":"opaque","output_format":"png","quality":"low","size":"1254x1254",` +
		`"data":[{"b64_json":"aGVsbG8=","generation_id":"gen-1"}],"usage":{"input_tokens":21,"input_tokens_details":{"image_tokens":0,"text_tokens":21},` +
		`"output_tokens":515,"output_tokens_details":{"image_tokens":515,"text_tokens":0},"total_tokens":536}}`
	imageLimit = `{"error":{"type":"usage_limit_reached","code":"usage_limit_reached","message":"The image generation limit was reached"}}`
	// 21 input tokens at $5 and 515 output tokens at $30 per 1M tokens.
	imageOKCost = 21*5 + 515*30
)

type imageCall struct {
	path, account, auth, contentType, accept string
	body                                     []byte
}

type imageStub struct {
	mu      sync.Mutex
	calls   []imageCall
	respond func(call imageCall, index int) (int, string)
	// headers, when set, adds response headers to every answer.
	headers map[string]string
}

func (s *imageStub) snapshot() []imageCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]imageCall(nil), s.calls...)
}

type imageFixture struct {
	stub       *imageStub
	store      *sqlite.Store
	operations *application.CodexOperations
	mux        *http.ServeMux
	vault      *credentials.Vault
}

// newImageFixture wires the image routes to a loopback ChatGPT stub with the
// given number of active subscription accounts (chatgpt-1 … chatgpt-n).
func newImageFixture(t *testing.T, accounts int, respond func(call imageCall, index int) (int, string)) imageFixture {
	t.Helper()
	stub := &imageStub{respond: respond}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		call := imageCall{path: r.URL.Path, account: r.Header.Get("ChatGPT-Account-ID"), auth: r.Header.Get("Authorization"),
			contentType: r.Header.Get("Content-Type"), accept: r.Header.Get("Accept"), body: body}
		stub.mu.Lock()
		index := len(stub.calls)
		stub.calls = append(stub.calls, call)
		stub.mu.Unlock()
		status, payload := stub.respond(call, index)
		contentType := "application/json"
		if strings.HasPrefix(payload, "event:") || strings.HasPrefix(payload, "data:") {
			contentType = "text/event-stream"
		}
		w.Header().Set("Content-Type", contentType)
		stub.mu.Lock()
		for name, value := range stub.headers {
			w.Header().Set(name, value)
		}
		stub.mu.Unlock()
		w.WriteHeader(status)
		_, _ = io.WriteString(w, payload)
	}))
	t.Cleanup(upstream.Close)
	_, store, proxy := wireFixtureWithProxy(t, nil)
	ctx := context.Background()
	vault, err := credentials.Open(filepath.Join(t.TempDir(), "image-key"), true)
	if err != nil {
		t.Fatal(err)
	}
	for index := 1; index <= accounts; index++ {
		id := "wire-account"
		if index > 1 {
			id = fmt.Sprintf("image-account-%d", index)
		}
		account := domain.Account{ID: id, Kind: domain.AccountChatGPT, Provider: "openai", PlanType: "plus",
			Email: fmt.Sprintf("image-%d@example.invalid", index), Status: domain.AccountActive, RoutingPolicy: "normal",
			ChatGPTAccountID: fmt.Sprintf("chatgpt-%d", index), CreatedAt: time.Now()}
		if existing, err := store.GetAccount(ctx, id); err == nil {
			existing.ChatGPTAccountID = account.ChatGPTAccountID
			account = existing
		}
		if err := store.SaveAccount(ctx, account); err != nil {
			t.Fatal(err)
		}
		saveImageCredential(t, store, vault, id)
	}
	adapter := provider.New(store, fixedTokenSource{}, vault, provider.Config{HTTPClient: upstream.Client(), ChatGPTBaseURL: upstream.URL + "/codex"})
	t.Cleanup(func() { _ = adapter.Close() })
	operations := application.NewCodexOperations(store, store, adapter, time.Hour)
	operations.ConfigureAdmission(proxy)
	operations.ConfigureAccountSelection(proxy)
	mux := http.NewServeMux()
	httpapi.RegisterCodexOperationRoutes(mux, store, operations, nil)
	return imageFixture{stub: stub, store: store, operations: operations, mux: mux, vault: vault}
}

// saveImageCredential stores a credential for the account's current route; the
// fixed token source never decrypts it.
func saveImageCredential(t *testing.T, store *sqlite.Store, vault *credentials.Vault, accountID string) {
	t.Helper()
	ctx := context.Background()
	account, err := store.GetAccount(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := vault.Encrypt([]byte("synthetic-credential"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveAccountCredential(ctx, domain.AccountCredential{AccountID: accountID, Generation: account.Generation, RouteRevision: account.RouteRevision,
		AccessTokenEncrypted: secret, RefreshTokenEncrypted: secret, IDTokenEncrypted: secret}); err != nil {
		t.Fatal(err)
	}
}

func (f imageFixture) post(t *testing.T, path, contentType string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	r.Header.Set("Content-Type", contentType)
	r.Header.Set("Authorization", "Bearer synthetic-key")
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	return w
}

func (f imageFixture) postJSON(t *testing.T, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	return f.post(t, path, "application/json", []byte(body))
}

func (f imageFixture) logs(t *testing.T) []domain.RequestLogEntry {
	t.Helper()
	logs, err := f.store.ListRequestLogs(context.Background(), domain.RequestLogFilter{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	return logs.Requests
}

func (f imageFixture) pending(t *testing.T) int {
	t.Helper()
	pending, err := f.store.ListReservationsNeedingReconciliation(context.Background(), "", 10)
	if err != nil {
		t.Fatal(err)
	}
	return len(pending)
}

func (f imageFixture) status(t *testing.T, accountID string) domain.AccountStatus {
	t.Helper()
	account, err := f.store.GetAccount(context.Background(), accountID)
	if err != nil {
		t.Fatal(err)
	}
	return account.Status
}

func errorParam(t *testing.T, body []byte) (string, string) {
	t.Helper()
	var envelope struct {
		Error struct {
			Code  string `json:"code"`
			Param string `json:"param"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("error envelope: %v: %s", err, body)
	}
	return envelope.Error.Code, envelope.Error.Param
}

func TestCodexImageGenerationForwardsAndSettlesOnce(t *testing.T) {
	f := newImageFixture(t, 1, func(imageCall, int) (int, string) { return 200, imageOK })
	request := `{"prompt":"A small red paper boat","model":"gpt-image-2","background":"opaque","quality":"auto","size":"auto"}`
	w := f.postJSON(t, "/backend-api/codex/images/generations", request)
	if w.Code != 200 || w.Body.String() != imageOK {
		t.Fatalf("upstream image body was not returned unchanged: %d %s", w.Code, w.Body.String())
	}
	calls := f.stub.snapshot()
	if len(calls) != 1 || calls[0].path != "/codex/images/generations" || calls[0].auth != "Bearer synthetic-test-token" ||
		calls[0].account != "chatgpt-1" || calls[0].contentType != "application/json" || string(calls[0].body) != request {
		t.Fatalf("subscription images endpoint did not receive the request as sent: %+v", calls)
	}
	totals, err := f.store.UsageTotals(context.Background(), "wire-key", "")
	if err != nil {
		t.Fatal(err)
	}
	if totals.Usage.InputTokens != 21 || totals.Usage.OutputTokens != 515 || totals.Usage.CostMicrodollars != imageOKCost {
		t.Fatalf("image usage was not settled from the response: %+v", totals)
	}
	logs := f.logs(t)
	if len(logs) != 1 || logs[0].RequestKind != "image_generation" || logs[0].Model != "gpt-image-2" || logs[0].Status != "success" {
		t.Fatalf("image request log: %+v", logs)
	}
	if f.pending(t) != 0 || f.status(t, "wire-account") != domain.AccountActive {
		t.Fatal("image request left a reservation or changed account health")
	}
}

func TestImageEditsAcceptJSONAndMultipart(t *testing.T) {
	f := newImageFixture(t, 1, func(imageCall, int) (int, string) { return 200, imageOK })
	edit := `{"prompt":"add a hat","model":"gpt-image-2","images":[{"image_url":"data:image/png;base64,Zm9v"}]}`
	if w := f.postJSON(t, "/v1/images/edits/", edit); w.Code != 200 {
		t.Fatalf("JSON edit: %d %s", w.Code, w.Body.String())
	}
	calls := f.stub.snapshot()
	if len(calls) != 1 || calls[0].path != "/codex/images/edits" || string(calls[0].body) != edit {
		t.Fatalf("JSON edit was not forwarded unchanged: %+v", calls)
	}
	if logs := f.logs(t); len(logs) != 1 || logs[0].RequestKind != "image_edit" {
		t.Fatalf("edit request kind: %+v", logs)
	}

	first := []byte("\x89PNG\r\n\x1a\nfirst-image")
	second := []byte("\x89PNG\r\n\x1a\nsecond-image")
	maskBytes := []byte("\x89PNG\r\n\x1a\nmask-image")
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	writeFile := func(name, contentType string, data []byte) {
		header := textproto.MIMEHeader{}
		header.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename="part.png"`, name))
		if contentType != "" {
			header.Set("Content-Type", contentType)
		}
		part, _ := form.CreatePart(header)
		_, _ = part.Write(data)
	}
	writeFile("image", "image/png", first)
	writeFile("image[]", "", second) // sniffed
	writeFile("mask", "image/png", maskBytes)
	_ = form.WriteField("prompt", "replace the sky")
	_ = form.WriteField("n", "1")
	_ = form.Close()
	if w := f.post(t, "/v1/images/edits", form.FormDataContentType(), body.Bytes()); w.Code != 200 {
		t.Fatalf("multipart edit: %d %s", w.Code, w.Body.String())
	}
	calls = f.stub.snapshot()
	if len(calls) != 2 || calls[1].path != "/codex/images/edits" || calls[1].contentType != "application/json" {
		t.Fatalf("multipart edit was not sent as JSON: %+v", calls)
	}
	var forwarded struct {
		Prompt string              `json:"prompt"`
		Model  string              `json:"model"`
		N      json.Number         `json:"n"`
		Images []map[string]string `json:"images"`
		Mask   map[string]string   `json:"mask"`
	}
	if err := json.Unmarshal(calls[1].body, &forwarded); err != nil {
		t.Fatal(err)
	}
	dataURL := func(data []byte) string { return "data:image/png;base64," + base64.StdEncoding.EncodeToString(data) }
	if forwarded.Prompt != "replace the sky" || forwarded.Model != "gpt-image-2" || forwarded.N != "1" || len(forwarded.Images) != 2 ||
		forwarded.Images[0]["image_url"] != dataURL(first) || forwarded.Images[1]["image_url"] != dataURL(second) ||
		forwarded.Mask["image_url"] != dataURL(maskBytes) {
		t.Fatalf("multipart conversion: %s", calls[1].body)
	}

	for name, build := range map[string]func(*multipart.Writer){
		"unknown file part": func(form *multipart.Writer) {
			part, _ := form.CreateFormFile("file", "x.png")
			_, _ = part.Write(first)
		},
		"text file as image": func(form *multipart.Writer) {
			header := textproto.MIMEHeader{}
			header.Set("Content-Disposition", `form-data; name="image"; filename="notes.txt"`)
			header.Set("Content-Type", "text/plain")
			part, _ := form.CreatePart(header)
			_, _ = part.Write([]byte("not an image"))
		},
	} {
		var invalid bytes.Buffer
		form := multipart.NewWriter(&invalid)
		build(form)
		_ = form.WriteField("prompt", "x")
		_ = form.Close()
		if w := f.post(t, "/v1/images/edits", form.FormDataContentType(), invalid.Bytes()); w.Code != 400 {
			t.Fatalf("%s: %d %s", name, w.Code, w.Body.String())
		}
	}
	if len(f.stub.snapshot()) != 2 {
		t.Fatal("an invalid multipart edit reached the upstream")
	}
}

func TestImageRequestsAreValidatedBeforeDispatch(t *testing.T) {
	f := newImageFixture(t, 1, func(imageCall, int) (int, string) { return 200, imageOK })
	images17 := strings.TrimSuffix(strings.Repeat(`{"image_url":"data:image/png;base64,Zm9v"},`, 17), ",")
	for _, test := range []struct {
		name, path, body, param string
	}{
		{"not an object", "/v1/images/generations", `[]`, ""},
		{"missing prompt", "/v1/images/generations", `{"model":"gpt-image-2"}`, "prompt"},
		{"blank prompt", "/backend-api/codex/images/generations", `{"prompt":"  "}`, "prompt"},
		{"text model", "/v1/images/generations", `{"prompt":"x","model":"gpt-6-sol"}`, "model"},
		{"unpriced image model", "/v1/images/generations", `{"prompt":"x","model":"gpt-image-0"}`, "model"},
		{"non-boolean stream", "/v1/images/generations", `{"prompt":"x","stream":"yes"}`, "stream"},
		{"n zero", "/v1/images/generations", `{"prompt":"x","n":0}`, "n"},
		{"fractional n", "/v1/images/generations", `{"prompt":"x","n":1.5}`, "n"},
		{"edit without images", "/backend-api/codex/images/edits", `{"prompt":"x"}`, "images"},
		{"edit image without reference", "/v1/images/edits", `{"prompt":"x","images":[{}]}`, "images"},
		{"seventeen images", "/v1/images/edits", `{"prompt":"x","images":[` + images17 + `]}`, "images"},
		{"mask without reference", "/v1/images/edits", `{"prompt":"x","images":[{"file_id":"file-1"}],"mask":{}}`, "mask"},
	} {
		w := f.postJSON(t, test.path, test.body)
		code, param := errorParam(t, w.Body.Bytes())
		if w.Code != 400 || code != "invalid_request" || param != test.param {
			t.Fatalf("%s: %d %s", test.name, w.Code, w.Body.String())
		}
	}
	r := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{"prompt":"x"}`))
	r.ContentLength = 33 << 20
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer synthetic-key")
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	if w.Code != 413 {
		t.Fatalf("oversized image body: %d %s", w.Code, w.Body.String())
	}
	if calls := f.stub.snapshot(); len(calls) != 0 || len(f.logs(t)) != 0 || f.pending(t) != 0 {
		t.Fatalf("an invalid image request reached the upstream or the ledger: %+v", calls)
	}
}

func TestImageRequestsApplyKeyModelPolicy(t *testing.T) {
	f := newImageFixture(t, 1, func(imageCall, int) (int, string) { return 200, imageOK })
	ctx := context.Background()
	setKey := func(change func(*domain.APIKey)) {
		key, err := f.store.GetAPIKey(ctx, "wire-key")
		if err != nil {
			t.Fatal(err)
		}
		change(&key)
		if err := f.store.SaveAPIKey(ctx, key, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	request := `{"prompt":"x","model":"gpt-image-2"}`
	setKey(func(key *domain.APIKey) { key.AllowedModels = []string{"gpt-6-sol"} })
	if w := f.postJSON(t, "/v1/images/generations", request); w.Code != 403 || !strings.Contains(w.Body.String(), "model_not_allowed") {
		t.Fatalf("allowed models were not applied: %d %s", w.Code, w.Body.String())
	}
	if len(f.stub.snapshot()) != 0 {
		t.Fatal("a disallowed image model reached the upstream")
	}
	text, image := "gpt-6-sol", "gpt-image-2.5-flare"
	setKey(func(key *domain.APIKey) { key.AllowedModels, key.EnforcedModel = nil, &text })
	if w := f.postJSON(t, "/v1/images/generations", request); w.Code != 200 || string(f.stub.snapshot()[0].body) != request {
		t.Fatalf("an enforced text model changed the image request: %d %s", w.Code, w.Body.String())
	}
	setKey(func(key *domain.APIKey) { key.EnforcedModel = &image })
	if w := f.postJSON(t, "/v1/images/generations", request); w.Code != 200 {
		t.Fatalf("enforced image model: %d %s", w.Code, w.Body.String())
	}
	var forwarded struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(f.stub.snapshot()[1].body, &forwarded); err != nil || forwarded.Model != image {
		t.Fatalf("an enforced image model did not replace the requested one: %s", f.stub.snapshot()[1].body)
	}
	if logs := f.logs(t); len(logs) != 2 || logs[0].Model != image {
		t.Fatalf("request log model: %+v", logs)
	}
}

func TestImageLimitRefusalMovesToAnotherAccount(t *testing.T) {
	var limited string
	var mu sync.Mutex
	f := newImageFixture(t, 2, func(call imageCall, index int) (int, string) {
		mu.Lock()
		defer mu.Unlock()
		if index == 0 {
			limited = call.account
		}
		if call.account == limited {
			return 429, imageLimit
		}
		return 200, imageOK
	})
	request := `{"prompt":"x","model":"gpt-image-2"}`
	if w := f.postJSON(t, "/backend-api/codex/images/generations", request); w.Code != 200 || w.Body.String() != imageOK {
		t.Fatalf("refused request was not moved to another account: %d %s", w.Code, w.Body.String())
	}
	calls := f.stub.snapshot()
	if len(calls) != 2 || calls[0].account != limited || calls[1].account == limited || string(calls[1].body) != request {
		t.Fatalf("failover order: %+v", calls)
	}
	if w := f.postJSON(t, "/backend-api/codex/images/generations", request); w.Code != 200 {
		t.Fatalf("second request: %d %s", w.Code, w.Body.String())
	}
	if calls = f.stub.snapshot(); len(calls) != 3 || calls[2].account == limited {
		t.Fatalf("a cooling account received an image request: %+v", calls)
	}
	for _, id := range []string{"wire-account", "image-account-2"} {
		if status := f.status(t, id); status != domain.AccountActive {
			t.Fatalf("image limit changed account %s health to %s", id, status)
		}
	}
	totals, err := f.store.UsageTotals(context.Background(), "wire-key", "")
	if err != nil || totals.Usage.CostMicrodollars != 2*imageOKCost || f.pending(t) != 0 {
		t.Fatalf("refused attempt was charged or left pending: %+v %v", totals, err)
	}
}

func TestImageFailoverIsLimitedToRefusalsBeforeOutput(t *testing.T) {
	t.Run("every account refuses", func(t *testing.T) {
		f := newImageFixture(t, 2, func(imageCall, int) (int, string) { return 429, imageLimit })
		w := f.postJSON(t, "/v1/images/generations", `{"prompt":"x"}`)
		if w.Code != 429 || w.Body.String() != imageLimit || len(f.stub.snapshot()) != 2 {
			t.Fatalf("last refusal: %d %s calls=%d", w.Code, w.Body.String(), len(f.stub.snapshot()))
		}
		if f.status(t, "wire-account") != domain.AccountActive || f.status(t, "image-account-2") != domain.AccountActive || f.pending(t) != 0 {
			t.Fatal("refusals changed account health or left reservations")
		}
	})
	t.Run("server error", func(t *testing.T) {
		f := newImageFixture(t, 2, func(imageCall, int) (int, string) { return 500, `{"error":{"code":"server_error"}}` })
		if w := f.postJSON(t, "/v1/images/generations", `{"prompt":"x"}`); w.Code != 500 || len(f.stub.snapshot()) != 1 {
			t.Fatalf("a failure that may have executed was retried: %d calls=%d", w.Code, len(f.stub.snapshot()))
		}
	})
	t.Run("file-pinned edit", func(t *testing.T) {
		f := newImageFixture(t, 2, func(imageCall, int) (int, string) { return 429, imageLimit })
		ctx := context.Background()
		owner, err := f.store.GetAccount(ctx, "wire-account")
		if err != nil {
			t.Fatal(err)
		}
		if err := f.store.SaveCodexResourceOwner(ctx, application.CodexResourceOwner{ResourceType: application.CodexResourceFile, ResourceID: "file-pinned",
			KeyID: "wire-key", AccountID: owner.ID, AccountGeneration: owner.Generation, RouteRevision: owner.RouteRevision, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
		w := f.postJSON(t, "/backend-api/codex/images/edits", `{"prompt":"x","images":[{"file_id":"file-pinned"}]}`)
		calls := f.stub.snapshot()
		if w.Code != 429 || len(calls) != 1 || calls[0].account != "chatgpt-1" {
			t.Fatalf("a pinned edit left its owner: %d %+v", w.Code, calls)
		}
		if w := f.postJSON(t, "/backend-api/codex/images/edits", `{"prompt":"x","images":[{"file_id":"file-unknown"}]}`); w.Code != 409 || len(f.stub.snapshot()) != 1 {
			t.Fatalf("unknown file owner: %d %s", w.Code, w.Body.String())
		}
	})
}

func TestImageOutcomesAreAccountNeutral(t *testing.T) {
	f := newImageFixture(t, 1, func(imageCall, int) (int, string) { return 200, imageOK })
	ctx := context.Background()
	account, err := f.store.GetAccount(ctx, "wire-account")
	if err != nil {
		t.Fatal(err)
	}
	account.Status = domain.AccountRateLimited
	if err := f.store.SaveAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	saveImageCredential(t, f.store, f.vault, account.ID)
	account, _ = f.store.GetAccount(ctx, account.ID)
	if err := f.store.SaveCodexResourceOwner(ctx, application.CodexResourceOwner{ResourceType: application.CodexResourceFile, ResourceID: "file-owned",
		KeyID: "wire-key", AccountID: account.ID, AccountGeneration: account.Generation, RouteRevision: account.RouteRevision, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if w := f.postJSON(t, "/backend-api/codex/images/edits", `{"prompt":"x","images":[{"file_id":"file-owned"}]}`); w.Code != 200 {
		t.Fatalf("pinned edit on a rate-limited owner: %d %s", w.Code, w.Body.String())
	}
	if status := f.status(t, account.ID); status != domain.AccountRateLimited {
		t.Fatalf("an image success changed account health to %s", status)
	}
}

func TestImageResponsesWithoutImageOrUsageAreNotSuccess(t *testing.T) {
	noData := `{"created":1,"data":[],"usage":{"input_tokens":21,"output_tokens":0}}`
	noUsage := `{"created":1,"data":[{"b64_json":"aGVsbG8="}]}`
	responses := []string{noData, noUsage}
	f := newImageFixture(t, 1, func(_ imageCall, index int) (int, string) { return 200, responses[index] })
	if w := f.postJSON(t, "/v1/images/generations", `{"prompt":"x"}`); w.Code != 502 {
		t.Fatalf("success without an image: %d %s", w.Code, w.Body.String())
	}
	if f.pending(t) != 0 {
		t.Fatal("a response with known usage was left for reconciliation")
	}
	if w := f.postJSON(t, "/v1/images/generations", `{"prompt":"x"}`); w.Code != 502 {
		t.Fatalf("success without usage: %d %s", w.Code, w.Body.String())
	}
	if f.pending(t) != 1 {
		t.Fatal("an image without usage did not keep its reservation for reconciliation")
	}
}

func TestImageDiagnosticsOmitImagePayloads(t *testing.T) {
	f := newImageFixture(t, 1, func(imageCall, int) (int, string) {
		return 400, `{"error":{"code":"invalid_prompt","message":"rejected"}}`
	})
	var mu sync.Mutex
	var recorded []application.ErrorDiagnostic
	f.operations.ConfigureDiagnostics(func(_ context.Context, diagnostic application.ErrorDiagnostic) {
		mu.Lock()
		defer mu.Unlock()
		recorded = append(recorded, diagnostic)
	})
	w := f.postJSON(t, "/v1/images/edits", `{"prompt":"keep this prompt","images":[{"image_url":"data:image/png;base64,c2VjcmV0LWltYWdl"}],"mask":{"image_url":"data:image/png;base64,c2VjcmV0LW1hc2s="}}`)
	if w.Code != 400 {
		t.Fatalf("upstream refusal: %d %s", w.Code, w.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(recorded) != 1 {
		t.Fatalf("diagnostics: %d", len(recorded))
	}
	request := string(recorded[0].Request)
	if strings.Contains(request, "c2VjcmV0") || !strings.Contains(request, "keep this prompt") || !strings.Contains(request, `"images_omitted":1`) {
		t.Fatalf("diagnostic request: %s", request)
	}
}

const imageStreamOK = "event: keepalive\ndata: {\"type\":\"keepalive\"}\n\n" +
	"event: image_generation.completed\ndata: {\"type\":\"image_generation.completed\",\"sequence_number\":0,\"b64_json\":\"aGVsbG8=\",\"created_at\":1793308156," +
	"\"generation_id\":\"gen-1\",\"background\":\"opaque\",\"quality\":\"low\",\"size\":\"1254x1254\",\"output_format\":\"png\"," +
	"\"usage\":{\"input_tokens\":21,\"input_tokens_details\":{\"image_tokens\":0,\"text_tokens\":21},\"output_tokens\":515,\"output_tokens_details\":{\"image_tokens\":515,\"text_tokens\":0},\"total_tokens\":536}}\n\n"

func TestImageStreamIsRelayedAndSettledOnce(t *testing.T) {
	f := newImageFixture(t, 1, func(imageCall, int) (int, string) { return 200, imageStreamOK })
	w := f.postJSON(t, "/backend-api/codex/images/generations", `{"prompt":"x","model":"gpt-image-2","stream":true,"partial_images":2}`)
	if w.Code != 200 || w.Header().Get("Content-Type") != "text/event-stream" || w.Body.String() != imageStreamOK {
		t.Fatalf("image stream was not relayed unchanged: %d %q %q", w.Code, w.Header().Get("Content-Type"), w.Body.String())
	}
	calls := f.stub.snapshot()
	if len(calls) != 1 || calls[0].accept != "text/event-stream" || !strings.Contains(string(calls[0].body), `"partial_images":2`) {
		t.Fatalf("streaming request was not forwarded as a stream: %+v", calls)
	}
	totals, err := f.store.UsageTotals(context.Background(), "wire-key", "")
	if err != nil || totals.Usage.InputTokens != 21 || totals.Usage.OutputTokens != 515 || totals.Usage.CostMicrodollars != imageOKCost || f.pending(t) != 0 {
		t.Fatalf("stream was not settled from the completed event: %+v %v", totals, err)
	}
	if logs := f.logs(t); len(logs) != 1 || logs[0].RequestKind != "image_generation" || logs[0].Status != "success" {
		t.Fatalf("stream request log: %+v", logs)
	}
}

func TestImageStreamingEditAnsweredWithJSONStaysJSON(t *testing.T) {
	f := newImageFixture(t, 1, func(imageCall, int) (int, string) { return 200, imageOK })
	w := f.postJSON(t, "/backend-api/codex/images/edits", `{"prompt":"x","stream":true,"images":[{"image_url":"data:image/png;base64,Zm9v"}]}`)
	if w.Code != 200 || w.Body.String() != imageOK || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("JSON answer to a streaming edit: %d %q %s", w.Code, w.Header().Get("Content-Type"), w.Body.String())
	}
	if f.stub.snapshot()[0].accept != "text/event-stream" || f.pending(t) != 0 {
		t.Fatal("streaming edit was not asked as a stream or left a reservation")
	}
}

func TestImageStreamWithoutImageEndsWithErrorAndKeepsReservation(t *testing.T) {
	f := newImageFixture(t, 1, func(imageCall, int) (int, string) {
		return 200, "event: keepalive\ndata: {\"type\":\"keepalive\"}\n\n"
	})
	w := f.postJSON(t, "/v1/images/generations", `{"prompt":"x","stream":true}`)
	body := w.Body.String()
	if w.Code != 200 || !strings.HasPrefix(body, "event: keepalive\n") || !strings.Contains(body, "event: error\ndata: ") || !strings.Contains(body, "invalid_upstream_response") {
		t.Fatalf("stream without an image: %d %q", w.Code, body)
	}
	if f.pending(t) != 1 {
		t.Fatal("a stream without a completed image did not keep its reservation for reconciliation")
	}
}

func TestFinalImageLimitRefusalCarriesResetHeaders(t *testing.T) {
	f := newImageFixture(t, 2, func(imageCall, int) (int, string) { return 429, imageLimit })
	f.stub.headers = map[string]string{
		"Retry-After": "3600", "X-Codex-Active-Limit": "image_gen", "X-Image-Gen-Primary-Reset-At": "1793390082",
		"X-Codex-Primary-Reset-At": "1793390082", "X-Codex-Plan-Type": "pro", "X-Codex-Credits-Balance": "7",
	}
	w := f.postJSON(t, "/backend-api/codex/images/generations", `{"prompt":"x"}`)
	if w.Code != 429 || w.Body.String() != imageLimit {
		t.Fatalf("final refusal: %d %s", w.Code, w.Body.String())
	}
	for _, name := range []string{"Retry-After", "X-Codex-Active-Limit", "X-Image-Gen-Primary-Reset-At", "X-Codex-Primary-Reset-At"} {
		if w.Header().Get(name) == "" {
			t.Fatalf("reset header %s was dropped: %v", name, w.Header())
		}
	}
	if w.Header().Get("X-Codex-Plan-Type") != "" || w.Header().Get("X-Codex-Credits-Balance") != "" {
		t.Fatalf("account plan or credit headers reached the client: %v", w.Header())
	}
}

func TestResponsesImageLimitRefusalKeepsAccountUsable(t *testing.T) {
	for _, test := range []struct {
		name        string
		activeLimit string
	}{{"image limit", "imagegen_premium"}, {"account quota", ""}} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int64
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if test.activeLimit != "" {
					w.Header().Set("X-Codex-Active-Limit", test.activeLimit)
					w.Header().Set("X-Image-Gen-Primary-Reset-At", "1793390082")
				}
				w.Header().Set("Retry-After", "3600")
				w.Header().Set("X-Codex-Plan-Type", "pro")
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = io.WriteString(w, imageLimit)
			}))
			defer upstream.Close()
			var adapter *provider.Adapter
			server, store, _ := wireFixtureWithProxy(t, wireProvider(func(ctx context.Context, target application.ResponseTarget, body json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
				return adapter.Respond(ctx, target, body, emit)
			}))
			adapter = provider.New(store, fixedTokenSource{}, nil, provider.Config{HTTPClient: upstream.Client(), ChatGPTBaseURL: upstream.URL})
			defer func() { _ = adapter.Close() }()
			ctx := context.Background()
			settings, _ := store.LoadSettings(ctx)
			settings.HTTPTransportPolicy = "always_http"
			if err := store.SaveSettings(ctx, settings); err != nil {
				t.Fatal(err)
			}
			other, _ := store.GetAccount(ctx, "wire-account")
			other.ID, other.Email = "other-account", "other@example.invalid"
			if err := store.SaveAccount(ctx, other); err != nil {
				t.Fatal(err)
			}
			credential, _ := store.GetAccountCredential(ctx, "wire-account")
			credential.AccountID = other.ID
			if err := store.SaveAccountCredential(ctx, credential); err != nil {
				t.Fatal(err)
			}
			request, _ := http.NewRequest(http.MethodPost, server.URL+"/backend-api/codex/responses",
				strings.NewReader(`{"model":"gpt-6-sol","input":"draw a boat","stream":true,"tools":[{"type":"image_generation"}]}`))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Authorization", "Bearer synthetic-key")
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(response.Body)
			response.Body.Close()
			statuses := map[string]domain.AccountStatus{}
			for _, id := range []string{"wire-account", "other-account"} {
				account, err := store.GetAccount(ctx, id)
				if err != nil {
					t.Fatal(err)
				}
				statuses[id] = account.Status
			}
			pending, _ := store.ListReservationsNeedingReconciliation(ctx, "", 10)
			if test.activeLimit == "" {
				if calls.Load() != 2 || statuses["wire-account"] == domain.AccountActive && statuses["other-account"] == domain.AccountActive {
					t.Fatalf("account quota refusal no longer marks and moves: calls=%d %v", calls.Load(), statuses)
				}
				return
			}
			var envelope struct {
				Error struct{ Type, Code string } `json:"error"`
			}
			_ = json.Unmarshal(body, &envelope)
			if response.StatusCode != 429 || envelope.Error.Type != "usage_limit_reached" || envelope.Error.Code != "usage_limit_reached" ||
				response.Header.Get("Retry-After") != "3600" || response.Header.Get("X-Codex-Active-Limit") != "imagegen_premium" ||
				response.Header.Get("X-Image-Gen-Primary-Reset-At") == "" || response.Header.Get("X-Codex-Plan-Type") != "" {
				t.Fatalf("image-limit refusal response: %d %v %s", response.StatusCode, response.Header, body)
			}
			if calls.Load() != 1 || statuses["wire-account"] != domain.AccountActive || statuses["other-account"] != domain.AccountActive || len(pending) != 0 {
				t.Fatalf("image-limit refusal marked or moved the account: calls=%d %v pending=%d", calls.Load(), statuses, len(pending))
			}
		})
	}
}
