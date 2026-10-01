package sqlite

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

func TestModelCatalogSnapshotRoundTrip(t *testing.T) {
	store, _ := testStore(t)
	ctx := context.Background()
	snapshot := &domain.CatalogSnapshot{
		Models: map[string]domain.CatalogModel{
			"gpt-live": {Slug: "gpt-live", DisplayName: "GPT Live", ContextWindow: 272000,
				Raw: map[string]json.RawMessage{"future": json.RawMessage(`{"kept":true}`)}},
		},
		ModelPlans:              map[string][]string{"gpt-live": {"pro"}},
		ModelAccounts:           map[string][]string{"gpt-live": {"acct-a"}},
		ModelTierAccounts:       map[string]map[string][]string{"gpt-live": {"priority": {"acct-a"}}},
		AccountPlans:            map[string]string{"acct-a": "pro"},
		FetchedAt:               fixedTime,
		AccountCatalogsComplete: true,
	}
	record := domain.ModelCatalogRecord{SchemaVersion: application.ModelCatalogSchemaVersion,
		RefreshedAt: fixedTime, ContentHash: "hash", Snapshot: snapshot}
	if err := store.SaveModelCatalogSnapshot(ctx, record); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LoadModelCatalogSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	model := loaded.Snapshot.Models["gpt-live"]
	if model.Slug != "gpt-live" || model.ContextWindow != 272000 ||
		string(model.Raw["future"]) != `{"kept":true}` ||
		!reflect.DeepEqual(loaded.Snapshot.ModelAccounts["gpt-live"], []string{"acct-a"}) ||
		!reflect.DeepEqual(loaded.Snapshot.ModelTierAccounts["gpt-live"]["priority"], []string{"acct-a"}) ||
		!loaded.Snapshot.AccountCatalogsComplete || !loaded.Snapshot.FetchedAt.Equal(snapshot.FetchedAt) {
		t.Fatalf("round trip changed snapshot: %#v", loaded.Snapshot)
	}
	record.Snapshot = nil
	record.ContentHash = "cleared"
	if err := store.SaveModelCatalogSnapshot(ctx, record); err != nil {
		t.Fatal(err)
	}
	loaded, err = store.LoadModelCatalogSnapshot(ctx)
	if err != nil || loaded.Snapshot != nil || loaded.ContentHash != "cleared" {
		t.Fatalf("cleared snapshot did not round trip: %#v %v", loaded, err)
	}
}
