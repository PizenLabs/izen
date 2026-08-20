package execution

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/changeset"
	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/core/authorization"
	"github.com/PizenLabs/izen/internal/core/stream"
	"github.com/PizenLabs/izen/internal/events"
	runtimegraph "github.com/PizenLabs/izen/internal/execution/graph"
	"github.com/PizenLabs/izen/internal/execution/strategy"
	"github.com/PizenLabs/izen/internal/language"
	"github.com/PizenLabs/izen/internal/retrieval"
)

// ── RuntimeExecutor (Steps 1-3 of the authority migration) ─────────────────
//
// RuntimeExecutor is the single execution boundary between the presentation
// layer and the runtime. It owns the complete lifecycle of one user execution:
//
//	Intent resolution → strategy selection → target resolution → context
//	compilation → model invocation → artifact production → approval →
//	mutation → verification → evidence.
//
// The UI MUST NOT call a provider, a PatchManager, or a MutationSet directly
// anymore on the paths routed through this boundary: it submits an
// ExecuteRequest and receives an ExecutionResult (with an ExecutionProof), and
// approves/rejects via Approve/Reject. Every stage emits a canonical
// events.Event* runtime lifecycle event, so the UI renders purely from events.
//
// The executor is self-contained: it owns its own PatchManager + Verifier +
// MutationSet transaction boundary. It never shares the UI's execution.Engine
// mutation state, so the authority is unambiguous.

// ExecuteRequest is a user execution submitted to the runtime.
type ExecuteRequest struct {
	// RequestID correlates every lifecycle event of this execution. Empty
	// yields a deterministic auto-generated ID.
	RequestID string
	// Mode is a PRESENTATION context label only ("ask", "build", ...). It never
	// selects the execution path — the strategy does.
	Mode string
	// Prompt is the raw user request.
	Prompt string
	// Target is an explicitly resolved single mutation target (workspace
	// relative). When empty, the strategy selector resolves it.
	Target string
	// Targets is the resolved target set. Mutually exclusive with Target.
	Targets []string
	// MaxOutputTokens bounds the provider output budget (0 = provider default).
	MaxOutputTokens int
	// Strategy is the deterministically selected execution strategy. The
	// IntentGateway always sets it (Strategy.Select is unconditional); when nil
	// the runtime selects it itself. It is the single source of the execution
	// path decision — never a mode.
	Strategy *strategy.ExecutionStrategyProfile
	// ── Autonomy handoff (Phase 1 Step 6) ─────────────────────────────
	// These fields carry the autonomy decision metadata so an already
	// classified intent is never re-classified downstream and the decision
	// facts (intent, confidence, target confidence, workspace scope) survive
	// into the execution proof. They are optional: direct/gated callers leave
	// them empty.
	Intent           string
	IntentConfidence float64
	TargetConfidence float64
	Scope            string
	// Evidence is the authoritative bounded evidence ledger compiled for the
	// target set (structural findings, redundancy ledger). It is authoritative
	// evidence; the full-file context the runtime reads is supporting context
	// only (Phase 1 Step 5).
	Evidence string
}

// ModelInvocation records one provider call with its authoritative usage.
// TokenInput/TokenOutput are the provider-reported counts (zero when usage is
// unknown); Known distinguishes "provider reported usage" from "usage unknown"
// so a genuine zero is never conflated with a missing usage record.
type ModelInvocation struct {
	Model           string `json:"model"`
	TokenInput      int    `json:"token_input"`
	TokenOutput     int    `json:"token_output"`
	Known           bool   `json:"known"`
	CachedTokens    int    `json:"cached_tokens,omitempty"`
	ReasoningTokens int    `json:"reasoning_tokens,omitempty"`
	// HTTPAttempts is the number of transport round-trips this single LOGICAL
	// invocation performed (1 + every 429 backoff / 400-schema retry). Retry
	// forensics (Phase 7 P5): one model invocation may span multiple HTTP
	// attempts; a rate-limited free-tier build that recovers is still ONE
	// invocation, never two.
	HTTPAttempts int `json:"http_attempts,omitempty"`
	// RateLimitedRetries is how many of HTTPAttempts were 429 rate-limit
	// retries that succeeded on a later attempt.
	RateLimitedRetries int `json:"rate_limited_retries,omitempty"`
}

// GraphStep is one executed node of the execution graph.
type GraphStep struct {
	Stage   string    `json:"stage"`
	State   string    `json:"state"`
	Started time.Time `json:"started"`
}

// ExecutionProof is the authoritative evidence account of one execution. It is
// produced ONLY by the runtime and reflects real runtime boundaries.
type ExecutionProof struct {
	RequestID      string      `json:"request_id"`
	Strategy       string      `json:"strategy"`
	StrategyReason string      `json:"strategy_reason"`
	Targets        []string    `json:"targets"`
	Graph          []GraphStep `json:"graph"`
	// RuntimeGraph is the runtime-owned execution graph evidence: the ordered,
	// per-stage lifecycle record produced by graph transitions. It is the
	// authoritative execution timeline; Graph is its compact projection.
	RuntimeGraph     []runtimegraph.StageSnapshot `json:"runtime_graph"`
	ModelInvocations []ModelInvocation            `json:"model_invocations"`
	Mutations        []MutationEvidence           `json:"mutations"`
	Verification     VerificationReport           `json:"verification"`
	// AffectedFiles is the set of files a mutation execution actually mutated.
	AffectedFiles []string `json:"affected_files,omitempty"`
	// DiffSummary is the compact per-file diff accounting of the mutation
	// (e.g. "index.html +12/-4").
	DiffSummary []string `json:"diff_summary,omitempty"`
	// TransactionID is the MutationSet transaction identity of the mutation.
	TransactionID string `json:"transaction_id,omitempty"`
	// ContextDecisions records the strategy-owned context decisions (policy,
	// budget, per-item inclusion reasons) of the execution.
	ContextDecisions []ContextDecision `json:"context_decisions,omitempty"`
	// Intent / IntentConfidence / TargetConfidence / Scope preserve the
	// autonomy decision handoff (Phase 1 Step 6): the execution proof carries
	// the classified intent and its confidence so the runtime never loses the
	// decision facts between autonomy and execution.
	Intent           string          `json:"intent,omitempty"`
	IntentConfidence float64         `json:"intent_confidence,omitempty"`
	TargetConfidence float64         `json:"target_confidence,omitempty"`
	Scope            string          `json:"scope,omitempty"`
	Outcome          MutationOutcome `json:"outcome"`
	StartedAt        time.Time       `json:"started_at"`
	FinishedAt       time.Time       `json:"finished_at"`
}

// ContextDecision is one strategy-owned context decision recorded in the
// execution proof: which policy applied, the budget, and why each context item
// was included.
type ContextDecision struct {
	Policy string                `json:"policy"`
	Budget int                   `json:"budget"`
	Items  []ContextItemDecision `json:"items"`
}

// ContextItemDecision names a context channel and its deterministic inclusion
// reason — the compiler never includes context it cannot explain.
type ContextItemDecision struct {
	Kind   string `json:"kind"`
	Reason string `json:"reason"`
}

// ExecutionCompleted is the authoritative terminal usage account of one
// execution. It is computed ONLY by the runtime from the provider-reported
// usage (ai.ProviderUsage) — the renderer consumes it directly and never
// re-sums model calls, so the displayed token counts are always the provider's
// real billing, never a UI estimate.
type ExecutionCompleted struct {
	// Provider is the provider that served the execution.
	Provider string
	// Model is the model the execution invoked (first invocation's model).
	Model string
	// InputTokens is the aggregate provider-reported input usage of every
	// invocation of the execution.
	InputTokens int
	// OutputTokens is the aggregate provider-reported output usage of every
	// invocation of the execution.
	OutputTokens int
	// CachedTokens / ReasoningTokens are the aggregate provider-reported
	// cached and reasoning token counts (semantically distinct from
	// input/output; zero when the provider did not report them).
	CachedTokens    int
	ReasoningTokens int
	// Known reports whether at least one invocation carried provider-reported
	// usage. When false, InputTokens/OutputTokens are NOT a genuine zero — the
	// usage is unknown and must render as "usage unknown", never "0 tok".
	Known bool
	// Latency is the wall-clock duration of the execution window.
	Latency time.Duration
	// Artifact is the semantic artifact the execution produced ("" when none).
	Artifact string
}

