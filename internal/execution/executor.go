package execution

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
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
	"github.com/PizenLabs/izen/internal/execution/ingestion"
	"github.com/PizenLabs/izen/internal/execution/strategy"
	"github.com/PizenLabs/izen/internal/language"
	"github.com/PizenLabs/izen/internal/retrieval"
	"github.com/PizenLabs/izen/pkg/capability/policy"
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

// StreamEvent represents a streaming event from the provider during execution.
// It is a domain-level type — no UI framework dependencies.
type StreamEvent struct {
	RequestID string
	// Kind is the event kind: "first_token", "content_delta",
	// "reasoning_delta", "done", "error".
	Kind string
	// Content is the text content for content_delta events.
	Content string
	// FinishReason is the provider's finish_reason for done events.
	FinishReason string
	// Usage is the provider's usage for done events.
	Usage ai.ProviderUsage
	// Err is the error for error events.
	Err error
}

// StreamCallback is an optional callback invoked during provider streaming.
// It is called from the executor's streaming goroutine for each content delta,
// first token, and completion. The callback must be non-blocking and return
// quickly. It allows the UI to receive incremental streaming progress without
// the executor depending on any UI framework.
type StreamCallback func(StreamEvent)

// ExecuteRequest is a user execution submitted to the runtime.
type ExecuteRequest struct {
	// RequestID correlates every lifecycle event of this execution. Empty
	// yields a deterministic auto-generated ID.
	RequestID string
	// SessionID is the originating session correlation (INV-SESSION-10). When
	// empty, the runtime resolves it from the wired session resolver at
	// admission. It is stamped onto the execution proof, the terminal evidence
	// and the canonical lifecycle events so mutation traces, token usage and
	// tool invocations map strictly to their originating session.
	SessionID string
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
	// Context is the frozen, integrity-sealed execution context snapshot
	// created at intent time (IntentGateway.Gate). Admission verifies the seal
	// and its binding to this request's declared context fields BEFORE any
	// execution stage runs: a snapshot that was modified mid-flight — or a
	// request whose prompt/targets/evidence diverge from the frozen payload —
	// fails closed with ErrContextIntegrity. When nil (direct callers that
	// bypassed the gateway), admission freezes the context fresh from this
	// request's own declared fields; middleware can never rebuild or substitute
	// prompt context after that point.
	Context *ContextSnapshot
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
	// RecoveryStrategy / RecoveryAttempt carry the autonomy recovery decision
	// into the execution contract (same handoff pattern as Intent above).
	// RecoveryStrategy "bounded_patch" selects the strict bounded-patch
	// protocol — runtime-derived windowed context and a SEARCH/REPLACE-only
	// output contract enforced at the artifact boundary. RecoveryAttempt is
	// the 1-indexed attempt number (0 = initial) used for deterministic
	// window rotation across repair cycles.
	RecoveryStrategy string
	RecoveryAttempt  int
	// RecoveryOf declares CAUSAL RECOVERY (Phase 2 P2): the failed parent
	// ContractID this execution recovers from. The runtime resolves the
	// recovery at admission — instantiating a NEW contract with an explicit
	// back-pointer and the parent's full ancestry — and enforces the bounded
	// chain limit (MaxRecoveryChainDepth). A unknown parent fails closed with
	// ErrUnknownParentContract; an exhausted chain with
	// ErrRecoveryChainExhausted. Failed contracts are never rewritten: every
	// recovery is an append-only causal step.
	RecoveryOf string
	// StagedSubTasks carries the sub-task windows of an approved decomposition
	// plan when this execution is ONE unit of a staged ExecutionDAG. When
	// non-empty, Boundary 2 evaluates every staged sub-task individually and
	// SUPPRESSES the monolithic full-file rewrite estimation — a plan-scoped
	// submission can never be refused for the size of the whole target it was
	// decomposed from.
	StagedSubTasks []SubTaskScope
	// NoOpEscalation marks this invocation as a NO-OP escalation attempt: the
	// previous attempt answered NO_CHANGES_REQUIRED while structural analysis
	// indicated work remains. The bounded-patch context window is WIDENED
	// (broader boundary window) so the re-hydrated attempt sees materially
	// more of the assigned slice before judging again.
	NoOpEscalation bool
	// FocusStartLine / FocusEndLine optionally pin the deterministic
	// bounded-patch context window to one sub-task's assigned inclusive
	// 1-indexed line interval (decomposition DAG execution). When both are
	// positive and start <= end, the copyable source shown to — and anchored
	// by — the model is derived from exactly that region instead of rotating
	// across the whole file: a unit can neither see nor patch another unit's
	// content, which removes the local-context blindness behind false
	// NO_CHANGES_REQUIRED claims. Zero values keep the pre-existing whole-file
	// window rotation.
	FocusStartLine int
	FocusEndLine   int
	// StreamCallback is an optional callback for incremental streaming progress.
	// When set, the executor invokes it during provider streaming for each
	// content delta, first token, and completion. This enables the UI to
	// render streaming output without the executor depending on any UI framework.
	StreamCallback StreamCallback
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
	FinishReason    string `json:"finish_reason,omitempty"`
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
	// SessionID correlates the execution with its originating session
	// (INV-SESSION-10). It is stamped at admission from the request or the
	// wired session resolver.
	SessionID string `json:"session_id,omitempty"`
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
	Intent           string  `json:"intent,omitempty"`
	IntentConfidence float64 `json:"intent_confidence,omitempty"`
	TargetConfidence float64 `json:"target_confidence,omitempty"`
	Scope            string  `json:"scope,omitempty"`
	// RiskScope records the deterministic blast-radius tier the admission
	// evaluator assigned to the intent (Phase 1 P1). Empty when execution was
	// rejected before evaluation completed.
	RiskScope string `json:"risk_scope,omitempty"`
	// ContextID / ContextDigest / ContextParentID record the verified context
	// lineage of the execution: which frozen snapshot the executor ran under,
	// its sealed digest and its causal ancestor. Callers cannot inject or
	// rebuild prompt context mid-execution — the proof names the exact payload
	// that crossed.
	ContextID       string `json:"context_id,omitempty"`
	ContextDigest   string `json:"context_digest,omitempty"`
	ContextParentID string `json:"context_parent_id,omitempty"`
	// Contract identity (Phase 2 P2): WHICH unique execution intent ran and
	// WHICH invocation attempt of it. The identity is computed at admission
	// from the sealed context + strategy + targets — retries keep the
	// ContractID while AttemptID increments; parameter/strategy changes fork a
	// new contract. Identity is immutable once stamped here.
	ContractID       string          `json:"contract_id,omitempty"`
	AttemptID        uint32          `json:"attempt_id,omitempty"`
	ParentContractID string          `json:"parent_contract_id,omitempty"`
	CausalAncestry   []string        `json:"causal_ancestry,omitempty"`
	Outcome          MutationOutcome `json:"outcome"`
	StartedAt        time.Time       `json:"started_at"`
	FinishedAt       time.Time       `json:"finished_at"`
	// OccAborted records a Phase 3 OCC commit-gate abort: the pre-commit
	// verification found the target state diverged from the admitted baseline.
	// The evidence outcome is ABORTED_OCC (never derived by the coarse outcome
	// mapper) and the mutations are tainted.
	OccAborted bool `json:"occ_aborted,omitempty"`
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
	// SessionID is the originating session correlation (INV-SESSION-10).
	SessionID string
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
	// SessionID is the originating session correlation (INV-SESSION-10).
	SessionID string
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
	// Evidence is the authoritative IMMUTABLE terminal record of the
	// execution (Phase 2 P2). It is sealed by the runtime at every terminal
	// path and is the SOLE artifact downstream projectors may consume to
	// derive terminal execution truth. It is nil while the execution is still
	// held at the approval gate (not yet terminated).
	Evidence *ExecutionEvidence
	// IngestionTrace is the forensic transport-normalization record of the raw
	// LLM response that produced this execution's artifact. It preserves the
	// exact unmutated model output and every transport transformation, and is
	// attached to the sealed ExecutionEvidence for post-mortem traceability.
	IngestionTrace *ingestion.IngestionTrace
	// Completed is the authoritative terminal usage account computed by the
	// runtime from the provider-reported usage. The renderer reads it for the
	// footer / EXPANDED token numbers and never re-derives them.
	Completed ExecutionCompleted
	// Diagnostics carries the advisory DiagnosticSignals of every boundary
	// rejection on this execution (Boundary 2 preflight, Boundary 3 output
	// gate, Boundary 4 artifact gate). Recovery Isolation (I2): signals are
	// metadata ONLY — a rejected generation's bytes never travel on the
	// result, so no projector or recovery path can re-inject them into prompt
	// context.
	Diagnostics []DiagnosticSignal
	// Err is the terminal execution error, if any.
	Err error
}

// pendingMutation is the approval-held state of a targeted mutation.
type pendingMutation struct {
	requestID      string
	sessionID      string
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
	// contract / attempt carry the immutable identity of the held attempt
	// (Phase 2 P2): Approve/Reject resolve the SAME contract attempt, never a
	// new one. The approval gate is not a termination — no evidence exists
	// until the gate resolves.
	contract *ExecutionContract
	attempt  AttemptID
	// baseline is the Phase 3 target-scoped workspace snapshot captured at
	// admission. Approve re-validates it immediately before the commit
	// pipeline writes anything; a mismatch aborts with ABORTED_OCC and zero
	// partial writes.
	baseline *WorkspaceBaseline
	// ingestionTrace is the forensic transport-normalization record of the raw
	// LLM response that produced the held artifact. It is carried through the
	// approval gate so the terminal (committed or rejected) evidence can attach
	// it for post-mortem traceability.
	ingestionTrace *ingestion.IngestionTrace
}

// RuntimeExecutor is the runtime-owned execution boundary.
type RuntimeExecutor struct {
	root      string
	cfg       *config.Config
	provider  ai.Provider
	bus       *events.Bus
	langID    language.ID
	patches   *PatchManager
	verifier  *Verifier
	auth      *authorization.MutationAuthorization
	admission *AdmissionGateway
	// sessionID resolves the active originating session at admission when the
	// request does not carry one (INV-SESSION-10). It is wired by the
	// composition root to the SessionManager's active-session accessor.
	sessionID func() string
	// contracts is the runtime-owned contract identity ledger (Phase 2 P2):
	// it derives immutable ContractIDs at admission, increments AttemptIDs
	// deterministically across retries, and instantiates bounded causal
	// recovery contracts.
	contracts *ContractRegistry
	// occ is the runtime-owned Phase 3 optimistic-concurrency engine: it
	// fingerprints the resolved targets at admission and re-validates them
	// immediately before the commit pipeline writes anything.
	occ *OCCVerifier
	// artifactValidator is the explicit boundary that parses and validates
	// mutation artifacts. It is injected so P1 NormalizingValidator decorators
	// can wrap it without modifying execution loops.
	artifactValidator ArtifactValidator
	// mutationBoundary is the explicit workspace-integrity assertion surface
	// used after any rollback to cryptographically verify base digest recovery.
	mutationBoundary MutationBoundary
	// manifestSystemPromptOverride, when non-empty, replaces the default Pass 1
	// manifest system prompt. The autonomy layer injects the compact manifest
	// prompt (buildManifestPrompt) at bootstrap via SetManifestSystemPrompt; a
	// direct InvokeManifestPass call without injection keeps the default.
	manifestSystemPromptOverride string

	mu      sync.Mutex
	pending map[string]*pendingMutation
	counter int

	// observeSnapshot is the Observation-phase memory cache: target → content.
	// It is populated ONCE per Execute via observeTargets and is the single
	// byte source for compileContext, invokeMutation, and verification — no
	// repetitive os.ReadFile occurs. It is cleared at the end of Execute.
	observeSnapshot   map[string][]byte
	observeSnapshotMu sync.RWMutex
}

