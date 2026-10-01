package upstream

import (
	"net/http"
	"strings"
)

// websocketHTTPFallback qualifies only pre-create protocol/edge rejections.
// A generic permission error or transport failure is not evidence for a retry.
func websocketHTTPFallback(response *http.Response, body []byte) bool {
	if response.StatusCode == http.StatusUpgradeRequired {
		return true
	}
	if response.StatusCode != http.StatusForbidden {
		return false
	}
	for _, value := range response.Header.Values("Cf-Mitigated") {
		if strings.EqualFold(strings.TrimSpace(value), "challenge") {
			return true
		}
	}
	server := strings.ToLower(strings.Join(response.Header.Values("Server"), " "))
	contentType := strings.ToLower(strings.Join(response.Header.Values("Content-Type"), " "))
	if !strings.Contains(server, "cloudflare") || !strings.Contains(contentType, "html") {
		return false
	}
	text := strings.ToLower(string(body))
	for _, marker := range []string{"cf-chl-", "challenge-platform", "enable javascript and cookies", "just a moment"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}
