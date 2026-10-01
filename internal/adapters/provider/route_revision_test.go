package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

type editedAfterSourceRead struct{ *sourceStore }

func (s *editedAfterSourceRead) GetModelSource(ctx context.Context, id string) (domain.ModelSource, error) {
	source, err := s.sourceStore.GetModelSource(ctx, id)
	s.credential.RouteRevision++
	s.credential.ExternalKeyEncrypted = []byte("enc:new-key")
	return source, err
}

func TestRouteEditBetweenSourceAndCredentialReadDoesNotDispatch(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer server.Close()
	source := domain.ModelSource{ID: "source", Name: "Source", Kind: domain.ModelSourceOpenAICompatible,
		BaseURL: server.URL + "/v1", Enabled: true, Responses: true,
		Models: []domain.ModelSourceModel{{Model: "model", Enabled: true}}}
	store := &editedAfterSourceRead{&sourceStore{source: source,
		credential: domain.AccountCredential{AccountID: source.ID, ExternalKeyEncrypted: []byte("enc:old-key")}}}
	adapter := New(store, tokenSource{}, testCipher{}, Config{HTTPClient: server.Client()})
	defer adapter.Close()
	_, err := adapter.Respond(context.Background(), application.ResponseTarget{Account: source.Account(), KeyID: "key"},
		[]byte(`{"model":"model","input":"hi"}`), nil)
	if err == nil || calls.Load() != 0 {
		t.Fatalf("new credential reached old endpoint: err=%v calls=%d", err, calls.Load())
	}
}
