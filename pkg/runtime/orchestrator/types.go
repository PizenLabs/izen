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

	"github.com/PizenLabs/izen/pkg/projection/diff"
	"github.com/PizenLabs/izen/pkg/runtime/authorization"
	"github.com/PizenLabs/izen/pkg/runtime/executor"
	"github.com/PizenLabs/izen/pkg/runtime/preflight"
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

// OrchestratorConfig carries execution options for a single RunCycle.
type OrchestratorConfig struct {
	// TokenBudget is the context token budget applied to preflight when the
	// PreflightRequest does not specify one.
	TokenBudget int
	// ViewportConfig is the terminal geometry used to project the proposal
	// diff.
	ViewportConfig diff.ViewportConfig
}

// ExecutionResult reports the outcome of one control-loop cycle.
type ExecutionResult struct {
	ProposalID string
	Target     string
	Action     authorization.ApprovalAction
	Committed  bool
	Evidence   diff.MutationEvidence
}
