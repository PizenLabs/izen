// Package autonomy binds the Phase 4 autonomous RuntimeLoop to the real
// RuntimeExecutor at the composition boundary. It is the ONLY package that may
// reach both the loop contract (internal/autonomy, which must stay
// execution-free) and the execution authority (internal/execution): the
// ExecutorAdapter below is the sole surface through which the loop reaches
// execution, and the Driver is the sole bounded loop control flow.
//
// The loop is a CONSUMER of the RuntimeExecutor — never an executor itself. It
// resolves targets through the IntentGateway (Strategy.Select), submits
// ExecuteRequests through the RuntimeExecutor, and forwards approval decisions
// through RuntimeExecutor.Approve/Reject. It never reads a file, never invokes
// a provider, and never mutates the filesystem directly.
package autonomy

import (
	"context"

	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/execution/strategy"
)

// Resolved is the deterministic target resolution of one objective. Target
// resolution is the gateway's authority (Strategy.Select); the loop never
// guesses a target or a workspace.
type Resolved struct {
	// Prompt is the objective the resolution was computed for.
	Prompt string
	// Profile is the unconditionally selected execution strategy profile.
	Profile strategy.ExecutionStrategyProfile
	// Targets is the resolved workspace-relative target set (empty when the
	// strategy is read-only or clarification).
	Targets []string
	// Options is the candidate surface for a clarification boundary (the raw
	// target tokens the human must disambiguate). Empty when not ambiguous.
	Options []string
	// Ambiguous reports whether the strategy demanded human clarification
	// before any execution.
	Ambiguous bool
}

// ExecutorAdapter binds the loop's Executor port (and the approval/resolution
// surfaces) to the RuntimeExecutor + IntentGateway. It maps LoopRequest onto
// ExecuteRequest, maps the canonical ExecutionResult onto a bounded
// Observation, and forwards approvals through RuntimeExecutor.Approve/Reject.
type ExecutorAdapter struct {
	root     string
	gateway  *execution.IntentGateway
	executor *execution.RuntimeExecutor
}

// NewExecutorAdapter wires the adapter over the unified IntentGateway and the
// RuntimeExecutor authority. Both MUST be non-nil.
func NewExecutorAdapter(root string, gateway *execution.IntentGateway, executor *execution.RuntimeExecutor) *ExecutorAdapter {
	return &ExecutorAdapter{root: root, gateway: gateway, executor: executor}
}

// Resolve determines the execution target set for an objective WITHOUT
// executing. It surfaces HumanClarification as an ambiguous resolution so the
// driver parks before any model call or mutation.
func (a *ExecutorAdapter) Resolve(prompt string) Resolved {
	profile := a.gateway.SelectStrategy(prompt)
	res := Resolved{Prompt: prompt, Profile: profile}
	if profile.Strategy == strategy.HumanClarification {
		// A clarification NEVER leaks a target set: the loop must park, not
		// execute. The raw candidate tokens surface as human options.
		res.Ambiguous = true
		for _, t := range profile.Targets {
			if t.Raw != "" {
				res.Options = append(res.Options, t.Raw)
			}
		}
		return res
	}
	for _, t := range profile.Targets {
		if t.Resolved != "" {
			res.Targets = append(res.Targets, t.Resolved)
		}
	}
	return res
}

