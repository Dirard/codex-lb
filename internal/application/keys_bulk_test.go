package application

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

// bulkFake records the write jobs; fail maps a key id to the error of its job.
type bulkFake struct {
	jobs    [][]string
	known   map[string]bool
	grouped map[string]bool
	fail    map[string]error
	moved   map[string]*string
	limits  map[string][]domain.LimitRule
	cancel  func(id string)
}

func newBulkFake(ids ...string) *bulkFake {
	fake := &bulkFake{known: map[string]bool{}, grouped: map[string]bool{}, fail: map[string]error{},
		moved: map[string]*string{}, limits: map[string][]domain.LimitRule{}}
	for _, id := range ids {
		fake.known[id] = true
	}
	return fake
}

func (f *bulkFake) SetAPIKeysActive(_ context.Context, ids []string, _ bool) ([]bool, error) {
	f.jobs = append(f.jobs, slices.Clone(ids))
	for _, id := range ids {
		if err := f.fail[id]; err != nil {
			return nil, err
		}
	}
	found := make([]bool, len(ids))
	for i, id := range ids {
		found[i] = f.known[id]
	}
	return found, nil
}

func (f *bulkFake) job(id string) error {
	f.jobs = append(f.jobs, []string{id})
	if f.cancel != nil {
		f.cancel(id)
	}
	if err := f.fail[id]; err != nil {
		return err
	}
	if !f.known[id] {
		return domain.ErrNotFound
	}
	return nil
}

func (f *bulkFake) MoveAPIKeyToGroup(_ context.Context, id string, group *string, _ time.Time) error {
	if err := f.job(id); err != nil {
		return err
	}
	f.moved[id] = group
	return nil
}

func (f *bulkFake) SetAPIKeyLimits(_ context.Context, id string, limits []domain.LimitRule, _ time.Time) error {
	if err := f.job(id); err != nil {
		return err
	}
	if f.grouped[id] {
		return domain.ErrKeyInGroup
	}
	f.limits[id] = limits
	return nil
}

func codes(results []domain.KeyBulkResult) []string {
	out := make([]string, len(results))
	for i, result := range results {
		out[i] = result.Code
		if result.OK {
			out[i] = "ok"
		}
	}
	return out
}

// A move that fails in the middle keeps the earlier keys moved, reports the
// failed key's code and still moves the later keys, one key per job, with
// results in request order (R36, R37).
func TestKeysBulkMoveFailsInTheMiddle(t *testing.T) {
	ids := []string{"k1", "k2", "k3", "k4", "k5"}
	fake := newBulkFake(ids...)
	fake.fail["k3"] = errors.New("database is locked")
	group := "grp-a"
	response, err := NewKeysBulkService(fake, nil).Apply(context.Background(),
		KeyBulkRequest{IDs: ids, Action: domain.KeyBulkMoveGroup, GroupID: &group})
	if err != nil {
		t.Fatal(err)
	}
	if got := codes(response.Results); !slices.Equal(got, []string{"ok", "ok", domain.KeyBulkInternal, "ok", "ok"}) {
		t.Fatalf("results %v", got)
	}
	for i, result := range response.Results {
		if result.ID != ids[i] {
			t.Fatalf("result %d is %s", i, result.ID)
		}
	}
	if len(fake.jobs) != 5 || len(fake.moved) != 4 || *fake.moved["k5"] != group || fake.moved["k3"] != nil {
		t.Fatalf("jobs %v moved %v", fake.jobs, fake.moved)
	}
}

// Disable and enable run in jobs of at most 25 keys; a failed job fails its
// keys only, and unknown ids fail with not_found in their position (R36, R37).
func TestKeysBulkDisableRunsInJobsOf25(t *testing.T) {
	var ids []string
	for i := range 60 {
		ids = append(ids, fmt.Sprintf("k%02d", i))
	}
	fake := newBulkFake(ids...)
	delete(fake.known, "k07")
	fake.fail["k30"] = domain.ErrConflict
	response, err := NewKeysBulkService(fake, nil).Apply(context.Background(), KeyBulkRequest{IDs: ids, Action: domain.KeyBulkDisable})
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.jobs) != 3 || len(fake.jobs[0]) != 25 || len(fake.jobs[1]) != 25 || len(fake.jobs[2]) != 10 {
		t.Fatalf("jobs %d: %v", len(fake.jobs), fake.jobs)
	}
	got := codes(response.Results)
	for i, code := range got {
		want := "ok"
		switch {
		case i == 7:
			want = domain.KeyBulkNotFound
		case i >= 25 && i < 50:
			want = domain.KeyBulkConflict
		}
		if code != want {
			t.Fatalf("key %d: %s, want %s", i, code, want)
		}
	}
}

