package domain

import (
	"reflect"
	"testing"
)

func TestImageLimitRuleAndHeaders(t *testing.T) {
	for id, want := range map[string]bool{"image_gen": true, "imagegen_premium": true, "Image_Gen": true, "codex": false, "": false, "premium": false} {
		if ImageLimitID(id) != want {
			t.Fatalf("ImageLimitID(%q) != %v", id, want)
		}
	}
	upstream := map[string][]string{
		"Retry-After": {"3600"}, "X-Codex-Active-Limit": {"imagegen_premium"}, "X-Image-Gen-Primary-Reset-At": {"1793390082"},
		"X-Codex-Primary-Used-Percent": {"100"}, "X-Codex-Primary-Reset-At": {"1793390082"}, "X-Codex-Secondary-Window-Minutes": {"0"},
		"X-Codex-Plan-Type": {"pro"}, "X-Codex-Credits-Balance": {"7"}, "Content-Type": {"application/json"}, "x-image-gen-secondary-used-percent": {"5"},
	}
	want := map[string][]string{
		"Retry-After": {"3600"}, "X-Codex-Active-Limit": {"imagegen_premium"}, "X-Image-Gen-Primary-Reset-At": {"1793390082"},
		"X-Codex-Primary-Used-Percent": {"100"}, "X-Codex-Primary-Reset-At": {"1793390082"}, "X-Codex-Secondary-Window-Minutes": {"0"},
		"X-Image-Gen-Secondary-Used-Percent": {"5"},
	}
	if got := ImageLimitHeaders(upstream); !reflect.DeepEqual(got, want) {
		t.Fatalf("image-limit headers: %v", got)
	}
	upstream["X-Codex-Active-Limit"] = []string{"codex"}
	got := ImageLimitHeaders(upstream)
	if _, ok := got["X-Codex-Primary-Used-Percent"]; ok || got["X-Image-Gen-Primary-Reset-At"] == nil || got["Retry-After"] == nil {
		t.Fatalf("account windows passed for a non-image limit: %v", got)
	}
}
