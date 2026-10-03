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
	"os"
	"path/filepath"
	"sort"
	"unicode/utf8"

	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/contextcompiler"
	"github.com/PizenLabs/izen/internal/core/domain"
	domaincap "github.com/PizenLabs/izen/internal/domain/capability"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/execution/planner"
	"github.com/PizenLabs/izen/internal/execution/strategy"
	"github.com/PizenLabs/izen/internal/runtime"
	runtimeAuth "github.com/PizenLabs/izen/internal/runtime/authority"
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
	root      string
	gateway   *execution.IntentGateway
	executor  *execution.RuntimeExecutor
	authority *runtime.RuntimeAuthority
	// caps is the workspace capability set the behavioral capability grant is
	// derived from. It is bound at composition time and is the same set the
	// PolicyEngine adjudicates against, so the grant can never exceed what the
	// Control Plane already permits.
	caps *domaincap.CapabilitySet
}

// NewExecutorAdapter wires the adapter over the unified IntentGateway and the
// RuntimeExecutor authority. Both MUST be non-nil.
func NewExecutorAdapter(root string, gateway *execution.IntentGateway, executor *execution.RuntimeExecutor) *ExecutorAdapter {
	return &ExecutorAdapter{root: root, gateway: gateway, executor: executor}
}

// SetAuthority wires the Workspace model authority so every ExecuteRequest
// carries an explicit TargetModel resolved at execution time. When not wired
// the adapter falls back to the executor's legacy resolution.
func (a *ExecutorAdapter) SetAuthority(auth *runtime.RuntimeAuthority) {
	if a == nil {
		return
	}
	a.authority = auth
}

// ── Behavioral stage seams ─────────────────────────────────────────────────
//
// These three methods are the ONLY surface the behavioral stage uses. They exist
// so the stage reaches the existing authority rather than growing its own:
//
//   - the capability set, to derive the capability grant;
//   - the executor's shell port, so behavioral commands pass the SAME
//     authorization and sandbox gates as every other command;
//   - the executor's mutation authorization, so a behavioral repair passes the
//     SAME gate as any other write.

// SetCapabilities binds the workspace capability set the behavioral grant is
// derived from. The set is IZEN's existing authorization state; the grant is
// only ever a projection of it.
func (a *ExecutorAdapter) SetCapabilities(caps *domaincap.CapabilitySet) {
	if a == nil {
		return
	}
	a.caps = caps
}

// grantSnapshot returns the bound capability set. The second return is false
// when no set is bound, which the stage treats as AUTHORIZATION_BLOCKED rather
// than defaulting to read access.
func (a *ExecutorAdapter) grantSnapshot(domain.ScopeProvenance) (*domaincap.CapabilitySet, bool) {
	if a == nil || a.caps == nil {
		return nil, false
	}
	return a.caps, true
}

// BindShellPort wires the executor's authorized shell port onto a behavioral
// runtime, so a command the behavioral runtime issues is gated exactly like one
// issued anywhere else in IZEN.
func (a *ExecutorAdapter) BindShellPort(rt *execution.BehavioralRuntime) {
	if a == nil || rt == nil || a.executor == nil {
		return
	}
	if port := a.executor.ShellPort(); port != nil {
		rt.SetShellPort(port)
	}
}

// ObservationAuthority returns the grant-authorized READ-ONLY observation surface
// for this workspace: the capability vocabulary the Control Plane has already
// granted, bound to the canonical capability layer.
//
// This exists because the capability layer's only production consumer was the
// behavioural stage, which the driver engages through a substring heuristic over
// the objective text. Post-execution observation must not depend on English word
// choice, so the driver derives the same GrantFor projection the behavioural stage
// uses and observes through it.
//
// The returned authority holds no authority of its own: it cannot widen a Grant,
// and every call is authorized by capability.Runner before the disk is touched.
// A caller with no bound capability set gets nil, which the driver treats as
// "observation unavailable" rather than as permission.
func (a *ExecutorAdapter) ObservationAuthority(provenance domain.ScopeProvenance) *execution.CapabilityAuthority {
	if a == nil || a.root == "" {
		return nil
	}
	caps, ok := a.grantSnapshot(provenance)
	if !ok {
		return nil
	}
	// No bus is threaded through this adapter: it is a capability-execution seam,
	// not an event source. Evidence still travels — capability.Evidence is
	// returned to the caller on every call — and the driver publishes it.
	auth := execution.NewCapabilityAuthority(a.root, nil)
	auth.SetGrant(execution.GrantFor(provenance, caps))
	return auth
}

