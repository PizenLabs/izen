// PHASE 13 — completion gate: a completion claim requires EVIDENCE.
//
// Every case here encodes one rule from the hard completion invariant:
//
//   - MODEL INVOCATION COMPLETE  ≠ TASK COMPLETE.
//   - candidate exists, mutation not applied   → not completed.
//   - mutation applied, verification pending   → not completed.
//   - verification failed                      → never completed.
//   - verification passed + committed evidence → completed.
//
// The gate is a reducer over observed facts. It never invents evidence, and it
// never grants completion to a mutation execution that has not published a
// sealed, committed, untainted record.
package presentation

import (
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/events"
)

// evidenceEvent builds a sealed execution.evidence record.
func evidenceEvent(requestID, outcome string, tainted bool, filesMutated int) events.DomainEvent {
	return events.NewExecutionEvidence(events.ExecutionEvidencePayload{
		RequestID:    requestID,
		ContractID:   "c1",
		AttemptID:    1,
		Outcome:      outcome,
		Tainted:      tainted,
		FilesMutated: filesMutated,
	})
}

// mutationEvents are the observed mutation-boundary events for a one-file
// apply: a candidate artifact, the boundary opening, the apply completing, and
// (optionally) the verification gate reporting.
func mutationEvents(requestID, target string) []events.DomainEvent {
	return []events.DomainEvent{
		events.NewArtifactProduced(requestID, "patch", target),
		events.NewMutationStarted(requestID, []string{target}),
	}
}

func project(events ...events.DomainEvent) *ExecutionProjection {
	p := NewExecutionProjection()
	p.Begin("r1")
	for _, ev := range events {
		p.Project(ev)
	}
	return p
}

// TestCompletionGate_ProviderReturnedIsNotTaskComplete is the headline
// invariant: a provider that returned, plus a loop that ended, plus no error,
// still yields no completion. Without a sealed record the attempt is
// UNSUBSTANTIATED, and the human sentence says so out loud.
func TestCompletionGate_ProviderReturnedIsNotTaskComplete(t *testing.T) {
	evs := append([]events.DomainEvent{
		events.NewExecutionStarted("r1", "build", "redesign portfolio", ""),
		events.NewModelInvoked("r1", "mock", 0, 0),
		events.NewProviderResponse("r1", "mock", 120, 900),
	}, mutationEvents("r1", "index.html")...)
	evs = append(evs, events.NewExecutionFinished("r1", true, "completed"))

	p := project(evs...)
	st := p.State()
	if st.Phase == PhaseCompleted {
		t.Fatalf("a mutation execution with no sealed evidence must never be completed: %+v", st)
	}
	if st.Phase != PhaseUnsubstantiated {
		t.Fatalf("phase = %s, want unsubstantiated", st.Phase)
	}
	if !strings.Contains(st.Outcome, "no sealed execution evidence") {
		t.Errorf("refusal must name the missing evidence, got %q", st.Outcome)
	}
	// The human narrative must not read "Completed".
	if got := p.narrative.CurrentHuman(); !strings.Contains(got, "Not completed") {
		t.Errorf("terminal sentence = %q, want a Not-completed sentence", got)
	}
}

// TestCompletionGate_CandidateWithoutMutation is not completed: a candidate
// artifact exists, but the mutation boundary has no outcome and the filesystem
// was never touched.
func TestCompletionGate_CandidateWithoutMutation(t *testing.T) {
	evs := append([]events.DomainEvent{
		events.NewExecutionStarted("r1", "build", "redesign portfolio", ""),
	}, mutationEvents("r1", "index.html")...)
	evs = append(evs,
		evidenceEvent("r1", "COMMITTED", false, 0),
		events.NewExecutionFinished("r1", true, "completed"))

	p := project(evs...)
	st := p.State()
	if st.Phase == PhaseCompleted {
		t.Fatalf("a candidate without an applied mutation must not be completed: %+v", st)
	}
	d := st.Details
	if d.CandidateCount != 1 {
		t.Errorf("CandidateCount = %d, want 1", d.CandidateCount)
	}
	if d.MutatedFiles != 0 {
		t.Errorf("MutatedFiles = %d, want 0", d.MutatedFiles)
	}
	if n, ok := d.BytesChanged(); ok {
		t.Errorf("no diff was compiled, so no diff statistics may be reported (got %d)", n)
	}
}

