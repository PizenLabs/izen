// Package events defines the strongly-typed domain events and the in-memory
// pub/sub event bus that decouples engine execution from its consumers.
//
// Engines are headless: they publish DomainEvents and never call UI update
// routines, log sinks, or terminal printers directly. The UI, logging, and
// state tracking layers subscribe to the bus and act purely as projections of
// the event stream.
package events

import "time"

// DomainEvent is the contract every domain event satisfies. The payload is a
// strongly-typed struct defined alongside the event constructor so consumers
// can type-assert without string parsing.
type DomainEvent interface {
	// Type returns the canonical event type discriminator.
	Type() string
	// Timestamp returns the wall-clock time the event was created.
	Timestamp() time.Time
	// Payload returns the strongly-typed event payload.
	Payload() interface{}
}

// Standard event type discriminators published by the mode engines.
const (
	EventCommandReceived      = "command.received"
	EventIntentParsed         = "intent.parsed"
	EventPlanStaged           = "plan.staged"
	EventPatchAttempted       = "patch.attempted"
	EventPatchApplied         = "patch.applied"
	EventExecutionFailed      = "execution.failed"
	EventStageCompleted       = "stage.completed"
	EventSelfHealingAttempt   = "execution.selfhealing.attempt"
	EventSelfHealingExhausted = "execution.selfhealing.exhausted"
	// EventReasoningStream carries LLM reasoning/thinking content streamed from
	// the LLM client as it arrives. Chunks are delivered incrementally with
	// IsComplete=false; a final event with IsComplete=true (and an empty Chunk)
	// marks the end of the reasoning block so projections can collapse it.
	EventReasoningStream = "reasoning.stream"
	// EventActivity is a free-form engine telemetry line published by the
	// retrieval/execution packages' activity sinks. Routing it through the bus
	// keeps the UI a pure projection: engines never call UI routines directly.
	EventActivity = "engine.activity"
	// EventEngineTelemetry is a typed engine I/O event (file read, search,
	// resolve, mutate metrics) wrapped for bus transport. The UI projects it
	// into its structured activity tree.
	EventEngineTelemetry = "engine.telemetry"
	// EventIntentClassified is emitted when the Hybrid Intent Gateway completes
	// a classification (either via the deterministic fast path or the semantic
	// classifier) and maps a raw prompt to a canonical execution phase.
	EventIntentClassified = "intent.classified"
	// EventPhaseChanged is emitted when the Workflow State Machine transitions
	// between execution phases (Ask/Investigate/Plan/Build/Review).
	EventPhaseChanged = "phase.changed"
	// EventPatchParsed is emitted when raw LLM output is successfully parsed
	// into structured patch representations by the patch pipeline.
	EventPatchParsed = "patch.parsed"
	// EventPatchValidated is emitted when a patch passes structural and safety
	// checks in the patch pipeline.
	EventPatchValidated = "patch.validated"
	// EventPatchRejected is emitted when safety validation fails or a patch
	// cannot be applied in the patch pipeline.
	EventPatchRejected = "patch.rejected"
	// EventApprovalRequested is emitted when the patch pipeline enters the
	// Tier 4 Human-in-the-Loop fallback or when intent disambiguation is
	// required at the gateway.
	EventApprovalRequested = "approval.requested"
)

// FailureClassification is the taxonomy used by EventExecutionFailed. It is
// required on every failure event so projections can route recovery policy.
type FailureClassification string

const (
	// FailureTransient is a failure that can be retried immediately without
	// any code change (e.g. a flaky test, a transient I/O error).
	FailureTransient FailureClassification = "transient"
	// FailureRecoverable is a failure that can be recovered from with a
	// deterministic corrective action (e.g. a build fix or a retry loop).
	FailureRecoverable FailureClassification = "recoverable"
	// FailurePermanent is a failure that cannot be recovered from within the
	// engine and requires human intervention.
	FailurePermanent FailureClassification = "permanent"
)

// ── Payloads ────────────────────────────────────────────────────────────────

// CommandReceivedPayload carries an incoming user command entering an engine.
type CommandReceivedPayload struct {
	Command string
	Mode    string
}

// IntentParsedPayload carries the result of classifying a raw request.
type IntentParsedPayload struct {
	Intent     string
	Raw        string
	Confidence float64
}

// PlanStagedPayload carries a staged execution plan (task targets).
type PlanStagedPayload struct {
	TaskCount int
	Tasks     []string
	Stage     string
}

// PatchAttemptedPayload carries an attempt to apply a mutation.
type PatchAttemptedPayload struct {
	File     string
	Strategy string
	Attempt  int
}

// PatchAppliedPayload carries a successfully applied mutation and its metrics.
type PatchAppliedPayload struct {
	File     string
	LinesAdd int
	LinesDel int
	Elapsed  time.Duration
}

// ExecutionFailedPayload carries a failure and its mandatory classification.
type ExecutionFailedPayload struct {
	Classification FailureClassification
	Error          string
	Stage          string
}

