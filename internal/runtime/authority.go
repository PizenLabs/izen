package runtime

// Runtime Authority: the single source of truth for the effective model (I5).
//
//   I1: Model Picker never owns runtime or persistence authority.
//   I2: Assignment and activation are distinct state transitions.
//   I3: Persistent assignment succeeds before runtime state is committed.
//   I4: Runtime commit is deterministic after successful preparation/persistence.
//   I8: Workspace targets are semantic policies, not independent runtimes.
//
// Assignment (persist) and activation (effective for current mode) are kept
// distinct: CommitTransition records Previous/Current plus Activated
// (Target == currentMode) without any business validation so it cannot fail.

import (
	"fmt"
	"strings"
	"sync"
	"time"

	coredomain "github.com/PizenLabs/izen/internal/core/domain"
)

// WorkspaceTarget aliases the core-domain semantic policy target so the
// runtime and the picker share one closed target schema (I8).
type WorkspaceTarget = coredomain.WorkspaceTarget

// Target aliases for call sites that prefer runtime-scoped names.
const (
	TargetAsk         = coredomain.WorkspaceAsk
	TargetInvestigate = coredomain.WorkspaceInvestigate
	TargetPlan        = coredomain.WorkspacePlan
	TargetBuild       = coredomain.WorkspaceBuild
	TargetReview      = coredomain.WorkspaceReview
	TargetNone        = coredomain.WorkspaceNone
)

// ModelRef aliases the core-domain model reference (I7: no wire semantics).
type ModelRef = coredomain.ModelRef

// ModelTransitionEvent aliases the core-domain transition event.
type ModelTransitionEvent = coredomain.ModelTransitionEvent

// PreparedTransition is the validated, committable assignment. It carries
// everything CommitTransition needs so the commit stays deterministic.
type PreparedTransition struct {
	Target   WorkspaceTarget
	Previous ModelRef
	Next     ModelRef
}

// RuntimeAuthority is the single source of truth for workspace model state.
// It owns no persistence: callers must persist before committing (I3).
type RuntimeAuthority struct {
	mu          sync.RWMutex
	assignments map[WorkspaceTarget]ModelRef
	currentMode WorkspaceTarget
}

// NewRuntimeAuthority builds an empty authority defaulting to ask mode.
func NewRuntimeAuthority() *RuntimeAuthority {
	return &RuntimeAuthority{
		assignments: make(map[WorkspaceTarget]ModelRef),
		currentMode: TargetAsk,
	}
}

// SetCurrentMode sets the active workspace mode (semantic policy, I8).
func (a *RuntimeAuthority) SetCurrentMode(m WorkspaceTarget) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if m == "" {
		m = TargetAsk
	}
	a.currentMode = m
}

// CurrentMode reports the active workspace mode.
func (a *RuntimeAuthority) CurrentMode() WorkspaceTarget {
	if a == nil {
		return TargetAsk
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.currentMode == "" {
		return TargetAsk
	}
	return a.currentMode
}

// EffectiveModel derives the model for target exclusively from runtime state (I5).
func (a *RuntimeAuthority) EffectiveModel(target WorkspaceTarget) ModelRef {
	if a == nil {
		return ModelRef{}
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.assignments[target]
}

// ActiveModel resolves EffectiveModel(currentMode) (I5).
func (a *RuntimeAuthority) ActiveModel() ModelRef {
	if a == nil {
		return ModelRef{}
	}
	return a.EffectiveModel(a.CurrentMode())
}

// SeedAssignment installs an assignment without validation (bootstrap only).
func (a *RuntimeAuthority) SeedAssignment(target WorkspaceTarget, ref ModelRef) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.assignments == nil {
		a.assignments = make(map[WorkspaceTarget]ModelRef)
	}
	a.assignments[target] = ref
}

// PrepareTransition validates an assignment without mutating runtime state.
// Errors: unknown target (TargetNone/empty/unknown) or empty model ID.
func (a *RuntimeAuthority) PrepareTransition(target WorkspaceTarget, next ModelRef) (PreparedTransition, error) {
	if a == nil {
		return PreparedTransition{}, fmt.Errorf("runtime: authority not initialized")
	}
	if !WorkspaceTarget(target).IsValid() {
		return PreparedTransition{}, fmt.Errorf("runtime: unknown workspace target: %q", string(target))
	}
	if strings.TrimSpace(next.ID) == "" {
		return PreparedTransition{}, fmt.Errorf("runtime: empty model id for target %q", string(target))
	}
	a.mu.RLock()
	prev := a.assignments[target]
	a.mu.RUnlock()
	return PreparedTransition{Target: target, Previous: prev, Next: next}, nil
}

// CommitTransition applies a prepared transition deterministically. It
// performs no business validation and never fails: preparation and
// persistence must already have succeeded (I3, I4). Activated is derived
// from Target == currentMode so assignment vs activation stay distinct (I2).
func (a *RuntimeAuthority) CommitTransition(prep PreparedTransition) ModelTransitionEvent {
	if a == nil {
		return ModelTransitionEvent{
			Target: prep.Target, Previous: prep.Previous, Current: prep.Next,
			Timestamp: time.Now(),
		}
	}
	a.mu.Lock()
	if a.assignments == nil {
		a.assignments = make(map[WorkspaceTarget]ModelRef)
	}
	a.assignments[prep.Target] = prep.Next
	mode := a.currentMode
	if mode == "" {
		mode = TargetAsk
	}
	a.mu.Unlock()
	return ModelTransitionEvent{
		Target:    prep.Target,
		Previous:  prep.Previous,
		Current:   prep.Next,
		Activated: prep.Target == mode,
		Timestamp: time.Now(),
	}
}