// ExecutionResult is the full result of one runtime execution.
type ExecutionResult struct {
	RequestID      string
	Mode           string
	Strategy       string
	StrategyReason string
	Targets        []string
	// ModelCalls lists every provider invocation performed by the runtime.
	ModelCalls []ModelInvocation
	// ArtifactKind names the produced artifact ("patch", "explanation", ...).
	ArtifactKind string
	// Original is the pre-mutation target content the patch was built against.
	Original string
	// Content is the produced artifact content (proposal preview / answer).
	Content string
	// Diff is the authoritative compiled unified diff of the produced patch
	// (best-effort for rendering; the patch apply is authoritative).
	Diff string
	// PendingPatchID is set when the execution stopped at the approval gate.
	// The caller approves/rejects via RuntimeExecutor.Approve/Reject.
	PendingPatchID string
	// ClarificationRequired is true when the strategy demanded human input
	// before any model call or mutation. No invocation occurred.
	ClarificationRequired bool
	// Mutations records the applied mutation evidence (populated after Approve).
	Mutations []MutationEvidence
	// Verification is the real verifier result (populated after Approve).
	Verification VerificationReport
	// Proof is the evidence account of the whole execution.
	Proof *ExecutionProof
	// Completed is the authoritative terminal usage account computed by the
	// runtime from the provider-reported usage. The renderer reads it for the
	// footer / EXPANDED token numbers and never re-derives them.
	Completed ExecutionCompleted
	// Err is the terminal execution error, if any.
	Err error
}

// pendingMutation is the approval-held state of a targeted mutation.
type pendingMutation struct {
	requestID      string
	mode           string
	target         string
	original       string
	patches        []*Patch
	diffs          []string
	ms             *MutationSet
	strategy       string
	strategyReason string
	targets        []string
	modelCalls     []ModelInvocation
	startedAt      time.Time
	// g is the runtime-owned execution graph resumed at approval time. It is
	// the single lifecycle authority of the whole execution.
	g *runtimegraph.Graph
}

// RuntimeExecutor is the runtime-owned execution boundary.
type RuntimeExecutor struct {
	root     string
	cfg      *config.Config
	provider ai.Provider
	bus      *events.Bus
	langID   language.ID
	patches  *PatchManager
	verifier *Verifier
	auth     *authorization.MutationAuthorization

	mu      sync.Mutex
	pending map[string]*pendingMutation
	counter int
}

// NewRuntimeExecutor wires a self-contained execution authority. When langID is
// non-empty a language-aware verifier is attached (resolving that language's
// own configured steps); when langID is empty a plain verifier is attached with
// NO implicit steps — verification is Skipped (not applicable) unless explicit
// steps are configured (Phase 7 P1: no implicit Go fallback). A nil provider
// makes model-required strategies fail with a deterministic error (the runtime
// still resolves deterministic strategies without a provider).
func NewRuntimeExecutor(root string, cfg *config.Config, provider ai.Provider, bus *events.Bus, langID language.ID) *RuntimeExecutor {
	x := &RuntimeExecutor{
		root:     root,
		cfg:      cfg,
		provider: provider,
		bus:      bus,
		langID:   langID,
		patches:  NewPatchManager(root),
		pending:  make(map[string]*pendingMutation),
	}
	if langID != "" {
		x.verifier = NewLanguageVerifier(root, langID)
	} else {
		x.verifier = NewVerifier(root)
	}
	return x
}

// SetVerifier overrides the attached verifier (test seam / config wiring).
func (x *RuntimeExecutor) SetVerifier(v *Verifier) {
	x.verifier = v
}

// SetAuthorization attaches the mutation authorization token the runtime uses
// to gate every apply. When nil, applies fail with a deterministic denial —
// the runtime never applies without an authorization token.
func (x *RuntimeExecutor) SetAuthorization(a *authorization.MutationAuthorization) {
	x.auth = a
}

// SetProvider re-binds the provider (provider switching is a runtime concern).
func (x *RuntimeExecutor) SetProvider(p ai.Provider) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.provider = p
}

// emit publishes a domain event when a bus is wired. Nil-bus disables emission
// so headless harnesses can execute silently.
func (x *RuntimeExecutor) emit(ev events.DomainEvent) {
	if x != nil && x.bus != nil && ev != nil {
		x.bus.Publish(ev)
	}
}

func (x *RuntimeExecutor) nextID() string {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.counter++
	return fmt.Sprintf("exec-%d", x.counter)
}

// ErrProviderModelMismatch is the deterministic error returned when the model
// resolved for an invocation does not belong to the provider the executor is
// bound to. It fires BEFORE any network call, so an OpenRouter model can never
// be executed by the Ollama adapter (or any other mismatched provider/model
// pair).
var ErrProviderModelMismatch = errors.New("executor: provider/model mismatch")

// ErrArtifactRejected is the deterministic error returned when a mutation
// artifact fails the artifact validation boundary before any approval or
// mutation surface (malformed HTML/JSON/Go, raw patch markers, truncated
// content). The execution outcome is OutcomeArtifactRejected — an artifact
// existed but was rejected, distinct from a missing artifact.
var ErrArtifactRejected = errors.New("executor: mutation artifact rejected")

// openRouterStyleModelIDRe matches OpenRouter's vendor/model schema — the same
// schema OpenRouter itself requires for every model ID. A model carrying a
// vendor prefix (vendor/model) is a cross-provider ID; a bare local ID
// (name[:tag], no slash) is a local/direct-provider model.
var openRouterStyleModelIDRe = regexp.MustCompile(`^[^/\s]+/[^/\s]+$`)

// modelBelongsTo reports whether model is a member of providerName's model
// family. The schema gate is deliberately focused on the two families whose
// crossing is catastrophic: OpenRouter REQUIRES the vendor/model schema and
// Ollama local models never carry a vendor prefix, so a vendor-prefixed model
// can never be executed by the Ollama adapter and a bare local ID can never be
// sent to OpenRouter. Every other adapter (direct cloud, routers, custom/test
// seams) validates its own model IDs.
func modelBelongsTo(providerName, model string) bool {
	model = strings.TrimSpace(model)
	if model == "" {
		return false
	}
	switch providerName {
	case "ollama":
		// Local models never carry an OpenRouter vendor prefix.
		return !openRouterStyleModelIDRe.MatchString(model)
	case "openrouter":
		// OpenRouter REQUIRES the vendor/model schema.
		return openRouterStyleModelIDRe.MatchString(model)
	default:
		return true
	}
}

// resolveModel resolves the model that travels with the provider the executor
// is bound to. The model is ALWAYS derived from the bound provider's own
// configuration — never from the global active provider, which can drift from
// the bound adapter — so provider identity and model identity travel together.
//
// The session model (the user's explicit /model selection) is authoritative but
// must belong to the bound provider: when it does not, the runtime fails
// deterministically before any network call instead of silently routing an
// OpenRouter model into an Ollama adapter. Model routing is a runtime decision;
// the caller never selects it.
func (x *RuntimeExecutor) resolveModel() (string, error) {
	x.mu.Lock()
	p := x.provider
	x.mu.Unlock()
	if p == nil {
		return "", fmt.Errorf("executor: no provider configured for model invocation")
	}
	if x.cfg == nil {
		return "", fmt.Errorf("executor: no configuration to resolve the model for provider %q", p.Name())
	}
	name := p.Name()
	model := ""
	switch {
	case x.cfg.Models.SessionModel != "":
		model = x.cfg.Models.SessionModel
	default:
		if provCfg, ok := x.cfg.AI.Providers[name]; ok && provCfg.DefaultModel != "" {
			model = provCfg.DefaultModel
		} else if x.cfg.Models.Default != "" {
			model = x.cfg.Models.Default
		} else {
			// The bound provider is not described in the config registry (a
			// test seam or custom adapter): fall back to the global active
			// model so the runtime still invokes with a model.
			model = x.cfg.ActiveModelName()
		}
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return "", fmt.Errorf("executor: no model bound to provider %q", name)
	}
	if !modelBelongsTo(name, model) {
		return "", fmt.Errorf("%w: model %q does not belong to provider %q",
			ErrProviderModelMismatch, model, name)
	}
	return model, nil
}