// StageCompletedPayload carries the completion of a pipeline stage.
type StageCompletedPayload struct {
	Stage    string
	Duration time.Duration
	Summary  string
}

// SelfHealingAttemptPayload carries a single self-healing retry triggered by a
// failed build verification. Retry is the 1-based attempt counter.
type SelfHealingAttemptPayload struct {
	Retry    int
	File     string
	Category string
}

// SelfHealingExhaustedPayload reports that the self-healing loop ran out of
// retries. Attempts is the total number of attempts executed.
type SelfHealingExhaustedPayload struct {
	Attempts int
	Output   string
}

// ActivityPayload carries a single free-form engine telemetry line emitted by
// the retrieval/execution activity sinks (e.g. "[ OK ] search %q: %d results").
// It exists so those sinks can publish to the bus instead of calling the UI.
type ActivityPayload struct {
	Line string
}

// EngineTelemetryPayload is the transport envelope for a typed engine I/O
// event (retrieval.FileReadEvent, retrieval.SearchEvent, etc.). The payload
// stays interface{} here because the concrete types live in their source
// packages; the UI type-asserts them at projection time.
type EngineTelemetryPayload struct {
	Event interface{}
}

// IntentClassifiedPayload carries the outcome of the Hybrid Intent Gateway
// classification: the canonical execution phase, the normalized confidence,
// the detected locale, the justification, and whether UI disambiguation is
// required (confirmation_requirement).
type IntentClassifiedPayload struct {
	Intent               string
	Raw                  string
	Confidence           float64
	Language             string
	Explanation          string
	ConfirmationRequired bool
}

// PhaseChangedPayload carries a workflow state machine transition between
// execution phases. From/To use the canonical phase names (ask, investigate,
// plan, build, review).
type PhaseChangedPayload struct {
	From string
	To   string
}

// PatchParsedPayload carries a successfully parsed structured patch.
type PatchParsedPayload struct {
	File     string
	Strategy string
	Tier     int
}

// PatchValidatedPayload carries a patch that passed structural and safety
// checks. Tiers: 1=Structured Diff, 2=Search/Replace, 3=Whole File Rewrite,
// 4=Human-in-the-Loop Approval.
type PatchValidatedPayload struct {
	File     string
	Strategy string
	Tier     int
}

// PatchRejectedPayload carries a patch that failed safety validation or could
// not be applied. Tier is the tier that rejected it.
type PatchRejectedPayload struct {
	File   string
	Reason string
	Tier   int
}

// ApprovalRequestedPayload carries a Tier 4 Human-in-the-Loop approval request
// or a gateway-level disambiguation request. Reason carries the justification;
// Target is the file under review (empty for intent disambiguation); Preview is
// a rendered preview diff when available.
type ApprovalRequestedPayload struct {
	Target  string
	Reason  string
	Preview string
}

// ReasoningPayload carries one chunk of an LLM reasoning/thinking stream.
// Chunk holds the verbatim reasoning text (never mixed with response content);
// IsComplete is true on the terminal event that closes the reasoning block
// (its Chunk is empty).
type ReasoningPayload struct {
	Chunk      string
	IsComplete bool
}

// ── Generic event implementation ────────────────────────────────────────────

// event is the shared DomainEvent implementation. All events are immutable:
// the payload is set at construction and never mutated.
type event struct {
	typ       string
	timestamp time.Time
	payload   interface{}
}

func (e *event) Type() string         { return e.typ }
func (e *event) Timestamp() time.Time { return e.timestamp }
func (e *event) Payload() interface{} { return e.payload }

func newEvent(typ string, payload interface{}) DomainEvent {
	return &event{
		typ:       typ,
		timestamp: time.Now(),
		payload:   payload,
	}
}

// ── Constructors ────────────────────────────────────────────────────────────

// NewCommandReceived publishes that a command entered an engine pipeline.
func NewCommandReceived(command, mode string) DomainEvent {
	return newEvent(EventCommandReceived, CommandReceivedPayload{
		Command: command,
		Mode:    mode,
	})
}

// NewIntentParsed publishes that a raw request was classified into an intent.
func NewIntentParsed(intent, raw string, confidence float64) DomainEvent {
	return newEvent(EventIntentParsed, IntentParsedPayload{
		Intent:     intent,
		Raw:        raw,
		Confidence: confidence,
	})
}

// NewPlanStaged publishes that a plan was staged into runnable tasks.
func NewPlanStaged(taskCount int, tasks []string, stage string) DomainEvent {
	return newEvent(EventPlanStaged, PlanStagedPayload{
		TaskCount: taskCount,
		Tasks:     tasks,
		Stage:     stage,
	})
}

// NewPatchAttempted publishes that a mutation attempt started.
func NewPatchAttempted(file, strategy string, attempt int) DomainEvent {
	return newEvent(EventPatchAttempted, PatchAttemptedPayload{
		File:     file,
		Strategy: strategy,
		Attempt:  attempt,
	})
}