// Execute implements autonomy.Executor. It maps a LoopRequest onto the
// RuntimeExecutor's canonical ExecuteRequest and maps the resulting
// ExecutionResult onto a bounded Observation. The executor is the single
// authority that invokes the provider, produces the patch, holds the approval
// and runs verification; the adapter only translates.
func (a *ExecutorAdapter) Execute(ctx context.Context, req autonomy.LoopRequest) (autonomy.Observation, error) {
	profile := a.gateway.SelectStrategy(req.Prompt)
	strategyPtr := &profile
	if (len(req.Targets) > 0 || req.Target != "") && profile.Strategy == strategy.HumanClarification {
		// The loop carries a resolved target set the raw prompt could not
		// resolve (e.g. human-specified after clarification). Hand the explicit
		// target to the executor's canonical explicit-target path instead of
		// re-asking for clarification.
		strategyPtr = nil
	}
	execReq := execution.ExecuteRequest{
		RequestID:        req.RequestID,
		Mode:             "autonomy",
		Prompt:           req.Prompt,
		Target:           req.Target,
		Targets:          req.Targets,
		Strategy:         strategyPtr,
		Intent:           req.Intent,
		IntentConfidence: req.IntentConfidence,
		TargetConfidence: req.TargetConfidence,
		Scope:            req.Scope,
		Evidence:         req.Evidence,
		// The strategy-selected output ceiling is a REQUEST budget, not a
		// reporting change: max_tokens bounds the provider's generation so a
		// verbose reasoning model cannot spend an unbounded output budget, and
		// whatever the provider does bill is reported verbatim (finalizeResult
		// preserves the authoritative usage). The /build path already carries
		// this bound via the intent gateway; the autonomous path must apply the
		// same strategy-owned bound or it runs unbounded (the 5,883-token
		// repro: max_tokens was omitted because req.MaxOutputTokens stayed 0).
		MaxOutputTokens: profile.MaxOutputTokens,
	}
	res, err := a.executor.Execute(ctx, execReq)
	if err != nil && res == nil {
		return autonomy.Observation{}, err
	}
	return a.observe(req, res), nil
}

// Approve resolves an approval gate held by the executor and returns the
// terminal observation of the SAME execution. It never re-executes the
// mutation: the held patch was already produced, approval applies it.
//
// The executor returns a NON-NIL terminal result even when the apply/verify
// fails (the result carries the real failure outcome). That result must flow
// back to the loop so a failed approval converges to a terminal state — a hard
// error is ONLY returned when no result exists at all (e.g. double-approve of
// an unknown patch id).
func (a *ExecutorAdapter) Approve(ctx context.Context, patchID string) (autonomy.Observation, error) {
	res, err := a.executor.Approve(ctx, patchID)
	if err != nil && res == nil {
		return autonomy.Observation{}, err
	}
	return a.observe(autonomy.LoopRequest{RequestID: res.RequestID}, res), nil
}

// Reject rejects a held patch at the approval gate and returns the terminal
// observation of the SAME execution.
func (a *ExecutorAdapter) Reject(ctx context.Context, patchID, reason string) (autonomy.Observation, error) {
	res, err := a.executor.Reject(ctx, patchID, reason)
	if err != nil && res == nil {
		return autonomy.Observation{}, err
	}
	return a.observe(autonomy.LoopRequest{RequestID: res.RequestID}, res), nil
}

// observe maps the canonical ExecutionResult onto a bounded Observation. The
// outcome vocabulary mirrors the canonical MutationOutcome strings one to one,
// so the mapping is a lossless projection — the loop never reclassifies an
// execution fact.
func (a *ExecutorAdapter) observe(req autonomy.LoopRequest, res *execution.ExecutionResult) autonomy.Observation {
	if res == nil {
		return autonomy.Observation{
			RequestID: req.RequestID,
			Intent:    autonomy.Intent(req.Intent),
			Outcome:   autonomy.OutcomeFailed,
		}
	}
	outcome := execution.OutcomeFailed
	if res.Proof != nil {
		outcome = res.Proof.Outcome
	}
	return autonomy.Observation{
		RequestID:             res.RequestID,
		Intent:                autonomy.Intent(req.Intent),
		Target:                firstTarget(res.Targets),
		Evidence:              req.Evidence,
		Outcome:               autonomy.ExecutionOutcome(outcome),
		PatchID:               res.PendingPatchID,
		ClarificationRequired: res.ClarificationRequired,
		Verification:          autonomy.VerificationOutcome{Passed: res.Verification.Passed},
		TokenUsage:            res.Completed.InputTokens + res.Completed.OutputTokens,
	}
}

func firstTarget(targets []string) string {
	if len(targets) == 0 {
		return ""
	}
	return targets[0]
}
