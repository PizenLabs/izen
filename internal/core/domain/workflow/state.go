package workflow

import (
	"sync"
	"time"

	"github.com/PizenLabs/izen/internal/core/domain"
	"github.com/PizenLabs/izen/internal/core/domain/occ"
)

// VersionedWorkflowState is the versioned projection of the workflow state
// machine. It owns a monotonic StateVersion that is incremented on every
// committed state transition (Idle → Investigating → Planning → Building →
// Reviewing → …). The version clock is guarded by an OCCGate so concurrent
// transition attempts against the same expected version result in exactly one
// success and N-1 ErrStaleDependency rejections.
type VersionedWorkflowState struct {
	mu    sync.RWMutex
	gate  occ.OCCGate
	state domain.WorkflowState
	occ.VersionedEntity
}

// NewVersionedWorkflowState creates a workflow holder in the Idle state at
// version 0.
func NewVersionedWorkflowState() *VersionedWorkflowState {
	return &VersionedWorkflowState{
		state: domain.StateIdle,
		VersionedEntity: occ.VersionedEntity{
			Version:   0,
			UpdatedAt: time.Now(),
		},
	}
}

// State returns the current workflow state and its committed version.
func (v *VersionedWorkflowState) State() (domain.WorkflowState, occ.StateVersion) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.state, v.Version
}

// CurrentVersion returns the committed version without the state.
func (v *VersionedWorkflowState) CurrentVersion() occ.StateVersion {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.Version
}

// Transition attempts to move the workflow to next, requiring that the
// caller's expected version matches the current committed version. On success
// the version is advanced by one and UpdatedAt is refreshed. On stale
// expected version it returns ErrStaleDependency and the current version is
// unchanged.
func (v *VersionedWorkflowState) Transition(next domain.WorkflowState, expected occ.StateVersion) (occ.StateVersion, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	newVer, err := v.gate.ValidateAndAdvance(&v.Version, expected)
	if err != nil {
		return newVer, err
	}
	v.state = next
	v.UpdatedAt = time.Now()
	return newVer, nil
}

// ForceTransition advances the state without OCC validation. It is used only
// by the runtime's authoritative control plane when it owns the version clock
// exclusively (e.g. recovery). The version is still incremented monotonically.
func (v *VersionedWorkflowState) ForceTransition(next domain.WorkflowState) occ.StateVersion {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.state = next
	v.Version = v.Version.Next()
	v.UpdatedAt = time.Now()
	return v.Version
}
