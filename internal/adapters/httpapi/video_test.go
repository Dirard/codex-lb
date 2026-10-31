package httpapi_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"codex-lb/internal/adapters/credentials"
	"codex-lb/internal/adapters/httpapi"
	"codex-lb/internal/adapters/sqlite"
	"codex-lb/internal/adapters/xai"
	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

const videoPinnedBody = `{"model":"grok-imagine-video-1.5","prompt":"The boat turns.","image":{"url":"data:image/jpeg;base64,Zmlyc3Q="},"last_frame":{"url":"data:image/jpeg;base64,bGFzdA=="},"duration":2,"resolution":"480p"}`

type videoCreate struct {
	account string
	body    string
}

// xaiStub is a loopback xAI: an access token names its account, and refresh
// rotates the refresh token as the real endpoint does.
type xaiStub struct {
	mu        sync.Mutex
	creates   []videoCreate
	gets      []string
	refreshes map[string]int
	revoked   []string
	create    func(account string, index int) (int, string, http.Header)
	get       func(account, id string) (int, string)
	refused   map[string]bool
	reject401 map[string]bool
	jobSeq    atomic.Int64
	server    *httptest.Server
}

func newXAIStub(t *testing.T) *xaiStub {
	t.Helper()
	stub := &xaiStub{refreshes: map[string]int{}, refused: map[string]bool{}, reject401: map[string]bool{}}
	stub.create = func(string, int) (int, string, http.Header) {
		return 200, fmt.Sprintf(`{"request_id":"upstream-%d"}`, stub.jobSeq.Add(1)), nil
	}
	stub.get = func(string, string) (int, string) {
		return 200, `{"status":"done","video":{"url":"https://vidgen.example.invalid/a.mp4","duration":2},"usage":{"cost_in_usd_ticks":1800000000},"progress":100}`
	}
	stub.server = httptest.NewServer(http.HandlerFunc(stub.serve))
	t.Cleanup(stub.server.Close)
	return stub
}

func (x *xaiStub) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Path == "/oauth2/token" || r.URL.Path == "/oauth2/revoke" {
		values := strings.Split(string(body), "&")
		fields := map[string]string{}
		for _, value := range values {
			name, field, _ := strings.Cut(value, "=")
			fields[name] = field
		}
		x.mu.Lock()
		defer x.mu.Unlock()
		if r.URL.Path == "/oauth2/revoke" {
			x.revoked = append(x.revoked, fields["token"])
			return
		}
		account, _, _ := strings.Cut(strings.TrimPrefix(fields["refresh_token"], "refresh-"), "-")
		if x.refused[account] || fields["client_id"] != "synthetic-cli-client" {
			w.WriteHeader(400)
			_, _ = io.WriteString(w, `{"error":"invalid_grant"}`)
			return
		}
		x.refreshes[account]++
		count := x.refreshes[account]
		_, _ = fmt.Fprintf(w, `{"access_token":"access-%s-%d","refresh_token":"refresh-%s-%d","expires_in":21600,"token_type":"Bearer"}`, account, count, account, count)
		return
	}
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	account, version, _ := strings.Cut(strings.TrimPrefix(token, "access-"), "-")
	x.mu.Lock()
	rejected := x.reject401[account+"-"+version]
	x.mu.Unlock()
	if rejected {
		w.WriteHeader(401)
		return
	}
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v1/videos/generations":
		x.mu.Lock()
		index := len(x.creates)
		x.creates = append(x.creates, videoCreate{account: account, body: string(body)})
		x.mu.Unlock()
		status, payload, headers := x.create(account, index)
		for name, value := range headers {
			w.Header()[name] = value
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, payload)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/videos/"):
		x.mu.Lock()
		x.gets = append(x.gets, account)
		x.mu.Unlock()
		status, payload := x.get(account, strings.TrimPrefix(r.URL.Path, "/v1/videos/"))
		w.WriteHeader(status)
		_, _ = io.WriteString(w, payload)
	default:
		w.WriteHeader(404)
	}
}

func (x *xaiStub) createCalls() []videoCreate {
	x.mu.Lock()
	defer x.mu.Unlock()
	return append([]videoCreate(nil), x.creates...)
}