// NewPatchApplied publishes that a mutation was applied successfully.
func NewPatchApplied(file string, linesAdd, linesDel int, elapsed time.Duration) DomainEvent {
	return newEvent(EventPatchApplied, PatchAppliedPayload{
		File:     file,
		LinesAdd: linesAdd,
		LinesDel: linesDel,
		Elapsed:  elapsed,
	})
}

// NewExecutionFailed publishes a failure with its mandatory classification.
func NewExecutionFailed(classification FailureClassification, err error, stage string) DomainEvent {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	return newEvent(EventExecutionFailed, ExecutionFailedPayload{
		Classification: classification,
		Error:          msg,
		Stage:          stage,
	})
}

// NewStageCompleted publishes that a pipeline stage completed.
func NewStageCompleted(stage string, duration time.Duration, summary string) DomainEvent {
	return newEvent(EventStageCompleted, StageCompletedPayload{
		Stage:    stage,
		Duration: duration,
		Summary:  summary,
	})
}

// NewSelfHealingAttempt publishes that the self-healing loop triggered a retry
// after a failed verification. Retry is the 1-based attempt counter.
func NewSelfHealingAttempt(retry int, file, category string) DomainEvent {
	return newEvent(EventSelfHealingAttempt, SelfHealingAttemptPayload{
		Retry:    retry,
		File:     file,
		Category: category,
	})
}

// NewSelfHealingExhausted publishes that the self-healing loop exhausted all
// retries and the workspace was rolled back to its clean pre-mutation state.
func NewSelfHealingExhausted(attempts int, output string) DomainEvent {
	return newEvent(EventSelfHealingExhausted, SelfHealingExhaustedPayload{
		Attempts: attempts,
		Output:   output,
	})
}

// NewActivity publishes a single free-form engine telemetry line. It is the
// bus transport for the retrieval/execution activity sinks, decoupling the
// engine packages from any direct UI callback.
func NewActivity(line string) DomainEvent {
	return newEvent(EventActivity, ActivityPayload{Line: line})
}

// NewEngineTelemetry publishes a typed engine I/O event wrapped for bus
// transport. The concrete event types (e.g. retrieval.SearchEvent) are
// type-asserted by the projection layer.
func NewEngineTelemetry(ev interface{}) DomainEvent {
	return newEvent(EventEngineTelemetry, EngineTelemetryPayload{Event: ev})
}

// NewReasoningStream publishes one chunk of an LLM reasoning/thinking stream.
// Pass IsComplete=true with an empty chunk on the terminal event that closes
// the reasoning block.
func NewReasoningStream(chunk string, isComplete bool) DomainEvent {
	return newEvent(EventReasoningStream, ReasoningPayload{
		Chunk:      chunk,
		IsComplete: isComplete,
	})
}

// NewIntentClassified publishes that the Hybrid Intent Gateway completed a
// classification and mapped the raw prompt to a canonical execution phase.
func NewIntentClassified(intent, raw string, confidence float64, language, explanation string, confirmRequired bool) DomainEvent {
	return newEvent(EventIntentClassified, IntentClassifiedPayload{
		Intent:               intent,
		Raw:                  raw,
		Confidence:           confidence,
		Language:             language,
		Explanation:          explanation,
		ConfirmationRequired: confirmRequired,
	})
}

// NewPhaseChanged publishes that the Workflow State Machine transitioned
// between execution phases.
func NewPhaseChanged(from, to string) DomainEvent {
	return newEvent(EventPhaseChanged, PhaseChangedPayload{
		From: from,
		To:   to,
	})
}

// NewPatchParsed publishes that raw LLM output was parsed into a structured
// patch representation. Strategy is the tier strategy label (DIFF_PATCH,
// SEARCH_REPLACE, WHOLE_FILE); Tier is the 1-4 tier index.
func NewPatchParsed(file, strategy string, tier int) DomainEvent {
	return newEvent(EventPatchParsed, PatchParsedPayload{
		File:     file,
		Strategy: strategy,
		Tier:     tier,
	})
}

// NewPatchValidated publishes that a patch passed structural and safety checks.
func NewPatchValidated(file, strategy string, tier int) DomainEvent {
	return newEvent(EventPatchValidated, PatchValidatedPayload{
		File:     file,
		Strategy: strategy,
		Tier:     tier,
	})
}

// NewPatchRejected publishes that a patch failed safety validation or could
// not be applied.
func NewPatchRejected(file, reason string, tier int) DomainEvent {
	return newEvent(EventPatchRejected, PatchRejectedPayload{
		File:   file,
		Reason: reason,
		Tier:   tier,
	})
}

// NewApprovalRequested publishes a Tier 4 Human-in-the-Loop approval request
// or a gateway-level disambiguation request.
func NewApprovalRequested(target, reason, preview string) DomainEvent {
	return newEvent(EventApprovalRequested, ApprovalRequestedPayload{
		Target:  target,
		Reason:  reason,
		Preview: preview,
	})
}
