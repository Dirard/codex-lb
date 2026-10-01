package application

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type testPrelude struct {
	events []ResponseEvent
	closed bool
	err    error
}

func (p *testPrelude) Append(event ResponseEvent) error {
	p.events = append(p.events, event)
	return p.err
}
func (p *testPrelude) Replay(emit func(ResponseEvent) error) error {
	for _, event := range p.events {
		if err := emit(event); err != nil {
			return err
		}
	}
	return nil
}
func (p *testPrelude) Close() error { p.closed = true; p.events = nil; return nil }

func TestResponsePreludeSpillsAtMemoryBoundAndPreservesOrder(t *testing.T) {
	store := &testPrelude{}
	var seen []string
	output := newResponseOutput(func(event ResponseEvent) error { seen = append(seen, event.Type); return nil }, func() (ResponsePrelude, error) { return store, nil })
	defer output.close()
	small := ResponseEvent{Type: "response.created", Data: json.RawMessage(`{"type":"response.created"}`)}
	large := ResponseEvent{Type: "response.in_progress", Data: json.RawMessage(strings.Repeat("x", 96<<10))}
	if err := output.send(small); err != nil || output.spilled != nil || len(store.events) != 0 {
		t.Fatal("small events unnecessarily spilled", err)
	}
	if err := output.send(large); err != nil || output.preludeBytes != 0 || output.prelude != nil || len(store.events) != 2 || output.visible || len(seen) != 0 {
		t.Fatal("large prelude was lost, exposed early or retained in memory", err)
	}
	if err := output.send(ResponseEvent{Type: "response.completed", Data: json.RawMessage(`{}`)}); err != nil || len(seen) != 0 {
		t.Fatal("terminal event exposed before settlement", err)
	}
	if err := output.finish(); err != nil || strings.Join(seen, ",") != "response.created,response.in_progress,response.completed" || !store.closed {
		t.Fatal("replay order or cleanup changed", err)
	}
}

func TestResponsePreludeStorageAndDeliveryErrorsKeepTheirClassification(t *testing.T) {
	for _, failAppend := range []bool{false, true} {
		store := &testPrelude{}
		output := newResponseOutput(func(ResponseEvent) error { return nil }, func() (ResponsePrelude, error) {
			if !failAppend {
				return nil, errors.New("synthetic private storage path")
			}
			store.err = errors.New("synthetic write failure")
			return store, nil
		})
		if err := output.send(ResponseEvent{Type: "response.created", Data: make([]byte, 96<<10)}); !errors.Is(err, ErrResponsePreludeStorage) || output.visible {
			t.Fatal("storage failure was hidden or exposed as a valid prelude", err)
		}
		output.close()
		if failAppend && !store.closed {
			t.Fatal("failed spool leaked")
		}
	}
	store := &testPrelude{}
	failure := errors.New("synthetic delivery failure")
	output := newResponseOutput(func(ResponseEvent) error { return failure }, func() (ResponsePrelude, error) { return store, nil })
	if err := output.send(ResponseEvent{Type: "response.created", Data: make([]byte, 96<<10)}); err != nil {
		t.Fatal(err)
	}
	if err := output.send(ResponseEvent{Type: "response.output_text.delta", Data: json.RawMessage(`{}`)}); !errors.Is(err, failure) || !output.visible || !store.closed {
		t.Fatal("failed delivery lost classification or cleanup", err)
	}
}

func TestResponsePreludeStillBoundsEvents(t *testing.T) {
	output := newResponseOutput(func(ResponseEvent) error { return nil }, nil)
	for range MaxResponsePreludeEvents {
		if err := output.send(ResponseEvent{Type: "response.created", Data: json.RawMessage(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}
	var failure *ProxyError
	if err := output.send(ResponseEvent{Type: "response.in_progress", Data: json.RawMessage(`{}`)}); !errors.As(err, &failure) || failure.Code != "response_prelude_limit_exceeded" {
		t.Fatal("event bound lost", err)
	}
}