func (x *xaiStub) getCalls() []string {
	x.mu.Lock()
	defer x.mu.Unlock()
	return append([]string(nil), x.gets...)
}

type videoFixture struct {
	stub    *xaiStub
	store   *sqlite.Store
	service *application.VideoService
	mux     *http.ServeMux
}

func newVideoFixture(t *testing.T, accounts ...string) videoFixture {
	t.Helper()
	stub := newXAIStub(t)
	_, store, proxy := wireFixtureWithProxy(t, nil)
	vault, err := credentials.Open(filepath.Join(t.TempDir(), "video-key"), true)
	if err != nil {
		t.Fatal(err)
	}
	service := application.NewVideoService(store, vault, xai.New(xai.Config{HTTPClient: stub.server.Client(), APIBaseURL: stub.server.URL, AuthBaseURL: stub.server.URL}))
	service.ConfigureAdmission(proxy)
	mux := http.NewServeMux()
	httpapi.NewVideoHandler(store, service).RegisterRoutes(mux)
	f := videoFixture{stub: stub, store: store, service: service, mux: mux}
	for _, account := range accounts {
		if _, err := service.ImportAccount(context.Background(), videoAuthFile(account)); err != nil {
			t.Fatal(err)
		}
	}
	if err := service.AllowKey(context.Background(), "wire-key"); err != nil {
		t.Fatal(err)
	}
	return f
}

func videoAuthFile(account string) []byte {
	return []byte(fmt.Sprintf(`{"https://auth.x.ai::synthetic":{"key":"cli-access","refresh_token":"refresh-%s-0","user_id":"user-%s","email":"%s@example.invalid","oidc_issuer":"https://auth.x.ai","oidc_client_id":"synthetic-cli-client"}}`, account, account, account))
}

func (f videoFixture) request(method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer synthetic-key")
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	return w
}

