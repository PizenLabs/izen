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
	// EventPlanFallback is emitted when the plan engine exhausts JSON synthesis
	// retries and falls back to heuristic file-path extraction because the model
	// produced narrative prose instead of structured JSON (a common failure
	// mode of free/mini cloud models).
	EventPlanFallback = "plan.synthesize.fallback"
	// EventStreamUsage carries the cumulative token usage of an interrupted LLM
	// stream. It is published by the stream reader / consumer when a request is
	// cut short by a context deadline or cancellation so tokens already billed
	// by the provider are never silently zeroed in local telemetry.
	EventStreamUsage = "stream.usage"
	// ── CANONICAL RUNTIME EXECUTION LIFECYCLE (RuntimeExecutor) ──────────
	// These events are the single authoritative stream of a full execution
	// through the RuntimeExecutor. They are emitted ONLY at real runtime
	// boundaries — never synthesised by the presentation layer — so the UI can
	// render a truthful execution timeline purely from events.
	EventExecutionStarted      = "execution.started"
	EventStrategySelected      = "execution.strategy.selected"
	EventTargetResolved        = "execution.target.resolved"
	EventContextPrepared       = "execution.context.prepared"
	EventModelInvoked          = "execution.model.invoked"
	EventProviderResponse      = "execution.provider.response"
	EventArtifactProduced      = "execution.artifact.produced"
	EventMutationStarted       = "execution.mutation.started"
	EventMutationCompleted     = "execution.mutation.completed"
	EventVerificationCompleted = "execution.verification.completed"
	// EventExecutionEvidence is the terminal AUTHORITATIVE record of one
	// execution attempt (Phase 2 P2). It is emitted exactly once per
	// execution, by the runtime, when the attempt terminates. Downstream
	// state projectors (UI/queue) MUST derive terminal truth from this event —
	// never from intermediate lifecycle events, which can never convey a
	// committed outcome.
	EventExecutionEvidence = "execution.evidence"
	EventExecutionFinished = "execution.finished"
	// EventApprovalRequired is the canonical runtime approval event emitted when
	// a RuntimeExecutor execution stops at the human-in-the-loop approval gate.
	// It is distinct from EventApprovalRequested (the patch-engine Tier-4
	// fallback event) — it carries the runtime request + target.
	EventApprovalRequired = "approval.required"
	// EventApprovalRejected is the canonical runtime event emitted when the
	// human explicitly rejects the held proposal at the approval gate. It is a
	// real lifecycle transition, distinct from EventExecutionFinished(success=
	// false) for an execution cancelled mid-run.
	EventApprovalRejected = "approval.rejected"

	// ── PROVIDER STREAM LIFECYCLE (RuntimeExecutor, live evidence) ───────
	// These events make a runtime model invocation observable BETWEEN
	// model.invoked (request begins) and provider.response (successful
	// completion). They are emitted ONLY by the runtime from a live streaming
	// invocation and carry provider telemetry — never invented progress:
	//
	//   model.invoked  → provider.waiting  → provider.first_token →
	//   provider.stream_delta* → provider.usage_update* → provider.response
	//
	// (*) stream_delta is pure evidence transport: a dropped delta never loses
	// execution truth because the authoritative content always travels on the
	// ExecutionResult. usage_update carries provider-reported counts only.
	EventProviderWaiting     = "provider.waiting"
	EventProviderFirstToken  = "provider.first_token"
	EventProviderStreamDelta = "provider.stream_delta"
	EventProviderUsageUpdate = "provider.usage_update"
	// EventReasoningTelemetry carries reasoning TELEMETRY ONLY — duration and
	// the provider-reported reasoning token count when available. Raw
	// chain-of-thought text NEVER travels on the bus.
	EventReasoningTelemetry = "reasoning.telemetry"
	// EventAutonomyDecision is emitted when the autonomy controller renders a
	// verdict (direct_response / auto_continue / ask_user / block). Every
	// autonomy decision is observable through this canonical event; the runtime
	// never hides a gate.
	EventAutonomyDecision = "autonomy.decision"
	// EventCapabilityGranted is emitted when a session capability grant is
	// issued. Grants replace per-file approvals: one grant authorizes every
	// operation inside its scope boundary.
	EventCapabilityGranted = "capability.granted"
	// EventLoopTransition is emitted on every autonomous loop step
	// (investigate -> plan -> build -> verify -> diagnose -> ...). Failure
	// transitions are published like any other: the loop produces diagnosis,
	// not termination.
	EventLoopTransition = "loop.transition"
	// EventContextCompiled is emitted when the context intelligence layer
	// compiles a structural understanding of an artifact (HTML structure,
	// orphan content, invalid regions, code symbols, dependencies).
	EventContextCompiled = "context.compiled"
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

