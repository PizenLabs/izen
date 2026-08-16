// Package graph implements the RUNTIME-OWNED execution graph. It is distinct
// from the strategy-compiled graph (internal/execution/strategy): the strategy
// graph describes WHAT Izen intends to execute; this graph is the ACTUAL
// lifecycle the RuntimeExecutor drives at runtime, and its transitions ARE the
// canonical runtime events.
//
// Invariants:
//
//   - Explicit: every execution stage is a first-class node of the graph. The
//     runtime never performs a step that is not a node.
//   - Deterministic: given the same strategy the graph topology is fixed and
//     nodes run in dependency order.
//   - Event-generating: events are generated ONLY from graph transitions. The
//     presentation layer never infers progress and the runtime never emits a
//     manually-injected progress message.
//   - Sequential now, parallel-ready: the graph tracks explicit dependencies
//     and can schedule runnable nodes topologically; today every topology is a
//     chain, tomorrow a DAG.
//   - Evidence-producing: each completed stage records its evidence, and the
//     graph folds into ExecutionProof.
package graph

import (
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/PizenLabs/izen/internal/events"
)

// StageKind is the bounded set of runtime execution stages. Adding a stage is
// an architectural decision, not a patch.
type StageKind string

// Canonical runtime execution stages. They mirror the required lifecycle:
// user intent → strategy → target → context → model → artifact → approval →
// mutation → verification → completion.
const (
	StageUserIntent          StageKind = "user_intent"
	StageStrategySelection   StageKind = "strategy_selection"
	StageTargetResolution    StageKind = "target_resolution"
	StageContextCompilation  StageKind = "context_compilation"
	StageModelInvocation     StageKind = "model_invocation"
	StageArtifactValidation  StageKind = "artifact_validation"
	StageApprovalGate        StageKind = "approval_gate"
	StageMutationTransaction StageKind = "mutation_transaction"
	StageVerification        StageKind = "verification"
	StageCompletion          StageKind = "completion"
)

// Label returns the compact machine label of the stage.
func (k StageKind) Label() string { return string(k) }

// StageState is the lifecycle state of one execution stage.
type StageState string

// Stage lifecycle states.
const (
	StagePending   StageState = "pending"
	StageRunning   StageState = "running"
	StageComplete  StageState = "complete"
	StageSkipped   StageState = "skipped"
	StageFailed    StageState = "failed"
	StageCancelled StageState = "cancelled"
)

// Terminal reports whether the stage state is terminal.
func (s StageState) Terminal() bool {
	switch s {
	case StageComplete, StageSkipped, StageFailed, StageCancelled:
		return true
	}
	return false
}

// Succeeded reports whether the stage provably produced its outcome.
func (s StageState) Succeeded() bool { return s == StageComplete || s == StageSkipped }

// Phase is the lifecycle phase of the whole execution graph.
type Phase string

// Graph lifecycle phases.
const (
	PhaseIdle             Phase = "idle"
	PhaseRunning          Phase = "running"
	PhaseAwaitingApproval Phase = "awaiting_approval"
	PhaseCompleted        Phase = "completed"
	PhaseFailed           Phase = "failed"
	PhaseCancelled        Phase = "cancelled"
)

// Terminal reports whether the graph reached a terminal phase.
func (p Phase) Terminal() bool {
	switch p {
	case PhaseCompleted, PhaseFailed, PhaseCancelled:
		return true
	}
	return false
}

// Stage is one explicit execution node.
type Stage struct {
	// ID is the stable ordinal identity within the graph ("s1", "s2", ...).
	ID string
	// Kind is the typed execution stage.
	Kind StageKind
	// State is the stage lifecycle state.
	State StageState
	// Dependencies are the stage IDs this stage depends on.
	Dependencies []string
	// Evidence is what the stage produced when it reached a terminal state.
	Evidence string
	// StartedAt / FinishedAt bound the stage's wall-clock execution window.
	StartedAt  time.Time
	FinishedAt time.Time
}

// Edge is a dependency edge: To depends on From.
type Edge struct {
	From   string // prerequisite stage ID
	To     string // dependent stage ID
	Reason string // why the dependency exists
}

