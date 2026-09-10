package runtime

// Runtime Authority: the single source of truth for the effective model.
//
// The runtime authority owns ONE active executable binding (ProviderID +
// ModelID + VariantParams). Per-workspace assignment matrices are removed.
// The currentMode field is retained purely for UI display and does NOT
// participate in model resolution.
//
// Invariants:
//   - The active binding is the sole source of runtime model state.
//   - Persistence must succeed before runtime commit (callers enforce I3).
//   - ResolveForIntent is the single model-resolution entry point.

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/PizenLabs/izen/internal/runtime/authority"
)

// WorkspaceTarget is a semantic mode label retained for transcript logging
// and UI mode display. It is NOT a model assignment slot.
type WorkspaceTarget string

const (
	TargetAsk         WorkspaceTarget = "ask"
	TargetInvestigate WorkspaceTarget = "investigate"
	TargetPlan        WorkspaceTarget = "plan"
	TargetBuild       WorkspaceTarget = "build"
	TargetReview      WorkspaceTarget = "review"
	TargetNone        WorkspaceTarget = "none"
)

// ModelRef is a lightweight model reference for transcript events.
type ModelRef struct {
	ID       string
	Provider string
}

// ModelTransitionEvent records one model activation for transcript logging.
type ModelTransitionEvent struct {
	Target    WorkspaceTarget
	Previous  ModelRef
	Current   ModelRef
	Activated bool
	Timestamp time.Time
}

// ToTranscriptLog renders the system feedback log line for the transcript.
func (e ModelTransitionEvent) ToTranscriptLog() string {
	prev := e.Previous.ID
	if prev == "" {
		prev = "(unset)"
	}
	curr := e.Current.ID
	if curr == "" {
		curr = "(unset)"
	}
	if e.Activated {
		return fmt.Sprintf("[System] Active mode '%s' switched: %s -> %s", string(e.Target), prev, curr)
	}
	return fmt.Sprintf("[System] %s binding updated: %s (Inactive)", string(e.Target), curr)
}

// RuntimeAuthority is the single source of truth for the active model binding.
type RuntimeAuthority struct {
	mu          sync.RWMutex
	active      authority.ModelBinding
	policy      authority.ModelPolicy
	currentMode WorkspaceTarget
}

// NewRuntimeAuthority builds an empty authority.
func NewRuntimeAuthority() *RuntimeAuthority {
	return &RuntimeAuthority{
		currentMode: TargetAsk,
	}
}

// Activate atomically sets the active model binding.
func (a *RuntimeAuthority) Activate(binding authority.ModelBinding) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.active = binding
}

// ActiveBinding returns the current active model binding.
func (a *RuntimeAuthority) ActiveBinding() authority.ModelBinding {
	if a == nil {
		return authority.ModelBinding{}
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.active
}

// SetPolicy sets the role-based model policy (read-only configuration).
func (a *RuntimeAuthority) SetPolicy(policy authority.ModelPolicy) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.policy = policy
}

// Policy returns the current role-based model policy.
func (a *RuntimeAuthority) Policy() authority.ModelPolicy {
	if a == nil {
		return authority.ModelPolicy{}
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.policy
}

// ResolveForIntent resolves the model binding for a semantic intent using
// the stateless Policy Resolver. This is the single model-resolution entry
// point for all execution paths.
func (a *RuntimeAuthority) ResolveForIntent(intent string) (authority.ModelBinding, error) {
	if a == nil {
		return authority.ModelBinding{}, fmt.Errorf("authority: not initialized")
	}
	a.mu.RLock()
	runtime := authority.ModelState{
		ActiveProvider: a.active.ProviderID,
		ActiveModel:    a.active.ModelID,
		ActiveVariant:  a.active.VariantParams,
		IsConfigured:   a.active.ModelID != "",
	}
	policy := a.policy
	a.mu.RUnlock()
	return authority.ResolveModel(intent, runtime, policy)
}

// SetCurrentMode sets the active workspace mode (for UI display only).
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

// SeedBootstrap installs the active binding without validation (bootstrap only).
func (a *RuntimeAuthority) SeedBootstrap(binding authority.ModelBinding) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.active = binding
}

// --- Backward-compatible API (deprecated, used by callers not yet migrated) ---

// EffectiveModel returns the active model for any target. The target parameter
// is ignored: there is only one active binding.
func (a *RuntimeAuthority) EffectiveModel(_ WorkspaceTarget) ModelRef {
	if a == nil {
		return ModelRef{}
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return ModelRef{
		ID:       string(a.active.ModelID),
		Provider: string(a.active.ProviderID),
	}
}

// ActiveModel returns the active model (convenience alias for EffectiveModel).
func (a *RuntimeAuthority) ActiveModel() ModelRef {
	if a == nil {
		return ModelRef{}
	}
	return a.EffectiveModel(a.CurrentMode())
}

// SeedAssignment installs an assignment for the given target. The target is
// ignored for model resolution; the binding becomes the active binding.
func (a *RuntimeAuthority) SeedAssignment(_ WorkspaceTarget, ref ModelRef) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.active = authority.ModelBinding{
		ProviderID: authority.ProviderID(ref.Provider),
		ModelID:    authority.ModelID(ref.ID),
	}
}

// PreparedTransition is retained for backward compatibility with callers
// that have not yet migrated to Activate(). It validates the model ID
// without mutating runtime state.
type PreparedTransition struct {
	Target   WorkspaceTarget
	Previous ModelRef
	Next     ModelRef
}

// PrepareTransition validates an assignment without mutating runtime state.
func (a *RuntimeAuthority) PrepareTransition(target WorkspaceTarget, next ModelRef) (PreparedTransition, error) {
	if a == nil {
		return PreparedTransition{}, fmt.Errorf("runtime: authority not initialized")
	}
	if strings.TrimSpace(next.ID) == "" {
		return PreparedTransition{}, fmt.Errorf("runtime: empty model id for target %q", string(target))
	}
	a.mu.RLock()
	prev := ModelRef{
		ID:       string(a.active.ModelID),
		Provider: string(a.active.ProviderID),
	}
	a.mu.RUnlock()
	return PreparedTransition{Target: target, Previous: prev, Next: next}, nil
}

// CommitTransition applies a prepared transition deterministically.
// Under the single-binding model, the active binding is only updated when
// the target matches the current mode. Assignments to inactive targets are
// persisted but do not change the active binding.
func (a *RuntimeAuthority) CommitTransition(prep PreparedTransition) ModelTransitionEvent {
	if a == nil {
		return ModelTransitionEvent{
			Target: prep.Target, Previous: prep.Previous, Current: prep.Next,
			Timestamp: time.Now(),
		}
	}
	a.mu.Lock()
	prev := ModelRef{
		ID:       string(a.active.ModelID),
		Provider: string(a.active.ProviderID),
	}
	mode := a.currentMode
	if mode == "" {
		mode = TargetAsk
	}
	activated := prep.Target == mode
	if activated {
		a.active = authority.ModelBinding{
			ProviderID: authority.ProviderID(prep.Next.Provider),
			ModelID:    authority.ModelID(prep.Next.ID),
		}
	}
	a.mu.Unlock()
	return ModelTransitionEvent{
		Target:    prep.Target,
		Previous:  prev,
		Current:   prep.Next,
		Activated: activated,
		Timestamp: time.Now(),
	}
}