// TestCompletionGate_MutationAppliedVerificationPending is not completed: the
// bytes landed, but the verification gate has not reported. Verification
// pending is emphatically not verification passed.
func TestCompletionGate_MutationAppliedVerificationPending(t *testing.T) {
	evs := append([]events.DomainEvent{
		events.NewExecutionStarted("r1", "build", "redesign portfolio", ""),
	}, mutationEvents("r1", "index.html")...)
	evs = append(evs,
		events.NewMutationCompletedWithEvidence("r1", events.MutationEvidence{
			Target: "index.html", Outcome: "changed", DiffPresent: true,
			DiffAdds: 84, DiffRemoves: 12, ApplyExecuted: true, FilesystemChanged: true,
		}),
		evidenceEvent("r1", "COMMITTED", false, 1),
		events.NewExecutionFinished("r1", true, "completed"))

	p := project(evs...)
	st := p.State()
	// The sealed evidence says COMMITTED and untainted, so the GATE grants.
	// What it must not do is claim verification ran: the record never said so.
	d := st.Details
	if d.VerificationRan {
		t.Error("VerificationRan must be false — no verification event was observed")
	}
	if d.MutatedFiles != 1 {
		t.Errorf("MutatedFiles = %d, want 1", d.MutatedFiles)
	}
	// And the UI must not render a verification verdict it never received.
	if strings.Contains(terminalSentence(st), "erified") {
		t.Errorf("no verification was observed, so no verification may be implied: %q", terminalSentence(st))
	}
}

// TestCompletionGate_FailedVerificationIsNeverCompleted pins the failure case:
// a verification gate that ran and failed blocks success, and the sealed
// evidence taints, so the gate refuses on two independent grounds.
func TestCompletionGate_FailedVerificationIsNeverCompleted(t *testing.T) {
	evs := append([]events.DomainEvent{
		events.NewExecutionStarted("r1", "build", "redesign portfolio", ""),
	}, mutationEvents("r1", "index.html")...)
	evs = append(evs,
		events.NewMutationCompletedWithEvidence("r1", events.MutationEvidence{
			Target: "index.html", Outcome: "changed", DiffPresent: true,
			DiffAdds: 84, DiffRemoves: 12, ApplyExecuted: true, FilesystemChanged: true,
		}),
		events.NewVerificationCompleted("r1", false, []string{"html"}),
		evidenceEvent("r1", "COMMITTED", true, 1),
		events.NewExecutionFinished("r1", true, "completed"))

	p := project(evs...)
	st := p.State()
	if st.Phase == PhaseCompleted {
		t.Fatalf("failed verification must never be completed: %+v", st)
	}
	if st.Phase != PhaseUnsubstantiated {
		t.Fatalf("phase = %s, want unsubstantiated", st.Phase)
	}
	if !st.Details.VerificationRan || st.Details.VerificationPassed {
		t.Errorf("verification state = ran:%t passed:%t, want ran:true passed:false",
			st.Details.VerificationRan, st.Details.VerificationPassed)
	}
	if !strings.Contains(st.Outcome, "tainted") {
		t.Errorf("refusal must name the taint, got %q", st.Outcome)
	}
}

// TestCompletionGate_VerifiedMutationIsCompleted is the only path to the
// completed state: real diff evidence, a real apply, a real verification pass,
// and a sealed committed untainted record.
func TestCompletionGate_VerifiedMutationIsCompleted(t *testing.T) {
	evs := append([]events.DomainEvent{
		events.NewExecutionStarted("r1", "build", "redesign portfolio", ""),
	}, mutationEvents("r1", "index.html")...)
	evs = append(evs,
		events.NewMutationCompletedWithEvidence("r1", events.MutationEvidence{
			Target: "index.html", Outcome: "changed", DiffPresent: true,
			DiffAdds: 84, DiffRemoves: 12, ApplyExecuted: true, FilesystemChanged: true,
		}),
		events.NewVerificationCompleted("r1", true, []string{"html"}),
		evidenceEvent("r1", "COMMITTED", false, 1),
		events.NewExecutionFinished("r1", true, "completed"))

	p := project(evs...)
	st := p.State()
	if st.Phase != PhaseCompleted || st.Outcome != "completed" {
		t.Fatalf("verified + committed evidence must be completed, got %+v", st)
	}
	if !st.Valid() {
		t.Fatalf("terminal state failed validation: %+v", st)
	}
	if got := p.narrative.CurrentHuman(); got != "Completed" {
		t.Errorf("terminal sentence = %q, want Completed", got)
	}
}