// Execute runs the deterministic execution flow for req, driving the
// runtime-owned ExecutionGraph. The graph is the single lifecycle authority:
// every canonical lifecycle event is generated from a graph transition, and
// the graph's stage evidence folds into ExecutionProof. For a targeted
// mutation it stops at the approval gate and returns PendingPatchID; the
// caller resolves it via Approve/Reject.
func (x *RuntimeExecutor) Execute(ctx context.Context, req ExecuteRequest) (*ExecutionResult, error) {
	requestID := req.RequestID
	if requestID == "" {
		requestID = x.nextID()
	}
	res := &ExecutionResult{
		RequestID: requestID,
		Mode:      req.Mode,
		Proof:     &ExecutionProof{RequestID: requestID, StrategyReason: "", StartedAt: time.Now()},
	}
	// Preserve the autonomy decision handoff (Phase 1 Step 6) so the intent,
	// confidence, target confidence and scope survive into the execution proof.
	res.Proof.Intent = req.Intent
	res.Proof.IntentConfidence = req.IntentConfidence
	res.Proof.TargetConfidence = req.TargetConfidence
	res.Proof.Scope = req.Scope

	start := time.Now()
	// ── RUNTIME-OWNED EXECUTION GRAPH (Phase 5) ───────────────────────
	// Every execution drives the explicit graph. Transitions generate events;
	// stage evidence becomes the proof timeline. The UI only projects.
	g := runtimegraph.New(requestID, x.emit)
	setProofGraph(res, g)
	g.Start(req.Mode, req.Prompt)
	g.CompleteUserIntent()

	// ── 1. Strategy selection ──────────────────────────────────────────
	profile, err := x.selectStrategy(ctx, req)
	if err != nil {
		g.FailExecution(events.FailureRecoverable, err, "executor")
		res.Err = err
		res.Proof.Outcome = OutcomeFailed
		res.Proof.FinishedAt = time.Now()
		setProofGraph(res, g)
		return x.finalizeResult(res), err
	}
	res.Strategy = string(profile.Strategy)
	res.StrategyReason = profile.StrategyReason
	res.Proof.Strategy = res.Strategy
	res.Proof.StrategyReason = profile.StrategyReason
	res.Proof.ContextDecisions = contextDecisions(profile)
	g.CompleteStrategy(res.Strategy, profile.ModelRequired, profile.StrategyReason)

	// ── 2. Target resolution ───────────────────────────────────────────
	targets := req.Targets
	if len(targets) == 0 && req.Target != "" {
		targets = []string{req.Target}
	}
	if len(targets) == 0 {
		for _, t := range profile.Targets {
			if t.Resolved != "" {
				targets = append(targets, t.Resolved)
			}
		}
	}
	res.Targets = targets
	res.Proof.Targets = targets
	for _, t := range targets {
		g.CompleteTarget(t, fileExists(filepath.Join(x.root, t)), "strategy")
	}

	// ── 3. Human clarification (no model, no mutation) ────────────────
	// HumanClarification is authoritative and MUST be handled before the
	// deterministic branch: the strategy carries Deterministic=true but its
	// outcome is a human stop, never a completed execution. The human is the
	// authority; no file is read into a prompt and no mutation is proposed.
	if profile.Strategy == strategy.HumanClarification {
		skipTail(g, "human clarification")
		g.CancelExecution("clarification_required")
		res.ClarificationRequired = true
		res.Proof.Outcome = OutcomeCancelled
		res.Proof.FinishedAt = time.Now()
		setProofGraph(res, g)
		return x.finalizeResult(res), nil
	}

	// ── 4. Deterministic strategies (zero model) ───────────────────────
	if profile.Deterministic {
		skipTail(g, "deterministic execution")
		g.CompleteExecution("deterministic")
		res.Proof.Outcome = OutcomeNoArtifact
		res.Proof.FinishedAt = time.Now()
		setProofGraph(res, g)
		return x.finalizeResult(res), nil
	}

	// ── 5. Target-bound clarification ─────────────────────────────────
	// A target-bound strategy whose target cannot be resolved stops before any
	// invocation. Read-only strategies (and zero-context direct response) may
	// run without a target set.
	if len(targets) == 0 && profile.Strategy != strategy.TargetedReasoning &&
		profile.Strategy != strategy.DirectResponse &&
		profile.Strategy != strategy.MultiFilePlanning &&
		profile.Strategy != strategy.RepositoryInvestigation {
		skipTail(g, "clarification required")
		g.CancelExecution("clarification_required")
		res.ClarificationRequired = true
		res.Proof.Outcome = OutcomeCancelled
		res.Proof.FinishedAt = time.Now()
		setProofGraph(res, g)
		return x.finalizeResult(res), nil
	}

	if x.provider == nil {
		err := fmt.Errorf("executor: strategy %q requires a provider but none is configured", res.Strategy)
		g.FailExecution(events.FailureRecoverable, err, "executor")
		res.Err = err
		res.Proof.Outcome = OutcomeFailed
		res.Proof.FinishedAt = time.Now()
		setProofGraph(res, g)
		return x.finalizeResult(res), res.Err
	}

	// ── 6. Context compilation ─────────────────────────────────────────
	// The strategy owns the context contract: profile.ContextPolicy and
	// profile.ContextBudget decide what crosses (zero for direct_response,
	// target content for a targeted mutation, repository evidence for
	// investigation). The generic compiler never decides.
	contextChannels, contextTokens := x.compileContext(profile, targets)
	g.CompleteContext(contextChannels, contextTokens)

	// ── 7. Read-only strategies (targeted_reasoning / direct_response /
	// multi_file_planning / repository_investigation): one bounded invocation,
	// content returned, no mutation path, no approval surface. ─────────
	//
	// EVENT SEMANTICS (Phase 4): model.invoked is emitted BEFORE the provider
	// call; provider.response is emitted ONLY after a successful response; and
	// artifact.produced can never precede it. A failed invocation emits NO
	// model.invoked, NO provider.response and NO artifact.produced — a failed
	// execution must never emit a misleading success artifact.
	if profile.Strategy != strategy.TargetedMutation {
		content, inv, err := x.invokeReadOnly(ctx, req, requestID, profile, targets, g)
		if err != nil {
			// A user cancellation is a clean terminal outcome, never a
			// failure: the workspace is untouched and no rollback runs.
			if errors.Is(err, context.Canceled) {
				g.CancelExecution(string(OutcomeCancelled))
				res.Err = nil
				res.Proof.Outcome = OutcomeCancelled
				res.Proof.FinishedAt = time.Now()
				setProofGraph(res, g)
				return x.finalizeResult(res), nil
			}
			g.FailExecution(events.FailureRecoverable, err, "executor.model")
			res.Err = err
			res.Proof.Outcome = OutcomeFailed
			res.Proof.FinishedAt = time.Now()
			setProofGraph(res, g)
			return x.finalizeResult(res), err
		}
		res.ModelCalls = append(res.ModelCalls, inv)
		res.Proof.ModelInvocations = append(res.Proof.ModelInvocations, inv)
		res.ArtifactKind = artifactKindFor(profile)
		res.Content = content
		g.CompleteArtifact(res.ArtifactKind, firstTarget(targets))
		g.Skip(runtimegraph.StageApprovalGate, "read-only execution")
		g.Skip(runtimegraph.StageMutationTransaction, "read-only execution")
		g.Skip(runtimegraph.StageVerification, "read-only execution")
		g.CompleteExecution(string(OutcomeCompleted))
		res.Proof.Outcome = OutcomeCompleted
		res.Proof.FinishedAt = time.Now()
		setProofGraph(res, g)
		return x.finalizeResult(res), nil
	}

	// ── 7. Targeted mutation: per-target model invocation ──────────────
	patches, invs, diffs, err := x.invokeMutation(ctx, req, requestID, targets, g)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			// A user cancellation is a clean terminal outcome: no artifact was
			// produced, nothing was applied, and no rollback runs.
			g.CancelExecution(string(OutcomeCancelled))
			res.ArtifactKind = ""
			res.Err = nil
			res.Proof.Outcome = OutcomeCancelled
			res.Proof.FinishedAt = time.Now()
			setProofGraph(res, g)
			return x.finalizeResult(res), nil
		}
		if errors.Is(err, ErrArtifactRejected) {
			// The model produced an artifact that FAILED the artifact
			// boundary. The execution is a permanent artifact rejection, never
			// a recoverable patch failure: retrying the same malformed output
			// cannot repair it.
			g.FailExecution(events.FailurePermanent, err, "executor.artifact")
			res.ArtifactKind = ""
			res.Err = err
			res.Proof.Outcome = OutcomeArtifactRejected
			res.Proof.FinishedAt = time.Now()
			setProofGraph(res, g)
			return x.finalizeResult(res), err
		}
		g.FailExecution(events.FailureRecoverable, err, "executor.model")
		res.ArtifactKind = ""
		res.Err = err
		res.Proof.Outcome = OutcomePatchGenerationFailed
		res.Proof.FinishedAt = time.Now()
		setProofGraph(res, g)
		return x.finalizeResult(res), err
	}
	res.ModelCalls = append(res.ModelCalls, invs...)
	res.Proof.ModelInvocations = append(res.Proof.ModelInvocations, invs...)

	// ── 8. Artifact production ─────────────────────────────────────────
	target := targets[0]
	res.ArtifactKind = "patch"
	res.Original = patches[0].Original
	res.Content = patches[0].Modified
	res.Diff = diffs[0]
	for _, p := range patches {
		g.CompleteArtifact("patch", p.File)
	}

	// ── 9. Approval gate ───────────────────────────────────────────────
	pm := &pendingMutation{
		requestID:      requestID,
		mode:           req.Mode,
		target:         target,
		original:       res.Original,
		patches:        patches,
		diffs:          diffs,
		strategy:       res.Strategy,
		strategyReason: res.StrategyReason,
		targets:        targets,
		modelCalls:     res.ModelCalls,
		startedAt:      start,
		g:              g,
	}
	x.mu.Lock()
	x.pending[patches[0].ID] = pm
	x.mu.Unlock()
	res.PendingPatchID = patches[0].ID
	// The execution stopped at the approval gate with a VALID held artifact —
	// the outcome is pending approval, never "no artifact".
	res.Proof.Outcome = OutcomePendingApproval
	res.Proof.FinishedAt = time.Now()
	g.WaitApproval(target, res.Diff)
	setProofGraph(res, g)
	return x.finalizeResult(res), nil
}