// AuthorizeMutation is the Control Plane gate every behavioral repair target must
// pass before a proposal may be written. It delegates to the executor's own
// authorization check, so the behavioral stage cannot authorize a write the rest
// of the runtime would refuse.
func (a *ExecutorAdapter) AuthorizeMutation(target string) error {
	if a == nil || a.executor == nil {
		return errors.New("autonomy: no execution authority is bound to authorize a behavioral repair")
	}
	return a.executor.AuthorizeMutationTarget(target)
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

// PreflightTarget is the adapter's Phase 16.1 target-resolution seam for the
// pre-flight admission gate. It delegates to the executor's resolver (pure
// os.Stat classification plus ISOLATED discovery) so the gate and the executor
// see the SAME target verdict. The adapter never scans a workspace itself and
// never grants authority: discovery candidates travel as evidence only (I13).
func (a *ExecutorAdapter) PreflightTarget(ctx context.Context, prompt string, explicit []string) execution.TargetBindingResult {
	if a == nil || a.executor == nil {
		return execution.TargetBindingResult{
			Status: execution.BindingUnresolved,
			Phase:  execution.PhaseUnsubstantiated,
			State:  execution.TargetStateUnboundPath,
			Reason: "no executor is bound to the adapter; target evidence is impossible",
		}
	}
	return a.executor.ResolveMutationTarget(ctx, prompt, explicit)
}

// DeriveScope is the adapter's EVIDENCE-BOUND SCOPE DERIVATION seam.
//
// It answers the one question the target resolver deliberately refuses to
// answer: when the objective names no file but DOES name artifact kinds, which
// OBSERVED files satisfy them? The resolver refuses to choose from a scan
// because choosing from a scan is how a broad objective silently becomes a write
// to an arbitrary file (I13). This seam does not relax that: it filters the
// scan by extension against a kind the objective itself declared, and hands the
// result to the SAME canonical resolver, so a derived target is bound, digested
// and admission-checked exactly like a stated one.
//
// The adapter owns this because it is the composition boundary that already owns
// both halves of the question — the gateway (what kind of work this is) and the
// executor (what the workspace actually contains). Putting it here keeps
// strategy.Select a pure text classifier and keeps the executor free of
// objective-language parsing.
func (a *ExecutorAdapter) DeriveScope(prompt string, stated []string) execution.Derivation {
	if a == nil || a.executor == nil {
		return execution.Derivation{Reason: "no executor is bound to the adapter; scope derivation is impossible"}
	}
	resolver := a.executor.TargetResolver()
	return execution.DeriveScope(execution.DerivationRequest{
		Prompt:        prompt,
		Profile:       resolver.DiscoverProfile(),
		StatedTargets: stated,
	})
}

// Resolve determines the execution target set for an objective WITHOUT
// executing. It surfaces HumanClarification as an ambiguous resolution so the
// driver parks before any model call or mutation.
func (a *ExecutorAdapter) Resolve(prompt string) Resolved {
	return a.resolveWith(prompt, a.SelectStrategy(prompt))
}

// SelectStrategy exposes the canonical gateway's strategy decision so the
// driver can re-resolve an objective against an evidence-bound scope without
// introducing a second strategy selector. The gateway remains the sole
// authority for which execution contract a request runs under.
func (a *ExecutorAdapter) SelectStrategy(prompt string) strategy.ExecutionStrategyProfile {
	if a == nil || a.gateway == nil {
		return strategy.ExecutionStrategyProfile{Intent: prompt}
	}
	return a.gateway.SelectStrategy(prompt)
}

func (a *ExecutorAdapter) resolveWith(prompt string, profile strategy.ExecutionStrategyProfile) Resolved {
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
	if a.executor == nil {
		return nil, false
	}
	data := a.executor.SnapshotContent(target)
	return data, data != nil
}

// RestoreTargets restores exact file contents under the workspace root. It is
// the DAG rollback SEAM: when a sub-task fails at Boundary 3, 4 or 5, the
// driver restores every plan target to its base content so the workspace
// provably returns to the BaseTreeDigest. Nothing is written for an empty
// restore set (atomicity means: no partial rollback).
//
// It holds NO authority of its own. The restore and the post-restore integrity
// assertion are delegated to the ONE authoritative Mutation Boundary
// (execution.RollbackAndVerify), which both performs the restore and
// cryptographically asserts the live tree digest against baseDigest. This seam
// previously wrote the workspace itself with raw os.WriteFile and asserted
// nothing, which made "the rollback succeeded" a claim rather than a fact and
// gave DAG abort a second mutation path around the boundary.
func (a *ExecutorAdapter) RestoreTargets(contents map[string][]byte, targets []string, baseDigest string) error {
	if a == nil {
		return errors.New("autonomy: restore requires an executor adapter")
	}
	if len(contents) == 0 {
		// Atomicity: no partial rollback. Nothing to restore, and nothing to
		// assert — a boundary call over an empty restore set would only
		// recompute a digest nobody changed.
		return nil
	}
	if len(targets) == 0 {
		targets = sortedKeys(contents)
	}
	return execution.RollbackAndVerify(a.root, targets, baseDigest, contents)
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
	// The adapter is a dispatcher boundary as well as a translation layer.
	// Validate before workspace digest checks/provider submission so a caller
	// cannot bypass the driver's contract guard by invoking the adapter
	// directly.
	if err := ValidateDispatchContract(req); err != nil {
		return autonomy.Observation{}, err
	}
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
	// ── STRATEGY SELECTION OVER THE BOUND SCOPE ─────────────────────────
	// The strategy MUST be decided against the scope the run actually holds,
	// not against the raw objective text. The driver may have bound an
	// evidence-derived target set (see ExecutorAdapter.DeriveScope) after the
	// initial resolve; re-selecting on the bare prompt would silently discard
	// that work and dispatch the read-only contract the text alone implies.
	//
	// This is the SAME gateway on the SAME objective — only the scope it is
	// asked about differs — so the adapter remains a translation layer and the
	// gateway remains the sole strategy authority.
	scopePrompt := req.Prompt
	if len(targets) > 0 {
		scopePrompt = req.Prompt + " " + joinTargets(targets)
	}
	profile := a.gateway.SelectStrategy(scopePrompt)
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
	// Synthetic sub-goal (repair_first) is prepended so the model fixes
	// syntax before processing the main objective.
	prompt := req.Prompt
	if req.SyntheticSubGoal != "" {
		prompt = req.SyntheticSubGoal + "\n" + prompt
	}
	if req.RecoveryStrategy != "" && req.RecoveryReason != "" {
		prompt = prompt + "\n\n[RECOVERY " + req.RecoveryStrategy + ": " + req.RecoveryReason + "]"
	}
	// Explicit output budget overrides the profile ceiling.
	if req.ExplicitOutputBudget > 0 {
		effectiveMax = req.ExplicitOutputBudget
		req.MaxOutputTokens = effectiveMax
	}
	execReq := execution.ExecuteRequest{
		RequestID:           req.RequestID,
		Mode:                "autonomy",
		Prompt:              prompt,
		Target:              req.Target,
		Targets:             req.Targets,
		Strategy:            strategyPtr,
		Intent:              req.Intent,
		IntentConfidence:    req.IntentConfidence,
		TargetConfidence:    req.TargetConfidence,
		Scope:               req.Scope,
		InteractionContract: req.InteractionContract,
		Contract:            req.Contract,
		Evidence:            req.Evidence,
		StreamCallback:      req.StreamCallback,
		// Explicit TargetModel: resolved from the active Workspace Target at
		// execution time. The executor enforces verbatim pass-through and
		// rejects empty models locally with ErrUnassignedTargetModel.
		// Explicit TargetModel: resolved through the stateless Policy Resolver
		// (ResolveModel). Zero independent model fallbacks allowed.
		Model: func() string {
			intent := req.Intent
			if intent == "" {
				intent = req.Prompt
			}
			var runtimeState runtimeAuth.ModelState
			if a.authority != nil {
				ref := a.authority.ActiveModel()
				runtimeState = runtimeAuth.ModelState{
					ActiveProvider: runtimeAuth.ProviderID(ref.ID),
					ActiveModel:    runtimeAuth.ModelID(ref.ID),
				}
			}
			binding, err := runtimeAuth.ResolveModel(intent, runtimeState, runtimeAuth.ModelPolicy{})
			if err != nil {
				return ""
			}
			return string(binding.ModelID)
		}(),
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
	} else if len(req.StagedSubTasks) > 0 {
		execReq.StagedSubTasks = append([]execution.SubTaskScope(nil), req.StagedSubTasks...)
	}
	if strategyPtr != nil && (req.RecoveryStrategy == "bounded_patch" || req.MutationStrategy == "bounded_patch" || req.MutationStrategy == "syntax_repair" || req.AllowASTBypass || req.MutationStrategy == string(ProposalInjectLineOffset) || req.RecoveryStrategy == string(ProposalInjectLineOffset)) {
		// Material artifact-contract change: the recovery attempt MUST produce
		// a structured bounded patch. The search_replace kind is enforced by
		// the executor at the artifact boundary — the model is never asked to
		// emit the full file and a full-file response is rejected — so a
		// truncated full-file regeneration can never repeat under a new label.
		mod := *strategyPtr
		mod.Artifact.Bounded = true
		mod.Artifact.Kind = "search_replace"
		mod.StrategyReason += " [recovery: bounded_patch after truncation]"
		// Inject line-offset evidence is already appended to req.Evidence above.
		execReq.Strategy = &mod
	}
	if strategyPtr == nil && (req.RecoveryStrategy == "bounded_patch" || req.MutationStrategy == "bounded_patch" || req.MutationStrategy == "syntax_repair" || req.AllowASTBypass || req.MutationStrategy == string(ProposalInjectLineOffset)) {
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
	// Full-file fallback: human-authorized overwrite (overwrite_allowed=true).
	// Bypass RMAH Tier 3 bounded patch requirement and route to direct writer.
	if req.RecoveryStrategy == "full_file_fallback" || req.MutationStrategy == "full_file_fallback" || req.MutationStrategy == string(ProposalFullFileFallback) {
		if strategyPtr != nil {
			mod := *strategyPtr
			mod.Artifact.Bounded = false
			mod.Artifact.Kind = "full_file"
			mod.StrategyReason += " [full-file fallback authorized: overwrite_allowed=true]"
			execReq.Strategy = &mod
		} else {
			execReq.Strategy = &strategy.ExecutionStrategyProfile{
				Strategy:        strategy.TargetedMutation,
				ModelRequired:   true,
				StrategyReason:  "full-file fallback on explicit human authorization (overwrite_allowed=true)",
				Artifact:        strategy.ArtifactContract{Kind: "full_file", Bounded: false, Description: "human-authorized full-file overwrite"},
				ContextPolicy:   strategy.ContextPolicyTargetFileOnly,
				MaxOutputTokens: effectiveMax,
			}
		}
		// Propagate the focus lines if set, but do not enforce bounded patch.
	}
	// Reprompt with full text context for hallucinated anchor.
	if req.MutationStrategy == "reprompt_full_text" || req.MutationStrategy == string(ProposalRepromptFullText) {
		if strategyPtr != nil {
			mod := *strategyPtr
			mod.Artifact.Bounded = false
			mod.Artifact.Kind = "full_file"
			mod.StrategyReason += " [reprompt full text context for hallucinated anchor]"
			execReq.Strategy = &mod
		}
	}
	// Propagate line-offset focus bounds for inject_line_offset recovery.
	if req.FocusStartLine > 0 && req.FocusEndLine >= req.FocusStartLine {
		execReq.FocusStartLine = req.FocusStartLine
		execReq.FocusEndLine = req.FocusEndLine
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
			Operation:       st.EffectiveOperation(),
		})
	}
	return scopes
}