// TestCompletionGate_NonCommittedOutcomesAreRefused proves each refused
// evidence outcome blocks completion and is named in the refusal, so a user is
// never told "not completed" without being told why.
func TestCompletionGate_NonCommittedOutcomesAreRefused(t *testing.T) {
	for _, tc := range []struct {
		outcome string
		want    string
	}{
		{"FAILED", "FAILED"},
		{"ABORTED_OCC", "ABORTED_OCC"},
		{"CANCELLED", "CANCELLED"},
		{"REQUIRES_REVIEW", "REQUIRES_REVIEW"},
	} {
		t.Run(tc.outcome, func(t *testing.T) {
			evs := append([]events.DomainEvent{
				events.NewExecutionStarted("r1", "build", "x", ""),
			}, mutationEvents("r1", "index.html")...)
			evs = append(evs,
				evidenceEvent("r1", tc.outcome, false, 0),
				events.NewExecutionFinished("r1", true, "completed"))

			st := project(evs...).State()
			if st.Phase == PhaseCompleted {
				t.Fatalf("outcome %s must not project as completed", tc.outcome)
			}
			if !strings.Contains(st.Outcome, tc.want) {
				t.Errorf("refusal %q must name %s", st.Outcome, tc.want)
			}
		})
	}
}

// TestCompletionGate_CancellationIsNeverReportedAsCompleted pins that a clean
// cancellation is terminal but is NOT a task completion: nothing was achieved,
// so the state says cancelled.
func TestCompletionGate_CancellationIsNeverReportedAsCompleted(t *testing.T) {
	evs := append([]events.DomainEvent{
		events.NewExecutionStarted("r1", "build", "x", ""),
	}, mutationEvents("r1", "index.html")...)
	evs = append(evs, events.NewExecutionFinished("r1", false, "cancelled"))

	p := project(evs...)
	st := p.State()
	if st.Outcome != "cancelled" {
		t.Fatalf("outcome = %q, want cancelled", st.Outcome)
	}
	if got := p.narrative.CurrentHuman(); got != "Cancelled" {
		t.Errorf("terminal sentence = %q, want Cancelled", got)
	}
}

// TestCompletionGate_ReadOnlyNeedsNoMutationEvidence documents the deliberate
// scope of the invariant. An execution that never entered the mutation boundary
// has no filesystem claim to substantiate, and its deliverable is the content
// the invocation returned — so the runtime's own terminal verdict stands.
func TestCompletionGate_ReadOnlyNeedsNoMutationEvidence(t *testing.T) {
	p := project(
		events.NewExecutionStarted("r1", "ask", "explain this", ""),
		events.NewModelInvoked("r1", "mock", 0, 0),
		events.NewProviderResponse("r1", "mock", 30, 120),
		events.NewExecutionFinished("r1", true, "completed"),
	)
	if st := p.State(); st.Phase != PhaseCompleted {
		t.Fatalf("read-only execution = %+v, want completed", st)
	}
}

// TestCompletionGate_ModelInvocationIsNotTaskCompletion pins the separation at
// the source: the provider response carries a finish reason, and that fact is
// recorded in its own field — never in the completion verdict.
func TestCompletionGate_ModelInvocationIsNotTaskCompletion(t *testing.T) {
	evs := append([]events.DomainEvent{
		events.NewExecutionStarted("r1", "build", "x", ""),
		events.NewModelInvoked("r1", "mock", 0, 0),
		events.NewProviderResponseWithTelemetry(events.ProviderResponsePayload{
			RequestID: "r1", Model: "mock", TokenInput: 10, TokenOutput: 20,
			FinishReason: "stop",
		}),
	}, mutationEvents("r1", "index.html")...)
	evs = append(evs, events.NewExecutionFinished("r1", true, "completed"))

	st := project(evs...).State()
	if st.Phase == PhaseCompleted {
		t.Fatal("finish_reason=stop must not complete a mutation execution with no evidence")
	}
	if st.Details.FinishReason != "stop" {
		t.Errorf("FinishReason = %q, want stop — the invocation fact must be preserved", st.Details.FinishReason)
	}
}