// Approve resolves the approval gate: it applies the held patch(es) through the
// PatchManager inside ONE MutationSet transaction (owning the filesystem write
// + verification gate), commits the MutationSet, and returns the terminal
// result with evidence. The runtime-owned execution graph is resumed and driven
// through the mutation/verification/completion stages — every event comes from
// a graph transition.
func (x *RuntimeExecutor) Approve(ctx context.Context, patchID string) (*ExecutionResult, error) {
	x.mu.Lock()
	pm, ok := x.pending[patchID]
	if ok {
		delete(x.pending, patchID)
	}
	x.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("executor: no pending mutation for patch %q", patchID)
	}

	res := &ExecutionResult{
		RequestID:      pm.requestID,
		Mode:           pm.mode,
		Strategy:       pm.strategy,
		StrategyReason: pm.strategyReason,
		Targets:        pm.targets,
		ModelCalls:     pm.modelCalls,
		ArtifactKind:   "patch",
		Content:        pm.patches[0].Modified,
		Proof: &ExecutionProof{
			RequestID:        pm.requestID,
			Strategy:         pm.strategy,
			StrategyReason:   pm.strategyReason,
			Targets:          pm.targets,
			ModelInvocations: pm.modelCalls,
			StartedAt:        pm.startedAt,
		},
	}
	g := pm.g
	if g == nil {
		g = runtimegraph.New(pm.requestID, x.emit)
	}
	setProofGraph(res, g)

	// The runtime owns a fresh mutation boundary for this apply.
	ms := NewMutationSet()
	x.patches.SetMutationSet(ms)
	x.patches.SetAuthorization(x.auth)
	if x.verifier != nil {
		// Phase 1 safety rule: the verifier is the APPLY GATE, not an
		// after-the-fact report. Attaching it to the runtime's own
		// PatchManager activates the micro-fix gate inside Apply, so a
		// verification failure restores the shadow backup and fails the apply
		// (never a committed mutation reported as changed).
		x.patches.SetVerifier(x.verifier)
		x.verifier.SetAuthorization(x.auth)
	}
	pm.ms = ms
	res.Proof.TransactionID = ms.ID

	g.BeginMutation(pm.targets)
	setProofGraph(res, g)

	// ── Apply EVERY held patch inside the single MutationSet transaction ──
	applyCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	var applyErr error
	for _, p := range pm.patches {
		if err := x.patches.ApplyContext(applyCtx, p); err != nil {
			applyErr = err
			break
		}
	}

	// ── Verification truth comes from the gate that ACTUALLY ran ──────
	// The verification gate executes INSIDE the apply boundary (PatchManager
	// Apply) as the apply gate, and its report is captured on the MutationSet.
	// The execution reads that real gate outcome — it never re-runs
	// verification after commit and never reports a verification that did not
	// happen. Verification therefore always precedes the terminal result.
	var verificationSteps []string
	if ms.Verification != nil {
		res.Verification = *ms.Verification
		res.Proof.Verification = res.Verification
		for _, s := range res.Verification.Results {
			verificationSteps = append(verificationSteps, s.Step.Name)
		}
	}

	if applyErr != nil {
		// ── FAILURE: roll back the WHOLE transaction ─────────────────
		// A failure at any point rolls back the entire boundary. The
		// aggregate outcome is a FAILURE outcome — never "changed", even when
		// a sibling file applied before the failure (the rollback restored
		// it). Per-file evidence is corrected to the actual post-rollback
		// filesystem state so nothing overclaims a mutation.
		_ = ms.RollbackTo(MutationFailed)
		// Reconcile per-file evidence to the actual post-rollback filesystem
		// state BEFORE it is copied into the result/proof.
		x.correctEvidenceAfterRollback(ms, pm.patches)
		outcome := OutcomeApplyFailed
		if ms.Verification != nil && !ms.Verification.Passed && !ms.Verification.Skipped {
			// Only a gate that actually ran and failed is a verify failure; a
			// not-applicable (Skipped) gate never makes an apply failure a
			// verify failure (Phase 7 P1).
			outcome = OutcomeVerifyFailed
		}
		res.Mutations = append(res.Mutations, ms.Outcomes...)
		res.Proof.Mutations = res.Mutations
		res.Proof.Outcome = outcome
		res.Proof.AffectedFiles = append([]string(nil), pm.targets...)
		res.Proof.DiffSummary = pm.diffs
		for _, p := range pm.patches {
			o := ms.OutcomeFor(p.File)
			if o == OutcomeNoArtifact {
				o = OutcomeApplyFailed
			}
			g.CompleteMutation(p.File, string(o))
		}
		if ms.Verification != nil {
			if ms.Verification.Skipped {
				g.Skip(runtimegraph.StageVerification, ms.Verification.Reason)
			} else {
				g.CompleteVerification(ms.Verification.Passed, verificationSteps)
			}
		}
		g.FailExecution(events.FailureRecoverable, applyErr, "executor.mutation")
		res.Err = applyErr
		res.Proof.FinishedAt = time.Now()
		setProofGraph(res, g)
		return x.finalizeResult(res), applyErr
	}

	// ── SUCCESS: commit the transaction ─────────────────────────────
	// The aggregate outcome is derived from the per-file evidence with
	// explicit multi-file semantics: any changed → changed; otherwise any
	// created → created; otherwise any nochange → nochange. A multi-file
	// mutation never reports success merely because one file changed.
	_ = ms.Commit()
	outcome := AggregateMutationOutcome(ms.Outcomes)
	res.Mutations = append(res.Mutations, ms.Outcomes...)
	res.Proof.Mutations = res.Mutations
	res.Proof.Outcome = outcome
	res.Proof.AffectedFiles = append([]string(nil), pm.targets...)
	res.Proof.DiffSummary = pm.diffs
	for _, p := range pm.patches {
		o := ms.OutcomeFor(p.File)
		if o == OutcomeNoArtifact {
			o = OutcomeNoChange
		}
		g.CompleteMutation(p.File, string(o))
	}
	if ms.Verification != nil {
		if ms.Verification.Skipped {
			g.Skip(runtimegraph.StageVerification, ms.Verification.Reason)
		} else {
			g.CompleteVerification(ms.Verification.Passed, verificationSteps)
		}
	} else {
		g.Skip(runtimegraph.StageVerification, "no verifier gate ran during apply")
	}

	g.CompleteExecution(string(outcome))
	res.Proof.FinishedAt = time.Now()
	setProofGraph(res, g)
	return x.finalizeResult(res), nil
}