func (f videoFixture) createJob(t *testing.T) string {
	t.Helper()
	w := f.request(http.MethodPost, "/video/v1/generations", videoPinnedBody)
	if w.Code != http.StatusAccepted {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var job struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil || job.ID == "" {
		t.Fatalf("create body: %s", w.Body.String())
	}
	return job.ID
}

func (f videoFixture) accountStatus(t *testing.T, email string) domain.VideoAccount {
	t.Helper()
	accounts, err := f.service.ListAccounts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, account := range accounts {
		if account.Email == email+"@example.invalid" {
			return account.VideoAccount
		}
	}
	t.Fatalf("no account %s", email)
	return domain.VideoAccount{}
}

func TestVideoFirstLastFrameAndTextRoundTrip(t *testing.T) {
	f := newVideoFixture(t, "one")
	w := f.request(http.MethodPost, "/video/v1/generations", videoPinnedBody)
	if w.Code != http.StatusAccepted || strings.Contains(w.Body.String(), "upstream-") || strings.Contains(w.Body.String(), "one") {
		t.Fatalf("created job names its upstream: %d %s", w.Code, w.Body.String())
	}
	calls := f.stub.createCalls()
	if len(calls) != 1 || calls[0].body != videoPinnedBody {
		t.Fatalf("frames were not forwarded unchanged: %+v", calls)
	}
	var job struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Mode   string `json:"mode"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil || !strings.HasPrefix(job.ID, "vid_") || job.Status != "pending" || job.Mode != "first_last_frame" {
		t.Fatalf("job: %s", w.Body.String())
	}
	status := f.request(http.MethodGet, "/video/v1/generations/"+job.ID, "")
	var done struct {
		Status  string   `json:"status"`
		CostUSD *float64 `json:"cost_usd"`
		Video   struct {
			URL string `json:"url"`
		} `json:"video"`
	}
	if err := json.Unmarshal(status.Body.Bytes(), &done); err != nil || status.Code != 200 || done.Status != "done" || done.Video.URL == "" || done.CostUSD == nil || *done.CostUSD != 0.18 {
		t.Fatalf("status: %d %s", status.Code, status.Body.String())
	}
	if again := f.request(http.MethodGet, "/video/v1/generations/"+job.ID, ""); again.Code != 200 || len(f.stub.getCalls()) != 1 {
		t.Fatalf("a finished job called xAI again: %d gets", len(f.stub.getCalls()))
	}
}

func TestVideoRequestsAreValidatedBeforeAnyJob(t *testing.T) {
	f := newVideoFixture(t, "one")
	frame := `{"url":"data:image/png;base64,Zm9v"}`
	keyframe := `{"image":` + frame + `,"timestamp_s":1}`
	for _, test := range []struct{ name, body, param string }{
		{"not an object", `[]`, ""},
		{"unknown model", `{"model":"gpt-image-2","prompt":"x"}`, "model"},
		{"unknown field", `{"model":"grok-imagine-video","prompt":"x","storage_options":{"filename":"a"}}`, "storage_options"},
		{"upload target", `{"model":"grok-imagine-video","prompt":"x","output":{"upload_url":"https://example.invalid"}}`, "output"},
		{"file reference", `{"model":"grok-imagine-video","image":{"file_id":"file-1"}}`, "image"},
		{"no prompt or frame", `{"model":"grok-imagine-video"}`, "prompt"},
		{"long clip", `{"model":"grok-imagine-video","prompt":"x","duration":16}`, "duration"},
		{"short clip", `{"model":"grok-imagine-video","prompt":"x","duration":0}`, "duration"},
		{"last frame on classic", `{"model":"grok-imagine-video","prompt":"x","last_frame":` + frame + `}`, "last_frame"},
		{"last frame on lite", `{"model":"grok-imagine-video-1.5-lite","prompt":"x","last_frame":` + frame + `}`, "last_frame"},
		{"five keyframes", `{"model":"grok-imagine-video-1.5","keyframes":[` + strings.TrimSuffix(strings.Repeat(keyframe+",", 5), ",") + `]}`, "keyframes"},
		{"keyframe at the end", `{"model":"grok-imagine-video-1.5","duration":2,"keyframes":[{"image":` + frame + `,"timestamp_s":2}]}`, "keyframes"},
		{"bad resolution", `{"model":"grok-imagine-video","prompt":"x","resolution":"4k"}`, "resolution"},
		{"bad aspect", `{"model":"grok-imagine-video","prompt":"x","aspect_ratio":"7:1"}`, "aspect_ratio"},
	} {
		w := f.request(http.MethodPost, "/video/v1/generations", test.body)
		code, param := errorParam(t, w.Body.Bytes())
		if w.Code != 400 || code != "invalid_request" || param != test.param {
			t.Fatalf("%s: %d %s", test.name, w.Code, w.Body.String())
		}
	}
	r := httptest.NewRequest(http.MethodPost, "/video/v1/generations", strings.NewReader("{}"))
	r.ContentLength = 33 << 20
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer synthetic-key")
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	if w.Code != 413 {
		t.Fatalf("oversized body: %d", w.Code)
	}
	jobs, err := f.store.VideoJobEntries(context.Background(), nil, 10)
	if err != nil || len(jobs) != 0 || len(f.stub.createCalls()) != 0 {
		t.Fatalf("an invalid request created work: %+v %v", jobs, err)
	}
}

func TestVideoAccessIsDeniedUntilTheKeyIsListed(t *testing.T) {
	f := newVideoFixture(t, "one")
	ctx := context.Background()
	if err := f.service.DenyKey(ctx, "wire-key"); err != nil {
		t.Fatal(err)
	}
	for _, request := range [][2]string{{http.MethodPost, "/video/v1/generations"}, {http.MethodGet, "/video/v1/generations"}, {http.MethodGet, "/video/v1/generations/vid_x"}} {
		w := f.request(request[0], request[1], videoPinnedBody)
		if code, _ := errorParam(t, w.Body.Bytes()); w.Code != 403 || code != "video_access_denied" {
			t.Fatalf("%v: %d %s", request, w.Code, w.Body.String())
		}
	}
	r := httptest.NewRequest(http.MethodPost, "/video/v1/generations", strings.NewReader(videoPinnedBody))
	r.Header.Set("Content-Type", "application/json")
	noKey := httptest.NewRecorder()
	f.mux.ServeHTTP(noKey, r)
	if noKey.Code != 401 || len(f.stub.createCalls()) != 0 {
		t.Fatalf("a request without a key: %d", noKey.Code)
	}
	if err := f.service.AllowKey(ctx, domain.LocalProxyKeyID); err == nil {
		t.Fatal("the internal principal was allowed")
	}
	if err := f.service.AllowKey(ctx, "wire-key"); err != nil {
		t.Fatal(err)
	}
	key, err := f.store.GetAPIKey(ctx, "wire-key")
	if err != nil {
		t.Fatal(err)
	}
	key.IsActive = false
	if err := f.store.SaveAPIKey(ctx, key, time.Now()); err != nil {
		t.Fatal(err)
	}
	if w := f.request(http.MethodPost, "/video/v1/generations", videoPinnedBody); w.Code != 401 {
		t.Fatalf("an inactive listed key: %d", w.Code)
	}
}

func TestVideoPoolStaysOutOfTheAccountPool(t *testing.T) {
	f := newVideoFixture(t, "one", "two")
	ctx := context.Background()
	id := f.createJob(t)
	if w := f.request(http.MethodGet, "/video/v1/generations/"+id, ""); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	accounts, err := f.store.ListAccounts(ctx)
	if err != nil || len(accounts) != 1 || accounts[0].ID != "wire-account" {
		t.Fatalf("video accounts reached the account pool: %+v %v", accounts, err)
	}
	eligible, err := f.store.EligibleAccounts(ctx, "wire-key")
	if err != nil || len(eligible) != 1 || eligible[0].ID != "wire-account" {
		t.Fatalf("video accounts reached selection: %+v %v", eligible, err)
	}
	totals, err := f.store.UsageTotals(ctx, "wire-key", "")
	if err != nil || totals.RequestCount != 0 || totals.Usage.CostMicrodollars != 0 {
		t.Fatalf("a video job reached usage reports: %+v %v", totals, err)
	}
	pending, err := f.store.ListReservationsNeedingReconciliation(ctx, "", 10)
	if err != nil || len(pending) != 0 {
		t.Fatalf("a video job reached the ledger: %+v %v", pending, err)
	}
}

func TestVideoBalancesAcrossAccountsAndRespectsTheBound(t *testing.T) {
	f := newVideoFixture(t, "one", "two")
	for range 4 {
		f.createJob(t)
	}
	counts := map[string]int{}
	for _, call := range f.stub.createCalls() {
		counts[call.account]++
	}
	if counts["one"] != 2 || counts["two"] != 2 {
		t.Fatalf("jobs did not spread: %v", counts)
	}
	single := newVideoFixture(t, "solo")
	for range 4 {
		single.createJob(t)
	}
	w := single.request(http.MethodPost, "/video/v1/generations", videoPinnedBody)
	if code, _ := errorParam(t, w.Body.Bytes()); w.Code != 503 || code != "video_no_accounts" || len(single.stub.createCalls()) != 4 {
		t.Fatalf("a full account: %d %s calls=%d", w.Code, w.Body.String(), len(single.stub.createCalls()))
	}
}

func TestVideoRefusalBeforeExecutionMovesToAnotherAccount(t *testing.T) {
	t.Run("429 cools the account down", func(t *testing.T) {
		f := newVideoFixture(t, "one", "two")
		var limited atomic.Value
		f.stub.create = func(account string, index int) (int, string, http.Header) {
			if index == 0 {
				limited.Store(account)
			}
			if account == limited.Load() {
				return 429, `{"code":"resource-exhausted","error":"limit"}`, http.Header{"Retry-After": {"120"}}
			}
			return 200, fmt.Sprintf(`{"request_id":"upstream-%d"}`, index), nil
		}
		f.createJob(t)
		calls := f.stub.createCalls()
		if len(calls) != 2 || calls[0].account == calls[1].account || calls[1].body != videoPinnedBody {
			t.Fatalf("failover: %+v", calls)
		}
		first := limited.Load().(string)
		account := f.accountStatus(t, first)
		if account.CooldownUntil == nil || time.Until(*account.CooldownUntil) > 2*time.Minute || account.LastRefusalCode != "http_429" || account.Status != domain.VideoAccountActive {
			t.Fatalf("cooldown: %+v", account)
		}
		f.createJob(t)
		if calls = f.stub.createCalls(); len(calls) != 3 || calls[2].account == first {
			t.Fatalf("a cooling account was selected: %+v", calls)
		}
		var listed struct {
			Jobs []struct {
				Status string `json:"status"`
			} `json:"jobs"`
		}
		w := f.request(http.MethodGet, "/video/v1/generations", "")
		if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil || len(listed.Jobs) != 2 {
			t.Fatalf("a refused attempt reached the client: %s", w.Body.String())
		}
	})
	t.Run("403 needs the operator", func(t *testing.T) {
		f := newVideoFixture(t, "one", "two")
		var denied atomic.Value
		f.stub.create = func(account string, index int) (int, string, http.Header) {
			if index == 0 {
				denied.Store(account)
				return 403, `{"error":"The caller does not have permission"}`, nil
			}
			return 200, `{"request_id":"upstream-ok"}`, nil
		}
		f.createJob(t)
		if account := f.accountStatus(t, denied.Load().(string)); account.Status != domain.VideoAccountAccessDenied {
			t.Fatalf("status: %+v", account)
		}
	})
	t.Run("every account refuses", func(t *testing.T) {
		f := newVideoFixture(t, "one", "two")
		f.stub.create = func(string, int) (int, string, http.Header) {
			return 429, `{"error":"limit"}`, http.Header{"Retry-After": {"90"}}
		}
		w := f.request(http.MethodPost, "/video/v1/generations", videoPinnedBody)
		if code, _ := errorParam(t, w.Body.Bytes()); w.Code != 429 || code != "video_pool_exhausted" || w.Header().Get("Retry-After") == "" || len(f.stub.createCalls()) != 2 {
			t.Fatalf("exhausted: %d %s calls=%d", w.Code, w.Body.String(), len(f.stub.createCalls()))
		}
	})
}

func TestVideoAttemptThatMayHaveExecutedIsNotReplayed(t *testing.T) {
	f := newVideoFixture(t, "one", "two")
	f.stub.create = func(string, int) (int, string, http.Header) { return 500, `{"error":"boom"}`, nil }
	w := f.request(http.MethodPost, "/video/v1/generations", videoPinnedBody)
	if code, _ := errorParam(t, w.Body.Bytes()); w.Code != 502 || code != "video_upstream_error" || len(f.stub.createCalls()) != 1 {
		t.Fatalf("server error: %d %s calls=%d", w.Code, w.Body.String(), len(f.stub.createCalls()))
	}
	entries, err := f.store.VideoJobEntries(context.Background(), nil, 10)
	if err != nil || len(entries) != 1 || entries[0].Status != domain.VideoJobUnknown {
		t.Fatalf("entries: %+v %v", entries, err)
	}

	rejected := newVideoFixture(t, "one", "two")
	rejected.stub.create = func(string, int) (int, string, http.Header) {
		return 400, `{"code":"invalid-argument","error":"The prompt was rejected by moderation."}`, nil
	}
	reject := rejected.request(http.MethodPost, "/video/v1/generations", videoPinnedBody)
	if reject.Code != 400 || !strings.Contains(reject.Body.String(), "moderation") || len(rejected.stub.createCalls()) != 1 {
		t.Fatalf("client error: %d %s calls=%d", reject.Code, reject.Body.String(), len(rejected.stub.createCalls()))
	}
}

func TestVideoJobsBelongToTheirKeyAndAccount(t *testing.T) {
	f := newVideoFixture(t, "one", "two")
	ctx := context.Background()
	id := f.createJob(t)
	owner := f.stub.createCalls()[0].account
	if w := f.request(http.MethodGet, "/video/v1/generations/vid_missing", ""); w.Code != 404 {
		t.Fatalf("missing job: %d", w.Code)
	}
	other, err := f.store.GetAPIKey(ctx, "wire-key")
	if err != nil {
		t.Fatal(err)
	}
	other.ID, other.Name, other.KeyPrefix = "second-key", "second", "second"
	other.KeyHash = fmt.Sprintf("%x", sha256.Sum256([]byte("second-key")))
	if err := f.store.SaveAPIKey(ctx, other, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Status(ctx, "second-key", id); err == nil {
		t.Fatal("another key read this job")
	}
	if gets := f.stub.getCalls(); len(gets) != 0 {
		t.Fatalf("another key's read reached xAI: %v", gets)
	}
	if w := f.request(http.MethodGet, "/video/v1/generations/"+id, ""); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if gets := f.stub.getCalls(); len(gets) != 1 || gets[0] != owner {
		t.Fatalf("the job was read on another account: %v (owner %s)", gets, owner)
	}
	for range 3 {
		f.createJob(t)
	}
	first := f.request(http.MethodGet, "/video/v1/generations?limit=3", "")
	var page struct {
		Jobs []struct {
			ID string `json:"id"`
		} `json:"jobs"`
		Next *string `json:"next_cursor"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &page); err != nil || len(page.Jobs) != 3 || page.Next == nil {
		t.Fatalf("first page: %s", first.Body.String())
	}
	second := f.request(http.MethodGet, "/video/v1/generations?limit=3&cursor="+*page.Next, "")
	var rest struct {
		Jobs []struct {
			ID string `json:"id"`
		} `json:"jobs"`
		Next *string `json:"next_cursor"`
	}
	if err := json.Unmarshal(second.Body.Bytes(), &rest); err != nil || len(rest.Jobs) != 1 || rest.Next != nil || rest.Jobs[0].ID == page.Jobs[2].ID {
		t.Fatalf("second page: %s", second.Body.String())
	}
	if bad := f.request(http.MethodGet, "/video/v1/generations?cursor=%21", ""); bad.Code != 400 {
		t.Fatalf("bad cursor: %d", bad.Code)
	}
}

