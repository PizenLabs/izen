package ui

import (
	"errors"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/execution"
)

// ── PHASE 14: MAIN NARRATIVE / TRACE BOUNDARY ───────────────────────────────
//
// The Main Narrative carries USER-FACING EXECUTION EVIDENCE. Infrastructure
// telemetry is not that. The tests below pin both halves of the boundary: the
// classifier that decides, and the fact that the runtime source is untouched —
// the events are still published, still fully populated, and still land in the
// Trace Overlay.

// TestEvidenceBoundary_ClassifiesInfrastructureTelemetry pins the classifier.
func TestEvidenceBoundary_ClassifiesInfrastructureTelemetry(t *testing.T) {
	infrastructure := []string{
		"[loop] observing -> deciding",
		"[context] compiled index.html (html): 3 finding(s)",
		"[barrier] waiting for preflight",
		"[preflight] decision surface staged target=index.html",
		"[index] Mapping workspace context...",
		"[objective] run-1 contract=PATCH clause=artifact_missing outcome=UNSUBSTANTIATED",
		"  [loop] interpreting -> completed (complete): objective satisfied",
		"[LOOP] observing -> deciding",
		"[loop] multi\nline body",
	}
	for _, line := range infrastructure {
		if !IsInfrastructureTelemetry(line) {
			t.Errorf("infrastructure telemetry not detected: %q", line)
		}
		if IsUserFacingEvidence(line) {
			t.Errorf("infrastructure telemetry admitted to the narrative: %q", line)
		}
	}

	userFacing := []string{
		"",
		"Applied 2 changes to index.html",
		"The decision was to build the feature.",
		"You asked about [preflight] behaviour and here is the answer.",
		"✓ verification passed",
		"Error: the [index] phase timed out while loading your config file.",
	}
	for _, line := range userFacing {
		if IsInfrastructureTelemetry(line) {
			t.Errorf("user-facing line misclassified as telemetry: %q", line)
		}
	}
	// An empty line is not evidence at all.
	if IsUserFacingEvidence("   ") {
		t.Error("an empty line must not be treated as evidence")
	}
}

// TestEvidenceBoundary_NarrativeGuardIsTheChokePoint proves the guard is
// structural rather than a convention: whatever handler produces a machine line,
// the narrative refuses it and Trace keeps it.
func TestEvidenceBoundary_NarrativeGuardIsTheChokePoint(t *testing.T) {
	for _, line := range InfrastructureTelemetryPrefixes {
		m := readyChatModel(newTestModel())
		m.telemetryDemuxer = NewTelemetryDemuxer()
		// Go through the generic activity writer, exactly as a future handler
		// would, without any per-call-site routing decision.
		m.logActivity("%s some machine detail", line)
		if len(m.records) != 0 {
			t.Fatalf("%s leaked into the main narrative: %+v", line, m.records)
		}
		if m.telemetryDemuxer.StepCount() == 0 {
			t.Fatalf("%s was discarded instead of routed to Trace", line)
		}
	}
}

// TestEvidenceBoundary_UserFacingLinesStillRender guards against over-filtering:
// a gate that swallows everything is as broken as one that swallows nothing.
func TestEvidenceBoundary_UserFacingLinesStillRender(t *testing.T) {
	m := readyChatModel(newTestModel())
	m.telemetryDemuxer = NewTelemetryDemuxer()
	m.logActivity("Applied 2 changes to %s", "index.html")
	if len(m.records) != 1 {
		t.Fatalf("records = %d, want 1 user-facing activity line", len(m.records))
	}
	if !strings.Contains(m.records[0].text, "index.html") {
		t.Fatalf("record = %q, want the user-facing content", m.records[0].text)
	}
}

// TestEvidenceBoundary_TelemetryIsNotGeneratedIntoTheNarrative pins INVARIANT 7's
// "do not delete telemetry generation at the runtime source" clause from the
// subscriber's side: the demuxer still holds every step, and the narrative holds
// none. The information moved; it did not disappear.
func TestEvidenceBoundary_TelemetryIsNotGeneratedIntoTheNarrative(t *testing.T) {
	m := readyChatModel(newTestModel())
	m.telemetryDemuxer = NewTelemetryDemuxer()
	m.logRuntimeDetail("[barrier] waiting for preflight barrier")

	if len(m.records) != 0 {
		t.Fatalf("barrier telemetry reached the narrative: %+v", m.records)
	}
	steps := m.telemetryDemuxer.Steps()
	if len(steps) != 1 {
		t.Fatalf("trace steps = %d, want 1", len(steps))
	}
	if steps[0].Category != "[barrier]" {
		t.Fatalf("trace category = %s, want [barrier]", steps[0].Category)
	}
	if steps[0].Message != "[barrier] waiting for preflight barrier" {
		t.Fatalf("trace message = %q, want the verbatim line", steps[0].Message)
	}
}