// correctEvidenceAfterRollback reconciles the per-file mutation evidence to
// the actual post-rollback filesystem state: a mutation that was applied and
// then rolled back is NOT changed on disk. The evidence records apply-time
// facts (the write happened); the rollback truth is byte comparison against
// the held patch originals. It is a no-op when a file is missing.
func (x *RuntimeExecutor) correctEvidenceAfterRollback(ms *MutationSet, patches []*Patch) {
	if ms == nil {
		return
	}
	originals := make(map[string]string, len(patches))
	for _, p := range patches {
		if p != nil {
			originals[p.File] = p.Original
		}
	}
	for i := range ms.Outcomes {
		orig, ok := originals[ms.Outcomes[i].File]
		if !ok {
			continue
		}
		fullPath := filepath.Join(x.root, ms.Outcomes[i].File)
		data, err := os.ReadFile(fullPath)
		if err != nil {
			// A file that no longer exists after rollback was created by this
			// mutation and removed — it is not changed.
			ms.Outcomes[i].FilesystemChanged = false
			continue
		}
		ms.Outcomes[i].FilesystemChanged = string(data) != orig
	}
}

// Reject resolves the approval gate negatively: the held patches are never
// applied and the mutation boundary is rolled back (restoring any recorded
// snapshot — none exist yet, since apply never ran). The graph is cancelled
// cleanly.
func (x *RuntimeExecutor) Reject(ctx context.Context, patchID, reason string) (*ExecutionResult, error) {
	x.mu.Lock()
	pm, ok := x.pending[patchID]
	if ok {
		delete(x.pending, patchID)
	}
	x.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("executor: no pending mutation for patch %q", patchID)
	}

	res := &ExecutionResult{
		RequestID:      pm.requestID,
		Mode:           pm.mode,
		Strategy:       pm.strategy,
		StrategyReason: pm.strategyReason,
		Targets:        pm.targets,
		ModelCalls:     pm.modelCalls,
		ArtifactKind:   "patch",
		Proof: &ExecutionProof{
			RequestID:        pm.requestID,
			Strategy:         pm.strategy,
			StrategyReason:   pm.strategyReason,
			Targets:          pm.targets,
			ModelInvocations: pm.modelCalls,
			StartedAt:        pm.startedAt,
		},
	}
	g := pm.g
	if g == nil {
		g = runtimegraph.New(pm.requestID, x.emit)
	}
	ms := NewMutationSet()
	for _, p := range pm.patches {
		_ = ms.Record(p.File)
	}
	_ = ms.RollbackTo(MutationRejected)
	// The rejection is a real lifecycle transition: the human decided against
	// the held proposal. It is distinct from an execution cancellation.
	g.RejectApproval(firstTarget(pm.targets), reason)
	g.CancelExecution(string(OutcomeRejected))
	res.Proof.Mutations = ms.Outcomes
	res.Proof.Outcome = OutcomeRejected
	res.Proof.FinishedAt = time.Now()
	setProofGraph(res, g)
	return x.finalizeResult(res), nil
}

// PendingPatchIDs returns the approval-held patch IDs (observability).
func (x *RuntimeExecutor) PendingPatchIDs() []string {
	x.mu.Lock()
	defer x.mu.Unlock()
	out := make([]string, 0, len(x.pending))
	for id := range x.pending {
		out = append(out, id)
	}
	return out
}

// ── internals ──────────────────────────────────────────────────────────────

func (x *RuntimeExecutor) selectStrategy(_ context.Context, req ExecuteRequest) (strategy.ExecutionStrategyProfile, error) {
	// The IntentGateway always selects the strategy (unconditional). When a
	// profile is carried on the request it is the single authoritative source
	// of the execution path decision — modes never select the path.
	if req.Strategy != nil {
		profile := *req.Strategy
		if req.MaxOutputTokens > 0 {
			profile.MaxOutputTokens = req.MaxOutputTokens
		}
		return profile, nil
	}
	// Direct runtime callers that resolved an explicit target without a
	// gateway profile target a bounded mutation.
	if req.Target != "" || len(req.Targets) > 0 {
		return strategy.ExecutionStrategyProfile{
			Strategy:        strategy.TargetedMutation,
			ModelRequired:   true,
			StrategyReason:  "explicit resolved target submitted to the runtime",
			MaxOutputTokens: req.MaxOutputTokens,
		}, nil
	}
	deps := strategy.Deps{Root: x.root, Workspace: executorWorkspace{root: x.root}}
	profile := strategy.Select(req.Prompt, deps)
	if req.MaxOutputTokens > 0 {
		profile.MaxOutputTokens = req.MaxOutputTokens
	}
	return profile, nil
}

// compileContext assembles the minimum-sufficient context envelope for the
// target set. The STRATEGY owns the context contract: profile.ContextPolicy
// decides what may be read. A ContextPolicyNone strategy (direct_response /
// casual chat) compiles ZERO channels and reads no file — no workspace scan,
// no repository context. A target_file_only policy reads exactly the resolved
// targets. A repository policy admits the wider evidence channels. Provider
// usage is never estimated here — this is context-accounting, not billing.
func (x *RuntimeExecutor) compileContext(profile strategy.ExecutionStrategyProfile, targets []string) (channels []string, tokens int) {
	switch profile.Policy() {
	case strategy.ContextPolicyNone:
		// Zero context: no workspace scan, no file channels, no repository
		// context. "hi" prepares nothing.
		return nil, 0
	case strategy.ContextPolicyRepository:
		channels = append(channels, "user_intent", "explicit_targets", "target_content",
			"dependency_evidence", "repository_constraints")
	default: // target_file_only
		channels = append(channels, "user_intent", "explicit_targets", "target_content")
	}
	var b strings.Builder
	for _, t := range targets {
		path := filepath.Join(x.root, t)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		// Runtime context activity evidence: the target content actually read
		// by the runtime crosses only when it really happens.
		x.emitContextActivity(t, len(data))
		if len(data) > maxExecutorContextBytes {
			data = data[:maxExecutorContextBytes]
		}
		b.Write(data)
		b.WriteByte('\n')
	}
	return channels, estimateTokens(b.String())
}

