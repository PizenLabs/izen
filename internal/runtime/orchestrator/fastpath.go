// Fast-path static gate seam for the control-plane orchestrator.
//
// This file owns Step 1b of RunCycle: the synchronous, deterministic,
// bounded fast-path gate that runs AFTER preflight and BEFORE any model
// invocation. It consolidates reference checks, target resolution, capability
// validation, and preflight checks into one pipeline stage with early exit,
// so unauthorized requests never burn LLM tokens or incur model latency.
//
// Staging (decoupled, never a mega-function):
//   - lexicalTargetSafety: always runs, O(N) on target depth, maps failures
//     to ErrProposalValidationFailed (legacy validation contract).
//   - fullStaticGate: runs only when cfg.FastPathAuth is set, evaluating the
//     executor.FastPathGate preflight stages and mapping failures to their
//     clause sentinels (unauthorized => ErrCapabilityDenied) while still
//     wrapping ErrProposalValidationFailed for observability.
//
// Authority invariant: this gate never grants authority. The 8-clause verdict
// remains owned by SimpleCapabilityGuard (via FastPathGate.Authorize at the
// execution boundary). This seam only denies early on static evidence.
package orchestrator

import (
	"context"
	"fmt"
	"strings"

	coreauth "github.com/PizenLabs/izen/internal/core/domain/authorization"
	"github.com/PizenLabs/izen/internal/runtime/executor"
	"github.com/PizenLabs/izen/internal/runtime/preflight"
)

// isProviderConfigDenial reports whether a fast-path denial reason carries
// the provider-configuration sentinel (fail-fast boundary, not a clause).
// The reason embeds ErrInvalidProviderConfiguration.Error() via %w wrapping,
// so substring match is the stable detector.
func isProviderConfigDenial(reason string) bool {
	if reason == "" {
		return false
	}
	return strings.Contains(reason, executor.ErrInvalidProviderConfiguration.Error())
}

// fastPathBeforeModel enforces Step 1b. It returns nil when the cycle may
// proceed to the provider, or a denial error when the request must be dropped
// before any network call.
func (o *Orchestrator) fastPathBeforeModel(ctx context.Context, req preflight.PreflightRequest, compiled *preflight.CompiledRequest, cfg OrchestratorConfig) error {
	if compiled == nil || compiled.TargetRef == nil {
		return nil
	}
	workDir := req.WorkDir
	if workDir == "" {
		workDir = "."
	}
	gate := executor.NewFastPathGate(nil, 0)

	// Lexical target safety always runs: normalization, containment, symlink
	// policy, bounded O(N) depth. Failure maps to the legacy validation
	// contract so pre-existing callers observe ErrProposalValidationFailed.
	lexIn := executor.FastPathInput{
		WorkDir:         workDir,
		RawTargets:      []string{compiled.TargetRef.Raw, compiled.TargetRef.Canonical},
		ProposalTargets: []string{compiled.TargetRef.Canonical},
	}
	if res := gate.ResolveTargets(lexIn); !res.Permitted {
		return fmt.Errorf("%w: fast-path target gate: %s", ErrProposalValidationFailed, res.Reason)
	}

	// Full static gate only when explicit static auth evidence is wired.
	// Nil preserves legacy behavior for callers without static context.
	if cfg.FastPathAuth == nil {
		return nil
	}
	auth := cfg.FastPathAuth
	in := executor.FastPathInput{
		WorkDir:             workDir,
		RawTargets:          []string{compiled.TargetRef.Raw},
		ProposalTargets:     []string{compiled.TargetRef.Canonical},
		Objective:           auth.Objective,
		Capabilities:        executor.ConstrainCapabilitiesForIntent(auth.Objective.Intent.Kind, auth.Capabilities),
		Budget:              auth.Budget,
		Artifact:            auth.Artifact,
		CheckpointID:        auth.CheckpointID,
		HasCheckpoint:       auth.HasCheckpoint,
		HumanApproved:       auth.HumanApproved,
		BudgetIsPreApproval: auth.BudgetIsPreApproval,
		SourceState:         auth.SourceState,
		ProposalDiffLines:   0,
		ProposalFiles:       1,
		Provider:            auth.Provider,
		Model:               auth.Model,
	}
	res := gate.EvaluateStaticPreflight(ctx, in)
	if res.Permitted {
		return nil
	}
	// Fail-fast provider boundary: preserve ErrInvalidProviderConfiguration
	// so errors.Is finds it immediately with zero timeout wait.
	if isProviderConfigDenial(res.Reason) {
		return fmt.Errorf("orchestrator: fast-path gate denied at stage %s: %w: %w: %s",
			res.Stage, ErrProposalValidationFailed, executor.ErrInvalidProviderConfiguration, res.Reason)
	}
	clauseErr := fastPathClauseSentinel(res.FailedClause)
	// Join preserves both the validation contract and the clause sentinel:
	// errors.Is finds ErrProposalValidationFailed AND (e.g.) ErrCapabilityDenied.
	return fmt.Errorf("orchestrator: fast-path gate denied at stage %s: %w: %w: %s",
		res.Stage, ErrProposalValidationFailed, clauseErr, res.Reason)
}

// fastPathClauseSentinel maps a failed clause to its core authorization sentinel.
func fastPathClauseSentinel(c coreauth.Clause) error {
	switch c {
	case coreauth.ClauseIntent:
		return coreauth.ErrInvalidIntent
	case coreauth.ClauseScope:
		return coreauth.ErrScopeViolation
	case coreauth.ClausePlan:
		return coreauth.ErrNoAuthorizedPlan
	case coreauth.ClauseCheckpoint:
		return coreauth.ErrNoCheckpoint
	case coreauth.ClauseSourceHash:
		return coreauth.ErrStaleDependency
	case coreauth.ClauseBudget:
		return coreauth.ErrBudgetExceeded
	case coreauth.ClauseCapability:
		return coreauth.ErrCapabilityDenied
	case coreauth.ClauseApproval:
		return coreauth.ErrApprovalRequired
	default:
		return fmt.Errorf("authorization denied")
	}
}
