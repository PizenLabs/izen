// Package telemetry implements Phase 5 of the Izen engine: cross-cutting
// telemetry, session replay and strategy feedback optimization.
//
// Telemetry observes all five engine layers (knowledge resolution, capability
// graph, context governance, execution pipeline, validation DAG) as a
// read-only projection. It never mutates layer state and never blocks the
// main execution pipelines: every event is published into a non-blocking,
// channel-backed EventBus and each consumer runs on its own worker goroutine.
//
// The event log serves two masters: Audit/Replay and Strategy Optimization. A
// Timeline records the ordered event stream of one request session for JSON
// export and decision-path reconstruction (ReplayTimeline); the
// StrategyOptimizer folds pass rates, token usage and latency back into
// ContextPolicy recommendations without touching any prompt.
package telemetry

import (
	"sort"
	"time"

	"github.com/PizenLabs/izen/pkg/engine/ir"
	"github.com/PizenLabs/izen/pkg/engine/layer0"
	"github.com/PizenLabs/izen/pkg/engine/layer1"
	"github.com/PizenLabs/izen/pkg/engine/layer2"
	"github.com/PizenLabs/izen/pkg/engine/layer3"
	"github.com/PizenLabs/izen/pkg/engine/layer4"
)

// EventType discriminates the telemetry events emitted by the engine layers.
type EventType string

const (
	// EventKnowledgeResolved is emitted when Layer 0 completes a workspace
	// knowledge scan.
	EventKnowledgeResolved EventType = "layer0.knowledge_resolved"
	// EventCapabilityDetected is emitted when Layer 1 builds a capability
	// graph for a workspace.
	EventCapabilityDetected EventType = "layer1.capability_detected"
	// EventContextGoverned is emitted when Layer 2 assembles a
	// policy-governed execution context.
	EventContextGoverned EventType = "layer2.context_governed"
	// EventPipelineStep is emitted for each execution stage transition in
	// Layer 3.
	EventPipelineStep EventType = "layer3.pipeline_step"
	// EventValidationDAG is emitted when Layer 4 finishes a validation DAG
	// run.
	EventValidationDAG EventType = "layer4.validation_dag"
	// EventControlIteration is emitted by the adaptive control loop after each
	// Observe, carrying the raw Dynamic IR facts (node states + attempts).
	EventControlIteration EventType = "control.iteration"
	// EventControlNodeObserved is emitted when a node observation lands on the
	// control loop.
	EventControlNodeObserved EventType = "control.node_observed"
	// EventControlTerminated is emitted when the control loop reaches a
	// terminal directive.
	EventControlTerminated EventType = "control.terminated"
)

// Layer returns the engine layer that produces the event type.
func (t EventType) Layer() string {
	switch t {
	case EventKnowledgeResolved:
		return "layer0"
	case EventCapabilityDetected:
		return "layer1"
	case EventContextGoverned:
		return "layer2"
	case EventPipelineStep:
		return "layer3"
	case EventValidationDAG:
		return "layer4"
	default:
		return "unknown"
	}
}

// Event is the contract every telemetry event satisfies. Events are immutable
// after construction: the payload is fixed at creation and never mutated.
type Event interface {
	// Type returns the event discriminator.
	Type() EventType
	// Timestamp returns the wall-clock time the event was created.
	Timestamp() time.Time
	// Payload returns the strongly-typed event payload.
	Payload() interface{}
}

// event is the shared Event implementation. All events are immutable: the
// payload is set at construction and never mutated.
type event struct {
	typ       EventType
	timestamp time.Time
	payload   interface{}
}

func (e *event) Type() EventType      { return e.typ }
func (e *event) Timestamp() time.Time { return e.timestamp }
func (e *event) Payload() interface{} { return e.payload }

func newEvent(typ EventType, payload interface{}) Event {
	return &event{
		typ:       typ,
		timestamp: time.Now(),
		payload:   payload,
	}
}

