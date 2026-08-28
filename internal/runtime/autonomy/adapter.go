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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"unicode/utf8"

	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/execution/planner"
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

// Root returns the workspace root the adapter resolves targets against. It is
// the source of truth for local file-reference resolution in the zero-token
// preflight evaluation.
func (a *ExecutorAdapter) Root() string {
	if a == nil {
		return ""
	}
	return a.root
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

// WorkspaceVersion returns the Boundary-5 workspace digest
// SHA256(Σ path(f)+hash(f)) over the given targets ("" when no executor or no
// targets are wired). The driver captures it once per run and every attempt
// re-validates it before executing.
func (a *ExecutorAdapter) WorkspaceVersion(targets []string) string {
	if a == nil || a.executor == nil || len(targets) == 0 {
		return ""
	}
	return a.executor.OCC().TreeDigest(targets)
}

// ReadTargetFile returns the raw bytes of one workspace-relative target. It
// is the decomposition surface the driver uses to feed the planner and to
// snapshot rollback contents; it never mutates anything.
func (a *ExecutorAdapter) ReadTargetFile(target string) ([]byte, bool) {
	if a == nil || a.root == "" || target == "" {
		return nil, false
	}
	data, err := os.ReadFile(filepath.Join(a.root, filepath.FromSlash(target)))
	if err != nil {
		return nil, false
	}
	return data, true
}

// RestoreTargets restores exact file contents under the workspace root. It is
// the ROLLBACK AUTHORITY of DAG execution: when a sub-task fails at Boundary
// 3, 4 or 5, the driver restores every plan target to its base content so the
// workspace provably returns to the BaseTreeDigest. Nothing is written for an
// empty restore set (atomicity means: no partial rollback).
func (a *ExecutorAdapter) RestoreTargets(contents map[string][]byte) error {
	if a == nil {
		return errors.New("autonomy: restore requires an executor adapter")
	}
	for _, target := range sortedKeys(contents) {
		full := filepath.Join(a.root, filepath.FromSlash(target))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return fmt.Errorf("autonomy: rollback mkdir %s: %w", target, err)
		}
		if err := os.WriteFile(full, contents[target], 0o644); err != nil {
			return fmt.Errorf("autonomy: rollback write %s: %w", target, err)
		}
	}
	return nil
}