// PlanFallbackPayload carries the outcome of a heuristic plan extraction that
// replaced a failed JSON synthesis. Kind describes the fallback strategy
// ("prose" when file paths were mined from narrative text, "root-context" when
// no specific file was detected); Reason carries the human-readable notice.
type PlanFallbackPayload struct {
	Kind   string
	Reason string
}

// StreamUsagePayload carries the cumulative token usage of an LLM stream that
// did not complete naturally. Interrupted is true when the stream was cut
// short by a context deadline/cancellation; Reason carries the error text.
// InputTokens/OutputTokens are the authoritative provider-reported counts when
// a usage chunk arrived before the interruption, otherwise a best-effort
// estimate from the streamed bytes — never silently zero.
type StreamUsagePayload struct {
	Model        string
	InputTokens  int
	OutputTokens int
	Interrupted  bool
	Reason       string
}

// ExecutionStartedPayload opens a runtime execution. RequestID links every
// subsequent lifecycle event of the same execution.
type ExecutionStartedPayload struct {
	RequestID string
	Mode      string
	Prompt    string
}

// StrategySelectedPayload records the deterministic strategy decision the
// runtime selected for the request. Strategy is the canonical name
// (targeted_mutation, multi_file_planning, repository_investigation, ...).
type StrategySelectedPayload struct {
	RequestID      string
	Strategy       string
	ModelRequired  bool
	StrategyReason string
}

// TargetResolvedPayload records one deterministically resolved mutation target.
type TargetResolvedPayload struct {
	RequestID string
	Target    string
	Exists    bool
	Source    string
}

// ContextPreparedPayload records the minimum-sufficient context compiled
// before any model invocation.
type ContextPreparedPayload struct {
	RequestID string
	Channels  []string
	Tokens    int
}

// ModelInvokedPayload records a single provider invocation. TokenInput/Output
// are the authoritative provider-reported usage of the completed call.
type ModelInvokedPayload struct {
	RequestID   string
	Model       string
	TokenInput  int
	TokenOutput int
}

// ProviderResponsePayload records a SUCCESSFUL provider response with the
// authoritative usage the provider reported. It is emitted only after the
// invocation returned without error — an artifact can never exist before this
// event, and a failed invocation never emits it.
type ProviderResponsePayload struct {
	RequestID   string
	Model       string
	TokenInput  int
	TokenOutput int
}

// ArtifactProducedPayload records a parsed artifact (e.g. a patch) produced by
// a model invocation.
type ArtifactProducedPayload struct {
	RequestID string
	Kind      string // "patch", "plan", "explanation", ...
	Target    string
}

// MutationStartedPayload records that the runtime began applying a mutation.
type MutationStartedPayload struct {
	RequestID string
	Targets   []string
}

// MutationCompletedPayload records a mutation outcome. Outcome uses the
// execution.MutationOutcome vocabulary (committed, rolled_back, apply_failed,
// cancelled, ...).
type MutationCompletedPayload struct {
	RequestID string
	Target    string
	Outcome   string
}

// VerificationCompletedPayload records the deterministic verification result of
// a mutation. Passed is the verifier's real verdict; Steps lists the executed
// step names. It is never rendered "verified" without this real result.
type VerificationCompletedPayload struct {
	RequestID string
	Passed    bool
	Steps     []string
}

// ExecutionFinishedPayload is the terminal event of a runtime execution.
// Success is true only when every stage reached a real terminal success.
type ExecutionFinishedPayload struct {
	RequestID string
	Success   bool
	Outcome   string
}

