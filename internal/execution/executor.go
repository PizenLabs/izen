package execution

import (
	"context"
	"errors"
	"fmt"
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
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution/strategy"
	"github.com/PizenLabs/izen/internal/language"
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
}

// ModelInvocation records one provider call with its authoritative usage.
type ModelInvocation struct {
	Model       string `json:"model"`
	TokenInput  int    `json:"token_input"`
	TokenOutput int    `json:"token_output"`
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
	RequestID        string             `json:"request_id"`
	Strategy         string             `json:"strategy"`
	StrategyReason   string             `json:"strategy_reason"`
	Targets          []string           `json:"targets"`
	Graph            []GraphStep        `json:"graph"`
	ModelInvocations []ModelInvocation  `json:"model_invocations"`
	Mutations        []MutationEvidence `json:"mutations"`
	Verification     VerificationReport `json:"verification"`
	Outcome          MutationOutcome    `json:"outcome"`
	StartedAt        time.Time          `json:"started_at"`
	FinishedAt       time.Time          `json:"finished_at"`
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
	// Err is the terminal execution error, if any.
	Err error
}

// pendingMutation is the approval-held state of a targeted mutation.
type pendingMutation struct {
	requestID      string
	mode           string
	target         string
	original       string
	patch          *Patch
	ms             *MutationSet
	strategy       string
	strategyReason string
	targets        []string
	modelCalls     []ModelInvocation
	startedAt      time.Time
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
// non-empty a language-aware verifier is attached; otherwise verification is
// attached with the default Go steps. A nil provider makes model-required
// strategies fail with a deterministic error (the runtime still resolves
// deterministic strategies without a provider).
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

// Execute runs the deterministic execution flow for req. For a targeted
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

	start := time.Now()
	x.emit(events.NewExecutionStarted(requestID, req.Mode, req.Prompt))
	res.Proof.Graph = append(res.Proof.Graph, GraphStep{Stage: "resolve_target", State: "started", Started: time.Now()})

	// ── 1. Strategy selection ──────────────────────────────────────────
	profile, err := x.selectStrategy(ctx, req)
	if err != nil {
		x.fail(ctx, res, err)
		return res, err
	}
	res.Strategy = string(profile.Strategy)
	res.StrategyReason = profile.StrategyReason
	res.Proof.Strategy = res.Strategy
	res.Proof.StrategyReason = profile.StrategyReason
	x.emit(events.NewStrategySelected(requestID, res.Strategy, profile.ModelRequired, profile.StrategyReason))
	res.Proof.Graph = append(res.Proof.Graph, GraphStep{Stage: "strategy_selected", State: res.Strategy, Started: time.Now()})

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
		exists := fileExists(filepath.Join(x.root, t))
		x.emit(events.NewTargetResolved(requestID, t, exists, "strategy"))
	}

	// ── 3. Human clarification (no model, no mutation) ────────────────
	// HumanClarification is authoritative and MUST be handled before the
	// deterministic branch: the strategy carries Deterministic=true but its
	// outcome is a human stop, never a completed execution. The human is the
	// authority; no file is read into a prompt and no mutation is proposed.
	if profile.Strategy == strategy.HumanClarification {
		res.ClarificationRequired = true
		res.Proof.Outcome = OutcomeCancelled
		res.Proof.FinishedAt = time.Now()
		x.emit(events.NewExecutionFinished(requestID, false, "clarification_required"))
		return res, nil
	}

	// ── 4. Deterministic strategies (zero model) ───────────────────────
	if profile.Deterministic {
		res.Proof.Graph = append(res.Proof.Graph, GraphStep{Stage: "deterministic", State: "completed", Started: time.Now()})
		res.Proof.Outcome = OutcomeNoArtifact
		res.Proof.FinishedAt = time.Now()
		x.emit(events.NewExecutionFinished(requestID, true, "deterministic"))
		return res, nil
	}

	// ── 5. Target-bound clarification ─────────────────────────────────
	// A target-bound strategy whose target cannot be resolved stops before any
	// invocation. Read-only strategies may run without a target set.
	if len(targets) == 0 && profile.Strategy != strategy.TargetedReasoning &&
		profile.Strategy != strategy.MultiFilePlanning &&
		profile.Strategy != strategy.RepositoryInvestigation {
		res.ClarificationRequired = true
		res.Proof.Outcome = OutcomeCancelled
		res.Proof.FinishedAt = time.Now()
		x.emit(events.NewExecutionFinished(requestID, false, "clarification_required"))
		return res, nil
	}

	if x.provider == nil {
		x.fail(ctx, res, fmt.Errorf("executor: strategy %q requires a provider but none is configured", res.Strategy))
		return res, res.Err
	}

	// ── 6. Context compilation ─────────────────────────────────────────
	contextChannels, contextTokens := x.compileContext(targets)
	x.emit(events.NewContextPrepared(requestID, contextChannels, contextTokens))
	res.Proof.Graph = append(res.Proof.Graph, GraphStep{Stage: "context_prepared", State: "completed", Started: time.Now()})

	// ── 7. Read-only strategies (targeted_reasoning / multi_file_planning /
	// repository_investigation): one bounded invocation, content returned, no
	// mutation path, no approval surface. ───────────────────────────────
	if profile.Strategy != strategy.TargetedMutation {
		content, inv, err := x.invokeReadOnly(ctx, req, profile, targets)
		res.ModelCalls = append(res.ModelCalls, inv)
		res.Proof.ModelInvocations = append(res.Proof.ModelInvocations, inv)
		if err != nil {
			res.Err = err
			res.Proof.Outcome = OutcomeFailed
			res.Proof.FinishedAt = time.Now()
			x.emit(events.NewExecutionFailed(events.FailureRecoverable, err, "executor.model"))
			x.emit(events.NewExecutionFinished(requestID, false, string(OutcomeFailed)))
			return res, err
		}
		x.emit(events.NewModelInvoked(requestID, inv.Model, inv.TokenInput, inv.TokenOutput))
		res.ArtifactKind = artifactKindFor(profile)
		res.Content = content
		x.emit(events.NewArtifactProduced(requestID, res.ArtifactKind, firstTarget(targets)))
		res.Proof.Graph = append(res.Proof.Graph, GraphStep{Stage: "artifact_produced", State: res.ArtifactKind, Started: time.Now()})
		res.Proof.Outcome = OutcomeCompleted
		res.Proof.FinishedAt = time.Now()
		x.emit(events.NewExecutionFinished(requestID, true, string(OutcomeCompleted)))
		return res, nil
	}

	// ── 7. Targeted mutation: model invocation ─────────────────────────
	original, modified, raw, inv, err := x.invokeMutation(ctx, req, targets)
	res.ModelCalls = append(res.ModelCalls, inv)
	res.Proof.ModelInvocations = append(res.Proof.ModelInvocations, inv)
	if err != nil {
		res.ArtifactKind = ""
		res.Err = err
		res.Proof.Outcome = OutcomePatchGenerationFailed
		res.Proof.FinishedAt = time.Now()
		x.emit(events.NewExecutionFailed(events.FailureRecoverable, err, "executor.model"))
		x.emit(events.NewExecutionFinished(requestID, false, string(OutcomePatchGenerationFailed)))
		return res, err
	}
	x.emit(events.NewModelInvoked(requestID, inv.Model, inv.TokenInput, inv.TokenOutput))

	// ── 8. Artifact production ─────────────────────────────────────────
	target := targets[0]
	patch := &Patch{
		ID:       fmt.Sprintf("%s-patch", requestID),
		File:     target,
		Original: original,
		Modified: modified,
	}
	res.ArtifactKind = "patch"
	res.Original = original
	res.Content = modified
	res.Diff = x.compileDiff(raw, target, original)
	x.emit(events.NewArtifactProduced(requestID, "patch", target))
	res.Proof.Graph = append(res.Proof.Graph, GraphStep{Stage: "artifact_produced", State: "patch", Started: time.Now()})

	// ── 9. Approval gate ───────────────────────────────────────────────
	pm := &pendingMutation{
		requestID:      requestID,
		mode:           req.Mode,
		target:         target,
		original:       original,
		patch:          patch,
		strategy:       res.Strategy,
		strategyReason: res.StrategyReason,
		targets:        targets,
		modelCalls:     res.ModelCalls,
		startedAt:      start,
	}
	x.mu.Lock()
	x.pending[patch.ID] = pm
	x.mu.Unlock()
	res.PendingPatchID = patch.ID
	res.Proof.Outcome = OutcomeNoArtifact // pending approval
	res.Proof.FinishedAt = time.Now()
	x.emit(events.NewApprovalRequired(requestID, target, res.Diff))
	return res, nil
}

