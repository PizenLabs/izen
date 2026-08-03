package runtime

import (
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/events"
)

func TestTranslateRecognizedEvents(t *testing.T) {
	tr := NewEventTranslator()

	cases := []struct {
		name string
		ev   events.DomainEvent
		typ  PresentationEventType
		sev  PresentationSeverity
	}{
		{"command", events.NewCommandReceived("/plan", "plan"), PresentationCommandReceived, SeverityInfo},
		{"intent", events.NewIntentParsed("build", "fix", 0.9), PresentationIntentParsed, SeverityInfo},
		{"intent-classified", events.NewIntentClassified("build", "fix", 0.9, "en", "explicit", false), PresentationIntentClassified, SeverityInfo},
		{"plan", events.NewPlanStaged(3, []string{"a"}, "build"), PresentationPlanStaged, SeverityInfo},
		{"phase", events.NewPhaseChanged("plan", "build"), PresentationPhaseChanged, SeverityInfo},
		{"patch-parsed", events.NewPatchParsed("a.go", "DIFF_PATCH", 1), PresentationPatchParsed, SeverityInfo},
		{"patch-validated", events.NewPatchValidated("a.go", "DIFF_PATCH", 1), PresentationPatchValidated, SeverityInfo},
		{"patch-rejected", events.NewPatchRejected("a.go", "mismatch", 2), PresentationPatchRejected, SeverityWarning},
		{"patch-applied", events.NewPatchApplied("a.go", 5, 2, time.Millisecond), PresentationPatchApplied, SeveritySuccess},
		{"failed", events.NewExecutionFailed(events.FailureTransient, nil, "build"), PresentationExecutionFailed, SeverityError},
		{"stage", events.NewStageCompleted("build", time.Second, "done"), PresentationStageCompleted, SeveritySuccess},
		{"selfheal", events.NewSelfHealingAttempt(1, "a.go", "flaky"), PresentationSelfHealingAttempt, SeverityWarning},
		{"selfheal-exhausted", events.NewSelfHealingExhausted(3, ""), PresentationSelfHealingExpired, SeverityError},
		{"approval", events.NewApprovalRequested("a.go", "high-risk", ""), PresentationApprovalRequested, SeverityWarning},
		{"activity", events.NewActivity("[ OK ] done"), PresentationActivity, SeverityInfo},
		{"plan-fallback", events.NewPlanFallback("prose", "extracted 2 heuristic tasks"), PresentationPlanFallback, SeverityWarning},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := tr.Translate(tc.ev)
			if !ok {
				t.Fatal("Translate returned ok=false for recognized event")
			}
			if got.Type != tc.typ {
				t.Errorf("Type = %q, want %q", got.Type, tc.typ)
			}
			if got.Severity != tc.sev {
				t.Errorf("Severity = %q, want %q", got.Severity, tc.sev)
			}
			if got.Summary == "" {
				t.Error("Summary is empty")
			}
			if got.Timestamp.IsZero() {
				t.Error("Timestamp is zero")
			}
		})
	}
}

func TestTranslateExecutionFailedSummary(t *testing.T) {
	tr := NewEventTranslator()
	ev := events.NewExecutionFailed(events.FailurePermanent, nil, "build")
	got, ok := tr.Translate(ev)
	if !ok {
		t.Fatal("Translate returned ok=false")
	}
	if got.Detail != string(events.FailurePermanent) {
		t.Errorf("Detail = %q, want classification", got.Detail)
	}
	if got.Summary == "" {
		t.Error("Summary empty for failed event with nil error")
	}
}

func TestTranslateActivityCarriesLine(t *testing.T) {
	tr := NewEventTranslator()
	got, ok := tr.Translate(events.NewActivity("search returned 4 results"))
	if !ok {
		t.Fatal("Translate returned ok=false")
	}
	if got.Summary != "search returned 4 results" {
		t.Errorf("Summary = %q, want activity line", got.Summary)
	}
}

func TestTranslatePlanFallbackCarriesReason(t *testing.T) {
	tr := NewEventTranslator()
	got, ok := tr.Translate(events.NewPlanFallback("prose", "extracted 2 heuristic tasks"))
	if !ok {
		t.Fatal("Translate returned ok=false")
	}
	if got.Type != PresentationPlanFallback {
		t.Errorf("Type = %q, want %q", got.Type, PresentationPlanFallback)
	}
	if got.Detail != "extracted 2 heuristic tasks" {
		t.Errorf("Detail = %q, want the fallback reason", got.Detail)
	}
	if got.Summary == "" {
		t.Error("Summary is empty")
	}
}

func TestTranslateUnknownEvent(t *testing.T) {
	tr := NewEventTranslator()
	if _, ok := tr.Translate(unknownEvent{}); ok {
		t.Fatal("Translate returned ok=true for unknown event")
	}
}

func TestTranslateNil(t *testing.T) {
	tr := NewEventTranslator()
	if _, ok := tr.Translate(nil); ok {
		t.Fatal("Translate(nil) returned ok=true")
	}
}

// unknownEvent is a DomainEvent whose payload is not covered by the
// translator.
type unknownEvent struct{}

func (unknownEvent) Type() string         { return "unknown" }
func (unknownEvent) Timestamp() time.Time { return time.Now() }
func (unknownEvent) Payload() interface{} { return struct{}{} }