// sortedKeys returns map keys in deterministic order (rollback must be
// reproducible for evidence).
func sortedKeys(m map[string][]byte) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Execute implements autonomy.Executor. It maps a LoopRequest onto the
// RuntimeExecutor's canonical ExecuteRequest and maps the resulting
// ExecutionResult onto a bounded Observation. The executor is the single
// authority that invokes the provider, produces the patch, holds the approval
// and runs verification; the adapter only translates.
//
// BOUNDARY 5 (pre-submission): when the request carries a workspace version,
// it is re-validated HERE — before any model call is submitted. A workspace
// that changed between attempts yields a workspace_drift observation with
// ZERO provider requests; a stale attempt never re-executes over moved ground.
func (a *ExecutorAdapter) Execute(ctx context.Context, req autonomy.LoopRequest) (autonomy.Observation, error) {
	targets := req.Targets
	if len(targets) == 0 && req.Target != "" {
		targets = []string{req.Target}
	}
	if req.WorkspaceDigest != "" && len(targets) > 0 {
		if current := a.WorkspaceVersion(targets); current != "" && current != req.WorkspaceDigest {
			diagnosticf("[boundary5] workspace_drift request=%s targets=%v expected=%s… actual=%s… — halting before execution",
				req.RequestID, targets, short(req.WorkspaceDigest), short(current))
			return a.driftObservation(req, targets), nil
		}
	}
	profile := a.gateway.SelectStrategy(req.Prompt)
	strategyPtr := &profile
	if (len(req.Targets) > 0 || req.Target != "") && profile.Strategy == strategy.HumanClarification {
		// The loop carries a resolved target set the raw prompt could not
		// resolve (e.g. human-specified after clarification). Hand the explicit
		// target to the executor's canonical explicit-target path instead of
		// re-asking for clarification.
		strategyPtr = nil
	}
	effectiveMax := profile.MaxOutputTokens
	if req.MaxOutputTokens > 0 {
		effectiveMax = req.MaxOutputTokens
	}
	// The observation must carry the budget the invocation will ACTUALLY be
	// bounded by (profile default or explicit override) — recovery decisions
	// and traces read it as the authoritative per-attempt output ceiling.
	req.MaxOutputTokens = effectiveMax
	// Recovery prompt augmentation: when a recovery strategy is set, the
	// objective is annotated with the explicit failure evidence so the next
	// model invocation does not have to rediscover the truncation.
	prompt := req.Prompt
	if req.RecoveryStrategy != "" && req.RecoveryReason != "" {
		prompt = prompt + "\n\n[RECOVERY " + req.RecoveryStrategy + ": " + req.RecoveryReason + "]"
	}
	execReq := execution.ExecuteRequest{
		RequestID:        req.RequestID,
		Mode:             "autonomy",
		Prompt:           prompt,
		Target:           req.Target,
		Targets:          req.Targets,
		Strategy:         strategyPtr,
		Intent:           req.Intent,
		IntentConfidence: req.IntentConfidence,
		TargetConfidence: req.TargetConfidence,
		Scope:            req.Scope,
		Evidence:         req.Evidence,
		StreamCallback:   req.StreamCallback,
		// The recovery decision travels with the request so the executor can
		// change the ACTUAL execution protocol (bounded-patch windowed
		// context + strict SEARCH/REPLACE contract), not just annotations.
		RecoveryStrategy: req.RecoveryStrategy,
		RecoveryAttempt:  req.RecoveryAttempt,
		// NO-OP escalation: the previous attempt's sentinel claim conflicted
		// with structural evidence; the executor widens the boundary window
		// for the re-hydrated judgment.
		NoOpEscalation: req.NoOpEscalation,
		// REGION FOCUS (Phase 2): under a staged decomposition plan each
		// sub-task pins its own line interval; the executor derives the
		// bounded-patch copyable window from exactly that region.
		FocusStartLine: req.FocusStartLine,
		FocusEndLine:   req.FocusEndLine,
		// CAUSAL RECOVERY (Phase 2 P2): the failed parent contract travels to
		// the executor's admission boundary, which resolves it into either a
		// same-contract retry (pure retry) or a new append-only causally
		// linked recovery contract (material change) under the bounded chain
		// limit. Failed contracts are never rewritten in place.
		RecoveryOf: req.ParentContractID,
		// The strategy-selected output ceiling is a REQUEST budget, not a
		// reporting change: max_tokens bounds the provider's generation so a
		// verbose reasoning model cannot spend an unbounded output budget, and
		// whatever the provider does bill is reported verbatim (finalizeResult
		// preserves the authoritative usage). The /build path already carries
		// this bound via the intent gateway; the autonomous path must apply the
		// same strategy-owned bound or it runs unbounded (the 5,883-token
		// repro: max_tokens was omitted because req.MaxOutputTokens stayed 0).
		MaxOutputTokens: effectiveMax,
	}
	if staged := stagedSubTaskScopes(req.StagedPlan); len(staged) > 0 {
		// DAG execution is active: hand every approved sub-task window to the
		// executor so Boundary 2 evaluates each unit individually and never
		// re-runs the monolithic full-rewrite estimation against the original
		// target (the false-positive preflight_infeasible leak).
		execReq.StagedSubTasks = staged
	}
	if strategyPtr != nil && req.RecoveryStrategy == "bounded_patch" {
		// Material artifact-contract change: the recovery attempt MUST produce
		// a structured bounded patch. The search_replace kind is enforced by
		// the executor at the artifact boundary — the model is never asked to
		// emit the full file and a full-file response is rejected — so a
		// truncated full-file regeneration can never repeat under a new label.
		mod := *strategyPtr
		mod.Artifact.Bounded = true
		mod.Artifact.Kind = "search_replace"
		mod.StrategyReason += " [recovery: bounded_patch after truncation]"
		execReq.Strategy = &mod
	}
	if strategyPtr == nil && req.RecoveryStrategy == "bounded_patch" {
		// The raw prompt could not be re-selected (the loop carries an
		// explicit human/decomposition-scoped target), but the recovery
		// protocol is still authoritative: hand the executor a minimal
		// targeted-mutation profile whose artifact contract is the bounded
		// patch itself. Without this the request would fall back to the
		// unbounded full-artifact protocol and re-trip the Boundary-2
		// preflight guard the recovery is escaping.
		execReq.Strategy = &strategy.ExecutionStrategyProfile{
			Strategy:       strategy.TargetedMutation,
			ModelRequired:  true,
			StrategyReason: "bounded_patch recovery on an explicit runtime-resolved target",
			Artifact: strategy.ArtifactContract{Kind: "search_replace", Bounded: true,
				Description: "recovery-enforced anchored SEARCH/REPLACE patch"},
			ContextPolicy:   strategy.ContextPolicyTargetFileOnly,
			MaxOutputTokens: effectiveMax,
		}
	}
	res, err := a.executor.Execute(ctx, execReq)
	if err != nil && res == nil {
		return autonomy.Observation{}, err
	}
	return a.observe(req, res), nil
}