// ExecutionEvidencePayload is the authoritative terminal record of one
// execution attempt (Phase 2 P2). It carries the immutable contract identity
// (ContractID + AttemptID), the causal recovery lineage, the sealed Phase 1
// context digest, the canonical outcome (COMMITTED / FAILED / ABORTED_OCC /
// CANCELLED), the mutation-set summary with its taint flag and the precise
// time window. Outcome uses the execution.ExecutionOutcome vocabulary;
// Tainted evidence must never project as success. All fields are scalars —
// no live pointers cross the bus.
type ExecutionEvidencePayload struct {
	RequestID        string
	ContractID       string
	AttemptID        uint32
	ParentContractID string
	CausalAncestry   []string
	ContextDigest    string
	Outcome          string
	Tainted          bool
	Targets          []string
	FilesMutated     int
	TransactionID    string
	StartedAt        time.Time
	FinishedAt       time.Time
}

// ApprovalRequiredPayload carries a RuntimeExecutor approval-gate request.
type ApprovalRequiredPayload struct {
	RequestID string
	Target    string
	Preview   string
}

// ApprovalRejectedPayload carries a human rejection of a held RuntimeExecutor
// proposal at the approval gate.
type ApprovalRejectedPayload struct {
	RequestID string
	Target    string
	Reason    string
}

// ProviderWaitingPayload records that a provider round-trip is in flight
// (request sent, no byte received yet). It is emitted when the streaming
// invocation begins and is the truthful "waiting" state of the model stage.
type ProviderWaitingPayload struct {
	RequestID string
	Model     string
}

// ProviderFirstTokenPayload records the arrival of the first provider byte of
// an invocation. Latency is the wall-clock time from invocation begin to the
// first byte.
type ProviderFirstTokenPayload struct {
	RequestID string
	Model     string
	Latency   time.Duration
}

// ProviderStreamDeltaPayload carries one content delta of a live provider
// stream. It is evidence transport ONLY — the authoritative content always
// travels on the ExecutionResult, so a dropped delta never loses execution
// truth.
type ProviderStreamDeltaPayload struct {
	RequestID string
	Delta     string
}

// ProviderUsageUpdatePayload carries the cumulative provider-reported usage of
// a live stream. It is emitted ONLY for authoritative counts (Known &&
// !Estimated) — never a character-count estimate.
type ProviderUsageUpdatePayload struct {
	RequestID       string
	Model           string
	InputTokens     int
	OutputTokens    int
	ReasoningTokens int
}

// ReasoningTelemetryPayload carries reasoning TELEMETRY ONLY: the wall-clock
// reasoning duration and the provider-reported reasoning token count when
// available. It never carries reasoning text — raw chain-of-thought is never
// exposed.
type ReasoningTelemetryPayload struct {
	RequestID string
	Model     string
	Duration  time.Duration
	Tokens    int
}

// AutonomyDecisionPayload carries an autonomy controller verdict. Decision is
// one of direct_response / auto_continue / ask_user / block. Intent is the
// canonical intent category, Workspace the selected capability domain, Risk
// the normalized mutation risk, MissingCapabilities the ungranted capability
// vector (empty when none), and Reason the observable justification.
type AutonomyDecisionPayload struct {
	Decision            string
	Intent              string
	Confidence          float64
	Workspace           string
	Risk                string
	MissingCapabilities []string
	Reason              string
}

// CapabilityGrantedPayload carries a session capability grant. GrantID/Scope
// identify the boundary; Capabilities lists the granted permission bits.
type CapabilityGrantedPayload struct {
	GrantID      string
	Scope        string
	Capabilities []string
	ExpiresAt    string
}

// LoopTransitionPayload carries one step of the autonomous execution loop.
// From/To use the canonical loop states (investigate/plan/build/verify/
// diagnose/ask_user/stop); Event names the loop event that caused the move.
type LoopTransitionPayload struct {
	From   string
	To     string
	Event  string
	Reason string
}

