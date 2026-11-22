package upstream

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

const (
	errorInfoType = "type.googleapis.com/google.rpc.ErrorInfo"
	retryInfoType = "type.googleapis.com/google.rpc.RetryInfo"
)

func TestClassifyAntigravityError(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name       string
		status     int
		header     http.Header
		body       string
		code       string
		retryAfter time.Duration
		actionURL  string
	}{
		{"quota exhausted", 429, nil, `{"error":{"code":429,"message":"Quota exhausted","status":"RESOURCE_EXHAUSTED","details":[
			{"@type":"` + errorInfoType + `","reason":"QUOTA_EXHAUSTED","domain":"cloudcode-pa.googleapis.com"},
			{"@type":"` + retryInfoType + `","retryDelay":"3600s"}]}}`, ErrorCodeInsufficientQuota, time.Hour, ""},
		{"credits exhausted", 429, nil, `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[
			{"@type":"` + errorInfoType + `","reason":"INSUFFICIENT_G1_CREDITS_BALANCE","metadata":{"quotaResetTimeStamp":"2026-11-22T12:30:00Z"}}]}}`,
			ErrorCodeInsufficientQuota, 30 * time.Minute, ""},
		{"rate limit", 429, nil, `{"error":{"code":429,"message":"You have exhausted your capacity on this model.","status":"RESOURCE_EXHAUSTED","details":[
			{"@type":"` + errorInfoType + `","reason":"RATE_LIMIT_EXCEEDED","metadata":{"model":"gemini-3.8-flash-high","quotaResetDelay":"479.417207ms"}}]}}`,
			ErrorCodeRateLimited, 479417207 * time.Nanosecond, ""},
		{"rate limit with header only", 429, http.Header{"Retry-After": {"7"}}, `{"error":{"code":429,"details":[
			{"@type":"` + errorInfoType + `","reason":"RATE_LIMIT_EXCEEDED"}]}}`, ErrorCodeRateLimited, 7 * time.Second, ""},
		{"bare 429", 429, http.Header{"Retry-After": {"30"}}, `{"error":{"code":429,"message":"Resource has been exhausted (e.g. check quota).","status":"RESOURCE_EXHAUSTED"}}`,
			ErrorCodeContentRefused, 0, ""},
		{"capacity", 503, nil, `{"error":{"code":503,"message":"No capacity available for model claude-sonnet-4-6","status":"UNAVAILABLE","details":[
			{"@type":"` + errorInfoType + `","reason":"MODEL_CAPACITY_EXHAUSTED"},{"@type":"` + retryInfoType + `","retryDelay":"12.5s"}]}}`,
			ErrorCodeCapacityExhausted, 12500 * time.Millisecond, ""},
		{"unavailable without delay", 503, nil, `{"error":{"code":503,"status":"UNAVAILABLE"}}`, ErrorCodeCapacityExhausted, antigravityCapacityRetry, ""},
		{"suspended", 403, nil, `{"error":{"code":403,"message":"This service has been disabled in this account for violation of Terms of Service.","status":"PERMISSION_DENIED","details":[
			{"@type":"` + errorInfoType + `","reason":"TOS_VIOLATION","metadata":{"appeal_url":"https://forms.gle/hGzM9MEUv2azZsrb9","appeal_url_link_text":"Appeal"}}]}}`,
			ErrorCodeAccountSuspended, 0, "https://forms.gle/hGzM9MEUv2azZsrb9"},
		{"suspended with unsafe link", 403, nil, `{"error":{"code":403,"status":"PERMISSION_DENIED","details":[
			{"@type":"` + errorInfoType + `","reason":"TOS_VIOLATION","metadata":{"appeal_url":"javascript:alert(1)"}}]}}`,
			ErrorCodeAccountSuspended, 0, ""},
		{"validation", 403, nil, `{"error":{"code":403,"status":"PERMISSION_DENIED","details":[
			{"@type":"` + errorInfoType + `","reason":"VALIDATION_REQUIRED","metadata":{"validation_url":"https://accounts.google.com/verify?x=1","validation_url_link_text":"Verify your account"}}]}}`,
			ErrorCodeAccountValidationRequired, 0, "https://accounts.google.com/verify?x=1"},
		{"model access", 403, nil, `{"error":{"code":403,"status":"PERMISSION_DENIED","details":[{"@type":"` + errorInfoType + `","reason":"MODEL_ACCESS_DENIED"}]}}`,
			ErrorCodeModelAccessDenied, 0, ""},
		{"other forbidden", 403, nil, `{"error":{"code":403,"status":"PERMISSION_DENIED"}}`, "http_403", 0, ""},
		{"unauthenticated", 401, nil, `{"error":{"code":401,"message":"Request had invalid authentication credentials.","status":"UNAUTHENTICATED"}}`, "http_401", 0, ""},
		{"invalid argument", 400, nil, `[{"error":{"code":400,"message":"Thinking level is unsupported","status":"INVALID_ARGUMENT"}}]`, "http_400", 0, ""},
		{"not JSON", 502, nil, `<html>bad gateway</html>`, "http_502", 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			failure := classifyAntigravityError(tc.status, tc.header, parseGoogleRPCError([]byte(tc.body)), now)
			if failure.Code != tc.code || failure.Status != tc.status || failure.RetryAfter != tc.retryAfter || failure.ActionURL != tc.actionURL {
				t.Fatalf("got code=%s status=%d retry=%s action=%q; want code=%s retry=%s action=%q",
					failure.Code, failure.Status, failure.RetryAfter, failure.ActionURL, tc.code, tc.retryAfter, tc.actionURL)
			}
			if strings.Contains(failure.Message, "{") || strings.Contains(failure.Message, "<html>") {
				t.Fatalf("message leaks the provider body: %q", failure.Message)
			}
		})
	}
}

func TestClassifyAntigravityErrorBoundsMessage(t *testing.T) {
	long := strings.Repeat("ё", antigravityMessageLimit)
	failure := classifyAntigravityError(400, nil, &googleRPCError{Code: 400, Message: long}, time.Now())
	if len(failure.Message) > antigravityMessageLimit+len("…") || !strings.HasSuffix(failure.Message, "…") {
		t.Fatalf("message was not bounded: %d bytes", len(failure.Message))
	}
	if failure := classifyAntigravityError(0, nil, &googleRPCError{Message: "boom"}, time.Now()); failure.Code != ErrorCodeUpstreamError {
		t.Fatalf("an error frame without a code = %s", failure.Code)
	}
}
