package upstream

import "testing"

func TestVirtualContinuationDoesNotCrossRouteRevision(t *testing.T) {
	store := NewContinuationStore()
	old := Owner{ProviderID: "openai_compatible", AccountID: "source", KeyID: "key"}
	if err := store.Save(old, "response", Continuation{Messages: []ChatMessage{{Role: "user", Content: mustJSON("old route")}}}); err != nil {
		t.Fatal(err)
	}
	for _, revision := range []int64{1, 2} {
		current := old
		current.RouteRevision = revision
		if _, found, err := store.Load(current, "response"); err != nil || found {
			t.Fatalf("old virtual history crossed route revision %d: found=%v err=%v", revision, found, err)
		}
	}
}