// NewRuntimeExecutor wires a self-contained execution authority. When langID is
// non-empty a language-aware verifier is attached (resolving that language's
// own configured steps); when langID is empty a plain verifier is attached with
// NO implicit steps — verification is Skipped (not applicable) unless explicit
// steps are configured (Phase 7 P1: no implicit Go fallback). A nil provider
// makes model-required strategies fail with a deterministic error (the runtime
// still resolves deterministic strategies without a provider).
func NewRuntimeExecutor(root string, cfg *config.Config, provider ai.Provider, bus *events.Bus, langID language.ID) *RuntimeExecutor {
	validator := NewNormalizingArtifactValidator(NewDefaultArtifactValidator()).WithRoot(root)
	x := &RuntimeExecutor{
		root:              root,
		cfg:               cfg,
		provider:          provider,
		bus:               bus,
		langID:            langID,
		patches:           NewPatchManager(root),
		admission:         NewAdmissionGateway(nil),
		contracts:         NewContractRegistry(),
		occ:               NewOCCVerifier(root),
		artifactValidator: validator,
		pending:           make(map[string]*pendingMutation),
	}
	// Wire the normalizer to consume the observe snapshot cache, eliminating
	// redundant os.ReadFile disk hits during artifact validation. The closure
	// captures x but is only called during Execute (after x is fully
	// constructed and observeTargets has populated the cache).
	validator.SetTargetLoader(func(target string) (string, error) {
		if data, ok := x.getSnapshotContent(target); ok {
			return string(data), nil
		}
		return "", os.ErrNotExist
	})
	if langID != "" {
		x.verifier = NewLanguageVerifier(root, langID)
	} else {
		x.verifier = NewVerifier(root)
	}
	return x
}

// SetAdmittedCapabilities replaces the capability set the runtime's admission
// boundary checks every intent against (test seam / runtime re-granting). A
// nil set restores StandardAdmittedCapabilities. Grants are never escalated
// implicitly: an intent evaluated beyond the admitted scope is rejected at
// admission and must be re-submitted.
func (x *RuntimeExecutor) SetAdmittedCapabilities(caps *AdmittedCapabilities) {
	x.admission.SetCapabilities(caps)
}

// AdmittedCapabilities returns the currently admitted capability surface
// (observability).
func (x *RuntimeExecutor) AdmittedCapabilities() AdmittedCapabilities {
	return x.admission.Capabilities()
}

// Contracts exposes the runtime-owned contract identity ledger
// (observability). Callers may read admitted contracts and attempt counters;
// all mutation authority stays inside the registry.
func (x *RuntimeExecutor) Contracts() *ContractRegistry { return x.contracts }

// OCC exposes the runtime-owned optimistic-concurrency verifier
// (observability). Callers may read its operational metrics; the baseline and
// pre-commit gate authority stays inside the executor's commit pipeline.
func (x *RuntimeExecutor) OCC() *OCCVerifier { return x.occ }

// SetVerifier overrides the attached verifier (test seam / config wiring).
func (x *RuntimeExecutor) SetVerifier(v *Verifier) {
	x.verifier = v
}

// SetArtifactValidator replaces the artifact validator (P1 extension seam).
// A nil value restores the default validator.
func (x *RuntimeExecutor) SetArtifactValidator(v ArtifactValidator) {
	if v == nil {
		v = NewDefaultArtifactValidator()
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	x.artifactValidator = v
}

// ArtifactValidator returns the currently wired artifact validator.
func (x *RuntimeExecutor) ArtifactValidator() ArtifactValidator {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.artifactValidator
}

// SetMutationBoundary replaces the workspace-integrity boundary (test seam).
func (x *RuntimeExecutor) SetMutationBoundary(b MutationBoundary) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.mutationBoundary = b
}

// MutationBoundary returns the currently wired mutation boundary.
func (x *RuntimeExecutor) MutationBoundary() MutationBoundary {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.mutationBoundary
}

// SetAuthorization attaches the mutation authorization token the runtime uses
// to gate every apply. When nil, applies fail with a deterministic denial —
// the runtime never applies without an authorization token.
func (x *RuntimeExecutor) SetAuthorization(a *authorization.MutationAuthorization) {
	x.auth = a
}

// SetSessionResolver wires the active-session correlation source
// (INV-SESSION-10). The resolver is consulted at admission when a request does
// not carry an explicit SessionID, so every execution — including autonomous
// and headless submissions — is correlated with the session that produced it.
func (x *RuntimeExecutor) SetSessionResolver(fn func() string) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.sessionID = fn
}

// observeTargets captures each target's bytes ONCE (Observation phase) and
// caches them as the single byte source for the execution. It emits the
// "[runtime] reading %s" log exactly once per target — verification and
// extraction consume the cached []byte via SnapshotContent without repeating
// os.ReadFile.
func (x *RuntimeExecutor) observeTargets(targets []string) {
	cache := make(map[string][]byte)
	for _, t := range targets {
		path := filepath.Join(x.root, t)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		x.emitContextActivity(t, len(data))
		cache[t] = data
		cache[path] = data
		cache[filepath.Base(t)] = data
	}
	x.observeSnapshotMu.Lock()
	x.observeSnapshot = cache
	x.observeSnapshotMu.Unlock()
}

func (x *RuntimeExecutor) getSnapshotContent(target string) ([]byte, bool) {
	x.observeSnapshotMu.RLock()
	defer x.observeSnapshotMu.RUnlock()
	if x.observeSnapshot == nil {
		return nil, false
	}
	if data, ok := x.observeSnapshot[target]; ok {
		return data, true
	}
	if data, ok := x.observeSnapshot[filepath.Base(target)]; ok {
		return data, true
	}
	path := filepath.Join(x.root, target)
	if data, ok := x.observeSnapshot[path]; ok {
		return data, true
	}
	return nil, false
}

func (x *RuntimeExecutor) clearObserveSnapshot() {
	x.observeSnapshotMu.Lock()
	x.observeSnapshot = nil
	x.observeSnapshotMu.Unlock()
}

// SnapshotContent returns the snapshot bytes for a target, if observed.
// It is the verification consumption point — no os.ReadFile.
func (x *RuntimeExecutor) SnapshotContent(target string) []byte {
	if data, ok := x.getSnapshotContent(target); ok {
		return data
	}
	return nil
}