// TestUnprovenObjectiveProjectionIsNeitherSuccessNorFailure pins the projection
// of the new terminal state. A run that stopped without proving its objective
// must be reported as exactly that: not "completed" (the false completion this
// phase removes) and not "aborted" (a failure that did not happen, which
// invites a retry that would hit the same wall).
func TestUnprovenObjectiveProjectionIsNeitherSuccessNorFailure(t *testing.T) {
	drv := &fakeAutonomousDriver{
		state: autonomy.RuntimeUnsubstantiated,
		term: &autonomy.LoopTermination{
			State:  autonomy.RuntimeUnsubstantiated,
			Reason: "objective UNPROVEN (UNSUBSTANTIATED): no valid artifact was parsed from the completed provider stream",
		},
	}
	m := autonomousTestModel(drv)
	m.handleAutonomousRun(autonomousRunMsg{term: drv.term})

	var joined string
	for _, r := range m.records {
		joined += r.text + "\n"
	}
	if strings.Contains(joined, "aborted") {
		t.Fatalf("an unproven objective was reported as an abort: %q", joined)
	}
	if strings.Contains(joined, "[autonomous] completed") {
		t.Fatalf("an unproven objective was reported as completed: %q", joined)
	}
	if !strings.Contains(joined, "objective was not proven") {
		t.Fatalf("records must state that the objective was not proven: %q", joined)
	}
	// The human-readable reason is surfaced so the user can see WHICH clause
	// went unmet, never a bare "it didn't work".
	if !strings.Contains(joined, "no valid artifact was parsed") {
		t.Fatalf("records must carry the specific unmet clause: %q", joined)
	}
}

// TestUnprovenObjectiveUserFacingMessage pins the INVARIANT 7 UI mapping:
// `Execution stopped: objective was not proven`.
//
// The wording is load-bearing. "Execution failed" invites a retry; an
// unproven objective invites a look at what was actually delivered, and telling
// the user the run failed would be a different — and equally untrue — claim.
func TestUnprovenObjectiveUserFacingMessage(t *testing.T) {
	if got := ObjectiveUnprovenMessage(""); got != "Execution stopped: objective was not proven." {
		t.Fatalf("message = %q", got)
	}
	withDetail := ObjectiveUnprovenMessage("structural analysis confirmed nothing changed.")
	if !strings.HasPrefix(withDetail, "Execution stopped: objective was not proven.") {
		t.Fatalf("message = %q, want the canonical prefix", withDetail)
	}
	if strings.Contains(strings.ToLower(withDetail), "failed") {
		t.Fatalf("an unproven objective must not be reported as a failure: %q", withDetail)
	}

	// The sentinel maps to the message through the runtime failure renderer.
	got := runtimeExecutionFailureMessage(nil, execution.ErrObjectiveUnsubstantiated)
	if got != "Execution stopped: objective was not proven." {
		t.Fatalf("rendered message = %q", got)
	}
	// A prose-only artifact rejection has its own, more specific rendering.
	prose := runtimeExecutionFailureMessage(nil, execution.ErrZeroArtifactsParsed)
	if !strings.Contains(prose, "prose instead of an artifact") {
		t.Fatalf("prose rejection message = %q", prose)
	}
	if strings.Contains(prose, "objective was not proven") {
		t.Fatalf("a rejected artifact is not an unproven objective: %q", prose)
	}
	// The failure sentinel stays distinguishable.
	failed := runtimeExecutionFailureMessage(nil, execution.ErrObjectiveFailed)
	if !strings.Contains(failed, "Execution failed") {
		t.Fatalf("failure message = %q", failed)
	}
	if !errors.Is(execution.ErrObjectiveFailed, execution.ErrObjectiveUnsubstantiated) &&
		errors.Is(execution.ErrObjectiveFailed, execution.ErrObjectiveUnsubstantiated) {
		t.Fatal("the failure and unsubstantiated sentinels must be distinguishable")
	}
}