// invokeMutation performs the bounded provider invocation(s) for a targeted
// mutation — one bounded call per resolved target. It drives the model stage of
// the execution graph: model.invoked is emitted BEFORE each provider call and
// provider.response ONLY after a successful response. On any failure it returns
// the error and emits neither response nor artifact. The returned patches carry
// the full resolved content of each target; the diffs are the authoritative
// unified diffs for rendering.
func (x *RuntimeExecutor) invokeMutation(ctx context.Context, req ExecuteRequest, requestID string, targets []string, g *runtimegraph.Graph) ([]*Patch, []ModelInvocation, []string, error) {
	if len(targets) == 0 {
		return nil, nil, nil, fmt.Errorf("executor: no mutation target resolved")
	}
	if x.provider == nil {
		return nil, nil, nil, fmt.Errorf("executor: no provider configured for model invocation")
	}

	model, modelErr := x.resolveModel()
	if modelErr != nil {
		return nil, nil, nil, modelErr
	}

	patches := make([]*Patch, 0, len(targets))
	invs := make([]ModelInvocation, 0, len(targets))
	diffs := make([]string, 0, len(targets))
	for _, target := range targets {
		path := filepath.Join(x.root, target)
		data, readErr := os.ReadFile(path)
		if readErr != nil && !os.IsNotExist(readErr) {
			return nil, nil, nil, fmt.Errorf("executor: read target %s: %w", target, readErr)
		}
		original := string(data)
		// Runtime context activity evidence: the target content actually read
		// by the runtime crosses only when it really happens.
		x.emitContextActivity(target, len(data))

		system := boundedMutationSystemPrompt()
		user := buildMutationUserPrompt(req.Prompt, target, original, req.Evidence)
		aiReq := ai.Request{
			Model:     model,
			System:    system,
			Messages:  []ai.Message{{Role: "user", Content: user}},
			MaxTokens: req.MaxOutputTokens,
		}
		// model.invoked is emitted when the invocation BEGINS — before the
		// provider call — so the event stream truthfully records the start.
		g.BeginModel(model)
		raw, usage, callErr := x.invokeStream(ctx, aiReq, requestID, model, g)
		if callErr != nil {
			return nil, nil, nil, fmt.Errorf("executor: model invocation: %w", callErr)
		}
		inv := ModelInvocation{Model: model}
		if usage.Known {
			inv.Known = true
			inv.TokenInput = usage.PromptTokens
			inv.TokenOutput = usage.CompletionTokens
			inv.CachedTokens = usage.CachedTokens
			inv.ReasoningTokens = usage.ReasoningTokens
		}
		inv.HTTPAttempts = usage.HTTPAttempts
		inv.RateLimitedRetries = usage.RateLimitedRetries
		// provider.response is emitted ONLY on a successful response — the
		// authoritative usage travels here. No artifact may precede it.
		g.CompleteModel(model, inv.TokenInput, inv.TokenOutput)
		invs = append(invs, inv)

		modified := ResolveModifiedContent(original, raw)
		if modified == "" {
			// The model returned only prose or a fence without content — treat
			// the full response as the replacement attempt (best-effort).
			modified = raw
		}
		if strings.TrimSpace(modified) == "" {
			// Phase 1 safety rule: an artifact extraction failure is a FAILURE,
			// never a proposal staged for approval. The model produced no
			// usable mutation artifact — abort before any approval surface.
			return nil, nil, nil, fmt.Errorf("executor: model produced no mutation artifact for %s", target)
		}
		// ── ARTIFACT BOUNDARY (Phase 2) ─────────────────────────────
		// A model response is NOT an artifact until it passes the artifact
		// validation boundary. Registered-language targets (go/html/json) that
		// fail validation are rejected BEFORE any approval or mutation surface —
		// a malformed artifact can never become a proposal. Unregistered
		// languages pass normalized (canonical bytes), so the proposal preview
		// and the eventual disk write agree.
		//nolint:contextcheck // artifact validation is pure content checking, no context needed
		normalized, gateErr := x.artifactGate(target, modified)
		if gateErr != nil {
			return nil, nil, nil, gateErr
		}
		modified = normalized
		patches = append(patches, &Patch{
			ID:       fmt.Sprintf("%s-patch-%d", requestID, len(patches)+1),
			File:     target,
			Original: original,
			Modified: modified,
		})
		diffs = append(diffs, x.compileDiff(raw, target, original))
	}
	return patches, invs, diffs, nil
}

// artifactGate validates the resolved mutation artifact against the target's
// language contract using the established V3 artifact pipeline. A
// registered-language target (go/html/json) that fails validation is rejected
// deterministically (ErrArtifactRejected) with the validation diagnostic. The
// canonical normalized content is returned so the proposal and the disk agree.
// A language with no registered validator passes normalized but unvalidated.
func (x *RuntimeExecutor) artifactGate(target, modified string) (string, error) {
	gate := v3Artifact.ValidateContent(target, []byte(modified), 0)
	if !gate.Passed {
		return "", fmt.Errorf("%w: %s: %w", ErrArtifactRejected, target, gate.Error)
	}
	return string(gate.Normalized), nil
}

// compileDiff runs the changeset pipeline over the raw model output to produce
// the authoritative unified diff for rendering. It is best-effort: a failure
// (output the pipeline cannot map) leaves Diff empty — the apply is the
// authoritative stage.
func (x *RuntimeExecutor) compileDiff(raw, target, original string) string {
	if raw == "" {
		return ""
	}
	compiled, err := changeset.NewPipeline().Run(raw, target, []byte(original))
	if err != nil || len(compiled) == 0 {
		return ""
	}
	return string(compiled[0].Diff)
}

// invokeReadOnly performs the single bounded provider invocation for read-only
// strategies (targeted_reasoning, direct_response, multi_file_planning,
// repository_investigation). It returns the produced content; no mutation path
// and no approval surface exist. Event semantics (Phase 4): model.invoked is
// emitted before the provider call; provider.response is emitted only after a
// successful response; a failure returns an error and emits neither — the
// artifact can never precede the response that produced it. The response is
// owned by the runtime and never reaches the UI raw.
func (x *RuntimeExecutor) invokeReadOnly(ctx context.Context, req ExecuteRequest, requestID string, profile strategy.ExecutionStrategyProfile, targets []string, g *runtimegraph.Graph) (content string, inv ModelInvocation, err error) {
	if x.provider == nil {
		return "", inv, fmt.Errorf("executor: no provider configured for read-only invocation")
	}

	var b strings.Builder
	b.WriteString(readOnlySystemPrompt(profile.Strategy))
	for _, t := range targets {
		path := filepath.Join(x.root, t)
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			continue
		}
		// Runtime context activity evidence: the file content the runtime
		// actually read crosses only when it really happens.
		x.emitContextActivity(t, len(data))
		if len(data) > maxExecutorContextBytes {
			data = data[:maxExecutorContextBytes]
		}
		b.WriteString("\n### TARGET FILE: ")
		b.WriteString(t)
		b.WriteString("\n```\n")
		b.Write(data)
		b.WriteString("\n```\n")
	}
	b.WriteString("\n### USER REQUEST\n")
	b.WriteString(req.Prompt)
	b.WriteString("\n")

	model, modelErr := x.resolveModel()
	if modelErr != nil {
		return "", inv, modelErr
	}
	aiReq := ai.Request{
		Model:     model,
		System:    readOnlySystemPrompt(profile.Strategy),
		Messages:  []ai.Message{{Role: "user", Content: b.String()}},
		MaxTokens: req.MaxOutputTokens,
	}
	// model.invoked is emitted when the invocation BEGINS — before the provider
	// call — so the event stream truthfully records the invocation start.
	g.BeginModel(model)
	raw, usage, callErr := x.invokeStream(ctx, aiReq, requestID, model, g)
	if callErr != nil {
		return "", inv, fmt.Errorf("executor: read-only invocation: %w", callErr)
	}
	inv.Model = model
	if usage.Known {
		inv.Known = true
		inv.TokenInput = usage.PromptTokens
		inv.TokenOutput = usage.CompletionTokens
		inv.CachedTokens = usage.CachedTokens
		inv.ReasoningTokens = usage.ReasoningTokens
	}
	// provider.response is emitted ONLY on a successful response — the
	// authoritative usage travels here. No artifact may precede it.
	g.CompleteModel(model, inv.TokenInput, inv.TokenOutput)
	return raw, inv, nil
}