// driftObservation builds the synthetic Boundary-5 rejection: the workspace
// moved between attempts, so the attempt is refused WITHOUT any execution.
// No provider is invoked and no artifact exists.
func (a *ExecutorAdapter) driftObservation(req autonomy.LoopRequest, targets []string) autonomy.Observation {
	interaction := req.InteractionContract
	if interaction == "" && req.Contract != nil {
		interaction = req.Contract.Contract
		if interaction == "" {
			interaction = req.Contract.Kind
		}
	}
	return autonomy.Observation{
		RequestID:           req.RequestID,
		Intent:              autonomy.Intent(req.Intent),
		Target:              firstTarget(targets),
		Evidence:            req.Evidence,
		Outcome:             autonomy.OutcomeWorkspaceDrift,
		RecoveryStrategy:    req.RecoveryStrategy,
		AttemptNum:          req.RecoveryAttempt,
		InteractionContract: interaction,
		Contract:            cloneContract(req.Contract),
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
	if res.Proof != nil && len(res.Proof.ModelInvocations) > 0 {
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
	interaction := req.InteractionContract
	if interaction == "" && req.Contract != nil {
		interaction = req.Contract.Contract
		if interaction == "" {
			interaction = req.Contract.Kind
		}
	}
	descriptor := cloneContract(req.Contract)
	// Approval and rejection are resolved through the held execution result,
	// not by resubmitting the original LoopRequest.  Recover the descriptor
	// from the sealed proof when the adapter is projecting those terminal
	// observations, otherwise the contract would disappear at the approval
	// seam even though the execution remained under the same authority.
	if res.Proof != nil {
		if interaction == "" {
			interaction = res.Proof.InteractionContract
		}
		if descriptor == nil {
			descriptor = cloneContract(res.Proof.ContractDescriptor)
		}
	}
	return autonomy.Observation{
		RequestID:             res.RequestID,
		ContractID:            observationContractID(res),
		InteractionContract:   interaction,
		Contract:              descriptor,
		Intent:                autonomy.Intent(req.Intent),
		Target:                firstTarget(res.Targets),
		Evidence:              req.Evidence,
		Diagnostic:            diagnosticEvidence(res),
		Outcome:               autonomy.ExecutionOutcome(outcome),
		PatchID:               res.PendingPatchID,
		ClarificationRequired: res.ClarificationRequired,
		Verification:          autonomy.VerificationOutcome{Passed: res.Verification.Passed},
		Objective:             a.objectiveEvidence(res),
		TokenUsage:            res.Completed.InputTokens + res.Completed.OutputTokens,
		InputTokens:           res.Completed.InputTokens,
		OutputTokens:          res.Completed.OutputTokens,
		UsageKnown:            res.Completed.Known,
		FinishReason:          finishReason,
		MaxOutputTokens:       maxOut,
		RecoveryStrategy:      req.RecoveryStrategy,
		ArtifactShape:         res.ArtifactShape,
	}
}

// objectiveEvidence seals the task-specific evidence bundle of one terminal
// execution result and then OVERWRITES its target-existence map with a live
// filesystem observation.
//
// The overwrite is deliberate. The executor's own view of "which targets exist"
// is an admission-time fact; a CREATE contract must be judged on the target
// being present AFTER the apply, and a DELETE contract on it being absent. The
// adapter is the composition boundary that owns the workspace root, so it — and
// only it — can read the disk and report the durable truth. An unobservable
// target stays unobserved; it is never optimistically assumed to exist.
func (a *ExecutorAdapter) objectiveEvidence(res *execution.ExecutionResult) execution.ObjectiveEvidence {
	ev := execution.ObjectiveEvidenceFromResult(res, execution.TargetPreState{})
	if a == nil || a.root == "" {
		return ev
	}
	targets := res.Targets
	if res.Proof != nil && len(res.Proof.Targets) > 0 {
		targets = res.Proof.Targets
	}
	ev.TargetExists = make(map[string]bool, len(targets))
	ev.TargetAbsent = make(map[string]bool, len(targets))
	for _, t := range targets {
		if t == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(a.root, filepath.FromSlash(t))); err == nil {
			ev.TargetExists[t] = true
		} else {
			ev.TargetAbsent[t] = true
		}
	}
	return ev
}

// TargetExistence observes, per target, whether it is present on disk right
// now. The driver captures it BEFORE dispatch: it is the only admissible
// evidence for the IDEMPOTENT contract ("the objective was already satisfied"),
// and a post-hoc reading could never distinguish "already done" from "done by
// this run".
func (a *ExecutorAdapter) TargetExistence(targets []string) map[string]bool {
	out := make(map[string]bool, len(targets))
	if a == nil || a.root == "" {
		return out
	}
	for _, t := range targets {
		if t == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(a.root, filepath.FromSlash(t))); err == nil {
			out[t] = true
		}
	}
	return out
}

// RecompileIntentContext re-compiles the workspace context for targets under an
// intent's contract and returns the semantic provenance verdict. It is the
// second half of the blocking intent revision; see the executor method for the
// contract semantics.
func (a *ExecutorAdapter) RecompileIntentContext(ctx context.Context, targets []string, intentLabel, required string) (contextcompiler.ContextProvenance, error) {
	if a == nil || a.executor == nil {
		return contextcompiler.ContextProvenance{}, errors.New("autonomy: intent context re-compilation requires an executor")
	}
	return a.executor.RecompileIntentContext(ctx, targets, intentLabel, required)
}

// RecompileGrantedIntentContext is the GRANT-GATED re-compilation: the same
// revision, plus the non-empty-target-context acceptance condition a granted
// workspace capability must satisfy. See the executor method for why the two
// entry points are not interchangeable.
func (a *ExecutorAdapter) RecompileGrantedIntentContext(ctx context.Context, targets []string, intentLabel, required string) (contextcompiler.ContextProvenance, error) {
	if a == nil || a.executor == nil {
		return contextcompiler.ContextProvenance{}, errors.New("autonomy: granted-context re-compilation requires an executor")
	}
	return a.executor.RecompileGrantedIntentContext(ctx, targets, intentLabel, required)
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
