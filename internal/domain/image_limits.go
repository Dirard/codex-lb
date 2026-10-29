package domain

import (
	"net/textproto"
	"strings"
)

// ImageLimitID reports whether an upstream X-Codex-Active-Limit identifier
// names the subscription's image generation limit ("image_gen",
// "imagegen_premium"), which is separate from the account's own quota.
func ImageLimitID(id string) bool {
	return strings.Contains(strings.ToLower(id), "image")
}

// ImageLimitHeaders selects the upstream headers that tell a client when an
// image limit resets: Retry-After, X-Codex-Active-Limit and every
// X-Image-Gen-* header, plus the X-Codex primary/secondary window headers when
// the active limit is an image limit. Plan and credit headers are never
// selected. Names are returned in canonical form.
func ImageLimitHeaders(headers map[string][]string) map[string][]string {
	imageLimit := false
	for name, values := range headers {
		if strings.EqualFold(name, "X-Codex-Active-Limit") && len(values) != 0 && ImageLimitID(values[0]) {
			imageLimit = true
		}
	}
	selected := map[string][]string{}
	for name, values := range headers {
		lower := strings.ToLower(name)
		keep := lower == "retry-after" || lower == "x-codex-active-limit" || strings.HasPrefix(lower, "x-image-gen-") ||
			imageLimit && (strings.HasPrefix(lower, "x-codex-primary-") || strings.HasPrefix(lower, "x-codex-secondary-"))
		if keep && len(values) != 0 {
			selected[textproto.CanonicalMIMEHeaderKey(name)] = append([]string(nil), values...)
		}
	}
	return selected
}
