package runtime

import (
	"fmt"
	"time"

	"github.com/PizenLabs/izen/internal/events"
)

// PresentationEventType discriminates a translated presentation event. These
// strings are deliberately decoupled from the domain discriminators so the UI
// never depends on internal event vocabulary.
type PresentationEventType string

const (
	PresentationCommandReceived    PresentationEventType = "presentation.command.received"
	PresentationIntentParsed       PresentationEventType = "presentation.intent.parsed"
	PresentationIntentClassified   PresentationEventType = "presentation.intent.classified"
	PresentationPlanStaged         PresentationEventType = "presentation.plan.staged"
	PresentationPhaseChanged       PresentationEventType = "presentation.phase.changed"
	PresentationPatchParsed        PresentationEventType = "presentation.patch.parsed"
	PresentationPatchValidated     PresentationEventType = "presentation.patch.validated"
	PresentationPatchRejected      PresentationEventType = "presentation.patch.rejected"
	PresentationPatchApplied       PresentationEventType = "presentation.patch.applied"
	PresentationExecutionFailed    PresentationEventType = "presentation.execution.failed"
	PresentationStageCompleted     PresentationEventType = "presentation.stage.completed"
	PresentationSelfHealingAttempt PresentationEventType = "presentation.selfhealing.attempt"
	PresentationSelfHealingExpired PresentationEventType = "presentation.selfhealing.exhausted"
	PresentationApprovalRequested  PresentationEventType = "presentation.approval.requested"
	PresentationActivity           PresentationEventType = "presentation.activity"
)

// PresentationSeverity is the coarse importance band of an event for the UI.
type PresentationSeverity string

const (
	SeverityInfo    PresentationSeverity = "info"
	SeveritySuccess PresentationSeverity = "success"
	SeverityWarning PresentationSeverity = "warning"
	SeverityError   PresentationSeverity = "error"
)

// PresentationEvent is a decoupled, UI-ready projection of a domain event. It
// carries only display-friendly data; consumers never need to type-assert the
// original domain payload.
type PresentationEvent struct {
	Type      PresentationEventType
	Severity  PresentationSeverity
	Summary   string
	Detail    string
	Target    string
	Timestamp time.Time
}

// EventTranslator maps internal DomainEvent instances into decoupled
// PresentationEvent payloads (RFC v1.0 section 3). The domain layer never
// emits UI-specific events; translation happens here, in the Application
// layer, so the UI can stay a pure projection.
type EventTranslator struct{}

// NewEventTranslator returns a stateless translator.
func NewEventTranslator() *EventTranslator {
	return &EventTranslator{}
}

// Translate converts one domain event into a presentation event. The bool
// reports whether the event was recognized; unknown event types yield a zero
// PresentationEvent and false so callers can skip them.
func (t *EventTranslator) Translate(ev events.DomainEvent) (PresentationEvent, bool) {
	if t == nil || ev == nil {
		return PresentationEvent{}, false
	}
	out := PresentationEvent{Timestamp: ev.Timestamp()}

	switch p := ev.Payload().(type) {
	case events.CommandReceivedPayload:
		out.Type = PresentationCommandReceived
		out.Severity = SeverityInfo
		out.Summary = fmt.Sprintf("command received: %s", p.Command)
		out.Detail = p.Mode

	case events.IntentParsedPayload:
		out.Type = PresentationIntentParsed
		out.Severity = SeverityInfo
		out.Summary = fmt.Sprintf("intent parsed: %s (%.0f%% confident)", p.Intent, p.Confidence*100)

	case events.IntentClassifiedPayload:
		out.Type = PresentationIntentClassified
		out.Severity = SeverityInfo
		out.Summary = fmt.Sprintf("intent classified: %s (%.0f%% confident)", p.Intent, p.Confidence*100)
		out.Detail = p.Language
		if p.Explanation != "" {
			out.Detail = p.Explanation
		}

	case events.PlanStagedPayload:
		out.Type = PresentationPlanStaged
		out.Severity = SeverityInfo
		out.Summary = fmt.Sprintf("plan staged: %d task(s) in %s", p.TaskCount, p.Stage)

	case events.PhaseChangedPayload:
		out.Type = PresentationPhaseChanged
		out.Severity = SeverityInfo
		out.Summary = fmt.Sprintf("phase: %s -> %s", p.From, p.To)

	case events.PatchParsedPayload:
		out.Type = PresentationPatchParsed
		out.Severity = SeverityInfo
		out.Summary = fmt.Sprintf("patch parsed: %s (%s)", p.File, p.Strategy)
		out.Target = p.File

	case events.PatchValidatedPayload:
		out.Type = PresentationPatchValidated
		out.Severity = SeverityInfo
		out.Summary = fmt.Sprintf("patch validated: %s (%s)", p.File, p.Strategy)
		out.Target = p.File

	case events.PatchRejectedPayload:
		out.Type = PresentationPatchRejected
		out.Severity = SeverityWarning
		out.Summary = fmt.Sprintf("patch rejected: %s", p.File)
		out.Detail = p.Reason
		out.Target = p.File

	case events.PatchAppliedPayload:
		out.Type = PresentationPatchApplied
		out.Severity = SeveritySuccess
		out.Summary = fmt.Sprintf("patch applied: %s (+%d -%d)", p.File, p.LinesAdd, p.LinesDel)
		out.Target = p.File

	case events.ExecutionFailedPayload:
		out.Type = PresentationExecutionFailed
		out.Severity = SeverityError
		out.Summary = fmt.Sprintf("execution failed in %s: %s", p.Stage, p.Error)
		out.Detail = string(p.Classification)

	case events.StageCompletedPayload:
		out.Type = PresentationStageCompleted
		out.Severity = SeveritySuccess
		out.Summary = fmt.Sprintf("stage completed: %s (%s)", p.Stage, p.Duration.Round(time.Millisecond))
		if p.Summary != "" {
			out.Detail = p.Summary
		}

	case events.SelfHealingAttemptPayload:
		out.Type = PresentationSelfHealingAttempt
		out.Severity = SeverityWarning
		out.Summary = fmt.Sprintf("self-healing attempt %d on %s", p.Retry, p.File)
		out.Target = p.File

	case events.SelfHealingExhaustedPayload:
		out.Type = PresentationSelfHealingExpired
		out.Severity = SeverityError
		out.Summary = fmt.Sprintf("self-healing exhausted after %d attempt(s)", p.Attempts)

	case events.ApprovalRequestedPayload:
		out.Type = PresentationApprovalRequested
		out.Severity = SeverityWarning
		out.Summary = "approval requested"
		out.Detail = p.Reason
		out.Target = p.Target

	case events.ActivityPayload:
		out.Type = PresentationActivity
		out.Severity = SeverityInfo
		out.Summary = p.Line

	default:
		return PresentationEvent{}, false
	}

	return out, true
}