// errString renders an error for payloads, preserving a stable empty value
// for nil errors.
func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// ── Layer 0: Knowledge Resolution ───────────────────────────────────────────

// KnowledgeResolvedPayload carries the outcome of a Layer 0 workspace
// knowledge scan.
type KnowledgeResolvedPayload struct {
	Root           string        `json:"root"`
	PrimaryManager string        `json:"primary_manager"`
	Managers       int           `json:"managers"`
	Conventions    int           `json:"conventions"`
	Constraints    int           `json:"constraints"`
	Conflicts      int           `json:"conflicts"`
	Duration       time.Duration `json:"duration_ns"`
}

// NewKnowledgeResolved projects a Layer 0 knowledge scan into a telemetry
// event. The payload keeps only serializable aggregates; the full
// ResolvedKnowledge is never captured.
func NewKnowledgeResolved(k *layer0.ResolvedKnowledge, d time.Duration) Event {
	payload := &KnowledgeResolvedPayload{Duration: d}
	if k != nil {
		payload.Root = k.Root
		payload.PrimaryManager = string(k.PrimaryManager)
		payload.Managers = len(k.Managers)
		payload.Conventions = len(k.ActiveConventions)
		payload.Constraints = len(k.StructuralConstraints)
		payload.Conflicts = len(k.Conflicts)
	}
	return newEvent(EventKnowledgeResolved, payload)
}

// ── Layer 1: Capability Detection ───────────────────────────────────────────

// CapabilityDetectedPayload carries the outcome of a Layer 1 capability
// detection pass.
type CapabilityDetectedPayload struct {
	Stack        string        `json:"stack"`
	Capabilities []string      `json:"capabilities,omitempty"`
	Duration     time.Duration `json:"duration_ns"`
}

// NewCapabilityDetected projects a Layer 1 capability graph into a telemetry
// event. Capabilities are emitted in the canonical declaration order of the
// layer.
func NewCapabilityDetected(g *layer1.Graph, d time.Duration) Event {
	payload := &CapabilityDetectedPayload{Stack: string(layer1.StackUnknown), Duration: d}
	if g != nil {
		payload.Stack = string(g.Stack())
		for _, c := range layer1.AllCapabilities() {
			if g.Supports(c) {
				payload.Capabilities = append(payload.Capabilities, string(c))
			}
		}
	}
	return newEvent(EventCapabilityDetected, payload)
}

// ── Layer 2: Context Governance ─────────────────────────────────────────────

// ContextGovernedPayload carries the token-budget, symbol-selection and
// compression outcome of a Layer 2 context assembly.
type ContextGovernedPayload struct {
	TargetFile       string        `json:"target_file,omitempty"`
	TargetSymbol     string        `json:"target_symbol,omitempty"`
	Files            int           `json:"files"`
	Symbols          int           `json:"symbols"`
	TokensUsed       int           `json:"tokens_used"`
	TokenBudget      int           `json:"token_budget"`
	BudgetMet        bool          `json:"budget_met"`
	CompressionRatio float64       `json:"compression_ratio"`
	CompressedFiles  int           `json:"compressed_files"`
	StrippedBodies   int           `json:"stripped_bodies"`
	Duration         time.Duration `json:"duration_ns"`
}

// NewContextGoverned projects a Layer 2 context assembly into a telemetry
// event. The execution context is read-only; only its stats are captured.
func NewContextGoverned(req layer2.ContextRequest, ctx *layer2.ExecutionContext, d time.Duration) Event {
	payload := &ContextGovernedPayload{
		TargetFile:   req.TargetFile,
		TargetSymbol: req.TargetSymbol,
		Duration:     d,
	}
	if ctx != nil {
		payload.Files = ctx.Stats.Files
		payload.Symbols = ctx.Stats.Symbols
		payload.TokensUsed = ctx.Stats.Tokens
		payload.TokenBudget = ctx.Stats.BudgetTokens
		payload.BudgetMet = ctx.Stats.BudgetMet
		payload.CompressionRatio = ctx.Policy.CompressionRatio
		payload.CompressedFiles = ctx.Stats.CompressedFiles
		payload.StrippedBodies = ctx.Stats.StrippedBodies
	}
	return newEvent(EventContextGoverned, payload)
}

