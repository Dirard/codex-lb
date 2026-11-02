package domain

import (
	"fmt"
	"strings"
	"time"
)

// RealtimeUsage is the usage of one Realtime response as its response.done
// event reports it, with text, audio and image tokens apart.
type RealtimeUsage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	InputDetails struct {
		TextTokens    int64 `json:"text_tokens"`
		AudioTokens   int64 `json:"audio_tokens"`
		ImageTokens   int64 `json:"image_tokens"`
		CachedTokens  int64 `json:"cached_tokens"`
		CachedDetails struct {
			TextTokens  int64 `json:"text_tokens"`
			AudioTokens int64 `json:"audio_tokens"`
			ImageTokens int64 `json:"image_tokens"`
		} `json:"cached_tokens_details"`
	} `json:"input_token_details"`
	OutputDetails struct {
		TextTokens  int64 `json:"text_tokens"`
		AudioTokens int64 `json:"audio_tokens"`
	} `json:"output_token_details"`
}

// RealtimeClientSecret is a short-lived credential a browser app uses for
// Realtime in place of its API key; only its SHA-256 is kept.
type RealtimeClientSecret struct {
	Hash             string
	APIKeyID         string
	SessionEncrypted []byte // vault ciphertext of the session JSON, nil without one
	ExpiresAt        time.Time
	CreatedAt        time.Time
}

// realtimeRates are list prices in microdollars per million tokens.
type realtimeRates struct {
	TextIn, CachedText, TextOut, AudioIn, CachedAudio, AudioOut int64
}

var (
	realtimeFullRates = realtimeRates{TextIn: 4_000_000, CachedText: 400_000, TextOut: 16_000_000, AudioIn: 32_000_000, CachedAudio: 400_000, AudioOut: 64_000_000}
	realtimeMiniRates = realtimeRates{TextIn: 600_000, CachedText: 60_000, TextOut: 2_400_000, AudioIn: 10_000_000, CachedAudio: 300_000, AudioOut: 20_000_000}
)

// RealtimeSessionBudget is what an app session reserves before it starts:
// 20000 input and 10000 output tokens at the model's audio rates.
func RealtimeSessionBudget(model string) UsageAmount {
	rates := realtimeRatesFor(model)
	cost := ceilMicros(20_000*rates.AudioIn + 10_000*rates.AudioOut)
	return UsageAmount{InputTokens: 20_000, OutputTokens: 10_000, CostMicrodollars: cost}
}

func realtimeRatesFor(model string) realtimeRates {
	if strings.HasPrefix(model, "gpt-realtime-mini") {
		return realtimeMiniRates
	}
	return realtimeFullRates
}

// Amount turns one response's usage into ledger tokens and its cost at the
// model's realtime rates; cached tokens take the cached rate, image input
// the text input rate.
func (usage RealtimeUsage) Amount(model string) (UsageAmount, error) {
	in, cached := usage.InputDetails, usage.InputDetails.CachedDetails
	out := usage.OutputDetails
	counts := []int64{usage.InputTokens, usage.OutputTokens, in.TextTokens, in.AudioTokens, in.ImageTokens, in.CachedTokens,
		cached.TextTokens, cached.AudioTokens, cached.ImageTokens, out.TextTokens, out.AudioTokens}
	for _, count := range counts {
		if count < 0 || count > 1<<40 {
			return UsageAmount{}, fmt.Errorf("%w: realtime usage out of range", ErrInvalid)
		}
	}
	if cached.TextTokens > in.TextTokens || cached.AudioTokens > in.AudioTokens || cached.ImageTokens > in.ImageTokens ||
		in.CachedTokens > usage.InputTokens {
		return UsageAmount{}, fmt.Errorf("%w: realtime cached tokens exceed input", ErrInvalid)
	}
	rates := realtimeRatesFor(model)
	micros := (in.TextTokens-cached.TextTokens+in.ImageTokens-cached.ImageTokens)*rates.TextIn +
		(cached.TextTokens+cached.ImageTokens)*rates.CachedText +
		(in.AudioTokens-cached.AudioTokens)*rates.AudioIn + cached.AudioTokens*rates.CachedAudio +
		out.TextTokens*rates.TextOut + out.AudioTokens*rates.AudioOut
	return UsageAmount{
		InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens, CachedInputTokens: in.CachedTokens,
		CostMicrodollars: ceilMicros(micros),
	}, nil
}

// ceilMicros divides a rate-weighted token sum by one million, rounding up.
func ceilMicros(weighted int64) int64 {
	return (weighted + 999_999) / 1_000_000
}

// ValidRealtimeModel bounds the model a client names for an app session.
func ValidRealtimeModel(model string) bool {
	if model == "" || len(model) > 128 {
		return false
	}
	for _, char := range model {
		if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '.' || char == '_' || char == '-') {
			return false
		}
	}
	return true
}