// resolveSessionID returns the originating session for a request: the explicit
// request value wins, otherwise the wired session resolver ("" when neither).
func (x *RuntimeExecutor) resolveSessionID(req ExecuteRequest) string {
	if req.SessionID != "" {
		return req.SessionID
	}
	x.mu.Lock()
	fn := x.sessionID
	x.mu.Unlock()
	if fn != nil {
		return fn()
	}
	return ""
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

var ErrArtifactRetryableRejected = errors.New("executor: mutation artifact rejected with retry directive")

// ErrNoOpMutation signals a bounded-patch invocation whose model answered with
// the NO_CHANGES_REQUIRED sentinel: a raw no-op CLAIM was detected. Detection
// alone decides nothing terminal — the wrapped NoOpClaimError carries the
// deterministic semantic classification (see ClassifyNoOpClaim) that maps the
// claim onto one of the three NO-OP sub-state outcomes.
var ErrNoOpMutation = errors.New("executor: model reported NO_CHANGES_REQUIRED")

// ErrNoOpObjectiveUnresolved is the deterministic error carried by an
// execution whose model claimed NO_CHANGES_REQUIRED while structural analysis
// contradicted the claim: the objective's signature is still present in the
// assigned slice. The outcome is OutcomeNoOpObjectiveUnresolved — never a
// success; the autonomy runtime owns escalation from here.
var ErrNoOpObjectiveUnresolved = errors.New("executor: no-op claim conflicts with structural evidence")

// NoOpClaimError is the typed carrier of a raw model no-op claim plus its
// deterministic classification. It unwraps to ErrNoOpMutation so existing
// detection keeps working, and its Assessment field selects the terminal
// sub-state at the convergence site.
type NoOpClaimError struct {
	// Claim is the RAW model claim (sentinel + bounded prose), propagated
	// verbatim from the response without any terminal interpretation.
	Claim NoOpRawClaim
	// Assessment is the deterministic structural classification.
	Assessment NoOpAssessment
	// Target is the mutation target the claim was about.
	Target string
}

func (e *NoOpClaimError) Error() string {
	return fmt.Sprintf("%v: %s: %s", ErrNoOpMutation, e.Target, e.Assessment.Verdict)
}

// Unwrap preserves errors.Is(err, ErrNoOpMutation) for detection sites.
func (e *NoOpClaimError) Unwrap() error { return ErrNoOpMutation }

var ErrOutputTruncated = errors.New("executor: model output truncated")

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
	sid := x.resolveSessionID(req)
	res := &ExecutionResult{
		RequestID: requestID,
		Mode:      req.Mode,
		SessionID: sid,
		Proof:     &ExecutionProof{RequestID: requestID, StrategyReason: "", StartedAt: time.Now()},
	}
	res.Proof.SessionID = sid
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
	g.SetSessionID(sid)
	setProofGraph(res, g)
	g.Start(req.Mode, req.Prompt)
	g.CompleteUserIntent()

	// ── ADMISSION I: CONTEXT FIDELITY (Phase 1 P1) ───────────────────
	// The intent's frozen context snapshot must still match its sealed digest
	// AND bind to the request's declared context fields. Any mid-flight
	// modification between caller submission and this boundary fails closed
	// here — before strategy selection, before any read, before anything.
	snapshot, ctxErr := verifyIntentContext(req, x.root)
	if ctxErr != nil {
		err := fmt.Errorf("executor: admission rejected request %s: %w", requestID, ctxErr)
		g.FailExecution(events.FailurePermanent, err, "executor.admission")
		res.Err = err
		res.Proof.Outcome = OutcomeFailed
		res.Proof.FinishedAt = time.Now()
		setProofGraph(res, g)
		return x.finalizeResult(res), err
	}
	// The verified (or freshly synthesized) snapshot is the authoritative
	// context of THIS execution and is carried forward on the request so no
	// downstream stage can rebuild or substitute prompt context.
	req.Context = snapshot
	res.Proof.ContextID = snapshot.ID
	res.Proof.ContextDigest = snapshot.Digest()
	res.Proof.ContextParentID = snapshot.Parent

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

	// ── ADMISSION II: RISK SCOPE GATING (Phase 1 P1) ──────────────────
	// The intent's blast radius is evaluated deterministically from the
	// selected strategy and the declared targets, then checked against the
	// admitted capabilities BEFORE any execution stage that could act on the
	// world. Out-of-scope intents are rejected here — never escalated
	// implicitly.
	decision, admitErr := x.admission.Admit(req, x.root, profile)
	if admitErr != nil {
		err := fmt.Errorf("executor: admission rejected request %s: %w", requestID, admitErr)
		g.FailExecution(events.FailurePermanent, err, "executor.admission")
		res.Err = err
		res.Proof.RiskScope = decision.Requested.String()
		res.Proof.Outcome = OutcomeFailed
		res.Proof.FinishedAt = time.Now()
		setProofGraph(res, g)
		return x.finalizeResult(res), err
	}
	req.Context = decision.Snapshot
	res.Proof.RiskScope = decision.Requested.String()

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

	// ── OBSERVE PHASE: single snapshot read (the ONLY disk read) ─────
	// The target file's bytes are captured ONCE via Observe and cached as the
	// single byte source for compileContext, invokeMutation, and verification.
	// All downstream stages consume SnapshotContent() without repeating os.ReadFile.
	x.observeTargets(targets)
	defer x.clearObserveSnapshot()

	// ── ADMISSION III: CONTRACT IDENTITY RESOLUTION (Phase 2 P2) ──────
	// The execution's immutable identity is derived from the VERIFIED context
	// digest, the selected strategy and the RESOLVED target set — never
	// declared by callers. Retries of one intent resolve to the SAME
	// ContractID with a deterministically incremented AttemptID; parameter or
	// strategy changes fork a NEW contract; an explicit causal recovery
	// instantiates a new contract that back-points at its failed parent under
	// the bounded chain limit. Resolution failures (unknown parent, exhausted
	// chain) fail closed here — before any acting stage.
	contract, attempt, contractErr := x.contracts.Resolve(req, snapshot.Digest(), targets)
	if contractErr != nil {
		err := fmt.Errorf("executor: admission rejected request %s: %w", requestID, contractErr)
		g.FailExecution(events.FailurePermanent, err, "executor.admission")
		res.Err = err
		res.Proof.Outcome = OutcomeFailed
		res.Proof.FinishedAt = time.Now()
		setProofGraph(res, g)
		return x.finalizeResult(res), err
	}
	stampContractIdentity(res, contract, attempt)

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
		content, inv, ingTrace, err := x.invokeReadOnly(ctx, req, requestID, profile, targets, g)
		if ingTrace != nil {
			res.IngestionTrace = ingTrace
		}
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

	// ── PHASE 3 OCC BASELINE (target-scoped) ───────────────────────────
	// Fingerprint EXACTLY the resolved target geometry at admission time —
	// never a workspace-wide scan. Approve re-validates this baseline
	// immediately before the commit pipeline writes anything; any out-of-band
	// divergence in between aborts with ABORTED_OCC and zero partial writes.
	var occBaseline *WorkspaceBaseline
	if profile.Strategy == strategy.TargetedMutation {
		occBaseline = x.occ.SnapshotBaseline(targets)
	}

	// ── BOUNDARY 2 — PREFLIGHT GUARD (I5) ──────────────────────────────
	// Validate the estimated generation budget against max_output BEFORE any
	// provider request:
	//
	//	EstimatedTokens = TargetFileTokens × FullRewriteTokenMultiplier
	//
	// An infeasible full-file rewrite is REJECTED here — never silently
	// re-scoped, never sent to the provider to be truncated mid-generation.
	// The caller must explicitly re-scope (reduce scope / raise the budget /
	// request a bounded patch contract). Zero HTTP requests cross this
	// boundary on rejection.
	//
	// DAG EXECUTION: when the request carries staged decomposition sub-tasks,
	// every unit is evaluated INDIVIDUALLY and the monolithic full-rewrite
	// estimation is suppressed — a staged plan is never refused for the size
	// of the target it was decomposed from. The guard runs even under a
	// patch-only artifact contract so each approved unit stays provably
	// budget-feasible.
	if profile.Strategy == strategy.TargetedMutation &&
		(!patchOnlyArtifact(profile) || len(req.StagedSubTasks) > 0) {
		for _, target := range targets {
			verdict := EvaluatePreflight(PreflightRequest{
				ArtifactBounded: false,
				TargetBytes:     preflightTargetBytes(x.root, target),
				StagedScopes:    req.StagedSubTasks,
				MaxOutputTokens: effectiveMaxOutput(req.MaxOutputTokens, &profile),
			})
			if verdict.Feasible {
				continue
			}
			err := fmt.Errorf("%w: %s: %s", ErrPreflightInfeasible, target, verdict.Reason)
			g.FailExecution(events.FailurePermanent, err, "executor.preflight")
			res.ArtifactKind = ""
			res.Err = err
			res.Diagnostics = append(res.Diagnostics, diagnosticSignal(
				SignalPreflightInfeasible, target,
				fmt.Sprintf("estimated=%d budget=%d", verdict.EstimatedTokens, verdict.Budget),
				"re-scope explicitly before retrying", false,
			))
			res.Proof.Outcome = OutcomePreflightInfeasible
			res.Proof.FinishedAt = time.Now()
			setProofGraph(res, g)
			return x.finalizeResult(res), err
		}
	}

	// ── 7. Targeted mutation: per-target model invocation ──────────────
	patches, invs, diffs, ingTrace, err := x.invokeMutation(ctx, req, requestID, profile, targets, g)
	if ingTrace != nil {
		res.IngestionTrace = ingTrace
	}
	if err != nil {
		// Retain the invocation evidence on EVERY error return: the provider
		// billed these tokens whether the run was cancelled, the artifact was
		// rejected, or the response was empty. Truthful accounting never drops
		// provider billing because the mutation failed. finalizeResult sums
		// res.ModelCalls, so the authoritative counts survive into
		// Completed.OutputTokens and Proof.ModelInvocations.
		res.ModelCalls = append(res.ModelCalls, invs...)
		res.Proof.ModelInvocations = append(res.Proof.ModelInvocations, invs...)
		if errors.Is(err, ErrNoOpMutation) {
			// ── NO-OP SEMANTIC CONVERGENCE ─────────────────────────────
			// The model emitted the NO_CHANGES_REQUIRED sentinel. The raw
			// claim was classified deterministically at detection time; each
			// verdict converges to a DIFFERENT terminal truth:
			//
			//   SATISFIED   → successful no-op unit (historical behavior,
			//                 now backed by structural analysis).
			//   NO_SAFE_…   → terminal warning: candidate edits below the
			//                 safety threshold seal as REQUIRES_REVIEW.
			//   UNRESOLVED  → escalation trigger: the claim contradicts
			//                 observable structure — a recoverable failure
			//                 the autonomy runtime must intercept, never a
			//                 completed DAG.
			var claimErr *NoOpClaimError
			if !errors.As(err, &claimErr) {
				// Defensive: an ErrNoOpMutation without its typed carrier is
				// treated as unclassified and held for review.
				claimErr = &NoOpClaimError{
					Assessment: NoOpAssessment{
						Verdict: NoOpNoSafeMutation,
						Reason:  "no-op claim arrived without a structural assessment",
					},
				}
			}
			g.Skip(runtimegraph.StageApprovalGate, "no-op convergence")
			g.Skip(runtimegraph.StageMutationTransaction, "no-op convergence")
			switch claimErr.Assessment.Verdict {
			case NoOpObjectiveUnresolved:
				unresolvedErr := fmt.Errorf("%w: %s: %s",
					ErrNoOpObjectiveUnresolved, claimErr.Target, claimErr.Assessment.Reason)
				g.FailExecution(events.FailureRecoverable, unresolvedErr, "executor.noop_semantics")
				res.ArtifactKind = ""
				res.Content = ""
				res.Err = unresolvedErr
				res.Diagnostics = append(res.Diagnostics, diagnosticSignal(
					SignalNoOpObjectiveUnresolved, claimErr.Target,
					claimErr.Assessment.Reason,
					"re-examine the assigned window with elevated context or escalate to review", false,
				))
				res.Proof.Outcome = VerdictToOutcome(claimErr.Assessment.Verdict)
				res.Proof.FinishedAt = time.Now()
				setProofGraph(res, g)
				return x.finalizeResult(res), unresolvedErr
			case NoOpNoSafeMutation:
				g.Skip(runtimegraph.StageVerification, "requires review")
				g.CompleteExecution(string(OutcomeNoOpNoSafeMutation))
				res.ArtifactKind = ""
				res.Content = ""
				res.Err = nil
				res.Diagnostics = append(res.Diagnostics, diagnosticSignal(
					SignalNoOpRequiresReview, claimErr.Target,
					claimErr.Assessment.Reason,
					"human review required before any further mutation", false,
				))
				res.Proof.Outcome = OutcomeNoOpNoSafeMutation
				res.Proof.FinishedAt = time.Now()
				setProofGraph(res, g)
				return x.finalizeResult(res), nil
			default: // NoOpObjectiveSatisfied
				g.Skip(runtimegraph.StageVerification, "no-op objective satisfied")
				g.CompleteExecution(string(OutcomeNoOpObjectiveSatisfied))
				res.ArtifactKind = ""
				res.Content = ""
				res.Err = nil
				res.Proof.Outcome = OutcomeNoOpObjectiveSatisfied
				res.Proof.FinishedAt = time.Now()
				setProofGraph(res, g)
				return x.finalizeResult(res), nil
			}
		}
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
		if errors.Is(err, ingestion.ErrSyntaxInvalid) {
			// TRANSPORT NORMALIZATION (L1 pre-gate): the normalized payload
			// failed basic envelope integrity (an unterminated code fence or an
			// unclosed structural tag). No silent semantic repair is attempted
			// — the generation is rejected to the contract retry loop with
			// explicit syntax/parse feedback so a successor attempt changes the
			// contract materially. The IngestionTrace (Classification =
			// ClassSyntaxInvalid) preserves the exact unmutated output for
			// post-mortem forensics.
			g.FailExecution(events.FailureRecoverable, err, "executor.ingestion")
			res.ArtifactKind = ""
			res.Content = "" // Recovery Isolation (I2): rejected bytes never travel
			detail := "transport normalization produced a syntactically invalid payload (unterminated fence or unclosed structural tag)"
			// AST STRUCTURAL AUDIT FEEDBACK: the ingestion layer detects the
			// broken envelope but does not parse line positions; run the V3
			// pipeline over the preserved payload to resolve the EXACT line of
			// the offending node and inject it into the retry context.
			if ingTrace != nil {
				if audit := structuralAuditForPayload(firstTarget(targets), []byte(ingTrace.NormalizedPayload)); audit != "" { //nolint:contextcheck // artifact validation is pure content checking, no context needed
					detail = audit
				}
			}
			res.Err = fmt.Errorf("%w: %s", ErrArtifactRetryableRejected, detail)
			res.Diagnostics = append(res.Diagnostics, diagnosticSignal(
				SignalSchemaViolation, firstTarget(targets),
				detail,
				"regenerate a complete, well-formed artifact; do not rely on silent repair",
				true,
			))
			res.Proof.Outcome = OutcomeArtifactRetryableRejected
			res.Proof.FinishedAt = time.Now()
			setProofGraph(res, g)
			return x.finalizeResult(res), res.Err
		}
		if errors.Is(err, ErrArtifactRejected) {
			// The model produced an artifact that FAILED the artifact
			// boundary. The execution is a permanent artifact rejection, never
			// a recoverable patch failure: retrying the same malformed output
			// cannot repair it.
			g.FailExecution(events.FailurePermanent, err, "executor.artifact")
			res.ArtifactKind = ""
			res.Content = "" // Recovery Isolation (I2): rejected bytes never travel
			res.Err = err
			res.Diagnostics = append(res.Diagnostics, artifactDiagnostic(firstTarget(targets), err, false))
			res.Proof.Outcome = OutcomeArtifactRejected
			res.Proof.FinishedAt = time.Now()
			setProofGraph(res, g)
			return x.finalizeResult(res), err
		}
		if errors.Is(err, ErrArtifactRetryableRejected) {
			g.FailExecution(events.FailureRecoverable, err, "executor.artifact")
			res.ArtifactKind = ""
			res.Content = "" // Recovery Isolation (I2): rejected bytes never travel
			res.Err = err
			res.Diagnostics = append(res.Diagnostics, artifactDiagnostic(firstTarget(targets), err, true))
			res.Proof.Outcome = OutcomeArtifactRetryableRejected
			res.Proof.FinishedAt = time.Now()
			setProofGraph(res, g)
			return x.finalizeResult(res), err
		}
		if gateErr := new(OutputGateError); errors.As(err, &gateErr) {
			// BOUNDARY 3 circuit break (I1): an incomplete or refused
			// generation strictly halts execution. Nothing was parsed, nothing
			// is staged, no recovery loop starts here. Only the advisory
			// DiagnosticSignal crosses toward recovery.
			g.FailExecution(events.FailureRecoverable, err, "executor.output-gate")
			res.ArtifactKind = ""
			res.Content = ""
			res.Err = err
			res.Diagnostics = append(res.Diagnostics, diagnosticSignal(
				gateErr.Outcome.String(), gateErr.Target,
				fmt.Sprintf("finish_reason=%q output_tokens=%d", gateErr.FinishReason, lastOutputTokens(invs)),
				"discard the partial generation; a successor attempt must change the execution contract materially",
				true,
			))
			if gateErr.Outcome == CanonicalOutputExhausted {
				res.Proof.Outcome = OutcomeTruncated
			} else {
				res.Proof.Outcome = OutcomeFailed
			}
			res.Proof.FinishedAt = time.Now()
			setProofGraph(res, g)
			return x.finalizeResult(res), err
		}
		if errors.Is(err, ErrOutputTruncated) {
			g.FailExecution(events.FailureRecoverable, err, "executor.model")
			res.ArtifactKind = ""
			res.Err = err
			res.Proof.Outcome = OutcomeTruncated
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
		sessionID:      res.SessionID,
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
		contract:       contract,
		attempt:        attempt,
		baseline:       occBaseline,
		ingestionTrace: res.IngestionTrace,
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
		SessionID:      pm.sessionID,
		Mode:           pm.mode,
		Strategy:       pm.strategy,
		StrategyReason: pm.strategyReason,
		Targets:        pm.targets,
		ModelCalls:     pm.modelCalls,
		ArtifactKind:   "patch",
		Content:        pm.patches[0].Modified,
		Proof: &ExecutionProof{
			RequestID:        pm.requestID,
			SessionID:        pm.sessionID,
			Strategy:         pm.strategy,
			StrategyReason:   pm.strategyReason,
			Targets:          pm.targets,
			ModelInvocations: pm.modelCalls,
			StartedAt:        pm.startedAt,
		},
		// Forensic continuity: the transport-normalization record of the raw
		// LLM response travels with the held artifact through the approval gate
		// to the terminal (committed) evidence.
		IngestionTrace: pm.ingestionTrace,
	}
	g := pm.g
	if g == nil {
		g = runtimegraph.New(pm.requestID, x.emit)
	}
	setProofGraph(res, g)

	// The approval resolves the SAME contract attempt that was held (Phase 2
	// P2): identity is immutable across the gate.
	stampContractIdentity(res, pm.contract, pm.attempt)

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

	// ── PHASE 3 OCC COMMIT GATE ────────────────────────────────────────
	// Re-verify the admitted target baseline IMMEDIATELY before the commit
	// pipeline applies any mutation or executes any final file write. Any
	// out-of-band divergence (LSP edit, external process, parallel tool) since
	// admission halts execution here — BEFORE a single byte is written — with
	// the canonical ABORTED_OCC outcome, tainted mutations and zero partial
	// writes. This gate is never substituted by Cancel(): an OCC abort is a
	// first-class terminal evidence outcome.
	if pm.baseline != nil {
		if conflict := x.occ.VerifyAgainst(pm.baseline); conflict != nil {
			err := fmt.Errorf("executor: occ commit gate rejected request %s: %w", pm.requestID, conflict)
			return x.abortOnStateConflict(res, pm, ms, g, err), err
		}
	}

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

// abortOnStateConflict is the clean Phase 3 OCC abort path. It runs when the
// pre-commit verification found a baseline mismatch: NOTHING was applied (the
// gate precedes the apply stage), so no partial write can exist by
// construction. The open mutation boundary is terminated cleanly, per-target
// evidence records the abort without ever claiming an apply, and the proof
// carries the OccAborted flag that seals ABORTED_OCC tainted evidence.
func (x *RuntimeExecutor) abortOnStateConflict(
	res *ExecutionResult,
	pm *pendingMutation,
	ms *MutationSet,
	g *runtimegraph.Graph,
	abortErr error,
) *ExecutionResult {
	// Terminate the open boundary cleanly: nothing was recorded or applied,
	// so rollback restores nothing — it only closes the transaction so no
	// staged state survives the abort.
	_ = ms.RollbackTo(MutationRolledBack)

	res.ArtifactKind = "patch"
	for _, t := range pm.targets {
		res.Mutations = append(res.Mutations, MutationEvidence{
			Stage:   StageApply,
			File:    t,
			Outcome: OutcomeOCCAborted,
			Reason:  abortErr.Error(),
		})
		g.CompleteMutation(t, string(OutcomeOCCAborted))
	}
	res.Proof.Mutations = res.Mutations
	res.Proof.OccAborted = true
	res.Proof.Outcome = OutcomeOCCAborted
	res.Proof.AffectedFiles = append([]string(nil), pm.targets...)
	res.Proof.DiffSummary = pm.diffs
	// The verification gate never ran — the OCC gate aborted before apply.
	g.Skip(runtimegraph.StageVerification, "occ abort before any apply")
	g.FailExecution(events.FailurePermanent, abortErr, "executor.occ")
	res.Err = abortErr
	res.Proof.FinishedAt = time.Now()
	setProofGraph(res, g)
	return x.finalizeResult(res)
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
	// The rejection resolves the SAME contract attempt that was held (Phase 2
	// P2): identity is immutable across the gate.
	stampContractIdentity(res, pm.contract, pm.attempt)
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

// RejectAllPending deterministically rejects every approval-held mutation. It
// is the session-boundary drain: /new and /session resume cross the execution
// lifecycle through this single RuntimeExecutor authority — never through a
// second state engine. It returns a per-patch error slice; the drain is
// best-effort cleanup and must never fail a session switch.
func (x *RuntimeExecutor) RejectAllPending(ctx context.Context, reason string) []error {
	var errs []error
	for _, id := range x.PendingPatchIDs() {
		if _, err := x.Reject(ctx, id, reason); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
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
		var data []byte
		if cached, ok := x.getSnapshotContent(t); ok {
			data = cached
			// Snapshot already emitted in Observe phase — do not re-emit.
		} else {
			path := filepath.Join(x.root, t)
			d, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			// Fallback read only when no snapshot was observed.
			x.emitContextActivity(t, len(d))
			data = d
		}
		if len(data) > maxExecutorContextBytes {
			data = data[:maxExecutorContextBytes]
		}
		b.Write(data)
		b.WriteByte('\n')
	}
	return channels, estimateTokens(b.String())
}

// effectiveMaxOutput returns the max tokens to use for a request. It prefers
// the explicitly-set request budget, falling back to the profile budget so that
// UI callers that omit max_tokens on the wire still receive the strategy-owned
// bound.
func effectiveMaxOutput(req int, profile *strategy.ExecutionStrategyProfile) int {
	if req > 0 {
		return req
	}
	if profile != nil && profile.MaxOutputTokens > 0 {
		return profile.MaxOutputTokens
	}
	return 0
}

// invokeMutation performs the bounded provider invocation(s) for a targeted
// mutation — one bounded call per resolved target. It drives the model stage of
// the execution graph: model.invoked is emitted BEFORE each provider call and
// provider.response ONLY after a successful response. On any failure it returns
// the error and emits neither response nor artifact. The returned patches carry
// the full resolved content of each target; the diffs are the authoritative
// unified diffs for rendering.
func (x *RuntimeExecutor) invokeMutation(ctx context.Context, req ExecuteRequest, requestID string, profile strategy.ExecutionStrategyProfile, targets []string, g *runtimegraph.Graph) ([]*Patch, []ModelInvocation, []string, *ingestion.IngestionTrace, error) {
	if len(targets) == 0 {
		return nil, nil, nil, nil, fmt.Errorf("executor: no mutation target resolved")
	}
	if x.provider == nil {
		return nil, nil, nil, nil, fmt.Errorf("executor: no provider configured for model invocation")
	}

	model, modelErr := x.resolveModel()
	if modelErr != nil {
		return nil, nil, nil, nil, modelErr
	}

	patches := make([]*Patch, 0, len(targets))
	invs := make([]ModelInvocation, 0, len(targets))
	diffs := make([]string, 0, len(targets))
	// trace carries the forensic transport-normalization record of the most
	// recent model invocation (nil until the first stream returns). It is
	// returned so the executor can attach it to the active ExecutionEvidence
	// even when the artifact is later rejected.
	var trace *ingestion.IngestionTrace
	// The artifact contract decides the OUTPUT representation the model must
	// produce AND, for bounded_patch recovery, the INPUT contract too. A
	// search_replace contract is a different mutation protocol, not a label:
	// the runtime derives a small deterministic context window (so even a
	// degenerate echo physically fits the output budget), asks for exactly one
	// anchored SEARCH/REPLACE block, and rejects everything else at the
	// artifact boundary.
	patchOnly := patchOnlyArtifact(profile)
	attempt := req.RecoveryAttempt
	if attempt < 1 {
		attempt = 1
	}
	recoveryLabel := req.RecoveryStrategy
	if recoveryLabel == "" {
		recoveryLabel = "none"
	}
	for _, target := range targets {
		var data []byte
		var readErr error
		if cached, ok := x.getSnapshotContent(target); ok {
			data = cached
			// Snapshot already emitted in Observe phase — consume []byte slice reference, no os.ReadFile
		} else {
			path := filepath.Join(x.root, target)
			d, err := os.ReadFile(path)
			if err != nil && !os.IsNotExist(err) {
				return nil, nil, nil, nil, fmt.Errorf("executor: read target %s: %w", target, err)
			}
			data = d
			readErr = err
			// Only emit when actually reading from disk (fallback, no snapshot)
			x.emitContextActivity(target, len(data))
		}
		_ = readErr
		original := string(data)

		system := boundedMutationSystemPrompt()
		outputContract := "full_file_or_patch"
		user := buildMutationUserPrompt(req.Prompt, target, original, req.Evidence)
		contextBytes := len(original)
		// judgedContent is exactly the context the model was asked to judge.
		// Under the bounded patch contract that is the small line-aligned
		// window — the no-op semantics classifier evaluates the claim against
		// the same bytes, never against the unseen remainder of the file.
		judgedContent := original
		if patchOnly {
			system = boundedPatchSystemPrompt()
			outputContract = "search_replace"
			// Bounded INPUT contract: the runtime — not the model — decides
			// what crosses. Only one small line-aligned window of the target
			// is shown as the copyable source; rotating it by attempt gives
			// every repair cycle materially different content while keeping
			// the worst-case response far below the output ceiling.
			//
			// REGION FOCUS: under a staged decomposition plan the rotation is
			// pinned INSIDE the sub-task's assigned interval (req.Focus*), so a
			// unit can neither see nor anchor on another unit's content. A NO-OP
			// escalation attempt receives a BROADER boundary window around that
			// same region: a claim that conflicted with structural evidence
			// re-judges against materially more of its assigned slice.
			windowBound := maxBoundedPatchContextBytes
			focusStart, focusEnd := req.FocusStartLine, req.FocusEndLine
			if req.NoOpEscalation {
				windowBound = maxEscalatedPatchContextBytes
				margin := (focusEnd - focusStart + 1) / 2
				if margin < maxNoOpEscalationFocusMargin {
					margin = maxNoOpEscalationFocusMargin
				}
				focusStart, focusEnd = focusStart-margin, focusEnd+margin
			}
			focusSource, focusOffset := focusSlice(original, focusStart, focusEnd)
			if focusSource == "" {
				focusSource, focusOffset = original, 0 // degenerate focus: whole-file rotation
			}
			window := selectBoundedPatchWindowScaled(focusSource, attempt, windowBound)
			window.startLine += focusOffset
			window.endLine += focusOffset
			window.totalLines = strings.Count(original, "\n")
			if !strings.HasSuffix(original, "\n") {
				window.totalLines++
			}
			user = buildBoundedPatchUserPrompt(req.Prompt, req.Evidence, target, window)
			contextBytes = len(window.content)
			judgedContent = window.content
		}
		maxOut := effectiveMaxOutput(req.MaxOutputTokens, &profile)
		disableReasoning := false
		if patchOnly {
			// Reasoning models spend the SHARED output budget on hidden
			// chain-of-thought before emitting any artifact text (the live
			// OpenRouter repro: cohere/north-mini-code consumed all 1024
			// output tokens as reasoning with ZERO visible content — and its
			// gateway IGNORES reasoning.max_tokens, so only an explicit
			// disable converges). The bounded patch is a deterministic small
			// artifact; the hidden reasoning pass is disabled for it.
			disableReasoning = true
		}
		reasoningMode := "provider_default"
		if disableReasoning {
			reasoningMode = "disabled"
		}
		// Truthful wire-contract trace: this line describes what the executor
		// ACTUALLY sends/expects for this invocation — not what a recovery
		// field claims.
		log.Printf("[execution] request=%s attempt=%d target=%s strategy=%s artifact_kind=%s output_contract=%s context_bytes=%d prompt_bytes=%d max_output=%d reasoning=%s recovery=%s",
			requestID, attempt, target, profile.Strategy, profile.Artifact.Kind, outputContract, contextBytes, len(user), maxOut, reasoningMode, recoveryLabel)
		aiReq := ai.Request{
			Model:     model,
			System:    system,
			Messages:  []ai.Message{{Role: "user", Content: user}},
			MaxTokens: maxOut,
		}
		if disableReasoning {
			aiReq.Reasoning = &ai.ReasoningConfig{Disabled: true}
		}
		// model.invoked is emitted when the invocation BEGINS — before the
		// provider call — so the event stream truthfully records the start.
		g.BeginModel(model)
		raw, usage, itrace, callErr := x.invokeStream(ctx, aiReq, requestID, model, g, req.StreamCallback)
		trace = itrace
		// The invocation evidence is built from the stream outcome REGARDLESS
		// of the artifact result: the provider billed these tokens whether the
		// stream succeeded, was cancelled mid-flight, or produced a malformed
		// artifact. Dropping the invocation on any error return erased real
		// billing from Completed.OutputTokens (the 5,883-token repro: the
		// artifact was rejected as "unterminated <script> element" and the
		// provider's authoritative usage vanished from Izen's account).
		inv := ModelInvocation{Model: model}
		if usage.Known {
			inv.Known = true
			inv.TokenInput = usage.PromptTokens
			inv.TokenOutput = usage.CompletionTokens
			inv.CachedTokens = usage.CachedTokens
			inv.ReasoningTokens = usage.ReasoningTokens
		}
		inv.FinishReason = usage.FinishReason
		inv.HTTPAttempts = usage.HTTPAttempts
		inv.RateLimitedRetries = usage.RateLimitedRetries
		log.Printf("[execution] result request=%s target=%s input=%d output=%d finish_reason=%s",
			requestID, target, inv.TokenInput, inv.TokenOutput, inv.FinishReason)
		if callErr != nil {
			// The invocation evidence (real billing when usage arrived)
			// survives even a hard transport failure.
			return nil, append(invs, inv), nil, trace, fmt.Errorf("executor: model invocation: %w", callErr)
		}
		// provider.response is emitted ONLY on a successful response — the
		// authoritative usage travels here. No artifact may precede it.
		g.CompleteModel(model, inv.TokenInput, inv.TokenOutput)
		invs = append(invs, inv)

		// ── BOUNDARY 3 — OUTPUT GATE (I1) ───────────────────────────
		// Normalize the provider terminal reason into a CanonicalOutcome and
		// enforce it BEFORE anything is parsed. An incomplete generation is
		// circuit-broken here: its bytes are DISCARDED (never handed to hunk
		// extraction or full-file resolution), no approval surface opens, and
		// no recovery loop starts inside the executor. Only a COMPLETE stream
		// may proceed to Boundary 4.
		if gate := gateFor(target, inv.FinishReason); gate != nil {
			return nil, invs, nil, trace, gate
		}

		var modified string
		if patchOnly {
			// NO-OP SENTINEL (pre-validation): a model that answers
			// NO_CHANGES_REQUIRED has satisfied the OUTPUT CONTRACT of the
			// bounded patch — but the claim itself is NOT yet a verdict. The
			// raw claim is propagated verbatim and classified by deterministic
			// structural analysis against the exact window the model judged;
			// the terminal sub-state (satisfied / requires review /
			// unresolved) is selected at the convergence site. Burning the
			// retry budget on prose-free compliant output is never the right
			// outcome.
			if claim, claimed := ExtractNoOpClaim(raw); claimed {
				assessment := ClassifyNoOpClaim(req.Prompt, judgedContent)
				log.Printf("[execution] request=%s target=%s artifact=no_op (%s) verdict=%s reason=%q",
					requestID, target, claim.Sentinel, assessment.Verdict, assessment.Reason)
				return nil, invs, nil, trace, &NoOpClaimError{
					Claim:      claim,
					Assessment: assessment,
					Target:     target,
				}
			}
			// Bounded-patch artifact boundary: extract ONLY structured patch
			// representations. A full-file (or otherwise unstructured)
			// response can NEVER satisfy this contract — rejecting it here is
			// what makes recovery semantically different from the initial
			// full-artifact attempt instead of a relabeled retry.
			patched, ok := ExtractBoundedPatch(original, raw)
			if !ok {
				return nil, invs, nil, trace, fmt.Errorf("%w: %s: bounded patch contract requires SEARCH/REPLACE blocks or unified diff hunks; full-file or unstructured output rejected", ErrArtifactRetryableRejected, target)
			}
			modified = patched
		} else {
			modified = ResolveModifiedContent(original, raw)
			if modified == "" {
				// The model returned only prose or a fence without content — treat
				// the full response as the replacement attempt (best-effort).
				modified = raw
			}
		}
		if strings.TrimSpace(modified) == "" {
			// Phase 1 safety rule: an artifact extraction failure is a FAILURE,
			// never a proposal staged for approval. The model produced no
			// usable mutation artifact — abort before any approval surface.
			// The billed invocation is still returned so usage is preserved.
			return nil, invs, nil, trace, fmt.Errorf("executor: model produced no mutation artifact for %s", target)
		}
		// ── ARTIFACT BOUNDARY (Phase 2) ─────────────────────────────
		// A model response is NOT an artifact until it passes the artifact
		// validation boundary. Registered-language targets (go/html/json) that
		// fail validation are rejected BEFORE any approval or mutation surface —
		// a malformed artifact can never become a proposal. Unregistered
		// languages pass normalized (canonical bytes), so the proposal preview
		// and the eventual disk write agree.
		//
		// P1 Decoupling: when the bounded-patch contract is active, the
		// explicit ArtifactValidator validates the RAW patch before syntax
		// gating. For full-file artifacts the validator is intentionally NOT
		// applied to the resolved file content — the V3 pipeline owns syntax
		// there. This preserves the existing full-file truth matrix.
		if patchOnly && x != nil && x.artifactValidator != nil {
			if _, err := x.artifactValidator.ValidateArtifact([]byte(raw), target); err != nil {
				if errors.Is(err, ErrAmbiguousAnchor) || errors.Is(err, ErrScopeViolation) {
					return nil, invs, nil, trace, fmt.Errorf("%w: %s: %w", ErrArtifactRejected, target, err)
				}
				if errors.Is(err, ErrFormatRejected) {
					gate := v3Artifact.ValidateContent(target, []byte(modified), 0) //nolint:contextcheck // artifact validation is pure content checking, no context needed
					if !gate.Passed && gate.Decision == policy.DecisionRetry {
						// AST STRUCTURAL AUDIT FEEDBACK: surface the exact line +
						// parse error of the V3 pipeline rejection into the retry
						// directive so the successor anchors its correction at the
						// precise defect instead of resending raw code.
						audit := StructuralAuditDirective(gate.Error.Error())
						return nil, invs, nil, trace, fmt.Errorf("%w: %s: %s", ErrArtifactRetryableRejected, target, audit)
					}
					return nil, invs, nil, trace, fmt.Errorf("%w: %s: %w", ErrArtifactRejected, target, err)
				}
				return nil, invs, nil, trace, fmt.Errorf("%w: %s: %w", ErrArtifactRejected, target, err)
			}
		}
		//nolint:contextcheck // artifact validation is pure content checking, no context needed
		normalized, gateErr := x.artifactGate(target, modified)
		if gateErr != nil {
			// The artifact was rejected, but the invocation evidence (and the
			// real provider billing it carries) must survive: the token count
			// is provider truth, not a function of artifact validity.
			return nil, invs, nil, trace, gateErr
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
	return patches, invs, diffs, trace, nil
}

// artifactDiagnostic builds the Boundary-4 advisory signal for a rejected
// artifact. The signal carries the failure CLASS and corrective directive
// only — never a byte of the rejected generation (Recovery Isolation, I2).
func artifactDiagnostic(target string, gateErr error, retryable bool) DiagnosticSignal {
	return diagnosticSignal(SignalSchemaViolation, target,
		"artifact failed hunk-schema/syntax validation at the artifact boundary",
		"produce exactly one anchored SEARCH/REPLACE block (or unified diff hunk); never re-emit the full file",
		retryable)
}

// lastOutputTokens returns the provider-reported output tokens of the most
// recent invocation (0 when usage is unknown).
func lastOutputTokens(invs []ModelInvocation) int {
	if len(invs) == 0 {
		return 0
	}
	return invs[len(invs)-1].TokenOutput
}

// artifactGate validates the resolved mutation artifact against the target's
// language contract using the V3 pipeline. The explicit ArtifactValidator is
// NOT invoked here — it is applied one layer up when the bounded-patch raw
// artifact is available, so full-file content never traverses the strict patch
// format gate. This keeps the validator interface decoupled (P1 decorators
// wrap it without touching this loop) while preserving the existing truth
// matrix for full-file rewrites.
func (x *RuntimeExecutor) artifactGate(target, modified string) (string, error) {
	gate := v3Artifact.ValidateContent(target, []byte(modified), 0)
	if !gate.Passed {
		if gate.Decision == policy.DecisionRetry {
			// AST STRUCTURAL AUDIT FEEDBACK: a structural parse rejection
			// (e.g. "html: unterminated <script> element at line 7") is
			// rewritten into the targeted [CONTRACT FAILURE] directive so the
			// retry loop prompt carries the exact line + parse error instead of
			// resending raw code. Non-structural rejections pass through
			// unchanged.
			audit := StructuralAuditDirective(gate.Error.Error())
			return "", fmt.Errorf("%w: %s: %s: %s", ErrArtifactRetryableRejected, target, audit, gate.Directive)
		}
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

// manifestSystemPrompt is the Pass 1 system prompt. Pass 1 is strictly
// READ-ONLY: the model emits a lightweight MutationManifest JSON that names the
// proposed mutation targets and operations. The runtime never treats this pass
// as a mutation surface — it only informs the DAG strategy (manifest-scoped
// atomic execution vs. semantic decomposition) that Pass 2 runs under.
//
// manifestPassMaxTokens bounds the Pass 1 generation: a manifest is a tiny
// JSON object, so a small fixed ceiling keeps the read-only pass cheap and
// cannot starve the bounded execution that follows.
const (
	// manifestSystemPrompt is the COMPACT Pass 1 manifest system prompt. Pass 1
	// is strictly read-only and must emit ONLY a raw MINIFIED MutationManifest
	// JSON with ZERO prose: weak/free-tier models (Cohere North Mini Code) that
	// are asked to "analyze" and "propose" ramble past the output budget and
	// truncate mid-JSON. The directive pins the MAX 200 TOKENS ceiling so a
	// compliant model completes on the first attempt. The autonomy layer
	// injects the authoritative buildManifestPrompt() at bootstrap via
	// SetManifestSystemPrompt; this constant is the pre-injection default.
	manifestSystemPrompt = "You are the Pass 1 manifest generator of a read-only planning stage. " +
		"Analyze the target file below against the user's objective and propose the MINIMAL set of mutations that achieve it. " +
		"OUTPUT ONLY VALID MINIFIED JSON ARRAY OF MUTATION TARGETS. DO NOT WRITE CODE, DO NOT EXPLAIN, DO NOT INCLUDE MARKDOWN FENCES. MAX 200 TOKENS.\n" +
		"Output a single raw JSON object (minified, no newlines) conforming exactly to:\n" +
		`{"targetFile":"<workspace-relative path>","intent":"<one-line objective>","mutations":[{"selector":"<css selector or symbol, e.g. #hero or section#hero>","action":"delete|modify|insert","estimatedLines":<positive int>}]}` + "\n" +
		"Rules: every mutation MUST name a selector or symbol that exists in the file; " +
		"omit any content the objective does not touch; " +
		"if the objective requires NO change, emit {\"targetFile\":\"<path>\",\"intent\":\"...\",\"mutations\":[]}. " +
		"This pass never writes to the workspace."

	// manifestPassMaxTokens is the fixed output ceiling of the read-only Pass 1
	// manifest generation. It matches the "MAX 200 TOKENS" directive so a
	// verbose free-tier model cannot ramble past the compact budget.
	manifestPassMaxTokens = 200

	// manifestPassRejectTokens is the post-hoc rejection threshold: a manifest
	// response whose token estimate still exceeds this ceiling (a provider that
	// ignores max_tokens) is rejected as INVALID JSON instead of exhausting the
	// output gate.
	manifestPassRejectTokens = 512
)

// SetManifestSystemPrompt overrides the Pass 1 manifest system prompt (the
// autonomy layer injects buildManifestPrompt at bootstrap). Passing an empty
// string restores the default.
func (x *RuntimeExecutor) SetManifestSystemPrompt(p string) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.manifestSystemPromptOverride = p
}

// manifestSystemPromptFor returns the effective Pass 1 manifest system prompt:
// the injected override when present, otherwise the default.
func (x *RuntimeExecutor) manifestSystemPromptFor() string {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.manifestSystemPromptOverride != "" {
		return x.manifestSystemPromptOverride
	}
	return manifestSystemPrompt
}

// InvokeManifestPass performs the lightweight READ-ONLY Pass 1 manifest
// request: a single bounded provider invocation whose only output is a raw
// MutationManifest JSON string. It never reads the workspace beyond the
// caller-provided targetContent bytes, never touches disk, and never mutates
// anything — its result only informs the DAG strategy Pass 2 decomposes under.
// The returned string is the verbatim model response; the caller parses it
// with ParseMutationManifest.
//
// An over-long output (a provider ignoring the 200-token ceiling) is rejected
// as INVALID JSON before it can exhaust the output gate: the error is a plain
// manifest failure the caller falls back from, never an OUTPUT_EXHAUSTED
// gate signal. A finish_reason="length" truncated response likewise crosses as
// raw bytes — ParseMutationManifest rejects the truncated JSON — so the DAG
// strategy decision falls back silently instead of surfacing exhaustion.
func (x *RuntimeExecutor) InvokeManifestPass(ctx context.Context, prompt string, targetContent []byte) (string, error) {
	if x == nil {
		return "", fmt.Errorf("executor: nil runtime for manifest pass")
	}
	x.mu.Lock()
	p := x.provider
	x.mu.Unlock()
	if p == nil {
		return "", fmt.Errorf("executor: no provider configured for the manifest pass")
	}
	model, err := x.resolveModel()
	if err != nil {
		return "", err
	}
	var user strings.Builder
	user.WriteString("USER OBJECTIVE:\n")
	user.WriteString(strings.TrimSpace(prompt))
	user.WriteString("\n\nTARGET FILE (read-only, " + strconv.Itoa(len(targetContent)) + " bytes):\n")
	user.WriteString("```\n")
	user.Write(targetContent)
	user.WriteString("\n```\n")
	req := ai.Request{
		Model:     model,
		System:    x.manifestSystemPromptFor(),
		Messages:  []ai.Message{{Role: "user", Content: user.String()}},
		MaxTokens: manifestPassMaxTokens,
		// The manifest is a tiny JSON object; a hidden reasoning pass would
		// spend the bounded output budget before any JSON appears.
		Reasoning: &ai.ReasoningConfig{Disabled: true},
	}
	resp, err := p.Execute(ctx, req)
	if err != nil {
		return "", fmt.Errorf("executor: manifest pass invocation: %w", err)
	}
	if resp == nil {
		return "", fmt.Errorf("executor: manifest pass returned an empty response")
	}
	raw := strings.TrimSpace(resp.Content)
	// A manifest is a TINY minified JSON payload; a response that still exceeds
	// the rejection ceiling (bytes/4 ≈ tokens) is by definition NOT the minimal
	// schema — reject it as invalid JSON rather than exhausting output gates.
	if len(raw)/4 > manifestPassRejectTokens {
		return "", fmt.Errorf(
			"executor: manifest pass output of ~%d tokens exceeds the %d-token ceiling — rejected as invalid manifest",
			len(raw)/4, manifestPassRejectTokens)
	}
	return raw, nil
}

// invokeReadOnly performs the single bounded provider invocation for read-only
// strategies (targeted_reasoning, direct_response, multi_file_planning,
// repository_investigation). It returns the produced content; no mutation path
// and no approval surface exist. Event semantics (Phase 4): model.invoked is
// emitted before the provider call; provider.response is emitted only after a
// successful response; a failure returns an error and emits neither — the
// artifact can never precede the response that produced it. The response is
// owned by the runtime and never reaches the UI raw.
func (x *RuntimeExecutor) invokeReadOnly(ctx context.Context, req ExecuteRequest, requestID string, profile strategy.ExecutionStrategyProfile, targets []string, g *runtimegraph.Graph) (content string, inv ModelInvocation, trace *ingestion.IngestionTrace, err error) {
	if x.provider == nil {
		return "", inv, nil, fmt.Errorf("executor: no provider configured for read-only invocation")
	}

	var b strings.Builder
	b.WriteString(readOnlySystemPrompt(profile.Strategy))
	for _, t := range targets {
		var data []byte
		if cached, ok := x.getSnapshotContent(t); ok {
			data = cached
			// Snapshot already emitted in Observe phase — consume []byte slice, no os.ReadFile
		} else {
			path := filepath.Join(x.root, t)
			d, readErr := os.ReadFile(path)
			if readErr != nil {
				continue
			}
			x.emitContextActivity(t, len(d))
			data = d
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
		return "", inv, nil, modelErr
	}
	aiReq := ai.Request{
		Model:     model,
		System:    readOnlySystemPrompt(profile.Strategy),
		Messages:  []ai.Message{{Role: "user", Content: b.String()}},
		MaxTokens: effectiveMaxOutput(req.MaxOutputTokens, &profile),
	}
	// model.invoked is emitted when the invocation BEGINS — before the provider
	// call — so the event stream truthfully records the invocation start.
	g.BeginModel(model)
	raw, usage, trace, callErr := x.invokeStream(ctx, aiReq, requestID, model, g, req.StreamCallback)
	if callErr != nil {
		return "", inv, trace, fmt.Errorf("executor: read-only invocation: %w", callErr)
	}
	inv.Model = model
	if usage.Known {
		inv.Known = true
		inv.TokenInput = usage.PromptTokens
		inv.TokenOutput = usage.CompletionTokens
		inv.CachedTokens = usage.CachedTokens
		inv.ReasoningTokens = usage.ReasoningTokens
	}
	inv.FinishReason = usage.FinishReason
	// provider.response is emitted ONLY on a successful response — the
	// authoritative usage travels here. No artifact may precede it.
	g.CompleteModel(model, inv.TokenInput, inv.TokenOutput)
	return raw, inv, trace, nil
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
func (x *RuntimeExecutor) invokeStream(ctx context.Context, req ai.Request, requestID, model string, g *runtimegraph.Graph, streamCb StreamCallback) (string, ai.ProviderUsage, *ingestion.IngestionTrace, error) {
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
			if streamCb != nil {
				streamCb(StreamEvent{RequestID: requestID, Kind: "error", Err: cerr})
			}
			return "", ai.ProviderUsage{}, nil, cerr
		}
		if resp == nil {
			err := fmt.Errorf("executor: provider returned an empty response")
			if streamCb != nil {
				streamCb(StreamEvent{RequestID: requestID, Kind: "error", Err: err})
			}
			return "", ai.ProviderUsage{}, nil, err
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
		if streamCb != nil {
			streamCb(StreamEvent{RequestID: requestID, Kind: "first_token"})
			if resp.Content != "" {
				streamCb(StreamEvent{RequestID: requestID, Kind: "content_delta", Content: resp.Content})
			}
			streamCb(StreamEvent{RequestID: requestID, Kind: "done", FinishReason: usage.FinishReason, Usage: usage})
		}
		// Transport normalization: preserve the raw response and record every
		// transformation in an IngestionTrace before the payload reaches the
		// L1 Execution Gate / artifact parser.
		trace, procErr := ingestion.Process(resp.Content)
		visible := ai.VisibleCompletion(trace.NormalizedPayload)
		if procErr != nil {
			if errors.Is(procErr, ingestion.ErrSyntaxInvalid) && trace != nil && trace.RepairCandidate != nil {
				candidate := trace.RepairCandidate
				if ingestion.IsASTValid(candidate.ProposedPayload) && ingestion.WithinSafetyThreshold(trace.NormalizedPayload, candidate) {
					ingestion.RecordRepairAccepted()
					log.Printf("[ingestion] repair candidate accepted rule=%s", candidate.RuleID)
					if globalActivityLog != nil {
						globalActivityLog("[ingestion] repair candidate accepted rule=%s", candidate.RuleID)
					}
					trace.NormalizedPayload = candidate.ProposedPayload
					trace.Classification = ingestion.ClassTransportNormalized
					visible = ai.VisibleCompletion(trace.NormalizedPayload)
					return visible, usage, trace, nil
				}
			}
			return visible, usage, trace, procErr
		}
		return visible, usage, trace, nil
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
			if streamCb != nil {
				streamCb(StreamEvent{RequestID: requestID, Kind: "first_token"})
			}
		}
		if tok.Kind == stream.TokenKindThinking {
			reasoningOpen()
			reasoningBuf.WriteString(tok.Text)
			// Reasoning chunks cross to the caller as a dedicated
			// reasoning_delta event so an attached UI can render the live
			// thought trace (Ctrl+O) during DAG_EXECUTING. The executor still
			// never publishes raw reasoning onto the event bus itself.
			if streamCb != nil {
				streamCb(StreamEvent{RequestID: requestID, Kind: "reasoning_delta", Content: tok.Text})
			}
			return
		}
		reasoningClose()
		content.WriteString(tok.Text)
		// stream_delta is evidence transport: a dropped delta never loses
		// execution truth because the content accumulates here regardless.
		g.StreamDelta(tok.Text)
		if streamCb != nil {
			streamCb(StreamEvent{RequestID: requestID, Kind: "content_delta", Content: tok.Text})
		}
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
			if streamCb != nil {
				streamCb(StreamEvent{RequestID: requestID, Kind: "error", Err: cerr})
			}
			return content.String(), lastUsage, nil, cerr
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
				if streamCb != nil {
					streamCb(StreamEvent{RequestID: requestID, Kind: "error", Err: cerr})
				}
				return content.String(), lastUsage, nil, cerr
			}
			if streamCb != nil {
				streamCb(StreamEvent{RequestID: requestID, Kind: "error", Err: rerr})
			}
			return content.String(), lastUsage, nil, rerr
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
	// Transport normalization: preserve the exact raw stream output and record
	// every transformation in an IngestionTrace before the payload reaches the
	// L1 Execution Gate / artifact parser.
	rawVisible := content.String()
	trace, procErr := ingestion.Process(rawVisible)
	visible := ai.VisibleCompletion(trace.NormalizedPayload)
	if strings.TrimSpace(visible) == "" && reasoningBuf.Len() > 0 {
		// The entire visible completion was reasoning: ingest the reasoning
		// text as the raw artifact so forensic traceability survives.
		reasoningRaw := reasoningBuf.String()
		trace, procErr = ingestion.Process(reasoningRaw)
		visible = ai.VisibleCompletion(trace.NormalizedPayload)
	}
	if streamCb != nil {
		streamCb(StreamEvent{RequestID: requestID, Kind: "done", FinishReason: usage.FinishReason, Usage: usage})
	}
	if procErr != nil {
		if errors.Is(procErr, ingestion.ErrSyntaxInvalid) && trace != nil && trace.RepairCandidate != nil {
			candidate := trace.RepairCandidate
			if ingestion.IsASTValid(candidate.ProposedPayload) && ingestion.WithinSafetyThreshold(trace.NormalizedPayload, candidate) {
				ingestion.RecordRepairAccepted()
				log.Printf("[ingestion] repair candidate accepted rule=%s", candidate.RuleID)
				if globalActivityLog != nil {
					globalActivityLog("[ingestion] repair candidate accepted rule=%s", candidate.RuleID)
				}
				trace.NormalizedPayload = candidate.ProposedPayload
				trace.Classification = ingestion.ClassTransportNormalized
				visible = ai.VisibleCompletion(trace.NormalizedPayload)
				return visible, usage, trace, nil
			}
		}
		return visible, usage, trace, procErr
	}
	return visible, usage, trace, nil
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

// stampContractIdentity copies the immutable contract identity onto the
// execution proof: ContractID, AttemptID, ParentContractID, the causal
// ancestry chain (root → parent) and the Phase 1 sealed ContextDigest. The
// contract carries exactly the verified context digest it was resolved with,
// so every terminal path that crosses this choke point — Execute, Approve and
// Reject alike — emits terminal evidence bound to its admitted context
// snapshot. Identity is stamped once per attempt.
func stampContractIdentity(res *ExecutionResult, c *ExecutionContract, attempt AttemptID) {
	if res == nil || res.Proof == nil || c == nil {
		return
	}
	res.Proof.ContractID = c.ID().String()
	res.Proof.AttemptID = uint32(attempt)
	res.Proof.ParentContractID = c.ParentID().String()
	res.Proof.ContextDigest = c.ContextDigest()
	if anc := c.CausalAncestry(); len(anc) > 0 {
		res.Proof.CausalAncestry = make([]string, 0, len(anc))
		for _, id := range anc {
			res.Proof.CausalAncestry = append(res.Proof.CausalAncestry, id.String())
		}
	}
}

// verificationGateState reports whether a real verification gate executed and
// whether it passed. A zero report or an explicitly Skipped gate never counts
// as "ran" — a skipped gate is not-applicable, not a pass and not a failure.
func verificationGateState(r VerificationReport) (ran bool, passed bool) {
	if r.Skipped {
		return false, false
	}
	ran = len(r.Results) > 0 || r.Passed
	return ran, r.Passed
}

// sealTerminalEvidence constructs the IMMUTABLE ExecutionEvidence for one
// terminated execution attempt from the runtime facts already on the result:
// contract identity, sealed context digest, canonical outcome, mutation-set
// summary (with taint rules) and the precise time window. The evidence is
// attached to res.Evidence exactly once and published on the domain bus so
// downstream projectors consume it as their sole authoritative terminal
// artifact. Executions still held at the approval gate are NOT terminated —
// no evidence exists for them yet.
func (x *RuntimeExecutor) sealTerminalEvidence(res *ExecutionResult) {
	if res == nil || res.Proof == nil || res.Evidence != nil {
		return
	}
	p := res.Proof
	if p.ContractID == "" || res.PendingPatchID != "" {
		// No identity (pre-admission failure) or still held at the approval
		// gate: the execution has NOT terminated — no evidence exists yet.
		return
	}
	// Phase 3 OCC commit gate: the abort outcome is NEVER derived by the
	// coarse mapper — it is produced here, exclusively from the runtime's own
	// OccAborted flag set by the pre-commit baseline verification.
	var outcome ExecutionOutcome
	if p.OccAborted {
		outcome = EvidenceAbortedOCC
	} else {
		outcome = evidenceOutcomeFor(p.Outcome, res.Err)
	}
	mutations := p.Mutations
	if len(mutations) == 0 {
		mutations = res.Mutations
	}
	ran, passed := verificationGateState(p.Verification)
	targets := p.Targets
	if len(targets) == 0 {
		targets = res.Targets
	}
	summary := summarizeMutationSet(p.TransactionID, targets, mutations, p.Outcome, ran, passed)
	if p.OccAborted && !summary.Tainted {
		// An OCC abort always taints the attempt: the held proposal is stale,
		// and every projector must invalidate its tentative state even though
		// zero bytes reached the workspace.
		summary.Tainted = true
	}
	res.Evidence = sealEvidence(
		ContractID(p.ContractID), AttemptID(p.AttemptID),
		x.contracts.Contract(ContractID(p.ContractID)),
		p.ContextDigest, outcome, summary, p.StartedAt, p.FinishedAt,
	)
	if res.IngestionTrace != nil {
		// Forensic invariant: preserve the transport-normalization record on
		// the immutable evidence for post-mortem debugging.
		res.Evidence.ingestionTrace = res.IngestionTrace
	}
	x.emitEvidenceEvent(res, res.Evidence)
}

// emitEvidenceEvent publishes the canonical execution.evidence event. The
// payload carries scalar facts only (no live pointers), so every bus consumer
// projects from immutable truth.
func (x *RuntimeExecutor) emitEvidenceEvent(res *ExecutionResult, ev *ExecutionEvidence) {
	if res == nil || ev == nil {
		return
	}
	ancestry := make([]string, 0, len(ev.CausalAncestry()))
	for _, id := range ev.CausalAncestry() {
		ancestry = append(ancestry, id.String())
	}
	x.emit(events.NewExecutionEvidence(events.ExecutionEvidencePayload{
		RequestID:        res.RequestID,
		SessionID:        res.SessionID,
		ContractID:       ev.ContractID().String(),
		AttemptID:        uint32(ev.AttemptID()),
		ParentContractID: ev.ParentContractID().String(),
		CausalAncestry:   ancestry,
		ContextDigest:    ev.ContextDigest(),
		Outcome:          string(ev.Outcome()),
		Tainted:          ev.Mutations().Tainted,
		Targets:          ev.Mutations().Targets,
		FilesMutated:     ev.Mutations().FilesMutated,
		TransactionID:    ev.Mutations().TransactionID,
		StartedAt:        ev.StartedAt(),
		FinishedAt:       ev.FinishedAt(),
	}))
}

// finalizeResult stamps the authoritative terminal usage account (provider,
// model, aggregate input/output tokens, latency, artifact) onto the result. It
// is the SINGLE place token accounting is computed — from the provider-reported
// ModelInvocations — so the renderer reads ExecutionResult.Completed and never
// re-sums usage. It must be called on every terminal return path of the
// executor (Execute / Approve / Reject). Phase 2 P2: it also seals the
// immutable ExecutionEvidence on every TERMINAL path — the single authoritative
// record downstream projectors consume.
func (x *RuntimeExecutor) finalizeResult(res *ExecutionResult) *ExecutionResult {
	if res == nil {
		return nil
	}
	cc := res.Completed
	cc.Provider = x.providerName()
	cc.SessionID = res.SessionID
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
	x.sealTerminalEvidence(res)
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

// patchOnlyArtifact reports whether the profile's artifact contract demands a
// structured bounded patch (search_replace) rather than tolerating a full-file
// replacement. This is the machine-readable recovery signal: when true, the
// executor asks the model ONLY for SEARCH/REPLACE / unified-diff output and
// structurally cannot accept a complete-file artifact.
func patchOnlyArtifact(p strategy.ExecutionStrategyProfile) bool {
	return p.Artifact.Bounded && p.Artifact.Kind == "search_replace"
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

// boundedPatchSystemPrompt is the STRICT bounded-patch contract used when the
// artifact contract demands search_replace (e.g. recovery after an output
// truncation). Unlike the tolerant mutation prompt it does NOT offer the
// full-file option and REQUIRES exactly one anchored SEARCH/REPLACE block: the
// executor rejects any other shape at the artifact boundary.
func boundedPatchSystemPrompt() string {
	return `You are the bounded patch engine. You improve a SMALL EXCERPT of one target
file by emitting EXACTLY ONE minimal SEARCH/REPLACE block. You MUST NOT rewrite or re-emit the
whole file.

Output format — this block and NOTHING else (no prose, no markdown fences):

<<<<<<< SEARCH
<1-10 consecutive lines copied BYTE-FOR-BYTE from the provided context window>
=======
<your replacement lines>
>>>>>>>

Rules:
- The SEARCH text MUST be copied verbatim from the context window and MUST
  appear exactly once in the file.
- Keep REPLACE to the minimum lines that satisfy the request.
- Never emit the full file content. Never explain.`
}

// maxBoundedPatchContextBytes bounds the runtime-derived context window of a
// bounded-patch attempt. It is deliberately small enough that even a degenerate
// model response that echoes the entire window completes far below a 1024-token
// output ceiling — the recovery invocation can no longer finish with
// finish_reason=length by construction.
const maxBoundedPatchContextBytes = 2048

// maxEscalatedPatchContextBytes is the BROADER boundary window granted to a
// NO-OP escalation attempt: a claim that conflicted with structural evidence
// re-judges against materially more of the assigned slice before the runtime
// accepts any human escalation. The ceiling stays bounded — escalation widens
// scrutiny, never the response budget.
const maxEscalatedPatchContextBytes = maxBoundedPatchContextBytes * 4

// maxNoOpEscalationFocusMargin is the minimum line margin a region-focused
// NO-OP escalation widens its assigned interval by on each side, so even a
// single-line sub-task re-judges against real surrounding structure.
const maxNoOpEscalationFocusMargin = 10

// focusSlice returns the [start,end] inclusive 1-indexed line window of
// original plus the offset that must be added to relative chunk line numbers
// to recover absolute document lines. Out-of-range bounds clamp to the file;
// an empty result (no focus, empty file or inverted range after clamping)
// signals the caller to fall back to whole-file rotation.
func focusSlice(original string, start, end int) (string, int) {
	if strings.TrimSpace(original) == "" || start < 1 || end < start {
		return "", 0
	}
	lines := strings.Split(original, "\n")
	total := len(lines)
	if start > total {
		return "", 0
	}
	if end > total {
		end = total
	}
	return strings.Join(lines[start-1:end], "\n"), start - 1
}

// boundedPatchWindow is one deterministic line-aligned window of the target.
type boundedPatchWindow struct {
	content    string // exact bytes shown to the model (the only copyable source)
	startLine  int    // 1-indexed inclusive
	endLine    int    // 1-indexed inclusive
	totalLines int
}

// selectBoundedPatchWindow splits original into deterministic line-aligned
// chunks of at most maxBoundedPatchContextBytes and returns the chunk selected
// by attempt. Rotating windows across repair cycles gives every retry
// materially different content (never an identical re-send) while keeping each
// request's copyable source small; attempt 1 (the first patch attempt) anchors
// at the head of the file.
func selectBoundedPatchWindow(original string, attempt int) boundedPatchWindow {
	return selectBoundedPatchWindowScaled(original, attempt, maxBoundedPatchContextBytes)
}

// selectBoundedPatchWindowScaled is the byte-bound-parameterized window
// selector. NO-OP escalation attempts pass maxEscalatedPatchContextBytes so
// the re-hydrated judgment sees a broader boundary window of the slice.
func selectBoundedPatchWindowScaled(original string, attempt, maxContextBytes int) boundedPatchWindow {
	lines := strings.Split(original, "\n")
	total := len(lines)
	if total == 0 || strings.TrimSpace(original) == "" {
		return boundedPatchWindow{content: "", startLine: 1, endLine: 0, totalLines: 0}
	}
	// Greedy chunking on byte size with line alignment.
	var chunks []boundedPatchWindow
	start := 0
	size := 0
	for i := 0; i < total; i++ {
		lineSize := len(lines[i]) + 1
		if size > 0 && size+lineSize > maxContextBytes {
			chunks = append(chunks, boundedPatchWindow{
				content:    strings.Join(lines[start:i], "\n"),
				startLine:  start + 1,
				endLine:    i,
				totalLines: total,
			})
			start = i
			size = 0
		}
		size += lineSize
	}
	chunks = append(chunks, boundedPatchWindow{
		content:    strings.Join(lines[start:], "\n"),
		startLine:  start + 1,
		endLine:    total,
		totalLines: total,
	})
	// Byte-level clamp: a file with no newline boundaries (or one huge line)
	// cannot be split along lines, so the window content is truncated to the
	// cap directly — the copyable source stays bounded by construction.
	for i := range chunks {
		if len(chunks[i].content) > maxBoundedPatchContextBytes {
			chunks[i].content = chunks[i].content[:maxBoundedPatchContextBytes]
		}
	}
	idx := (attempt - 1) % len(chunks)
	return chunks[idx]
}

// buildBoundedPatchUserPrompt builds the bounded-patch USER message. The
// message SCOPES THE TASK, not just the context: without an explicitly
// bounded task a vague "rewrite it" goal makes small models regenerate an
// entire document from imagination (the live OpenRouter repro: 898 input /
// 1024 output / length even with a 2KB window). So the protocol and its hard
// limits lead, the user goal is demoted to background, the format skeleton is
// stated up front, and ONLY the runtime-derived window is presented as the
// mutable region — the complete file never crosses to the model in this mode.
func buildBoundedPatchUserPrompt(request, evidence, target string, window boundedPatchWindow) string {
	var b strings.Builder
	fmt.Fprintf(&b, "TASK: improve a SMALL EXCERPT of %s by returning EXACTLY ONE SEARCH/REPLACE block.\n\n", target)
	b.WriteString("HARD LIMITS:\n")
	b.WriteString("- Do NOT regenerate the document. Do NOT write a new file. Do NOT continue the page.\n")
	b.WriteString("- Change AT MOST 5 consecutive lines. Your ENTIRE response is one small block.\n\n")
	b.WriteString("OUTPUT FORMAT (your entire response):\n")
	b.WriteString("<<<<<<< SEARCH\n<1-10 consecutive lines copied BYTE-FOR-BYTE from the context window below>\n=======\n<your improved version of those lines>\n>>>>>>>\n\n")
	b.WriteString("### USER GOAL (background only — apply it to the excerpt, not the whole file)\n")
	b.WriteString(strings.TrimSpace(request))
	b.WriteString("\n\n")
	if trimmed := strings.TrimSpace(evidence); trimmed != "" {
		b.WriteString("### PRIOR FAILURE EVIDENCE\n")
		ev := trimmed
		const maxEvidenceBytes = 1500
		if len(ev) > maxEvidenceBytes {
			ev = ev[:maxEvidenceBytes] + "\n...(truncated)"
		}
		b.WriteString(ev)
		b.WriteString("\n\n")
	}
	if window.totalLines == 0 {
		b.WriteString("### TARGET FILE: ")
		b.WriteString(target)
		b.WriteString("\n(the file is empty — instead output the FULL new content inside one code fence)\n")
		return b.String()
	}
	fmt.Fprintf(&b, "### CONTEXT WINDOW — %s lines %d-%d of %d (the ONLY lines you may touch)\n",
		target, window.startLine, window.endLine, window.totalLines)
	b.WriteString(window.content)
	b.WriteString("\n\nREMINDERS: pick at most 5 consecutive lines from the window; copy them into SEARCH byte-for-byte (exact match is verified); put the improved lines in REPLACE. No prose. No markdown fences. Never the whole file.")
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