// invokeStream executes one bounded provider invocation through a live
// streaming connection, emitting the canonical provider lifecycle events
// (provider.waiting → provider.first_token → provider.stream_delta →
// provider.usage_update) as REAL evidence while the call runs. It is the
// evidence transport of the model stage: the UI never infers provider state.
//
// Reasoning is never surfaced as text: the stream classifier and the request's
// ReasoningHandler only drive reasoning TELEMETRY (duration + the
// provider-reported reasoning token count when available) emitted as a single
// reasoning.telemetry event on completion. The accumulated visible content and
// the authoritative provider usage are returned; the authoritative artifact
// always travels on the ExecutionResult afterwards.
func (x *RuntimeExecutor) invokeStream(ctx context.Context, req ai.Request, requestID, model string, g *runtimegraph.Graph) (string, ai.ProviderUsage, error) {
	var reasoningStartedAt time.Time
	var reasoningDuration time.Duration
	var reasoningSeen bool

	reasoningOpen := func() {
		reasoningSeen = true
		if reasoningStartedAt.IsZero() {
			reasoningStartedAt = time.Now()
		}
	}
	reasoningClose := func() {
		if !reasoningStartedAt.IsZero() {
			reasoningDuration += time.Since(reasoningStartedAt)
			reasoningStartedAt = time.Time{}
		}
	}

	// Reasoning chunks are consumed for telemetry ONLY — the verbatim text is
	// never published to the bus or exposed to the presentation layer.
	req.Stream = true
	req.ReasoningHandler = func(_ string) error {
		reasoningOpen()
		return nil
	}

	// provider.waiting: the round-trip is in flight before the first byte.
	g.BeginWaiting(model)
	began := time.Now()
	rawStream, err := x.provider.ExecuteStream(ctx, req)
	if err != nil || rawStream == nil {
		// Streaming is not available (the provider failed to start a stream
		// before any byte, or lacks one entirely): revert to the non-streaming
		// Execute. The invocation stays observable via model.invoked →
		// provider.response and never double-bills — the failed stream
		// consumed nothing. `where possible` is honoured: providers that
		// stream get the full lifecycle, others stay correct.
		resp, cerr := x.provider.Execute(ctx, req)
		if cerr != nil {
			return "", ai.ProviderUsage{}, cerr
		}
		if resp == nil {
			return "", ai.ProviderUsage{}, fmt.Errorf("executor: provider returned an empty response")
		}
		usage := resp.Usage
		if !usage.Known && (resp.TokenInput > 0 || resp.TokenOutput > 0) {
			// Legacy usage transport: some adapters/mocks report usage on the
			// response fields rather than the ProviderUsage record.
			usage = ai.ProviderUsage{
				PromptTokens:     resp.TokenInput,
				CompletionTokens: resp.TokenOutput,
				Known:            true,
			}
		}
		return ai.VisibleCompletion(resp.Content), usage, nil
	}
	defer func() { _ = rawStream.Close() }()

	// Authoritative live usage: only provider-reported counts (Known &&
	// !Estimated) are ever emitted as provider.usage_update.
	var usageUp ai.UsageProvider
	if up, ok := rawStream.(ai.UsageProvider); ok {
		usageUp = up
	}
	var lastUsage ai.ProviderUsage
	emitUsage := func() {
		if usageUp == nil {
			return
		}
		u := usageUp.Usage()
		if !u.Known || u.Estimated {
			return
		}
		if u.PromptTokens == lastUsage.PromptTokens && u.CompletionTokens == lastUsage.CompletionTokens &&
			u.ReasoningTokens == lastUsage.ReasoningTokens {
			return
		}
		lastUsage = u
		g.UpdateUsage(model, u.PromptTokens, u.CompletionTokens, u.ReasoningTokens)
	}

	var content strings.Builder
	// reasoningBuf is the reasoning fallback ONLY for models that emit their
	// whole answer inside reasoning_content. It is never published.
	var reasoningBuf strings.Builder
	firstToken := false
	runeBuf := stream.NewRuneBuffer()
	classifier := stream.NewClassifier()

	emitFrame := func(tok stream.Token) {
		if tok.Text == "" {
			return
		}
		if !firstToken {
			firstToken = true
			g.FirstToken(model, time.Since(began))
		}
		if tok.Kind == stream.TokenKindThinking {
			reasoningOpen()
			reasoningBuf.WriteString(tok.Text)
			return
		}
		reasoningClose()
		content.WriteString(tok.Text)
		// stream_delta is evidence transport: a dropped delta never loses
		// execution truth because the content accumulates here regardless.
		g.StreamDelta(tok.Text)
	}

	flushStream := func() {
		if rem := runeBuf.Flush(); rem != "" {
			classifier.Write(rem, emitFrame)
		}
		classifier.Flush(emitFrame)
	}

	buf := make([]byte, 4096)
	for {
		if cerr := ctx.Err(); cerr != nil {
			reasoningClose()
			return content.String(), lastUsage, cerr
		}
		n, rerr := rawStream.Read(buf)
		if n > 0 {
			if text := runeBuf.Write(buf[:n]); text != "" {
				classifier.Write(text, emitFrame)
			}
			emitUsage()
		}
		if rerr == io.EOF {
			flushStream()
			reasoningClose()
			break
		}
		if rerr != nil {
			flushStream()
			reasoningClose()
			if cerr := ctx.Err(); cerr != nil {
				return content.String(), lastUsage, cerr
			}
			return content.String(), lastUsage, rerr
		}
	}

	usage := lastUsage
	if usageUp != nil {
		if u := usageUp.Usage(); u.Known {
			usage = u
		}
	}
	// Reasoning telemetry: duration + provider-reported token count only.
	if reasoningSeen && (reasoningDuration > 0 || usage.ReasoningTokens > 0) {
		g.ReasoningTelemetry(model, reasoningDuration, usage.ReasoningTokens)
	}
	visible := ai.VisibleCompletion(content.String())
	if strings.TrimSpace(visible) == "" && reasoningBuf.Len() > 0 {
		visible = ai.VisibleCompletion(reasoningBuf.String())
	}
	return visible, usage, nil
}

// emitContextActivity surfaces a real runtime file read as engine evidence
// through the existing activity/event loggers (wired by the UI at startup).
// It fires ONLY when the runtime actually reads the file, and is a no-op when
// no logger is attached (headless/harness).
func (x *RuntimeExecutor) emitContextActivity(target string, bytes int) {
	if globalActivityLog != nil {
		globalActivityLog("[runtime] reading %s (%d bytes)", target, bytes)
	}
	if globalEventLog != nil {
		globalEventLog(retrieval.FileReadEvent{File: target, Bytes: int64(bytes)})
	}
}

// readOnlySystemPrompt selects the bounded system prompt for a read-only
// strategy. It is the runtime's decision, never the UI's.
func readOnlySystemPrompt(s strategy.ExecutionStrategy) string {
	switch s {
	case strategy.RepositoryInvestigation:
		return "You are the repository investigation engine. Produce a root-cause analysis from the provided repository evidence. Be concrete and cite the evidence. Never modify files."
	case strategy.MultiFilePlanning:
		return "You are the execution planner. Produce a concrete, structured execution plan (files, tasks, rationale) for the requested change. Never modify files."
	case strategy.DirectResponse:
		return "You are IZEN. Answer the user's greeting or casual question directly and concisely. You are not an agent here — no files, no planning, no tooling."
	default:
		return "You are the understanding engine. Answer the user's question using only the provided context. Never modify files."
	}
}

// artifactKindFor names the artifact a read-only strategy produces.
func artifactKindFor(profile strategy.ExecutionStrategyProfile) string {
	switch profile.Strategy {
	case strategy.RepositoryInvestigation:
		return "investigation"
	case strategy.MultiFilePlanning:
		return "plan"
	case strategy.DirectResponse:
		return "response"
	default:
		return "explanation"
	}
}