// ContextCompiledPayload carries the structural understanding of one artifact.
// Kind is html/code/text; FindingCount is the number of evidence findings the
// compiler produced for it. Language, Strategy and Confidence are the File
// Intelligence fingerprint of the artifact (empty when compiled without
// intelligence).
type ContextCompiledPayload struct {
	Path         string
	Kind         string
	FindingCount int
	Language     string
	Strategy     string
	Confidence   float64
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

// NewPlanFallback publishes that the plan engine generated a heuristic plan
// because the model produced non-JSON narrative prose.
func NewPlanFallback(kind, reason string) DomainEvent {
	return newEvent(EventPlanFallback, PlanFallbackPayload{
		Kind:   kind,
		Reason: reason,
	})
}

// NewStreamUsage publishes the cumulative token usage of an interrupted LLM
// stream (context deadline / cancellation). It is the transport for
// "Explicit Over Implicit" token accounting: tokens billed by the provider are
// surfaced even when the request never completed.
func NewStreamUsage(model string, inputTokens, outputTokens int, interrupted bool, reason string) DomainEvent {
	return newEvent(EventStreamUsage, StreamUsagePayload{
		Model:        model,
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		Interrupted:  interrupted,
		Reason:       reason,
	})
}

// ── Runtime execution lifecycle constructors ────────────────────────────────

// NewExecutionStarted publishes the start of a runtime execution.
func NewExecutionStarted(requestID, mode, prompt string) DomainEvent {
	return newEvent(EventExecutionStarted, ExecutionStartedPayload{RequestID: requestID, Mode: mode, Prompt: prompt})
}

// NewStrategySelected publishes the deterministic strategy decision.
func NewStrategySelected(requestID, strategy string, modelRequired bool, reason string) DomainEvent {
	return newEvent(EventStrategySelected, StrategySelectedPayload{
		RequestID: requestID, Strategy: strategy, ModelRequired: modelRequired, StrategyReason: reason,
	})
}

// NewTargetResolved publishes one resolved mutation target.
func NewTargetResolved(requestID, target string, exists bool, source string) DomainEvent {
	return newEvent(EventTargetResolved, TargetResolvedPayload{RequestID: requestID, Target: target, Exists: exists, Source: source})
}

// NewContextPrepared publishes the compiled context envelope.
func NewContextPrepared(requestID string, channels []string, tokens int) DomainEvent {
	return newEvent(EventContextPrepared, ContextPreparedPayload{RequestID: requestID, Channels: channels, Tokens: tokens})
}

// NewModelInvoked publishes that a provider invocation began with the resolved
// model. It is emitted BEFORE the provider call; authoritative usage travels on
// NewProviderResponse.
func NewModelInvoked(requestID, model string, tokenInput, tokenOutput int) DomainEvent {
	return newEvent(EventModelInvoked, ModelInvokedPayload{RequestID: requestID, Model: model, TokenInput: tokenInput, TokenOutput: tokenOutput})
}

// NewProviderResponse publishes a successful provider response with its
// authoritative usage. It is emitted AFTER the invocation completes and MUST
// precede any artifact.produced event of the same execution.
func NewProviderResponse(requestID, model string, tokenInput, tokenOutput int) DomainEvent {
	return newEvent(EventProviderResponse, ProviderResponsePayload{RequestID: requestID, Model: model, TokenInput: tokenInput, TokenOutput: tokenOutput})
}

// NewArtifactProduced publishes a parsed artifact from a model invocation.
func NewArtifactProduced(requestID, kind, target string) DomainEvent {
	return newEvent(EventArtifactProduced, ArtifactProducedPayload{RequestID: requestID, Kind: kind, Target: target})
}

// NewMutationStarted publishes the start of a mutation application.
func NewMutationStarted(requestID string, targets []string) DomainEvent {
	return newEvent(EventMutationStarted, MutationStartedPayload{RequestID: requestID, Targets: targets})
}

// NewMutationCompleted publishes a mutation outcome for one target.
func NewMutationCompleted(requestID, target, outcome string) DomainEvent {
	return newEvent(EventMutationCompleted, MutationCompletedPayload{RequestID: requestID, Target: target, Outcome: outcome})
}

// NewVerificationCompleted publishes the verifier's real result.
func NewVerificationCompleted(requestID string, passed bool, steps []string) DomainEvent {
	return newEvent(EventVerificationCompleted, VerificationCompletedPayload{RequestID: requestID, Passed: passed, Steps: steps})
}

// NewExecutionFinished publishes the terminal outcome of a runtime execution.
func NewExecutionFinished(requestID string, success bool, outcome string) DomainEvent {
	return newEvent(EventExecutionFinished, ExecutionFinishedPayload{RequestID: requestID, Success: success, Outcome: outcome})
}

// NewExecutionEvidence publishes the authoritative terminal record of one
// execution attempt (Phase 2 P2). It is emitted exactly once per execution by
// the runtime at termination.
func NewExecutionEvidence(p ExecutionEvidencePayload) DomainEvent {
	return newEvent(EventExecutionEvidence, p)
}

// NewApprovalRequired publishes a RuntimeExecutor approval-gate request.
func NewApprovalRequired(requestID, target, preview string) DomainEvent {
	return newEvent(EventApprovalRequired, ApprovalRequiredPayload{RequestID: requestID, Target: target, Preview: preview})
}

// NewApprovalRejected publishes that the human rejected the held RuntimeExecutor
// proposal at the approval gate.
func NewApprovalRejected(requestID, target, reason string) DomainEvent {
	return newEvent(EventApprovalRejected, ApprovalRejectedPayload{RequestID: requestID, Target: target, Reason: reason})
}

// NewProviderWaiting publishes that a provider round-trip is in flight.
func NewProviderWaiting(requestID, model string) DomainEvent {
	return newEvent(EventProviderWaiting, ProviderWaitingPayload{RequestID: requestID, Model: model})
}

// NewProviderFirstToken publishes the arrival of the first provider byte.
func NewProviderFirstToken(requestID, model string, latency time.Duration) DomainEvent {
	return newEvent(EventProviderFirstToken, ProviderFirstTokenPayload{RequestID: requestID, Model: model, Latency: latency})
}

// NewProviderStreamDelta publishes one content delta of a live provider stream
// (evidence transport only).
func NewProviderStreamDelta(requestID, delta string) DomainEvent {
	return newEvent(EventProviderStreamDelta, ProviderStreamDeltaPayload{RequestID: requestID, Delta: delta})
}

// NewProviderUsageUpdate publishes the cumulative provider-reported usage of a
// live stream.
func NewProviderUsageUpdate(requestID, model string, inputTokens, outputTokens, reasoningTokens int) DomainEvent {
	return newEvent(EventProviderUsageUpdate, ProviderUsageUpdatePayload{
		RequestID: requestID, Model: model, InputTokens: inputTokens, OutputTokens: outputTokens, ReasoningTokens: reasoningTokens,
	})
}

// NewReasoningTelemetry publishes reasoning TELEMETRY only (duration + token
// count when provided) — never reasoning text.
func NewReasoningTelemetry(requestID, model string, duration time.Duration, tokens int) DomainEvent {
	return newEvent(EventReasoningTelemetry, ReasoningTelemetryPayload{RequestID: requestID, Model: model, Duration: duration, Tokens: tokens})
}

// NewAutonomyDecision publishes an autonomy controller verdict.
func NewAutonomyDecision(decision, intent string, confidence float64, workspace, risk string, missing []string, reason string) DomainEvent {
	return newEvent(EventAutonomyDecision, AutonomyDecisionPayload{
		Decision:            decision,
		Intent:              intent,
		Confidence:          confidence,
		Workspace:           workspace,
		Risk:                risk,
		MissingCapabilities: missing,
		Reason:              reason,
	})
}

// NewCapabilityGranted publishes a session capability grant.
func NewCapabilityGranted(grantID, scope string, capabilities []string, expiresAt string) DomainEvent {
	return newEvent(EventCapabilityGranted, CapabilityGrantedPayload{
		GrantID:      grantID,
		Scope:        scope,
		Capabilities: capabilities,
		ExpiresAt:    expiresAt,
	})
}

// NewLoopTransition publishes one step of the autonomous execution loop.
func NewLoopTransition(from, to, event, reason string) DomainEvent {
	return newEvent(EventLoopTransition, LoopTransitionPayload{
		From:   from,
		To:     to,
		Event:  event,
		Reason: reason,
	})
}

// NewContextCompiled publishes the structural understanding of one artifact.
func NewContextCompiled(path, kind string, findingCount int) DomainEvent {
	return newEvent(EventContextCompiled, ContextCompiledPayload{
		Path:         path,
		Kind:         kind,
		FindingCount: findingCount,
	})
}

// NewContextCompiledIntel publishes the structural understanding of one
// artifact together with its File Intelligence fingerprint (language, analysis
// strategy and aggregate evidence confidence).
func NewContextCompiledIntel(path, kind string, findingCount int, language, strategy string, confidence float64) DomainEvent {
	return newEvent(EventContextCompiled, ContextCompiledPayload{
		Path:         path,
		Kind:         kind,
		FindingCount: findingCount,
		Language:     language,
		Strategy:     strategy,
		Confidence:   confidence,
	})
}
