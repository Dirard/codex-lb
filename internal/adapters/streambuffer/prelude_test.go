package streambuffer

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"codex-lb/internal/adapters/credentials"
	"codex-lb/internal/application"
)

func TestPreludeEncryptedAnonymousReplayAndCleanup(t *testing.T) {
	dir := t.TempDir()
	vault, err := credentials.Open(filepath.Join(t.TempDir(), "key"), true)
	if err != nil {
		t.Fatal(err)
	}
	buffer, err := New(dir, vault)
	if err != nil {
		t.Fatal(err)
	}
	defer buffer.Close()
	p := buffer.(*prelude)
	file := p.file
	info, err := file.Stat()
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("temporary context storage is not private")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatal("temporary file was not unlinked immediately")
	}
	big, _ := json.Marshal(map[string]string{"context": string(bytes.Repeat([]byte("synthetic-private-context"), 8192))})
	want := []application.ResponseEvent{
		{Type: "response.created", Data: big},
		{Type: "response.in_progress", Data: json.RawMessage(`{"type":"response.in_progress"}`)},
	}
	for _, event := range want {
		if err := buffer.Append(event); err != nil {
			t.Fatal(err)
		}
	}
	disk := make([]byte, p.offset)
	if _, err := file.ReadAt(disk, 0); err != nil || bytes.Contains(disk, []byte("synthetic-private-context")) {
		t.Fatal("temporary disk payload is not encrypted")
	}
	var got []application.ResponseEvent
	if err := buffer.Replay(func(event application.ResponseEvent) error { got = append(got, event); return nil }); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatal("replay changed bytes or event order", err)
	}
	failure := errors.New("synthetic downstream failure")
	if err := buffer.Replay(func(application.ResponseEvent) error { return failure }); !errors.Is(err, failure) {
		t.Fatal("downstream failure lost")
	}
	if err := buffer.Append(application.ResponseEvent{Type: "response.created", Data: make([]byte, application.MaxResponsePreludeEventBytes+1)}); !errors.Is(err, application.ErrResponsePreludeStorage) {
		t.Fatal("oversized event not bounded")
	}
	for len(p.events) < application.MaxResponsePreludeEvents {
		if err := buffer.Append(want[1]); err != nil {
			t.Fatal(err)
		}
	}
	if err := buffer.Append(want[1]); err == nil {
		t.Fatal("unbounded event count")
	}
	if err := buffer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := buffer.Close(); err != nil || p.file != nil || p.events != nil {
		t.Fatal("cleanup is not idempotent")
	}
	if _, err := file.Stat(); err == nil {
		t.Fatal("temporary descriptor leaked")
	}
	if err := buffer.Replay(func(application.ResponseEvent) error { return nil }); !errors.Is(err, application.ErrResponsePreludeStorage) {
		t.Fatal("closed buffer remained readable")
	}
}

func TestPreludeStorageFailuresDoNotExposePathsOrPayloads(t *testing.T) {
	if _, err := New(filepath.Join(t.TempDir(), "missing"), nil); !errors.Is(err, application.ErrResponsePreludeStorage) {
		t.Fatal("invalid storage configuration not rejected")
	}
	vault, err := credentials.Open(filepath.Join(t.TempDir(), "key"), true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(filepath.Join(t.TempDir(), "missing"), vault); !errors.Is(err, application.ErrResponsePreludeStorage) {
		t.Fatal("create failure was not classified")
	}
	buffer, err := New(t.TempDir(), vault)
	if err != nil {
		t.Fatal(err)
	}
	defer buffer.Close()
	p := buffer.(*prelude)
	if err := buffer.Append(application.ResponseEvent{Type: "response.created", Data: json.RawMessage(`{"type":"response.created"}`)}); err != nil {
		t.Fatal(err)
	}
	if err := p.file.Truncate(0); err != nil {
		t.Fatal(err)
	}
	if err := buffer.Replay(func(application.ResponseEvent) error { t.Fatal("corrupt data exposed"); return nil }); !errors.Is(err, application.ErrResponsePreludeStorage) {
		t.Fatal("read failure was not classified")
	}
	_ = p.file.Close()
	if err := buffer.Append(application.ResponseEvent{Type: "response.created", Data: json.RawMessage(`{}`)}); !errors.Is(err, application.ErrResponsePreludeStorage) {
		t.Fatal("write failure was not classified")
	}
}
