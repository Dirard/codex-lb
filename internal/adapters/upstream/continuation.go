package upstream

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"sync"
	"time"
)

const (
	DefaultMaxContinuations     = 256
	DefaultMaxContinuationBytes = 64 << 20
	DefaultContinuationTTL      = 7 * 24 * time.Hour
)

type Continuation struct {
	Messages        []ChatMessage
	ReasoningByID   map[string]string
	ReasoningByTurn map[uint64]string
	// Signatures are opaque provider thought signatures of the turns in
	// Messages (Antigravity), keyed by antigravityCallSignatureKey or
	// antigravityTurnSignatureKey; they are replayed on the same part.
	Signatures map[string]string
}

type ContinuationStore struct {
	mu sync.Mutex

	entries map[continuationKey]continuationEntry
	lru     []continuationKey
	bytes   int
	// signatureOwners names, per owner and signature key, the latest saved
	// response whose continuation holds that signature. It lets a stateless
	// replay (full input, no previous_response_id) find signatures; entries
	// leave it with their continuation.
	signatureOwners map[signatureIndexKey]string

	maxEntries int
	maxBytes   int
	ttl        time.Duration
	now        func() time.Time
}

type continuationKey struct {
	owner      Owner
	responseID string
}

type signatureIndexKey struct {
	owner Owner
	key   string
}

type continuationEntry struct {
	value     Continuation
	lastUsed  time.Time
	sizeBytes int
}

func NewContinuationStore() *ContinuationStore {
	return NewContinuationStoreWithLimits(DefaultMaxContinuations, DefaultMaxContinuationBytes, DefaultContinuationTTL)
}

func NewContinuationStoreWithLimits(maxEntries int, maxBytes int, ttl time.Duration) *ContinuationStore {
	if maxEntries < 1 {
		maxEntries = 1
	}
	if maxBytes < 1 {
		maxBytes = 1
	}
	if ttl < time.Second {
		ttl = time.Second
	}
	return &ContinuationStore{
		entries:         make(map[continuationKey]continuationEntry),
		signatureOwners: make(map[signatureIndexKey]string),
		maxEntries:      maxEntries,
		maxBytes:        maxBytes,
		ttl:             ttl,
		now:             time.Now,
	}
}

