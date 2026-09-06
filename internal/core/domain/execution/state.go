package execution

import (
	"sync"
	"time"

	"github.com/PizenLabs/izen/internal/core/domain/occ"
)

// ExecutionState is the versioned execution and subprocess state. Every step
// execution and subprocess result advances its monotonic StateVersion by
// exactly one under OCC validation. Concurrent step completions against the
// same expected version result in exactly one committed result and N-1
// ErrStaleDependency rejections.
type ExecutionState struct {
	mu   sync.RWMutex
	gate occ.OCCGate
	occ.VersionedEntity
	steps map[string]string // stepID -> result
	order []string
}

// NewExecutionState creates an empty execution state at version 0.
func NewExecutionState() *ExecutionState {
	return &ExecutionState{
		steps: make(map[string]string),
		VersionedEntity: occ.VersionedEntity{
			Version:   0,
			UpdatedAt: time.Now(),
		},
	}
}

// Version returns the current committed version.
func (s *ExecutionState) Version() occ.StateVersion {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.VersionedEntity.Version
}

// RecordStep attempts to record a step result with OCC validation. The caller
// must supply the version it observed; on stale version the call fails with
// ErrStaleDependency and the state is unchanged.
func (s *ExecutionState) RecordStep(stepID string, result string, expected occ.StateVersion) (occ.StateVersion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	newVer, err := s.gate.ValidateAndAdvance(&s.VersionedEntity.Version, expected)
	if err != nil {
		return newVer, err
	}
	if _, exists := s.steps[stepID]; !exists {
		s.order = append(s.order, stepID)
	}
	s.steps[stepID] = result
	s.UpdatedAt = time.Now()
	return newVer, nil
}

// RecordStepForce records a step without OCC validation. It is used only by
// the authoritative execution pipeline when it owns the clock.
func (s *ExecutionState) RecordStepForce(stepID string, result string) occ.StateVersion {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.steps[stepID]; !exists {
		s.order = append(s.order, stepID)
	}
	s.steps[stepID] = result
	s.VersionedEntity.Version = s.VersionedEntity.Version.Next()
	s.UpdatedAt = time.Now()
	return s.VersionedEntity.Version
}

// Get returns the result for stepID, if any.
func (s *ExecutionState) Get(stepID string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.steps[stepID]
	return v, ok
}

// Steps returns the step IDs in insertion order.
func (s *ExecutionState) Steps() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, len(s.order))
	copy(out, s.order)
	return out
}

// Count returns the number of recorded steps.
func (s *ExecutionState) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.steps)
}

// ResetToBaseline rewinds versioning to the frame's baseline version and
// clears recorded steps. It is idempotent and used by CheckpointCoordinator.Rollback.
func (s *ExecutionState) ResetToBaseline(baseline occ.StateVersion) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.steps = make(map[string]string)
	s.order = nil
	s.VersionedEntity.Version = baseline
	s.UpdatedAt = time.Now()
}

// Clear removes all steps without resetting version.
func (s *ExecutionState) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.steps = make(map[string]string)
	s.order = nil
	s.UpdatedAt = time.Now()
}