// ── Layer 3: Pipeline Steps ─────────────────────────────────────────────────

// PipelineStepPayload carries one Layer 3 execution stage transition: the
// intent, the selected execution strategy and the stage execution time.
type PipelineStepPayload struct {
	RunID      string        `json:"run_id,omitempty"`
	Intent     string        `json:"intent"`
	Strategy   string        `json:"strategy"`
	Stage      string        `json:"stage"`
	StageIndex int           `json:"stage_index"`
	State      string        `json:"state"`
	Patches    int           `json:"patches,omitempty"`
	Tokens     int           `json:"tokens,omitempty"`
	Err        string        `json:"error,omitempty"`
	Duration   time.Duration `json:"duration_ns"`
}

// NewPipelineStepDone emits a successful Layer 3 stage transition.
func NewPipelineStepDone(runID string, intent layer3.Intent, route layer3.Route, stage layer3.Stage, index, patches, tokens int, d time.Duration) Event {
	return newEvent(EventPipelineStep, &PipelineStepPayload{
		RunID:      runID,
		Intent:     string(intent),
		Strategy:   route.String(),
		Stage:      string(stage),
		StageIndex: index,
		State:      string(layer3.StateDone),
		Patches:    patches,
		Tokens:     tokens,
		Duration:   d,
	})
}

// NewPipelineStepFailed emits a failed Layer 3 stage transition.
func NewPipelineStepFailed(runID string, intent layer3.Intent, route layer3.Route, stage layer3.Stage, index int, err error, d time.Duration) Event {
	return newEvent(EventPipelineStep, &PipelineStepPayload{
		RunID:      runID,
		Intent:     string(intent),
		Strategy:   route.String(),
		Stage:      string(stage),
		StageIndex: index,
		State:      string(layer3.StateFailed),
		Err:        errString(err),
		Duration:   d,
	})
}

// NewPipelineStepCancelled emits a Layer 3 stage transition aborted by
// context cancellation.
func NewPipelineStepCancelled(runID string, intent layer3.Intent, route layer3.Route, stage layer3.Stage, index int, err error, d time.Duration) Event {
	return newEvent(EventPipelineStep, &PipelineStepPayload{
		RunID:      runID,
		Intent:     string(intent),
		Strategy:   route.String(),
		Stage:      string(stage),
		StageIndex: index,
		State:      string(layer3.StateCancelled),
		Err:        errString(err),
		Duration:   d,
	})
}

// ── Layer 4: Validation DAG ─────────────────────────────────────────────────

// ValidationDAGPayload carries the pass/fail rates and short-circuit outcome
// of a Layer 4 validation DAG run.
type ValidationDAGPayload struct {
	OK             bool          `json:"ok"`
	NodesTotal     int           `json:"nodes_total"`
	NodesPassed    int           `json:"nodes_passed"`
	NodesFailed    int           `json:"nodes_failed"`
	NodesSkipped   int           `json:"nodes_skipped"`
	Stages         []string      `json:"stages,omitempty"`
	FailedStages   []string      `json:"failed_stages,omitempty"`
	ShortCircuited bool          `json:"short_circuited,omitempty"`
	Err            string        `json:"error,omitempty"`
	Duration       time.Duration `json:"duration_ns"`
}

