package domain

import "fmt"

const (
	// DefaultLiveModel is the GPT-Live model a call gets when its session
	// names none; Codex Desktop uses it on the subscription.
	DefaultLiveModel = "gpt-live-1.1-codex"
	// LiveAlpha selects the frameless GPT-Live protocol on call creation and
	// on the v3 sideband.
	LiveAlpha = "quicksilver=v2"
	// liveMicrodollarsPerMinute is the public GPT-Live list price, $0.05 per
	// minute of session audio, billed by the millisecond.
	liveMicrodollarsPerMinute = 50_000
	// liveBudgetMinutes is how much of a call a key reserves before dispatch.
	liveBudgetMinutes = 10
)

// LiveUsage prices a GPT-Live call by its cumulative audio duration, as
// session.usage.updated and session.closed report it, rounded to the nearest
// microdollar. Live usage has no tokens.
func LiveUsage(audioDurationMS int64) (UsageAmount, error) {
	// Two weeks of audio bounds the multiplication far below overflow.
	if audioDurationMS < 0 || audioDurationMS > 14*24*3_600_000 {
		return UsageAmount{}, fmt.Errorf("%w: live audio duration out of range", ErrInvalid)
	}
	return UsageAmount{CostMicrodollars: (audioDurationMS*liveMicrodollarsPerMinute + 30_000) / 60_000}, nil
}

// LiveCallBudget is what a key reserves before a GPT-Live call is created:
// ten minutes at the Live price, plus one input and one output token so a
// token-limited key holds the call too; settlement returns them, since Live
// usage has no tokens.
func LiveCallBudget() UsageAmount {
	return UsageAmount{InputTokens: 1, OutputTokens: 1, CostMicrodollars: liveBudgetMinutes * liveMicrodollarsPerMinute}
}