// stagedSubTaskScopes projects a staged decomposition plan onto the executor's
// Boundary-2 scope view: one entry per sub-task with its identity, change
// window and generation estimate. A nil or empty plan yields nil (monolithic
// preflight stays authoritative).
func stagedSubTaskScopes(dag *planner.ExecutionDAG) []execution.SubTaskScope {
	if dag == nil || len(dag.SubTasks) == 0 {
		return nil
	}
	scopes := make([]execution.SubTaskScope, 0, len(dag.SubTasks))
	for _, st := range dag.SubTasks {
		scopes = append(scopes, execution.SubTaskScope{
			ID:              st.ID,
			StartLine:       st.Region.StartLine,
			EndLine:         st.Region.EndLine,
			EstimatedTokens: st.EstimatedTokens,
		})
	}
	return scopes
}

// driftObservation builds the synthetic Boundary-5 rejection: the workspace
// moved between attempts, so the attempt is refused WITHOUT any execution.
// No provider is invoked and no artifact exists.
func (a *ExecutorAdapter) driftObservation(req autonomy.LoopRequest, targets []string) autonomy.Observation {
	return autonomy.Observation{
		RequestID:        req.RequestID,
		Intent:           autonomy.Intent(req.Intent),
		Target:           firstTarget(targets),
		Evidence:         req.Evidence,
		Outcome:          autonomy.OutcomeWorkspaceDrift,
		RecoveryStrategy: req.RecoveryStrategy,
		AttemptNum:       req.RecoveryAttempt,
	}
}

// short renders the leading edge of a hex digest for compact evidence.
func short(digest string) string {
	if len(digest) > 12 {
		return digest[:12]
	}
	return digest
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
	// Extract finish reason and budget from the authoritative invocation when present.
	finishReason := ""
	maxOut := 0
	if len(res.Proof.ModelInvocations) > 0 {
		finishReason = res.Proof.ModelInvocations[len(res.Proof.ModelInvocations)-1].FinishReason
	}
	if len(res.ModelCalls) > 0 {
		if fr := res.ModelCalls[len(res.ModelCalls)-1].FinishReason; fr != "" {
			finishReason = fr
		}
	}
	// MaxOutputTokens is not stored on the result; recover via request's effective budget or profile.
	if req.MaxOutputTokens > 0 {
		maxOut = req.MaxOutputTokens
	}
	return autonomy.Observation{
		RequestID:             res.RequestID,
		ContractID:            observationContractID(res),
		Intent:                autonomy.Intent(req.Intent),
		Target:                firstTarget(res.Targets),
		Evidence:              req.Evidence,
		Diagnostic:            diagnosticEvidence(res),
		Outcome:               autonomy.ExecutionOutcome(outcome),
		PatchID:               res.PendingPatchID,
		ClarificationRequired: res.ClarificationRequired,
		Verification:          autonomy.VerificationOutcome{Passed: res.Verification.Passed},
		TokenUsage:            res.Completed.InputTokens + res.Completed.OutputTokens,
		InputTokens:           res.Completed.InputTokens,
		OutputTokens:          res.Completed.OutputTokens,
		UsageKnown:            res.Completed.Known,
		FinishReason:          finishReason,
		MaxOutputTokens:       maxOut,
		RecoveryStrategy:      req.RecoveryStrategy,
	}
}

func firstTarget(targets []string) string {
	if len(targets) == 0 {
		return ""
	}
	return targets[0]
}

// maxDiagnosticEvidence bounds the diagnostic text an observation carries.
const maxDiagnosticEvidence = 512

// diagnosticEvidence extracts the bounded validation-error text of a FAILED
// execution for the observation's Diagnostic field (I2 Recovery Isolation:
// advisory metadata only — the rejected artifact bytes never travel). The
// executor's own error is preferred because it names the concrete contract
// violation; the sealed Boundary-4 advisory signal is the fallback.
func diagnosticEvidence(res *execution.ExecutionResult) string {
	if res == nil {
		return ""
	}
	msg := ""
	if res.Err != nil {
		msg = res.Err.Error()
	}
	if msg == "" {
		if n := len(res.Diagnostics); n > 0 {
			d := res.Diagnostics[n-1]
			msg = d.Subtype + ": " + d.Detail
			if d.Directive != "" {
				msg += " — " + d.Directive
			}
		}
	}
	if len(msg) > maxDiagnosticEvidence {
		cut := maxDiagnosticEvidence
		for cut > 0 && !utf8.RuneStart(msg[cut]) {
			cut--
		}
		msg = msg[:cut] + "…"
	}
	return msg
}

// observationContractID extracts the immutable contract identity of a
// terminated execution (Phase 2 P2). The authoritative source is the sealed
// ExecutionEvidence; the proof's stamped identity is the fallback for results
// that predate evidence sealing.
func observationContractID(res *execution.ExecutionResult) string {
	if res == nil {
		return ""
	}
	if res.Evidence != nil && !res.Evidence.ContractID().IsZero() {
		return res.Evidence.ContractID().String()
	}
	if res.Proof != nil {
		return res.Proof.ContractID
	}
	return ""
}