func TestVideoJobCostCountsOnceAcrossPollsAndSweep(t *testing.T) {
	f := newVideoFixture(t, "one")
	ctx := context.Background()
	id := f.createJob(t)
	var group sync.WaitGroup
	for range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			_, _ = f.service.Status(ctx, "wire-key", id)
		}()
	}
	group.Wait()
	summary, err := f.service.Summary(ctx)
	if err != nil || summary.Today.CostMicrodollars != 180000 || summary.Today.Done != 1 {
		t.Fatalf("cost was not counted once: %+v %v", summary.Today, err)
	}
}

func TestVideoSweepFinishesJobsNobodyPolls(t *testing.T) {
	f := newVideoFixture(t, "one", "two")
	ctx := context.Background()
	for range 2 {
		f.createJob(t)
	}
	var offset atomic.Int64
	f.service.ConfigureClock(func() time.Time { return time.Now().Add(time.Duration(offset.Load())) })
	offset.Store(int64(11 * time.Second))
	f.stub.get = func(_, id string) (int, string) {
		if strings.HasSuffix(id, "-1") {
			return 500, `{"error":"temporary"}`
		}
		return 200, `{"status":"failed"}`
	}
	if err := f.service.Sweep(ctx); err != nil {
		t.Fatalf("a temporary read failure failed the sweep: %v", err)
	}
	entries, err := f.store.VideoJobEntries(ctx, nil, 10)
	if err != nil || len(entries) != 2 {
		t.Fatalf("entries: %+v %v", entries, err)
	}
	statuses := map[domain.VideoJobStatus]int{}
	for _, entry := range entries {
		statuses[entry.Status]++
	}
	if statuses[domain.VideoJobPending] != 1 || statuses[domain.VideoJobFailed] != 1 {
		t.Fatalf("partial failure lost a result: %v", statuses)
	}
	for _, entry := range entries {
		if entry.CostMicrodollars != nil {
			t.Fatalf("a job without a result has a cost: %+v", entry)
		}
	}
	offset.Store(int64(2 * time.Hour))
	if err := f.service.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	entries, _ = f.store.VideoJobEntries(ctx, nil, 10)
	statuses = map[domain.VideoJobStatus]int{}
	for _, entry := range entries {
		statuses[entry.Status]++
	}
	if statuses[domain.VideoJobUnknown] != 1 {
		t.Fatalf("a stale pending job stayed: %v", statuses)
	}
}