// Approve resolves the approval gate: it applies the held patch through the
// PatchManager (owning the filesystem write + verification gate), commits the
// MutationSet, and returns the terminal result with evidence.
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
		Content:        pm.patch.Modified,
		Proof: &ExecutionProof{
			RequestID:        pm.requestID,
			Strategy:         pm.strategy,
			StrategyReason:   pm.strategyReason,
			Targets:          pm.targets,
			ModelInvocations: pm.modelCalls,
			StartedAt:        pm.startedAt,
		},
	}

	// The runtime owns a fresh mutation boundary for this apply.
	ms := NewMutationSet()
	x.patches.SetMutationSet(ms)
	x.patches.SetAuthorization(x.auth)
	if x.verifier != nil {
		x.verifier.SetAuthorization(x.auth)
	}
	pm.ms = ms

	x.emit(events.NewMutationStarted(pm.requestID, pm.targets))
	res.Proof.Graph = append(res.Proof.Graph, GraphStep{Stage: "mutate", State: "started", Started: time.Now()})

	applyCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	if err := x.patches.ApplyContext(applyCtx, pm.patch); err != nil {
		_ = ms.RollbackTo(MutationFailed)
		outcome := OutcomeApplyFailed
		if ms.OutcomeFor(pm.target) != OutcomeNoArtifact {
			outcome = ms.OutcomeFor(pm.target)
		}
		res.Mutations = append(res.Mutations, ms.Outcomes...)
		res.Proof.Mutations = res.Mutations
		res.Proof.Outcome = outcome
		res.Proof.FinishedAt = time.Now()
		x.emit(events.NewMutationCompleted(pm.requestID, pm.target, string(outcome)))
		x.emit(events.NewExecutionFailed(events.FailureRecoverable, err, "executor.mutation"))
		x.emit(events.NewExecutionFinished(pm.requestID, false, string(outcome)))
		res.Err = err
		return res, err
	}

	outcome := OutcomeChanged
	if ms.OutcomeFor(pm.target) != OutcomeNoArtifact {
		outcome = ms.OutcomeFor(pm.target)
	}
	_ = ms.Commit()
	res.Mutations = append(res.Mutations, ms.Outcomes...)
	res.Proof.Mutations = res.Mutations
	res.Proof.Outcome = outcome
	res.Proof.Graph = append(res.Proof.Graph, GraphStep{Stage: "mutate", State: "committed", Started: time.Now()})

	// ── Verification evidence comes from the real verifier ─────────────
	if x.verifier != nil {
		//nolint:contextcheck // Verifier.RunAll predates context propagation; the apply is already bounded.
		report := x.verifier.RunAll()
		res.Verification = report
		res.Proof.Verification = report
		var steps []string
		for _, s := range report.Results {
			steps = append(steps, s.Step.Name)
		}
		x.emit(events.NewVerificationCompleted(pm.requestID, report.Passed, steps))
		res.Proof.Graph = append(res.Proof.Graph, GraphStep{Stage: "verify", State: fmt.Sprintf("passed=%t", report.Passed), Started: time.Now()})
	}

	x.emit(events.NewMutationCompleted(pm.requestID, pm.target, string(outcome)))
	x.emit(events.NewExecutionFinished(pm.requestID, true, string(outcome)))
	res.Proof.FinishedAt = time.Now()
	return res, nil
}