// firstTarget returns the first resolved target, or "".
func firstTarget(targets []string) string {
	if len(targets) == 0 {
		return ""
	}
	return targets[0]
}

// skipTail marks every stage after context compilation as explicitly skipped
// with the given reason (clarification / deterministic paths never reach the
// model or mutation boundaries). Skipped stages emit no canonical event — they
// are honest "not reached", never fabricated progress.
func skipTail(g *runtimegraph.Graph, reason string) {
	for _, kind := range []runtimegraph.StageKind{
		runtimegraph.StageContextCompilation,
		runtimegraph.StageModelInvocation,
		runtimegraph.StageArtifactValidation,
		runtimegraph.StageApprovalGate,
		runtimegraph.StageMutationTransaction,
		runtimegraph.StageVerification,
	} {
		g.Skip(kind, reason)
	}
}

// setProofGraph folds the runtime graph's stage evidence into the proof: the
// authoritative RuntimeGraph record plus the compact GraphStep projection.
func setProofGraph(res *ExecutionResult, g *runtimegraph.Graph) {
	if res == nil || res.Proof == nil || g == nil {
		return
	}
	evidence := g.Evidence()
	res.Proof.RuntimeGraph = evidence
	steps := make([]GraphStep, 0, len(evidence))
	for _, e := range evidence {
		steps = append(steps, GraphStep{Stage: e.Kind, State: e.State, Started: e.StartedAt})
	}
	res.Proof.Graph = steps
}

// finalizeResult stamps the authoritative terminal usage account (provider,
// model, aggregate input/output tokens, latency, artifact) onto the result. It
// is the SINGLE place token accounting is computed — from the provider-reported
// ModelInvocations — so the renderer reads ExecutionResult.Completed and never
// re-sums usage. It must be called on every terminal return path of the
// executor (Execute / Approve / Reject).
func (x *RuntimeExecutor) finalizeResult(res *ExecutionResult) *ExecutionResult {
	if res == nil {
		return nil
	}
	cc := res.Completed
	cc.Provider = x.providerName()
	for _, inv := range res.ModelCalls {
		if cc.Model == "" {
			cc.Model = inv.Model
		}
		if inv.Known {
			// The usage is authoritative (provider-reported): aggregate the
			// counts and mark the account known. The account is Known when at
			// least one invocation reported usage (the reported counts are
			// real billing); it stays Unknown only when NO invocation reported
			// usage, so "usage unknown" is never rendered as a genuine zero.
			cc.InputTokens += inv.TokenInput
			cc.OutputTokens += inv.TokenOutput
			cc.CachedTokens += inv.CachedTokens
			cc.ReasoningTokens += inv.ReasoningTokens
			cc.Known = true
		}
	}
	if res.Proof != nil && !res.Proof.StartedAt.IsZero() {
		cc.Latency = time.Since(res.Proof.StartedAt)
		if cc.Latency < 0 {
			cc.Latency = 0
		}
	}
	cc.Artifact = res.ArtifactKind
	res.Completed = cc
	return res
}

// providerName returns the bound provider's name ("" when none).
func (x *RuntimeExecutor) providerName() string {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.provider == nil {
		return ""
	}
	return x.provider.Name()
}

// contextDecisions records the strategy-owned context decisions of the
// execution into the proof: the policy, the budget, and why each context
// channel was included. The compiler never includes context it cannot explain.
func contextDecisions(profile strategy.ExecutionStrategyProfile) []ContextDecision {
	budget := profile.ContextBudget
	dec := ContextDecision{
		Policy: string(profile.Policy()),
		Budget: budget.Tokens,
	}
	for _, k := range profile.ContextKinds {
		dec.Items = append(dec.Items, ContextItemDecision{
			Kind:   string(k),
			Reason: contextInclusionReason(k),
		})
	}
	return []ContextDecision{dec}
}

// contextInclusionReason is the deterministic inclusion reason of a context
// channel — the single explanation source for both the compiler and the proof.
func contextInclusionReason(k strategy.ContextKind) string {
	switch k {
	case strategy.ContextUserIntent:
		return "every execution is anchored to the user intent"
	case strategy.ContextExplicitTargets:
		return "explicit targets bound the execution scope"
	case strategy.ContextTargetContent:
		return "the mutation requires the target content"
	case strategy.ContextStructuralEvidence:
		return "the mutation anchors to a located block"
	case strategy.ContextDependencyEvidence:
		return "cross-file coupling must be visible before reasoning"
	case strategy.ContextRepositoryConstraints:
		return "the execution must honor the workspace contract"
	case strategy.ContextRelevantHistory:
		return "prior conversation directly relevant to the decision"
	case strategy.ContextPriorExecution:
		return "execution continuity preserves prior evidence"
	case strategy.ContextArtifactContract:
		return "the model must produce a bounded parseable artifact"
	case strategy.ContextVerificationContract:
		return "success is defined by the verification gate"
	default:
		return string(k)
	}
}

// ── helpers ────────────────────────────────────────────────────────────────

// maxExecutorContextBytes bounds the context a target contributes to the
// prompt so a single execution can never swallow an unbounded file.
const maxExecutorContextBytes = 200 * 1024

// estimateTokens is a coarse context-accounting heuristic (chars/4). It is
// NEVER used for provider billing — authoritative usage comes from the
// provider's Usage record.
func estimateTokens(s string) int {
	if s == "" {
		return 0
	}
	return len(s) / 4
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func boundedMutationSystemPrompt() string {
	return `You are the bounded mutation engine. You modify one target file to satisfy
the user's request. Output ONLY the changed file content in one of these forms:

1. SEARCH/REPLACE block:
<<<<<<< SEARCH
<exact lines from the current file>
=======
<replacement lines>
>>>>>>>

2. A unified diff with @@ hunk headers.

3. The full modified file content.

Never explain. Never add markdown outside a single code fence. Preserve every
unrelated line byte-for-byte.`
}

func buildMutationUserPrompt(request, target, original, evidence string) string {
	var b strings.Builder
	b.WriteString("### USER REQUEST\n")
	b.WriteString(request)
	b.WriteString("\n\n### EVIDENCE LEDGER\n")
	if strings.TrimSpace(evidence) == "" {
		b.WriteString("(no deterministic evidence compiled — resolve from the target content below)\n")
	} else {
		// The deterministic evidence ledger is authoritative: structural
		// findings, redundancy blocks and line ranges the model must reason
		// over. It never re-discovers deterministic facts from raw text.
		b.WriteString(evidence)
	}
	b.WriteString("\n\n### TARGET FILE: ")
	b.WriteString(target)
	b.WriteString("\n")
	if strings.TrimSpace(original) == "" {
		b.WriteString("(file is empty or does not exist yet — provide the full new content)\n")
	} else {
		b.WriteString("```\n")
		b.WriteString(original)
		b.WriteString("\n```\n")
	}
	return b.String()
}

// executorWorkspace adapts the workspace root to the strategy selector's
// deterministic Workspace contract (existence + bounded fuzzy lookup only).
type executorWorkspace struct {
	root string
}

func (w executorWorkspace) Root() string { return w.root }

func (w executorWorkspace) Exists(path string) bool {
	if w.root == "" {
		_, err := os.Stat(path)
		return err == nil
	}
	return fileExists(filepath.Join(w.root, path))
}

func (w executorWorkspace) ResolveFuzzy(name string, max int) []string {
	root := w.root
	if root == "" {
		root = "."
	}
	var out []string
	visited := 0
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return filepath.SkipDir
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".izen", "node_modules", "vendor", ".venv", "venv":
				return filepath.SkipDir
			}
			return nil
		}
		visited++
		if visited > 2000 {
			return filepath.SkipAll
		}
		if len(out) >= max {
			return filepath.SkipAll
		}
		if strings.EqualFold(d.Name(), name) {
			if rel, rerr := filepath.Rel(root, path); rerr == nil {
				out = append(out, filepath.ToSlash(rel))
			}
		}
		return nil
	})
	return out
}