// Emitter publishes a canonical runtime lifecycle event. It is the graph's only
// output channel — events are generated from graph transitions.
type Emitter func(ev events.DomainEvent)

// graphIDCounter produces monotonic graph IDs.
var graphIDCounter atomic.Uint64

// Graph is the runtime-owned execution graph of ONE user execution. The
// RuntimeExecutor drives its stages; every transition emits the canonical
// event and records evidence for ExecutionProof.
type Graph struct {
	// ID is the stable identity of the execution graph.
	ID string
	// RequestID correlates the graph with its lifecycle events.
	RequestID string
	// Phase is the graph lifecycle phase.
	Phase Phase
	// Stages is the deterministic, compile-ordered stage sequence.
	Stages []*Stage
	// Edges are the explicit dependency edges.
	Edges []Edge
	// emit is the event emitter (may be nil to disable emission).
	emit Emitter
	// StartedAt / FinishedAt bound the whole execution.
	StartedAt  time.Time
	FinishedAt time.Time
}

// New constructs a fresh runtime graph with the canonical stage topology. The
// topology is deterministic and ordered; the strategy decides which stages
// complete and which are explicitly skipped. emit may be nil (headless).
func New(requestID string, emit Emitter) *Graph {
	if requestID == "" {
		requestID = fmt.Sprintf("g-%d", graphIDCounter.Add(1))
	}
	g := &Graph{
		ID:        fmt.Sprintf("eg-%d", graphIDCounter.Add(1)),
		RequestID: requestID,
		Phase:     PhaseIdle,
		emit:      emit,
	}
	chain := []StageKind{
		StageUserIntent,
		StageStrategySelection,
		StageTargetResolution,
		StageContextCompilation,
		StageModelInvocation,
		StageArtifactValidation,
		StageApprovalGate,
		StageMutationTransaction,
		StageVerification,
		StageCompletion,
	}
	for i, k := range chain {
		g.Stages = append(g.Stages, &Stage{ID: fmt.Sprintf("s%d", i+1), Kind: k, State: StagePending})
	}
	// Sequential chain edges: every stage depends on its predecessor. The
	// dependency model is real — parallel topologies only require adding DAG
	// edges (the scheduler is already dependency-aware).
	for i := 1; i < len(chain); i++ {
		g.Edges = append(g.Edges, Edge{From: g.Stages[i-1].ID, To: g.Stages[i].ID, Reason: "sequential execution"})
	}
	for _, e := range g.Edges {
		if s := g.Node(e.To); s != nil {
			s.Dependencies = append(s.Dependencies, e.From)
		}
	}
	return g
}

// Stage returns the stage with the given kind, or nil.
func (g *Graph) Stage(kind StageKind) *Stage {
	if g == nil {
		return nil
	}
	for _, s := range g.Stages {
		if s.Kind == kind {
			return s
		}
	}
	return nil
}

// Node returns the stage with the given ordinal ID, or nil.
func (g *Graph) Node(id string) *Stage {
	if g == nil {
		return nil
	}
	for _, s := range g.Stages {
		if s.ID == id {
			return s
		}
	}
	return nil
}

// Runnable returns the stages whose dependencies are all terminal (complete or
// skipped) and that are not yet terminal themselves — the frontier the runtime
// may execute next. Today every topology is a chain, so the frontier holds at
// most one stage; parallel topologies will yield multiple runnable stages.
func (g *Graph) Runnable() []*Stage {
	if g == nil || g.Phase.Terminal() {
		return nil
	}
	var out []*Stage
	for _, s := range g.Stages {
		if s.State.Terminal() {
			continue
		}
		ready := true
		for _, dep := range s.Dependencies {
			if d := g.Node(dep); d == nil || !d.State.Terminal() {
				ready = false
				break
			}
		}
		if ready {
			out = append(out, s)
		}
	}
	return out
}

// Ready reports whether the stage's dependencies are all satisfied.
func (g *Graph) Ready(kind StageKind) bool {
	s := g.Stage(kind)
	if s == nil || s.State.Terminal() {
		return false
	}
	for _, dep := range s.Dependencies {
		if d := g.Node(dep); d == nil || !d.State.Terminal() {
			return false
		}
	}
	return true
}