// Reject resolves the approval gate negatively: the held patch is never
// applied and the mutation boundary is rolled back (restoring any recorded
// snapshot — none exist yet, since apply never ran).
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
	ms := NewMutationSet()
	if pm.patch != nil {
		_ = ms.Record(pm.patch.File)
	}
	_ = ms.RollbackTo(MutationCancelled)
	res.Proof.Mutations = ms.Outcomes
	res.Proof.Outcome = OutcomeCancelled
	res.Proof.FinishedAt = time.Now()
	res.Proof.Graph = append(res.Proof.Graph, GraphStep{Stage: "approval", State: "rejected", Started: time.Now()})
	x.emit(events.NewExecutionFinished(pm.requestID, false, string(OutcomeCancelled)))
	return res, nil
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
// target set: the bounded target content (owner: runtime). It returns the
// context channel names and a token estimate. Provider usage is never
// estimated here — this is context-accounting, not billing.
func (x *RuntimeExecutor) compileContext(targets []string) (channels []string, tokens int) {
	channels = append(channels, "user_intent", "explicit_targets", "target_content")
	var b strings.Builder
	for _, t := range targets {
		path := filepath.Join(x.root, t)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if len(data) > maxExecutorContextBytes {
			data = data[:maxExecutorContextBytes]
		}
		b.Write(data)
		b.WriteByte('\n')
	}
	return channels, estimateTokens(b.String())
}

