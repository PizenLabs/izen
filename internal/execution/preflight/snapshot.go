package preflight

import (
	"sync"
	"time"

	"github.com/PizenLabs/izen/internal/execution/planner"
)

// StructuralSnapshot is the async discovery result published to Observation
// State by BackgroundPreflight. It is immutable after publication.
type StructuralSnapshot struct {
	// Target is the workspace-relative file that was scanned.
	Target string
	// SHA256 is the hex digest of the file content at preflight time.
	SHA256 string
	// Scan is the read-only AST/DOM analysis (nil when format not scannable).
	Scan *planner.LeaScanReport
	// EstimatedTokens is the tokenizer/budget estimate for the target scope.
	EstimatedTokens int
	// BudgetTokens is the scope budget estimate (max_output-derived).
	BudgetTokens int
	// TotalLines is the file line count.
	TotalLines int
	// ReadyAt is the wall-clock completion time.
	ReadyAt time.Time
	// Err carries an unrecoverable IO/parse error when preflight failed.
	Err error
}

// ObservationState is the thread-safe store where BackgroundPreflight
// publishes StructuralSnapshot records. Consumers (the PreflightSyncBarrier
// and the state-machine loop) read from it without blocking on IO.
type ObservationState struct {
	mu        sync.RWMutex
	snapshots map[string]*StructuralSnapshot
}

// NewObservationState returns an empty state.
func NewObservationState() *ObservationState {
	return &ObservationState{snapshots: make(map[string]*StructuralSnapshot)}
}

// Publish stores a snapshot keyed by target.
func (s *ObservationState) Publish(snap *StructuralSnapshot) {
	if s == nil || snap == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.snapshots == nil {
		s.snapshots = make(map[string]*StructuralSnapshot)
	}
	s.snapshots[snap.Target] = snap
}

// Get returns the snapshot for target, or nil.
func (s *ObservationState) Get(target string) *StructuralSnapshot {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshots[target]
}

// All returns a copy of all snapshots.
func (s *ObservationState) All() map[string]*StructuralSnapshot {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]*StructuralSnapshot, len(s.snapshots))
	for k, v := range s.snapshots {
		out[k] = v
	}
	return out
}
