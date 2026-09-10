package domain

// Model picker domain: semantic policies for workspace model assignment.
//
// Invariants enforced by this package:
//   I1: Model Picker never owns runtime or persistence authority (pure types).
//   I5: Effective model is derived exclusively from Runtime Authority.
//   I6: Model capabilities are truthful; unknown capabilities are never inferred.
//   I7: Provider wire semantics never leak into the core domain model.
//   I8: Workspace targets are semantic policies, not independent runtimes.

import (
	"fmt"
	"time"
)

// WorkspaceTarget is the semantic policy scope for a model assignment.
// It is NOT an independent runtime; the Runtime Authority owns execution.
type WorkspaceTarget string

const (
	WorkspaceAsk         WorkspaceTarget = "ask"
	WorkspaceInvestigate WorkspaceTarget = "investigate"
	WorkspacePlan        WorkspaceTarget = "plan"
	WorkspaceBuild       WorkspaceTarget = "build"
	WorkspaceReview      WorkspaceTarget = "review"
	WorkspaceNone        WorkspaceTarget = "none"
)

// AllWorkspaceTargets is the ordered assignable set (excludes none).
var AllWorkspaceTargets = []WorkspaceTarget{
	WorkspaceAsk,
	WorkspaceInvestigate,
	WorkspacePlan,
	WorkspaceBuild,
	WorkspaceReview,
}

// IsValid reports whether t is a known assignable target (excludes none).
func (t WorkspaceTarget) IsValid() bool {
	switch t {
	case WorkspaceAsk, WorkspaceInvestigate, WorkspacePlan, WorkspaceBuild, WorkspaceReview:
		return true
	default:
		return false
	}
}

// ModelRef is the core-domain model reference. Provider is an opaque label;
// no provider wire semantics (tiers, effort keys, budgets) live here (I7).
type ModelRef struct {
	ID       string
	Provider string
}

// ModelTransitionEvent records one assignment/activation transition.
// Assignment and activation are distinct state transitions (I2):
// Activated reports whether Target == current mode at commit time.
type ModelTransitionEvent struct {
	Target    WorkspaceTarget
	Previous  ModelRef
	Current   ModelRef
	Activated bool
	Timestamp time.Time
}

// ToTranscriptLog renders the system feedback log line for the transcript.
// Activated transitions log the active-mode switch; inactive ones log the
// binding update explicitly marked (Inactive) so the status bar contract
// (unchanged when target != active mode) stays truthful.
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

// ReasoningOption is one truthful, provider-declared reasoning grade.
type ReasoningOption struct {
	ID    string
	Label string
}

// ReasoningCapability is the truthful capability record for a model.
// Unknown capabilities are never inferred (I6): Supported=false means the
// model declares no reasoning support; Configurable=false means the
// provider manages reasoning without caller-selected grades.
type ReasoningCapability struct {
	Supported    bool
	Configurable bool
	Options      []ReasoningOption
}

// ReasoningPolicy is the workspace reasoning policy for an assignment.
// It carries only the selected option ID (or "default"); it never invents
// grades the capability record does not declare.
type ReasoningPolicy struct {
	Option string
}

// DefaultReasoningPolicy is the factory fallback (omit effort downstream).
const DefaultReasoningPolicy = "default"
