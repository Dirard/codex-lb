package domain

import (
	"errors"
	"fmt"
	"math"
	"time"
)

var ErrInvalidLimit = ErrInvalid

func (w LimitWindow) Duration() (time.Duration, error) {
	switch w {
	case WindowDaily:
		return 24 * time.Hour, nil
	case WindowWeekly, WindowSevenDays:
		return 7 * 24 * time.Hour, nil
	case WindowMonthly:
		return 30 * 24 * time.Hour, nil
	case WindowFiveHours:
		return 5 * time.Hour, nil
	default:
		return 0, fmt.Errorf("window %q: %w", w, ErrInvalidLimit)
	}
}

func (r LimitRule) Validate() error {
	if r.MaxValue <= 0 {
		return fmt.Errorf("nonpositive maximum: %w", ErrInvalidLimit)
	}
	if _, err := r.Window.Duration(); err != nil {
		return err
	}
	switch r.Type {
	case LimitTotalTokens, LimitInputTokens, LimitOutputTokens, LimitCostUSD:
	case LimitCredits:
		if r.ModelFilter != nil {
			return fmt.Errorf("credits cannot have model filter: %w", ErrInvalidLimit)
		}
	default:
		return fmt.Errorf("type %q: %w", r.Type, ErrInvalidLimit)
	}
	if r.ModelFilter != nil && *r.ModelFilter == "" {
		return fmt.Errorf("empty model filter: %w", ErrInvalidLimit)
	}
	return nil
}

func (a UsageAmount) Validate() error {
	if a.InputTokens < 0 || a.OutputTokens < 0 || a.CachedInputTokens < 0 ||
		a.ReasoningTokens < 0 || a.CostMicrodollars < 0 || a.CachedInputTokens > a.InputTokens {
		return errors.New("invalid usage amounts")
	}
	if a.InputTokens > math.MaxInt64-a.OutputTokens {
		return errors.New("usage total overflows")
	}
	return nil
}

func (a UsageAmount) ForLimit(t LimitType) int64 {
	switch t {
	case LimitTotalTokens:
		return a.InputTokens + a.OutputTokens
	case LimitInputTokens:
		return a.InputTokens
	case LimitOutputTokens:
		return a.OutputTokens
	case LimitCostUSD:
		return a.CostMicrodollars
	default:
		return 0 // Legacy credits limits are display-only; reset-credit redemption is separate.
	}
}
