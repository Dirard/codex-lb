package provider

import (
	"context"
	"testing"

	"codex-lb/internal/adapters/egress"
	"codex-lb/internal/domain"
)

// Every model-source protocol is admitted through beginSource, so its context
// must carry the egress restriction enforced by the runtime dial guard.
func TestModelSourceContextsAreEgressRestricted(t *testing.T) {
	adapter := &Adapter{}
	ctx, release, err := adapter.beginSource(context.Background(), domain.ModelSource{ID: "src_test"})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if !egress.Restricted(ctx) {
		t.Fatal("model-source context is not restricted to public destinations")
	}
	if egress.Restricted(context.Background()) {
		t.Fatal("unmarked context reported as restricted")
	}
}