// emit publishes an event through the injected emitter.
func (g *Graph) emitEvent(ev events.DomainEvent) {
	if g != nil && g.emit != nil && ev != nil {
		g.emit(ev)
	}
}

// Begin marks a stage running.
func (g *Graph) Begin(kind StageKind) {
	if g == nil || g.Phase.Terminal() {
		return
	}
	if s := g.Stage(kind); s != nil && !s.State.Terminal() {
		s.State = StageRunning
		s.StartedAt = time.Now()
	}
}

// Start opens the execution: the graph enters running and emits
// execution.started. It is the first transition of every execution.
func (g *Graph) Start(mode, prompt string) {
	if g == nil || g.Phase.Terminal() {
		return
	}
	g.Phase = PhaseRunning
	g.StartedAt = time.Now()
	g.emitEvent(events.NewExecutionStarted(g.RequestID, mode, prompt))
}

// CompleteUserIntent closes the user-intent stage (no canonical event — the
// intent is carried by execution.started).
func (g *Graph) CompleteUserIntent() {
	g.Complete(StageUserIntent, "")
}

// CompleteStrategy closes the strategy-selection stage and emits
// strategy.selected.
func (g *Graph) CompleteStrategy(strategy string, modelRequired bool, reason string) {
	g.Complete(StageStrategySelection, strategy)
	g.emitEvent(events.NewStrategySelected(g.RequestID, strategy, modelRequired, reason))
}

// CompleteTarget closes the target-resolution stage (per resolved target) and
// emits target.resolved.
func (g *Graph) CompleteTarget(target string, exists bool, source string) {
	g.Complete(StageTargetResolution, target)
	g.emitEvent(events.NewTargetResolved(g.RequestID, target, exists, source))
}

// CompleteContext closes the context-compilation stage and emits
// context.prepared.
func (g *Graph) CompleteContext(channels []string, tokens int) {
	g.Complete(StageContextCompilation, fmt.Sprintf("%d channels, %d tokens", len(channels), tokens))
	g.emitEvent(events.NewContextPrepared(g.RequestID, channels, tokens))
}

// BeginModel opens the model-invocation stage and emits model.invoked BEFORE
// the provider call.
func (g *Graph) BeginModel(model string) {
	g.Begin(StageModelInvocation)
	g.emitEvent(events.NewModelInvoked(g.RequestID, model, 0, 0))
}

// CompleteModel closes the model-invocation stage on a successful response and
// emits provider.response with the authoritative usage. A failed invocation
// never reaches this transition.
func (g *Graph) CompleteModel(model string, tokenInput, tokenOutput int) {
	g.Complete(StageModelInvocation, model)
	g.emitEvent(events.NewProviderResponse(g.RequestID, model, tokenInput, tokenOutput))
}

// BeginWaiting emits provider.waiting — the provider round-trip is in flight
// and no byte has arrived yet. It is emitted right before the streaming
// invocation begins so the model stage truthfully reads "waiting", never a
// fabricated thinking/processing claim.
func (g *Graph) BeginWaiting(model string) {
	g.emitEvent(events.NewProviderWaiting(g.RequestID, model))
}

// FirstToken emits provider.first_token when the first provider byte arrives.
// Latency is measured from invocation begin (BeginModel/BeginWaiting) to the
// first byte — the truthful first-token latency of the model stage.
func (g *Graph) FirstToken(model string, latency time.Duration) {
	g.emitEvent(events.NewProviderFirstToken(g.RequestID, model, latency))
}

// StreamDelta emits one content delta of the live provider stream. It is pure
// evidence transport: the authoritative content always travels on the
// ExecutionResult, so a dropped delta never loses execution truth.
func (g *Graph) StreamDelta(delta string) {
	g.emitEvent(events.NewProviderStreamDelta(g.RequestID, delta))
}

// UpdateUsage emits the cumulative provider-reported usage of the live stream
// (authoritative counts only — never a local estimate).
func (g *Graph) UpdateUsage(model string, inputTokens, outputTokens, reasoningTokens int) {
	g.emitEvent(events.NewProviderUsageUpdate(g.RequestID, model, inputTokens, outputTokens, reasoningTokens))
}

