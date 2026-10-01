package ui

// PHASE 16.1 — TUI TELEMETRY DOMAIN ISOLATION (I14)
//
// Background telemetry and execution narrative are separate domains. The
// background domain has no ExecutionSpinner field, so a background event cannot
// instantiate or advance one; the execution narrative owns the spinner, and the
// reducer routes an event to exactly one domain.
//
// The test emits alternating EventIndexerProgress and EventModelStreaming and
// asserts, step by step, that only the execution event moves the spinner.

import (
	"testing"

	"github.com/PizenLabs/izen/internal/events"
)

// TestPhase16_1_TUISpinnerOwnership is ACCEPTANCE TEST C for I14.
func TestPhase16_1_TUISpinnerOwnership(t *testing.T) {
	// ── 1. A lone background event must NOT instantiate the spinner ────
	fresh := authorizationTestModel(t)
	fresh.handleDomainEvent(events.NewIndexerProgress("scan", 1, 10))

	if sp := fresh.executionSpinnerIfAny(); sp != nil {
		t.Fatalf("EventIndexerProgress instantiated the ExecutionSpinner: %+v", sp)
	}
	if _, _, _, reports := fresh.backgroundTelemetryState().IndexerProgress(); reports != 1 {
		t.Fatalf("indexer progress must be recorded on BackgroundTelemetryState, reports=%d", reports)
	}

	// ── 2. Alternating background / execution events ───────────────────
	m := authorizationTestModel(t)
	for i := 0; i < 4; i++ {
		prevActive, prevFrame, prevStarts, prevUpdates := false, 0, 0, 0
		if sp := m.executionSpinnerIfAny(); sp != nil {
			prevActive, prevFrame, prevStarts, prevUpdates = sp.Snapshot()
		}

		// A background event: the spinner must not change AT ALL.
		m.handleDomainEvent(events.NewIndexerProgress("scan", i+1, 20))
		if sp := m.executionSpinnerIfAny(); sp != nil {
			active, frame, starts, updates := sp.Snapshot()
			if active != prevActive || frame != prevFrame || starts != prevStarts || updates != prevUpdates {
				t.Fatalf("EventIndexerProgress modified ExecutionSpinner: before=(%t,%d,%d,%d) after=(%t,%d,%d,%d)",
					prevActive, prevFrame, prevStarts, prevUpdates, active, frame, starts, updates)
			}
		}

		// An execution event: it MUST instantiate and advance the spinner.
		m.handleDomainEvent(events.NewModelStreaming("exec-1", "gpt-test", "chunk", true))
		sp := m.executionSpinnerIfAny()
		if sp == nil {
			t.Fatal("EventModelStreaming (execution domain) must instantiate the ExecutionSpinner")
		}
		if _, _, starts, updates := sp.Snapshot(); starts == 0 || updates == 0 {
			t.Fatalf("model streaming must advance the spinner: starts=%d updates=%d", starts, updates)
		}
	}

	// ── 3. Attribution: exactly the execution events reached the spinner ─
	if n := m.executionNarrativeState().StreamEvents(); n != 4 {
		t.Fatalf("execution narrative consumed %d streaming events, want 4", n)
	}
	if n := m.executionNarrativeState().ExecutionEvents(); n != 4 {
		t.Fatalf("execution narrative consumed %d execution events, want 4", n)
	}
	if _, _, _, reports := m.backgroundTelemetryState().IndexerProgress(); reports != 4 {
		t.Fatalf("background telemetry consumed %d indexer reports, want 4", reports)
	}
}

// TestPhase16_1_NonExecutionEventsDoNotTouchSpinner: an event outside both
// domains must not create the spinner either.
func TestPhase16_1_NonExecutionEventsDoNotTouchSpinner(t *testing.T) {
	m := authorizationTestModel(t)
	m.handleDomainEvent(events.NewActivity("background chatter that is not execution"))
	if sp := m.executionSpinnerIfAny(); sp != nil {
		t.Fatal("a non-execution event created the ExecutionSpinner")
	}
}