func (x *RuntimeExecutor) invokeMutation(ctx context.Context, req ExecuteRequest, targets []string) (original, modified, raw string, inv ModelInvocation, err error) {
	target := targets[0]
	path := filepath.Join(x.root, target)
	data, readErr := os.ReadFile(path)
	if readErr != nil && !os.IsNotExist(readErr) {
		return "", "", "", inv, fmt.Errorf("executor: read target %s: %w", target, readErr)
	}
	original = string(data)

	if x.provider == nil {
		return original, "", "", inv, fmt.Errorf("executor: no provider configured for model invocation")
	}

	system := boundedMutationSystemPrompt()
	user := buildMutationUserPrompt(req.Prompt, target, original)
	model, modelErr := x.resolveModel()
	if modelErr != nil {
		return original, "", "", inv, modelErr
	}
	aiReq := ai.Request{
		Model:     model,
		System:    system,
		Messages:  []ai.Message{{Role: "user", Content: user}},
		MaxTokens: req.MaxOutputTokens,
	}
	resp, callErr := x.provider.Execute(ctx, aiReq)
	if callErr != nil {
		return original, "", "", inv, fmt.Errorf("executor: model invocation: %w", callErr)
	}
	if resp == nil {
		return original, "", "", inv, fmt.Errorf("executor: model returned an empty response")
	}
	inv.Model = model
	if resp.Usage.Known {
		inv.TokenInput = resp.Usage.PromptTokens
		inv.TokenOutput = resp.Usage.CompletionTokens
	} else {
		inv.TokenInput = resp.TokenInput
		inv.TokenOutput = resp.TokenOutput
	}

	raw = strings.TrimSpace(resp.Content)
	modified = ResolveModifiedContent(original, raw)
	if modified == "" {
		// The model returned only prose or a fence without content — treat the
		// full response as the replacement attempt (best-effort).
		modified = raw
	}
	return original, modified, raw, inv, nil
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
// strategies (targeted_reasoning, multi_file_planning,
// repository_investigation). It returns the produced content; no mutation path
// and no approval surface exist. ProviderResponse → Artifact → ExecutionResult:
// the response is owned by the runtime and never reaches the UI raw.
func (x *RuntimeExecutor) invokeReadOnly(ctx context.Context, req ExecuteRequest, profile strategy.ExecutionStrategyProfile, targets []string) (content string, inv ModelInvocation, err error) {
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
	resp, callErr := x.provider.Execute(ctx, aiReq)
	if callErr != nil {
		return "", inv, fmt.Errorf("executor: read-only invocation: %w", callErr)
	}
	if resp == nil {
		return "", inv, fmt.Errorf("executor: model returned an empty response")
	}
	inv.Model = model
	if resp.Usage.Known {
		inv.TokenInput = resp.Usage.PromptTokens
		inv.TokenOutput = resp.Usage.CompletionTokens
	} else {
		inv.TokenInput = resp.TokenInput
		inv.TokenOutput = resp.TokenOutput
	}
	return strings.TrimSpace(resp.Content), inv, nil
}

// readOnlySystemPrompt selects the bounded system prompt for a read-only
// strategy. It is the runtime's decision, never the UI's.
func readOnlySystemPrompt(s strategy.ExecutionStrategy) string {
	switch s {
	case strategy.RepositoryInvestigation:
		return "You are the repository investigation engine. Produce a root-cause analysis from the provided repository evidence. Be concrete and cite the evidence. Never modify files."
	case strategy.MultiFilePlanning:
		return "You are the execution planner. Produce a concrete, structured execution plan (files, tasks, rationale) for the requested change. Never modify files."
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

func (x *RuntimeExecutor) fail(_ context.Context, res *ExecutionResult, err error) {
	res.Err = err
	res.Proof.Outcome = OutcomeFailed
	res.Proof.FinishedAt = time.Now()
	x.emit(events.NewExecutionFailed(events.FailureRecoverable, err, "executor"))
	x.emit(events.NewExecutionFinished(res.RequestID, false, string(OutcomeFailed)))
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

func buildMutationUserPrompt(request, target, original string) string {
	var b strings.Builder
	b.WriteString("### USER REQUEST\n")
	b.WriteString(request)
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
