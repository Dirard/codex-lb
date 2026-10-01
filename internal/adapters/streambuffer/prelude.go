// Package streambuffer provides request-owned encrypted temporary stream storage.
package streambuffer

import (
	"io"
	"os"

	"codex-lb/internal/application"
)

type storedEvent struct {
	kind   string
	offset int64
	size   int
}

type prelude struct {
	file   *os.File
	cipher application.SecretCipher
	events []storedEvent
	offset int64
}

// New unlinks the private file immediately: close, cancellation and process exit
// all release it. Only encrypted payloads are written, never plaintext context.
func New(directory string, cipher application.SecretCipher) (application.ResponsePrelude, error) {
	if cipher == nil {
		return nil, storageError()
	}
	file, err := os.CreateTemp(directory, ".response-prelude-")
	if err != nil {
		return nil, storageError()
	}
	if err := os.Remove(file.Name()); err != nil {
		_ = file.Close()
		return nil, storageError()
	}
	return &prelude{file: file, cipher: cipher}, nil
}

func (p *prelude) Append(event application.ResponseEvent) error {
	if p.file == nil || len(p.events) >= application.MaxResponsePreludeEvents ||
		len(event.Data) > application.MaxResponsePreludeEventBytes ||
		(event.Type != "response.created" && event.Type != "response.in_progress") {
		return storageError()
	}
	encrypted, err := p.cipher.Encrypt(event.Data)
	if err != nil || len(encrypted) > 2*application.MaxResponsePreludeEventBytes {
		return storageError()
	}
	n, err := p.file.Write(encrypted)
	if err != nil || n != len(encrypted) {
		return storageError()
	}
	p.events = append(p.events, storedEvent{event.Type, p.offset, n})
	p.offset += int64(n)
	return nil
}

func (p *prelude) Replay(emit func(application.ResponseEvent) error) error {
	if p.file == nil {
		return storageError()
	}
	for _, event := range p.events {
		encrypted := make([]byte, event.size)
		if _, err := io.ReadFull(io.NewSectionReader(p.file, event.offset, int64(event.size)), encrypted); err != nil {
			return storageError()
		}
		plain, err := p.cipher.Decrypt(encrypted)
		if err != nil || len(plain) > application.MaxResponsePreludeEventBytes {
			return storageError()
		}
		if err := emit(application.ResponseEvent{Type: event.kind, Data: plain}); err != nil {
			return err
		}
	}
	return nil
}

func (p *prelude) Close() error {
	if p.file == nil {
		return nil
	}
	err := p.file.Close()
	p.file, p.events = nil, nil
	return err
}

func storageError() error {
	return application.ErrResponsePreludeStorage
}
