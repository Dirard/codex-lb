package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

func TestTranscriptionDurationUsageIsNotMalformedTokenUsage(t *testing.T) {
	for _, field := range []string{"seconds", "duration"} {
		t.Run(field, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"text":"transcript","usage":{%q:3}}`, field)
			}))
			defer upstream.Close()
			source := domain.ModelSource{ID: "audio", Kind: domain.ModelSourceOpenAICompatible,
				Enabled: true, Audio: true, BaseURL: upstream.URL,
				Models: []domain.ModelSourceModel{{Model: "transcribe", Enabled: true, AudioPerMinute: floatPointer(.06)}}}
			store := &sourceStore{source: source, credential: domain.AccountCredential{AccountID: source.ID, ExternalKeyEncrypted: []byte("enc:synthetic-audio")}}
			adapter := New(store, tokenSource{}, testCipher{}, Config{HTTPClient: upstream.Client()})
			defer adapter.Close()
			result, err := adapter.Transcribe(context.Background(), application.CodexOperationTarget{Account: source.Account(), KeyID: "key"},
				application.CodexTranscriptionRequest{Model: "transcribe", Audio: []byte("synthetic-audio"), Filename: "clip.wav"})
			if err != nil || result.Failed || result.Status != 200 || !result.AudioSecondsKnown || result.AudioSeconds != 3 {
				t.Fatalf("duration-only bill rejected: status=%d failed=%v seconds=%v known=%v err=%v", result.Status, result.Failed, result.AudioSeconds, result.AudioSecondsKnown, err)
			}
		})
	}
}
