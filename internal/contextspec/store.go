package contextspec

import (
	"sync"
	"time"
)

// Store is the Control Plane's single-slot, CAS-guarded home of the committed
// ContextSpec. It is the only place a compiled candidate becomes authoritative
// context state.
type Store struct {
	mu   sync.Mutex
	spec *ContextSpec
}

// NewStore constructs an empty spec store.
func NewStore() *Store { return &Store{} }

// Current returns a deep copy of the committed spec, or nil when nothing has
// been committed yet.
func (s *Store) Current() *ContextSpec {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.spec.Clone()
}

// Commit applies optimistic concurrency: the candidate is accepted only when
// its base conversation revision still equals the current conversation
// revision. A stale candidate is discarded and never overwrites newer human
// state.
func (s *Store) Commit(candidate CompiledCandidate, currentConversationRevision uint64, now time.Time) (*ContextSpec, error) {
	if s == nil {
		return nil, ErrInvalidContext
	}
	if candidate.Spec.ConversationRevision != candidate.BaseConversationRevision {
		return nil, ErrInvalidContext
	}
	if candidate.BaseConversationRevision != currentConversationRevision {
		return nil, ErrStaleContextCandidate
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	nextRevision := uint64(1)
	if s.spec != nil {
		nextRevision = s.spec.SpecRevision + 1
	}
	committed := candidate.Spec.Clone()
	committed.SpecRevision = nextRevision
	if committed.CompiledAt.IsZero() {
		committed.CompiledAt = now
	}
	s.spec = committed
	return committed.Clone(), nil
}
