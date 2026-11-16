package httpapi

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

// Request pool headers follow OpenAI's request rate-limit headers.
const (
	headerLimitRequests     = "x-ratelimit-limit-requests"
	headerRemainingRequests = "x-ratelimit-remaining-requests"
	headerResetRequests     = "x-ratelimit-reset-requests"
	// A fork's share of its parent's pool, and its own cap.
	headerForkUsedRequests  = "x-ratelimit-fork-used-requests"
	headerForkLimitRequests = "x-ratelimit-fork-limit-requests"
)

// requestPoolExposedHeaders are the pool headers a browser page may read on
// a cross-origin answer.
const requestPoolExposedHeaders = "Retry-After, " + headerLimitRequests + ", " + headerRemainingRequests + ", " + headerResetRequests +
	", " + headerForkUsedRequests + ", " + headerForkLimitRequests

// requestPoolHeaders renders status with lower-case names; it is empty for a
// key without a pool. A refusal adds Retry-After.
func requestPoolHeaders(status domain.RequestPoolStatus, refused bool) map[string]string {
	if !status.Pooled() {
		return nil
	}
	headers := map[string]string{
		headerLimitRequests:     strconv.Itoa(status.Limit),
		headerRemainingRequests: strconv.Itoa(status.Remaining),
		// A Go duration, as OpenAI writes it: "850ms", "1s", "59.5s", "1m0s".
		headerResetRequests: max(status.Reset, 0).Round(time.Millisecond).String(),
	}
	if refused {
		headers["retry-after"] = strconv.FormatInt(status.RetryAfterSeconds(), 10)
	}
	if status.Fork {
		headers[headerForkUsedRequests] = strconv.Itoa(status.ForkUsed)
		if status.ForkLimit > 0 {
			headers[headerForkLimitRequests] = strconv.Itoa(status.ForkLimit)
		}
	}
	return headers
}

func setRequestPoolHeaders(header http.Header, status domain.RequestPoolStatus, refused bool) {
	for name, value := range requestPoolHeaders(status, refused) {
		header.Set(name, value)
	}
}

// admitRequestPool takes one slot of key's request pool for a model request
// and writes the pool headers. It waits while the pool queues the request
// and reports false after writing the refusal or cancellation. A key without
// a pool, or a handler without pools, is admitted unchanged.
func admitRequestPool(w http.ResponseWriter, r *http.Request, pools *application.RequestPools, key domain.APIKey) bool {
	if pools == nil || !key.HasRequestPool() {
		return true
	}
	status, err := pools.Admit(r.Context(), key)
	if err != nil {
		writeProxyError(w, err)
		return false
	}
	setRequestPoolHeaders(w.Header(), status, false)
	return true
}

// writeRequestPoolRefusal answers 429 in OpenAI's rate-limit shape; Codex
// reads "Please try again in Ns." as its retry delay.
func writeRequestPoolRefusal(w http.ResponseWriter, status domain.RequestPoolStatus) {
	setRequestPoolHeaders(w.Header(), status, true)
	writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": requestPoolRefusalDetail(status)})
}

func requestPoolRefusalDetail(status domain.RequestPoolStatus) map[string]any {
	return map[string]any{"type": "requests", "code": "rate_limit_exceeded", "param": nil, "message": requestPoolRefusalMessage(status)}
}

func requestPoolRefusalMessage(status domain.RequestPoolStatus) string {
	return fmt.Sprintf("Request pool limit reached for this API key: %d requests per minute. Please try again in %ds.",
		status.Limit, status.RetryAfterSeconds())
}
