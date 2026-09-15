// Package orchestrator implements the Izen control-plane orchestrator. It
// enforces the deterministic control loop
//
//	User Intent -> Preflight -> LLM Proposal -> Validation -> Gate Arming
//	             -> Authorization -> Atomic Commit
//
// and guarantees fail-safe rollback: any failure during LLM execution,
// deterministic validation, or authorization rejection results in zero
// side-effects on the workspace filesystem.
package orchestrator

import (
	"context"
	"errors"

	"github.com/PizenLabs/izen/internal/core/domain"
	"github.com/PizenLabs/izen/internal/core/domain/evidence"
	"github.com/PizenLabs/izen/internal/presentation/diff"
	"github.com/PizenLabs/izen/internal/runtime/authorization"
	"github.com/PizenLabs/izen/internal/runtime/executor"
	"github.com/PizenLabs/izen/internal/runtime/preflight"
)

// Sentinel errors returned by the Orchestrator. Callers should test with
// errors.Is rather than comparing directly.
var (
	// ErrProposalValidationFailed is returned when a generated proposal fails
	// the deterministic validation stage.
	ErrProposalValidationFailed = errors.New("orchestrator: proposed mutation validation failed")
	// ErrExecutionRejected is returned when the authorization gate did not
	// grant execution because the user cancelled the proposal.
	ErrExecutionRejected = errors.New("orchestrator: execution rejected by authorization gate")
)

// ProposalProvider abstracts the non-deterministic LLM generation stage of the
// control loop. The returned proposal is strictly untrusted and must pass the
// deterministic validation stage before any authorization is attempted.
type ProposalProvider interface {
	GenerateProposal(ctx context.Context, req *preflight.CompiledRequest) (*executor.ProposedMutation, error)
}

// UIProjectionBridge handles rendering proposal diffs, signaling gate arming,
// and relaying the user's authorization decision back to the orchestrator.
//
// The bridge is the sole source of ApprovalEvents: after the diff has been
// rendered and the session armed, WaitForApproval blocks until the UI/PTY
// layer produces an explicit user decision.
type UIProjectionBridge interface {
	// RenderProposal projects the validated mutation evidence into the
	// terminal. It must complete before the gate is armed.
	RenderProposal(evidence diff.MutationEvidence, config diff.ViewportConfig) error
	// OnSessionArmed notifies the bridge that the approval session with the
	// given epoch has been armed and is eligible for event evaluation.
	OnSessionArmed(epoch authorization.InteractionEpoch)
	// WaitForApproval blocks until the user produces an explicit
	// authorization decision and returns it as an ApprovalEvent targeted at
	// the current epoch.
	WaitForApproval(ctx context.Context) (authorization.ApprovalEvent, error)
}

// FastPathAuthConfig carries the static authorization evidence for the
// pre-model fast-path gate. When nil, RunCycle enforces only lexical target
// safety before the provider call (backward-compatible). When non-nil, the
// full static gate (capabilities → targets → references → preflight) runs
// synchronously after preflight and drops unauthorized requests before any
// network call to the LLM provider. The execution boundary still enforces the
// 8-clause guard before mutation.
type FastPathAuthConfig struct {
	Capabilities        domain.DomainCapabilitySet
	Budget              domain.ResourceBudget
	Artifact            domain.ArtifactRef
	Objective           domain.Objective
	CheckpointID        domain.CheckpointID
	HasCheckpoint       bool
	HumanApproved       bool
	BudgetIsPreApproval bool
	SourceState         domain.SourceState
	// Provider/Model carry the active runtime binding for fail-fast
	// compatibility verification. Empty means unwired (legacy harness).
	Provider string
	Model    string
}

// OrchestratorConfig carries execution options for a single RunCycle.
type OrchestratorConfig struct {
	// TokenBudget is the context token budget applied to preflight when the
	// PreflightRequest does not specify one.
	TokenBudget int
	// ViewportConfig is the terminal geometry used to project the proposal
	// diff.
	ViewportConfig diff.ViewportConfig
	// FastPathAuth, when non-nil, enables the synchronous static
	// authorization gate before the provider call. Nil preserves legacy
	// behavior (lexical target safety only).
	FastPathAuth *FastPathAuthConfig
}

// ExecutionResult reports the outcome of one control-loop cycle.
//
// Terminal/Verdict/Completed form the Truthful State Transition product:
// audit persistence failure structurally invalidates success (Completed=false,
// Verdict != VerdictPassed) while the disk mutation is left intact
// (Mutation Non-Rollback Isolation; see EvidenceCompromised). AuditError wraps
// ErrAuditPersistenceFailed when the synchronous session-finalization flush
// failed.
type ExecutionResult struct {
	ProposalID string
	Target     string
	Action     authorization.ApprovalAction
	Committed  bool
	Evidence   diff.MutationEvidence
	// Terminal is the authoritative completion product bound to audit
	// durability. On audit flush failure it is Failed/FAIL/incomplete even
	// when Committed is true.
	Terminal evidence.TerminalState
	// Verdict is the terminal evidence classification (Passed/Failed/
	// Inconclusive). Audit failure forces Failed.
	Verdict evidence.EvidenceState
	// Completed mirrors Terminal.Completed: false when audit persistence
	// failed, regardless of mutation success.
	Completed bool
	// AuditError wraps ErrAuditPersistenceFailed on flush failure, else nil.
	AuditError error
	// EvidenceCompromised marks that the filesystem mutation stands on disk
	// but its audit evidence is compromised (failed flush, never rolled back).
	EvidenceCompromised bool
}