// NewValidationDAG projects a Layer 4 DAG run result into a telemetry event.
// Per-node results are reduced to pass/fail/skip counts so the payload stays
// serializable and bounded.
func NewValidationDAG(res *layer4.Result, d time.Duration) Event {
	payload := &ValidationDAGPayload{Duration: d}
	if res != nil {
		payload.OK = res.OK
		payload.NodesTotal = len(res.Nodes)
		payload.ShortCircuited = len(res.Cancelled) > 0
		payload.Err = errString(res.Err)
		stages := make(map[string]bool, len(res.Nodes))
		for _, nr := range res.Nodes {
			stage := string(nr.Stage)
			if stage != "" {
				stages[stage] = true
			}
			switch nr.Status {
			case layer4.StatusPassed:
				payload.NodesPassed++
			case layer4.StatusFailed:
				payload.NodesFailed++
				payload.FailedStages = append(payload.FailedStages, stage)
			case layer4.StatusSkipped:
				payload.NodesSkipped++
			}
		}
		payload.Stages = make([]string, 0, len(stages))
		for s := range stages {
			payload.Stages = append(payload.Stages, s)
		}
		sort.Strings(payload.Stages)
		sort.Strings(payload.FailedStages)
	}
	return newEvent(EventValidationDAG, payload)
}

// ── Adaptive Control Loop ──────────────────────────────────────────────────

// ControlIterationPayload carries the Dynamic IR facts of one reconciliation
// iteration. It is fact-only: raw node states and attempt counts, never a
// decision.
type ControlIterationPayload struct {
	RunID      string            `json:"run_id"`
	NodeStates map[string]string `json:"node_states"`
	Attempts   map[string]int    `json:"attempts"`
}

// NewControlIteration projects an ExecutionSnapshot into a fact-only iteration
// event.
func NewControlIteration(runID string, states map[string]ir.NodeState, attempts map[string]int) Event {
	return newEvent(EventControlIteration, &ControlIterationPayload{
		RunID:      runID,
		NodeStates: nodeStateStrings(states),
		Attempts:   attempts,
	})
}

// ControlNodeObservedPayload carries one node observation fact: the outcome,
// the environment and file-mutation counts, and a truncated output slice.
type ControlNodeObservedPayload struct {
	RunID      string    `json:"run_id"`
	NodeID     string    `json:"node_id"`
	OK         bool      `json:"ok"`
	Err        string    `json:"error,omitempty"`
	SkipReason string    `json:"skip_reason,omitempty"`
	Files      int       `json:"files"`
	EnvSignals int       `json:"env_signals"`
	Output     string    `json:"output,omitempty"`
	Timestamp  time.Time `json:"timestamp"`
}

// NewControlNodeObserved projects an ObservationPayload into a fact-only event.
func NewControlNodeObserved(runID string, obs ir.ObservationPayload) Event {
	return newEvent(EventControlNodeObserved, &ControlNodeObservedPayload{
		RunID:      runID,
		NodeID:     obs.NodeID,
		OK:         obs.OK,
		Err:        obs.Err,
		SkipReason: obs.SkipReason,
		Files:      len(obs.FileMutations),
		EnvSignals: len(obs.EnvSignals),
		Output:     truncate(obs.Output, 256),
		Timestamp:  obs.Timestamp,
	})
}

// ControlTerminatedPayload carries the terminal facts of a control loop run:
// the terminal directive, the final node states and the attempt total.
type ControlTerminatedPayload struct {
	RunID      string            `json:"run_id"`
	Directive  string            `json:"directive"`
	NodeStates map[string]string `json:"node_states"`
	Attempts   int               `json:"attempts"`
	Duration   time.Duration     `json:"duration_ns"`
}

// NewControlTerminated projects a terminal control loop state into a fact-only
// event.
func NewControlTerminated(runID, directive string, states map[string]ir.NodeState, attempts int, d time.Duration) Event {
	return newEvent(EventControlTerminated, &ControlTerminatedPayload{
		RunID:      runID,
		Directive:  directive,
		NodeStates: nodeStateStrings(states),
		Attempts:   attempts,
		Duration:   d,
	})
}

// nodeStateStrings projects a NodeState map into stable string labels.
func nodeStateStrings(states map[string]ir.NodeState) map[string]string {
	if len(states) == 0 {
		return nil
	}
	out := make(map[string]string, len(states))
	for k, v := range states {
		out[k] = string(v)
	}
	return out
}

// truncate bounds a fact string so event payloads stay lean.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
