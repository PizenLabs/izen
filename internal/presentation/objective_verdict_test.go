package presentation

// ── UI TRUTH: Completed ⇔ authoritative ObjectiveState == PROVEN ───────────
//
// The projection is a pure function of the runtime event stream. The runtime's
// AUTHORITATIVE objective verdict (objective.evaluated) is published AFTER
// execution.finished, so these tests pin that the projection records it and
// re-derives the terminal state. A "successful" execution whose objective was
// never proven must not be rendered as Completed.

import (
	"testing"

	"github.com/PizenLabs/izen/internal/events"
)

func started(t *testing.T) *ExecutionProjection {
	t.Helper()
	p := NewExecutionProjection()
	p.Project(events.NewExecutionStarted("run-1", "build", "objective", "sess-1"))
	return p
}

func TestProjection_TerminalIsProvisionalUntilObjectiveVerdict(t *testing.T) {
	// Before any verdict arrives, the execution-evidence gate stands: a
	// non-mutation execution that terminated cleanly is provisionally completed.
	p := started(t)
	p.Project(events.NewExecutionFinished("run-1", true, "completed"))
	if got := p.State().Phase; got != PhaseCompleted {
		t.Fatalf("phase before verdict = %s, want completed", got)
	}

	// The late authoritative verdict refuses the completion: the objective was
	// never proven.
	p.Project(events.NewObjectiveEvaluated(events.ObjectiveEvaluatedPayload{
		RunID:   "run-1",
		State:   "UNSUBSTANTIATED",
		Granted: false,
		Reason:  "no workspace observation event was produced",
	}))
	if got := p.State().Phase; got != PhaseUnsubstantiated {
		t.Fatalf("phase after UNSUBSTANTIATED verdict = %s, want unsubstantiated", got)
	}
}

func TestProjection_CompletedRequiresProvenVerdict(t *testing.T) {
	p := started(t)
	p.Project(events.NewExecutionFinished("run-1", true, "completed"))
	p.Project(events.NewObjectiveEvaluated(events.ObjectiveEvaluatedPayload{
		RunID:   "run-1",
		State:   "PROVEN",
		Granted: true,
	}))
	if got := p.State().Phase; got != PhaseCompleted {
		t.Fatalf("phase after PROVEN verdict = %s, want completed", got)
	}
}

func TestProjection_VerdictBeforeFinishedAlsoGates(t *testing.T) {
	p := started(t)
	p.Project(events.NewObjectiveEvaluated(events.ObjectiveEvaluatedPayload{
		RunID:   "run-1",
		State:   "FAILED",
		Granted: false,
		Reason:  "verification failed",
	}))
	p.Project(events.NewExecutionFinished("run-1", true, "completed"))
	if got := p.State().Phase; got != PhaseFailed {
		t.Fatalf("phase after FAILED verdict = %s, want failed", got)
	}
}

func TestProjection_IdleAcceptsUntaggedVerdict(t *testing.T) {
	// A verdict without a run id must not be silently dropped by the request-id
	// filter: it is still authoritative.
	p := started(t)
	p.Project(events.NewExecutionFinished("run-1", true, "completed"))
	p.Project(events.NewObjectiveEvaluated(events.ObjectiveEvaluatedPayload{
		State:   "UNSUBSTANTIATED",
		Granted: false,
		Reason:  "not proven",
	}))
	if got := p.State().Phase; got != PhaseUnsubstantiated {
		t.Fatalf("phase after untagged verdict = %s, want unsubstantiated", got)
	}
}