func TestVideoTokensRefreshOnceAndRotate(t *testing.T) {
	f := newVideoFixture(t, "one")
	ctx := context.Background()
	if count := f.stub.refreshes["one"]; count != 1 {
		t.Fatalf("import refreshes once, got %d", count)
	}
	f.stub.mu.Lock()
	f.stub.reject401["one-1"] = true
	f.stub.mu.Unlock()
	var group sync.WaitGroup
	for range 3 {
		group.Add(1)
		go func() {
			defer group.Done()
			f.request(http.MethodPost, "/video/v1/generations", videoPinnedBody)
		}()
	}
	group.Wait()
	f.stub.mu.Lock()
	refreshes := f.stub.refreshes["one"]
	f.stub.mu.Unlock()
	if refreshes != 2 {
		t.Fatalf("concurrent 401s refreshed %d times", refreshes-1)
	}
	if calls := f.stub.createCalls(); len(calls) != 3 {
		t.Fatalf("repeat after refresh: %+v", calls)
	}

	f.stub.mu.Lock()
	f.stub.refused["one"] = true
	f.stub.reject401["one-2"] = true
	f.stub.mu.Unlock()
	w := f.request(http.MethodPost, "/video/v1/generations", videoPinnedBody)
	if w.Code != 429 && w.Code != 503 {
		t.Fatalf("a dead session: %d %s", w.Code, w.Body.String())
	}
	if account := f.accountStatus(t, "one"); account.Status != domain.VideoAccountReauthRequired {
		t.Fatalf("status: %+v", account)
	}
	if err := f.service.UpdateAccount(ctx, f.accountStatus(t, "one").ID, nil, videoStatusPtr(domain.VideoAccountActive)); err == nil {
		t.Fatal("resume brought back an account without a session")
	}
	f.stub.mu.Lock()
	f.stub.refused["one"] = false
	f.stub.mu.Unlock()
	if _, err := f.service.ImportAccount(ctx, videoAuthFile("one")); err != nil {
		t.Fatal(err)
	}
	accounts, err := f.service.ListAccounts(ctx)
	if err != nil || len(accounts) != 1 || accounts[0].Status != domain.VideoAccountActive {
		t.Fatalf("re-import of the same user: %+v %v", accounts, err)
	}
}