// ReasoningTelemetry emits reasoning TELEMETRY only: the wall-clock reasoning
// duration and the provider-reported reasoning token count when available.
// Raw chain-of-thought text never travels on the event stream.
func (g *Graph) ReasoningTelemetry(model string, duration time.Duration, tokens int) {
	g.emitEvent(events.NewReasoningTelemetry(g.RequestID, model, duration, tokens))
}

// CompleteArtifact closes the artifact-validation stage and emits
// artifact.produced. It can never precede CompleteModel for the same execution.
func (g *Graph) CompleteArtifact(kind, target string) {
	g.Complete(StageArtifactValidation, kind)
	g.emitEvent(events.NewArtifactProduced(g.RequestID, kind, target))
}

// WaitApproval parks the graph at the approval gate and emits approval.required.
func (g *Graph) WaitApproval(target, preview string) {
	g.Wait(StageApprovalGate, "awaiting human approval")
	g.emitEvent(events.NewApprovalRequired(g.RequestID, target, preview))
}

// BeginMutation opens the mutation transaction stage and emits mutation.started.
func (g *Graph) BeginMutation(targets []string) {
	g.Resume()
	g.Begin(StageMutationTransaction)
	g.emitEvent(events.NewMutationStarted(g.RequestID, targets))
}

// CompleteMutation closes a per-target mutation and emits mutation.completed.
func (g *Graph) CompleteMutation(target, outcome string) {
	g.Complete(StageMutationTransaction, target+"="+outcome)
	g.emitEvent(events.NewMutationCompleted(g.RequestID, target, outcome))
}

// CompleteVerification closes the verification stage and emits
// verification.completed with the real verifier result.
func (g *Graph) CompleteVerification(passed bool, steps []string) {
	g.Complete(StageVerification, fmt.Sprintf("passed=%t", passed))
	g.emitEvent(events.NewVerificationCompleted(g.RequestID, passed, steps))
}

// Skip marks a stage as cleanly unnecessary (its boundary is never reached).
func (g *Graph) Skip(kind StageKind, reason string) {
	if g == nil {
		return
	}
	if s := g.Stage(kind); s != nil && !s.State.Terminal() {
		s.State = StageSkipped
		s.Evidence = reason
		s.StartedAt = time.Now()
		s.FinishedAt = time.Now()
	}
}

// Complete marks a stage complete with the evidence it produced. It is the
// transition that generates the stage's canonical event (mapped per stage by
// the executor via the stage's evidence; the graph itself records evidence).
func (g *Graph) Complete(kind StageKind, evidence string) {
	if g == nil {
		return
	}
	if s := g.Stage(kind); s != nil && !s.State.Terminal() {
		s.State = StageComplete
		s.Evidence = evidence
		s.FinishedAt = time.Now()
	}
}

// Wait parks the graph at the approval gate. The graph pauses in the
// awaiting_approval phase until the runtime resumes it via Approve/Reject.
func (g *Graph) Wait(kind StageKind, reason string) {
	if g == nil || g.Phase.Terminal() {
		return
	}
	if s := g.Stage(kind); s != nil && !s.State.Terminal() {
		s.State = StageRunning
		s.StartedAt = time.Now()
		s.Evidence = reason
	}
	g.Phase = PhaseAwaitingApproval
}

// Resume resumes a graph parked at a human boundary (BeginMutation after
// approval).
func (g *Graph) Resume() {
	if g == nil || g.Phase.Terminal() {
		return
	}
	if g.Phase == PhaseAwaitingApproval {
		g.Phase = PhaseRunning
	}
}

// CompleteExecution terminates the graph successfully. It emits
// execution.finished(success=true) and records the outcome as completion
// evidence.
func (g *Graph) CompleteExecution(outcome string) {
	if g == nil || g.Phase.Terminal() {
		return
	}
	g.FinishedAt = time.Now()
	g.Phase = PhaseCompleted
	if s := g.Stage(StageCompletion); s != nil && !s.State.Terminal() {
		s.State = StageComplete
		s.Evidence = outcome
		s.FinishedAt = g.FinishedAt
	}
	g.emitEvent(events.NewExecutionFinished(g.RequestID, true, outcome))
}

