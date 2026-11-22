package upstream

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// antigravityCapacityRetry is the retry hint of a capacity refusal that
	// names no delay.
	antigravityCapacityRetry = 5 * time.Second
	antigravityMaxRetryAfter = 7 * 24 * time.Hour
	// antigravityMessageLimit bounds the provider message kept in an error.
	antigravityMessageLimit = 512
	antigravityMaxActionURL = 2048
)

// googleRPCError is the Google RPC error object of Cloud Code answers and
// mid-stream error frames.
type googleRPCError struct {
	Code    int               `json:"code"`
	Message string            `json:"message"`
	Status  string            `json:"status"`
	Details []googleRPCDetail `json:"details"`
}

// googleRPCDetail holds the fields of google.rpc.ErrorInfo and RetryInfo.
type googleRPCDetail struct {
	Type       string            `json:"@type"`
	Reason     string            `json:"reason"`
	Metadata   map[string]string `json:"metadata"`
	RetryDelay string            `json:"retryDelay"`
}

// parseGoogleRPCError reads {"error":{...}} (or a one-element array of it, the
// shape of non-SSE stream errors); nil when the body is not one.
func parseGoogleRPCError(body []byte) *googleRPCError {
	type envelope struct {
		Error *googleRPCError `json:"error"`
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) != 0 && trimmed[0] == '[' {
		var list []envelope
		if json.Unmarshal(trimmed, &list) != nil || len(list) == 0 {
			return nil
		}
		return list[0].Error
	}
	var value envelope
	if json.Unmarshal(trimmed, &value) != nil {
		return nil
	}
	return value.Error
}

// classifyAntigravityError realizes the workflow step "classify Antigravity
// refusals" (R7, R8): the HTTP status (or a mid-stream error's code), the
// ErrorInfo reason and the retry hints become a stable error code with the
// provider's retry delay or the account's action link. rpc may be nil for a
// body that is not a Google RPC error. Only the short provider message is
// kept, never the body.
func classifyAntigravityError(status int, header http.Header, rpc *googleRPCError, now time.Time) *Error {
	var info googleRPCDetail
	detailed := false
	if rpc != nil {
		detailed = len(rpc.Details) != 0
		for _, detail := range rpc.Details {
			if detail.Reason != "" {
				info = detail
				break
			}
		}
	}
	failure := &Error{Code: fmt.Sprintf("http_%d", status), Status: status, Message: antigravityMessage(status, rpc)}
	if status == 0 {
		failure.Code = ErrorCodeUpstreamError
	}
	switch {
	case info.Reason == "MODEL_CAPACITY_EXHAUSTED" || status == http.StatusServiceUnavailable:
		failure.Code = ErrorCodeCapacityExhausted
		failure.RetryAfter = antigravityRetryAfter(rpc, info, header, now)
		if failure.RetryAfter == 0 {
			failure.RetryAfter = antigravityCapacityRetry
		}
	case status == http.StatusTooManyRequests:
		switch {
		case info.Reason == "QUOTA_EXHAUSTED" || info.Reason == "INSUFFICIENT_G1_CREDITS_BALANCE":
			failure.Code = ErrorCodeInsufficientQuota
		case info.Reason == "RATE_LIMIT_EXCEEDED" || detailed:
			failure.Code = ErrorCodeRateLimited
		default:
			// A bare 429 is Google's content filter, not an account signal.
			failure.Code = ErrorCodeContentRefused
			return failure
		}
		failure.RetryAfter = antigravityRetryAfter(rpc, info, header, now)
	case status == http.StatusForbidden:
		switch info.Reason {
		case "TOS_VIOLATION":
			failure.Code = ErrorCodeAccountSuspended
			failure.ActionURL = antigravityActionURL(info.Metadata["appeal_url"])
		case "VALIDATION_REQUIRED":
			failure.Code = ErrorCodeAccountValidationRequired
			failure.ActionURL = antigravityActionURL(info.Metadata["validation_url"])
		case "MODEL_ACCESS_DENIED":
			failure.Code = ErrorCodeModelAccessDenied
		}
	}
	return failure
}

func antigravityMessage(status int, rpc *googleRPCError) string {
	message := ""
	if rpc != nil {
		message = strings.Join(strings.Fields(rpc.Message), " ")
	}
	if len(message) > antigravityMessageLimit {
		cut := antigravityMessageLimit
		for cut > 0 && !utf8.RuneStart(message[cut]) {
			cut--
		}
		message = message[:cut] + "…"
	}
	if message == "" {
		message = http.StatusText(status)
	}
	if message == "" {
		message = "Antigravity request failed"
	}
	return message
}

// antigravityRetryAfter reads the retry hint in order: RetryInfo.retryDelay,
// ErrorInfo quotaResetDelay, quotaResetTimeStamp, the Retry-After header.
func antigravityRetryAfter(rpc *googleRPCError, info googleRPCDetail, header http.Header, now time.Time) time.Duration {
	bounded := func(delay time.Duration) time.Duration {
		return min(max(delay, 0), antigravityMaxRetryAfter)
	}
	if rpc != nil {
		for _, detail := range rpc.Details {
			if delay, err := time.ParseDuration(strings.TrimSpace(detail.RetryDelay)); err == nil && delay > 0 {
				return bounded(delay)
			}
		}
	}
	if delay, err := time.ParseDuration(strings.TrimSpace(info.Metadata["quotaResetDelay"])); err == nil && delay > 0 {
		return bounded(delay)
	}
	if reset, err := time.Parse(time.RFC3339, strings.TrimSpace(info.Metadata["quotaResetTimeStamp"])); err == nil && reset.After(now) {
		return bounded(reset.Sub(now))
	}
	if header != nil {
		value := strings.TrimSpace(header.Get("Retry-After"))
		if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds > 0 {
			return bounded(time.Duration(min(seconds, int64(antigravityMaxRetryAfter/time.Second))) * time.Second)
		}
		if at, err := http.ParseTime(value); err == nil && at.After(now) {
			return bounded(at.Sub(now))
		}
	}
	return 0
}

// antigravityActionURL keeps an appeal or verification link only when it is
// an absolute HTTPS URL, so the dashboard never renders another scheme.
func antigravityActionURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > antigravityMaxActionURL {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return ""
	}
	return parsed.String()
}