func videoStatusPtr(status domain.VideoAccountStatus) *domain.VideoAccountStatus { return &status }

func TestVideoAdminRoutes(t *testing.T) {
	f := newVideoFixture(t)
	vault, err := credentials.Open(filepath.Join(t.TempDir(), "admin-key"), true)
	if err != nil {
		t.Fatal(err)
	}
	api := httpapi.New(f.store, vault, httpapi.Config{BootstrapToken: "synthetic-bootstrap-token-0123456789abcdef"}, nil)
	api.ConfigureVideo(f.service)
	handler := api.Handler(f.mux, nil)
	var cookie *http.Cookie
	call := func(method, path, contentType string, body []byte) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://localhost:2455"+path, bytes.NewReader(body))
		r.RemoteAddr = "127.0.0.1:5678"
		if contentType != "" {
			r.Header.Set("Content-Type", contentType)
		}
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if w := call(http.MethodGet, "/api/video/accounts", "", nil); w.Code != 401 {
		t.Fatalf("unauthenticated admin: %d", w.Code)
	}
	setup := call(http.MethodPost, "/api/dashboard-auth/password/setup", "application/json", []byte(`{"password":"synthetic-test-password","bootstrapToken":"synthetic-bootstrap-token-0123456789abcdef"}`))
	if setup.Code != 200 {
		t.Fatalf("setup: %d", setup.Code)
	}
	cookie = setup.Result().Cookies()[0]
	upload := func(content []byte) *httptest.ResponseRecorder {
		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		part, _ := form.CreateFormFile("auth_json", "auth.json")
		_, _ = part.Write(content)
		_ = form.Close()
		return call(http.MethodPost, "/api/video/accounts/import", form.FormDataContentType(), body.Bytes())
	}
	for name, content := range map[string][]byte{
		"wrong issuer":  bytes.ReplaceAll(videoAuthFile("one"), []byte("https://auth.x.ai\","), []byte("https://evil.example.invalid\",")),
		"two entries":   []byte(`{"a":{},"b":{}}`),
		"not json":      []byte("synthetic-secret-text"),
		"missing token": []byte(`{"a":{"oidc_issuer":"https://auth.x.ai"}}`),
	} {
		w := upload(content)
		if code, _ := errorParam(t, w.Body.Bytes()); w.Code != 400 || code != "invalid_xai_auth" || strings.Contains(w.Body.String(), "synthetic-secret") {
			t.Fatalf("%s: %d %s", name, w.Code, w.Body.String())
		}
	}
	imported := upload(videoAuthFile("one"))
	if imported.Code != 200 || strings.Contains(imported.Body.String(), "access-") || strings.Contains(imported.Body.String(), "refresh") {
		t.Fatalf("import: %d %s", imported.Code, imported.Body.String())
	}
	var account struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(imported.Body.Bytes(), &account); err != nil || account.ID == "" {
		t.Fatal(imported.Body.String())
	}
	listed := call(http.MethodGet, "/api/video/accounts", "", nil)
	if listed.Code != 200 || !strings.Contains(listed.Body.String(), "one@example.invalid") || strings.Contains(listed.Body.String(), "access-") {
		t.Fatalf("list: %s", listed.Body.String())
	}
	if w := call(http.MethodPatch, "/api/video/accounts/"+account.ID, "application/json", []byte(`{"label":"Main","status":"paused"}`)); w.Code != 204 {
		t.Fatalf("patch: %d %s", w.Code, w.Body.String())
	}
	// A paused account leaves selection, and the key is on the list from the fixture.
	r := httptest.NewRequest(http.MethodPost, "/video/v1/generations", strings.NewReader(videoPinnedBody))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer synthetic-key")
	mounted := httptest.NewRecorder()
	handler.ServeHTTP(mounted, r)
	if mounted.Code != 503 {
		t.Fatalf("mounted route with a paused pool: %d %s", mounted.Code, mounted.Body.String())
	}
	if w := call(http.MethodPatch, "/api/video/accounts/"+account.ID, "application/json", []byte(`{"status":"active"}`)); w.Code != 204 {
		t.Fatalf("resume: %d", w.Code)
	}
	keys := call(http.MethodGet, "/api/video/keys", "", nil)
	if keys.Code != 200 || !strings.Contains(keys.Body.String(), `"wire-key"`) || strings.Contains(keys.Body.String(), domain.LocalProxyKeyID) {
		t.Fatalf("keys: %s", keys.Body.String())
	}
	if w := call(http.MethodDelete, "/api/video/keys/wire-key", "", nil); w.Code != 204 {
		t.Fatalf("deny: %d", w.Code)
	}
	if w := call(http.MethodPut, "/api/video/keys/wire-key", "", nil); w.Code != 204 {
		t.Fatalf("allow: %d", w.Code)
	}
	if w := call(http.MethodPut, "/api/video/keys/unknown", "", nil); w.Code != 404 {
		t.Fatalf("unknown key: %d", w.Code)
	}
	id := f.createJob(t)
	if w := f.request(http.MethodGet, "/video/v1/generations/"+id, ""); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	jobs := call(http.MethodGet, "/api/video/jobs?limit=10", "", nil)
	if jobs.Code != 200 || !strings.Contains(jobs.Body.String(), id) || strings.Contains(jobs.Body.String(), "vidgen") {
		t.Fatalf("jobs: %s", jobs.Body.String())
	}
	summary := call(http.MethodGet, "/api/video/summary", "", nil)
	var totals struct {
		Today struct {
			Done    int64   `json:"done"`
			CostUSD float64 `json:"costUsd"`
		} `json:"today"`
	}
	if err := json.Unmarshal(summary.Body.Bytes(), &totals); err != nil || totals.Today.Done != 1 || strconv.FormatFloat(totals.Today.CostUSD, 'f', 2, 64) != "0.18" {
		t.Fatalf("summary: %s", summary.Body.String())
	}
	second := f.createJob(t)
	if w := call(http.MethodDelete, "/api/video/accounts/"+account.ID, "", nil); w.Code != 204 {
		t.Fatalf("delete: %d", w.Code)
	}
	f.stub.mu.Lock()
	revoked := len(f.stub.revoked)
	f.stub.mu.Unlock()
	if revoked != 1 {
		t.Fatalf("delete did not revoke: %d", revoked)
	}
	job, err := f.store.GetVideoJob(context.Background(), second)
	if err != nil || job.Status != domain.VideoJobUnknown {
		t.Fatalf("a pending job of a deleted account: %+v %v", job, err)
	}
}