// FailExecution terminates the graph as a failure: it emits execution.failed
// (with classification + stage) followed by execution.finished(success=false).
func (g *Graph) FailExecution(classification events.FailureClassification, err error, stage string) {
	if g == nil || g.Phase.Terminal() {
		return
	}
	g.FinishedAt = time.Now()
	g.Phase = PhaseFailed
	if s := g.Stage(StageCompletion); s != nil && !s.State.Terminal() {
		s.State = StageFailed
		if err != nil {
			s.Evidence = err.Error()
		}
		s.FinishedAt = g.FinishedAt
	}
	g.emitEvent(events.NewExecutionFailed(classification, err, stage))
	g.emitEvent(events.NewExecutionFinished(g.RequestID, false, string(PhaseFailed)))
}

// CancelExecution terminates the graph cleanly (cancellation / clarification /
// rejection) with execution.finished(success=false, outcome). It never emits
// execution.failed — a clean cancellation is not a failure.
func (g *Graph) CancelExecution(outcome string) {
	if g == nil || g.Phase.Terminal() {
		return
	}
	g.FinishedAt = time.Now()
	g.Phase = PhaseCancelled
	if s := g.Stage(StageCompletion); s != nil && !s.State.Terminal() {
		s.State = StageCancelled
		s.Evidence = outcome
		s.FinishedAt = g.FinishedAt
	}
	g.emitEvent(events.NewExecutionFinished(g.RequestID, false, outcome))
}

// Terminal reports whether the graph reached a terminal phase.
func (g *Graph) Terminal() bool { return g != nil && g.Phase.Terminal() }

// EvidencelessStageNames maps runtime stages onto the compact proof step names
// existing consumers read (strategy_selected, context_prepared,
// artifact_produced, mutate, verify, ...).
func proofStepName(kind StageKind) string {
	switch kind {
	case StageStrategySelection:
		return "strategy_selected"
	case StageTargetResolution:
		return "target_resolved"
	case StageContextCompilation:
		return "context_prepared"
	case StageModelInvocation:
		return "model_invoked"
	case StageArtifactValidation:
		return "artifact_produced"
	case StageApprovalGate:
		return "approval"
	case StageMutationTransaction:
		return "mutate"
	case StageVerification:
		return "verify"
	case StageCompletion:
		return "completion"
	default:
		return string(kind)
	}
}

// StageSnapshot is the JSON-safe per-stage evidence record folded into
// ExecutionProof.
type StageSnapshot struct {
	ID        string    `json:"id"`
	Kind      string    `json:"stage"`
	State     string    `json:"state"`
	Evidence  string    `json:"evidence,omitempty"`
	StartedAt time.Time `json:"started_at"`
}

// Evidence returns the compact, ordered evidence record of the graph — the
// graph's contribution to ExecutionProof. Only non-pending stages are reported;
// a pending stage is a stage that was never reached (honest, not fabricated).
func (g *Graph) Evidence() []StageSnapshot {
	if g == nil {
		return nil
	}
	out := make([]StageSnapshot, 0, len(g.Stages))
	for _, s := range g.Stages {
		if s.State == StagePending {
			continue
		}
		out = append(out, StageSnapshot{
			ID:        s.ID,
			Kind:      proofStepName(s.Kind),
			State:     string(s.State),
			Evidence:  s.Evidence,
			StartedAt: s.StartedAt,
		})
	}
	return out
}

// String renders the graph as a compact inspectable record.
func (g *Graph) String() string {
	if g == nil {
		return "execution-graph: <nil>"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "execution-graph: %s phase=%s", g.ID, g.Phase)
	for _, s := range g.Stages {
		fmt.Fprintf(&b, "\n  %s %s: %s", s.ID, s.Kind.Label(), s.State)
		if s.Evidence != "" {
			fmt.Fprintf(&b, " — %s", s.Evidence)
		}
	}
	return b.String()
}