func (s *ContinuationStore) Load(owner Owner, responseID string) (Continuation, bool, error) {
	if responseID == "" {
		return Continuation{}, false, &Error{Code: ErrorCodeInvalidRequest, Message: "previous_response_id is empty"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupLocked()

	key := continuationKey{owner: owner, responseID: responseID}
	entry, ok := s.entries[key]
	if !ok {
		return Continuation{}, false, nil
	}
	entry.lastUsed = s.now()
	s.entries[key] = entry
	s.touchLocked(key)
	return cloneContinuation(entry.value), true, nil
}

func (s *ContinuationStore) Save(owner Owner, responseID string, value Continuation) error {
	if responseID == "" {
		return &Error{Code: ErrorCodeInvalidRequest, Message: "cannot save an empty response ID"}
	}
	size, err := continuationSize(value)
	if err != nil {
		return &Error{Code: ErrorCodeContinuationTooLarge, Message: "continuation is not serializable"}
	}
	if size > s.maxBytes {
		return &Error{
			Code:    ErrorCodeContinuationTooLarge,
			Message: fmt.Sprintf("continuation is %d bytes, above %d byte limit", size, s.maxBytes),
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupLocked()

	key := continuationKey{owner: owner, responseID: responseID}
	if old, ok := s.entries[key]; ok {
		s.dropLocked(key, old)
		s.removeFromLRULocked(key)
	}

	s.entries[key] = continuationEntry{
		value: cloneContinuation(value), lastUsed: s.now(), sizeBytes: size,
	}
	s.lru = append(s.lru, key)
	s.bytes += size
	for signature := range value.Signatures {
		s.signatureOwners[signatureIndexKey{owner: owner, key: signature}] = responseID
	}

	for len(s.entries) > s.maxEntries {
		s.evictOldestLocked()
	}
	for s.bytes > s.maxBytes && len(s.entries) > 1 {
		s.evictOldestLocked()
	}
	return nil
}

// Signatures returns the stored thought signatures of owner for keys, each
// from the latest saved continuation that recorded it. Keys without one are
// absent from the result.
func (s *ContinuationStore) Signatures(owner Owner, keys []string) map[string]string {
	found := make(map[string]string)
	if len(keys) == 0 {
		return found
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupLocked()
	for _, key := range keys {
		responseID, ok := s.signatureOwners[signatureIndexKey{owner: owner, key: key}]
		if !ok {
			continue
		}
		if signature, ok := s.entries[continuationKey{owner: owner, responseID: responseID}].value.Signatures[key]; ok {
			found[key] = signature
		}
	}
	return found
}

func (s *ContinuationStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupLocked()
	return len(s.entries)
}

func (s *ContinuationStore) cleanupLocked() {
	cutoff := s.now().Add(-s.ttl)
	i := 0
	for i < len(s.lru) {
		key := s.lru[i]
		entry, ok := s.entries[key]
		if ok && entry.lastUsed.After(cutoff) {
			i++
			continue
		}
		if ok {
			s.dropLocked(key, entry)
		}
		s.lru = append(s.lru[:i], s.lru[i+1:]...)
	}
}

func (s *ContinuationStore) evictOldestLocked() {
	if len(s.lru) == 0 {
		return
	}
	key := s.lru[0]
	if entry, ok := s.entries[key]; ok {
		s.dropLocked(key, entry)
	}
	s.lru = s.lru[1:]
}

// dropLocked removes an entry and the signature index keys that still name it;
// the caller maintains the LRU list.
func (s *ContinuationStore) dropLocked(key continuationKey, entry continuationEntry) {
	s.bytes -= entry.sizeBytes
	delete(s.entries, key)
	for signature := range entry.value.Signatures {
		index := signatureIndexKey{owner: key.owner, key: signature}
		if s.signatureOwners[index] == key.responseID {
			delete(s.signatureOwners, index)
		}
	}
}

func (s *ContinuationStore) touchLocked(key continuationKey) {
	for i, existing := range s.lru {
		if existing == key {
			updated := append([]continuationKey(nil), s.lru[:i]...)
			updated = append(updated, key)
			updated = append(updated, s.lru[i+1:]...)
			s.lru = updated
			break
		}
	}
}

func (s *ContinuationStore) removeFromLRULocked(key continuationKey) {
	for i, existing := range s.lru {
		if existing == key {
			s.lru = append(s.lru[:i], s.lru[i+1:]...)
			return
		}
	}
}

func continuationSize(value Continuation) (int, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return 0, err
	}
	return len(encoded), nil
}

func cloneContinuation(value Continuation) Continuation {
	out := Continuation{
		Messages:        make([]ChatMessage, len(value.Messages)),
		ReasoningByID:   make(map[string]string, len(value.ReasoningByID)),
		ReasoningByTurn: make(map[uint64]string, len(value.ReasoningByTurn)),
		Signatures:      make(map[string]string, len(value.Signatures)),
	}
	for i, message := range value.Messages {
		message.Content = append(json.RawMessage(nil), message.Content...)
		message.ToolCalls = append([]ChatToolCall(nil), message.ToolCalls...)
		out.Messages[i] = message
	}
	for k, v := range value.ReasoningByID {
		out.ReasoningByID[k] = v
	}
	for k, v := range value.ReasoningByTurn {
		out.ReasoningByTurn[k] = v
	}
	for k, v := range value.Signatures {
		out.Signatures[k] = v
	}
	return out
}

func newID(prefix string) string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		// crypto/rand failure is unrecoverable in-process; never fall back to a
		// predictable response ID because it would collide continuation state.
		panic(errors.New("upstream: secure response ID generation failed: " + err.Error()))
	}
	return prefix + hex.EncodeToString(raw[:])
}

func turnReasoningKey(prior []ChatMessage, assistantContent string) uint64 {
	hash := fnv.New64a()
	for _, message := range prior {
		_, _ = hash.Write([]byte(message.Role))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(message.Content)
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(message.ToolCallID))
		_, _ = hash.Write([]byte{0})
		for _, call := range message.ToolCalls {
			_, _ = hash.Write([]byte(call.ID))
			_, _ = hash.Write([]byte(call.Function.Name))
			_, _ = hash.Write([]byte(call.Function.Arguments))
		}
		_, _ = hash.Write([]byte{0})
	}
	_, _ = hash.Write([]byte("assistant"))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(assistantContent))
	return hash.Sum64()
}