// set_limits fails with key_in_group for a grouped key and applies the rules
// to the others (R38).
func TestKeysBulkSetLimitsRefusesGroupedKeys(t *testing.T) {
	fake := newBulkFake("solo", "team")
	fake.grouped["team"] = true
	rules := []domain.LimitRule{{Type: domain.LimitCostUSD, Window: domain.WindowDaily, MaxValue: 5_000_000}}
	response, err := NewKeysBulkService(fake, nil).Apply(context.Background(),
		KeyBulkRequest{IDs: []string{"team", "solo", "gone"}, Action: domain.KeyBulkSetLimits, Limits: rules})
	if err != nil {
		t.Fatal(err)
	}
	if got := codes(response.Results); !slices.Equal(got, []string{domain.KeyBulkKeyInGroup, "ok", domain.KeyBulkNotFound}) {
		t.Fatalf("results %v", got)
	}
	if len(fake.limits["solo"]) != 1 || fake.limits["team"] != nil {
		t.Fatalf("limits %v", fake.limits)
	}
}

// After the request is cancelled no further job runs and the remaining keys
// are reported not applied.
func TestKeysBulkStopsWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fake := newBulkFake("k1", "k2", "k3")
	fake.cancel = func(id string) {
		if id == "k2" {
			cancel()
		}
	}
	fake.fail["k2"] = context.Canceled
	response, err := NewKeysBulkService(fake, nil).Apply(ctx, KeyBulkRequest{IDs: []string{"k1", "k2", "k3"},
		Action: domain.KeyBulkMoveGroup})
	if err != nil {
		t.Fatal(err)
	}
	if got := codes(response.Results); !slices.Equal(got, []string{"ok", domain.KeyBulkNotApplied, domain.KeyBulkNotApplied}) || len(fake.jobs) != 2 {
		t.Fatalf("results %v, jobs %v", got, fake.jobs)
	}
}

// Invalid requests are refused before any job runs; invalid limit rules are
// told apart from other invalid bodies.
func TestKeysBulkRejectsInvalidRequests(t *testing.T) {
	fake := newBulkFake("k1")
	service := NewKeysBulkService(fake, nil)
	blank := ""
	many := make([]string, domain.KeyIDsLimit+1)
	for i := range many {
		many[i] = fmt.Sprintf("k%d", i)
	}
	for name, request := range map[string]KeyBulkRequest{
		"no ids":         {Action: domain.KeyBulkDisable},
		"too many ids":   {IDs: many, Action: domain.KeyBulkDisable},
		"repeated id":    {IDs: []string{"k1", "k1"}, Action: domain.KeyBulkEnable},
		"blank id":       {IDs: []string{""}, Action: domain.KeyBulkEnable},
		"unknown action": {IDs: []string{"k1"}, Action: "delete"},
		"blank group":    {IDs: []string{"k1"}, Action: domain.KeyBulkMoveGroup, GroupID: &blank},
		"no limits":      {IDs: []string{"k1"}, Action: domain.KeyBulkSetLimits},
	} {
		if _, err := service.Apply(context.Background(), request); !errors.Is(err, ErrBulkRequest) {
			t.Errorf("%s: %v", name, err)
		}
	}
	_, err := service.Apply(context.Background(), KeyBulkRequest{IDs: []string{"k1"}, Action: domain.KeyBulkSetLimits,
		Limits: []domain.LimitRule{{Type: domain.LimitCostUSD, Window: domain.WindowDaily, MaxValue: 0}}})
	if !errors.Is(err, ErrBulkLimits) || errors.Is(err, ErrBulkRequest) {
		t.Fatalf("invalid limits: %v", err)
	}
	if len(fake.jobs) != 0 {
		t.Fatalf("%d jobs for invalid requests", len(fake.jobs))
	}
}
