package ui

import (
	"os"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/presentation"
)

// ── THE UI IS A PROJECTION OF RUNTIME TRUTH ────────────────────────────────
//
// Spec §17: "The UI must render runtime state. The UI must not predict runtime
// state." Three defects are pinned here, each of which made the UI assert
// something the runtime had not done.
//
// 1. The TUI did not subscribe to the events its own reducer requires, so the
//    completion gate permanently refused every committed mutation and the
//    bounded-step ledger was permanently empty.
// 2. The stage dock rendered a green check for any mutation.completed and for
//    any verification.completed — including skipped, rejected, rolled-back and
//    FAILED ones.
// 3. The approval keybindings printed "✓ Approved — applying…" BEFORE the apply
//    command had resolved.

// ── 1. Subscription completeness ───────────────────────────────────────────

// Every event the execution projection and the authorization ledger consume
// must be reachable from the real subscription list. Driving handleDomainEvent
// directly (as the existing projection tests do) bypasses the subscription,
// which is exactly how the omission survived: the reducer was proven correct
// against events the production wiring never delivered.
func TestProjectedEventTypesCoverEveryEventTheReducerRequires(t *testing.T) {
	subscribed := make(map[string]bool)
	for _, typ := range projectedEventTypes() {
		subscribed[typ] = true
	}

	required := map[string]string{
		events.EventExecutionEvidence:     "the sealed terminal record; the completion gate refuses a mutation execution without it",
		events.EventStepStarted:           "the bounded-step ledger's active step line",
		events.EventStepCompleted:         "the bounded-step ledger's completed step line",
		events.EventMutationStarted:       "the completion gate's mutation-boundary discriminator",
		events.EventMutationCompleted:     "the stage dock's apply verdict",
		events.EventVerificationCompleted: "the stage dock's validate verdict",
		events.EventExecutionFinished:     "the terminal state reduction",
	}
	for typ, why := range required {
		if !subscribed[typ] {
			t.Errorf("the TUI does not subscribe to %q — %s. A reducer fed a subset of the runtime's events renders a subset of the truth, and the gap is silent.", typ, why)
		}
	}
}

// A subscribed lifecycle event must be one the runtime actually publishes for an
// execution. Stage completion is owned by the workflow state machine, not by an
// execution, and projecting it as execution lifecycle would be predicting state.
func TestProjectedEventTypesContainNoFabricatedLifecycleEvents(t *testing.T) {
	subscribed := make(map[string]bool)
	for _, typ := range projectedEventTypes() {
		subscribed[typ] = true
	}
	if subscribed[events.EventStageCompleted] {
		t.Error("the TUI subscribes to stage.completed, a stage event the runtime never publishes for an execution")
	}
}

// The wiring must actually deliver a committed mutation's evidence to the
// projection. This drives the REAL subscription list over a REAL bus, so it
// fails if the list regresses — not merely if the reducer does.
func TestSubscriptionDeliversSealedEvidenceToTheProjection(t *testing.T) {
	bus := events.NewBus(events.DefaultBufferSize)
	got := make(chan events.DomainEvent, 8)
	for _, typ := range projectedEventTypes() {
		bus.Subscribe(typ, func(ev events.DomainEvent) {
			select {
			case got <- ev:
			default:
			}
		})
	}

	reqID := "req-truth"
	bus.Publish(events.NewExecutionStarted(reqID, "build", "p", ""))
	bus.Publish(events.NewMutationStarted(reqID, []string{"index.html"}))
	bus.Publish(events.NewExecutionEvidence(events.ExecutionEvidencePayload{
		RequestID:    reqID,
		Outcome:      string(execution.OutcomeChanged),
		Tainted:      false,
		FilesMutated: 1,
	}))
	bus.Publish(events.NewExecutionFinished(reqID, true, string(execution.OutcomeChanged)))

	want := []string{
		events.EventExecutionStarted,
		events.EventMutationStarted,
		events.EventExecutionEvidence,
		events.EventExecutionFinished,
	}
	seen := map[string]bool{}
	deadline := time.After(3 * time.Second)
	for len(seen) < len(want) {
		select {
		case ev := <-got:
			seen[ev.Type()] = true
		case <-deadline:
			for _, typ := range want {
				if !seen[typ] {
					t.Errorf("the production subscription did not deliver %q (delivered: %v)", typ, seen)
				}
			}
			return
		}
	}
}

// ── 2. The stage dock must not claim success it does not have ──────────────

func TestMutationStageStateNeverRendersSuccessForANonSuccessOutcome(t *testing.T) {
	cases := []struct {
		outcome execution.MutationOutcome
		want    execStageState
	}{
		// Real, committed outcomes: the boundary ran and the bytes changed.
		{outcome: execution.OutcomeChanged, want: stageDone},
		{outcome: execution.OutcomeCreated, want: stageDone},
		{outcome: execution.OutcomeNoChange, want: stageDone},
		// Everything else is not a success.
		{outcome: execution.OutcomeSkipped, want: stageFailed},
		{outcome: execution.OutcomeRejected, want: stageFailed},
		{outcome: execution.OutcomeCancelled, want: stageCancelled},
		{outcome: execution.OutcomeApplyFailed, want: stageFailed},
		{outcome: execution.OutcomeVerifyFailed, want: stageFailed},
		{outcome: execution.OutcomeOCCAborted, want: stageFailed},
		{outcome: execution.OutcomeTruncated, want: stageFailed},
		{outcome: execution.OutcomeNoArtifact, want: stageFailed},
		{outcome: execution.OutcomeArtifactRejected, want: stageFailed},
		{outcome: execution.OutcomePatchGenerationFailed, want: stageFailed},
		// Unknown is a valid state and is never a success.
		{outcome: execution.MutationOutcome(""), want: stageFailed},
		{outcome: execution.MutationOutcome("a_new_outcome_the_runtime_adds_later"), want: stageFailed},
	}
	for _, tc := range cases {
		t.Run(string(tc.outcome), func(t *testing.T) {
			if got := mutationStageState(string(tc.outcome)); got != tc.want {
				t.Fatalf("mutationStageState(%q) = %q, want %q", tc.outcome, got, tc.want)
			}
		})
	}
}

// The rendered glyph is what the user actually reads, so the state→rendering
// mapping is asserted directly against the real stage renderer.
func TestStageRenderNeverShowsACheckForAFailedOrCancelledStage(t *testing.T) {
	for _, tc := range []struct {
		state    execStageState
		contains string
	}{
		{state: stageDone, contains: "✓"},
		{state: stageFailed, contains: "✖"},
		{state: stageCancelled, contains: "✕"},
	} {
		m := &model{}
		m.setStage("apply", "index.html", tc.state)
		rendered := renderStageStatus(m.stageSnapshot())
		if !strings.Contains(rendered, tc.contains) {
			t.Fatalf("stage %q rendered %q, want it to carry %q", tc.state, rendered, tc.contains)
		}
		if tc.state != stageDone && strings.Contains(rendered, "✓") {
			t.Fatalf("stage %q rendered a success check: %q", tc.state, rendered)
		}
	}
}

// The two defect sites themselves, driven through the real event handler.
func TestDomainEventsDriveTheStageVerdictFromTheActualPayload(t *testing.T) {
	cases := []struct {
		name        string
		ev          events.DomainEvent
		wantContain string
		wantAbsent  string
	}{
		{
			name:        "a skipped mutation must not render a success check",
			ev:          events.NewMutationCompleted("r", "index.html", string(execution.OutcomeSkipped)),
			wantContain: "✖",
			wantAbsent:  "✓",
		},
		{
			name:        "a rolled-back mutation must not render a success check",
			ev:          events.NewMutationCompleted("r", "index.html", string(execution.OutcomeApplyFailed)),
			wantContain: "✖",
			wantAbsent:  "✓",
		},
		{
			name:        "a committed mutation renders the success check",
			ev:          events.NewMutationCompleted("r", "index.html", string(execution.OutcomeChanged)),
			wantContain: "✓",
		},
		{
			name:        "a FAILED verification must not render a success check",
			ev:          events.NewVerificationCompleted("r", false, []string{"build"}),
			wantContain: "✖",
			wantAbsent:  "✓",
		},
		{
			name:        "a passed verification renders the success check",
			ev:          events.NewVerificationCompleted("r", true, []string{"build"}),
			wantContain: "✓",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := &model{}
			m.execView = presentation.NewExecutionProjection()
			m.handleDomainEvent(tc.ev)
			line := renderStageStatus(m.stageSnapshot())
			if !strings.Contains(line, tc.wantContain) {
				t.Fatalf("rendered %q, want it to carry %q", line, tc.wantContain)
			}
			if tc.wantAbsent != "" && strings.Contains(line, tc.wantAbsent) {
				t.Fatalf("rendered %q, want it to NOT carry %q", line, tc.wantAbsent)
			}
		})
	}
}

// ── 3. No optimistic claim before the operation resolves ───────────────────

// Behavioural: the autonomous approval boundary must state the DECISION without
// claiming the RESULT. The driver resume it issues has not returned yet, so any
// success glyph at this point is a claim about a future event.
func TestAutonomousApprovalBoundaryDoesNotRenderSuccessBeforeExecution(t *testing.T) {
	drv := &fakeAutonomousDriver{
		state:     autonomy.RuntimeAwaitingHuman,
		parkOnRun: true,
		boundary: &autonomy.HumanBoundary{
			PatchID:   "p1",
			Reason:    "ready",
			Action:    autonomy.HumanBoundaryApproval,
			Resumable: true,
			Targets:   []string{"note.txt"},
		},
	}
	m := autonomousTestModel(drv)
	runCmd := m.runAutonomousDriver("change bar to qux @note.txt")
	if runCmd == nil {
		t.Fatal("the parked run produced no command")
	}
	// The driver parks at the approval boundary; the UI renders it and holds it.
	m.handleAutonomousRun(extractAutonomousRunMsg(t, runCmd()))
	if !m.autonomousParked() {
		t.Fatal("the run did not park at its approval boundary")
	}

	_, kcmd := m.handleKey(tea.KeyMsg{Alt: true, Type: tea.KeyRunes, Runes: []rune{'a'}})
	if kcmd == nil {
		t.Fatal("Alt+A on a parked approval must return a command")
	}

	out := stripANSITest(renderRecordsForTest(m))
	if strings.Contains(out, Icon.Success) || strings.Contains(out, "\u2713") {
		t.Fatalf("the approval boundary rendered a success glyph before the runtime applied anything:\n%s", out)
	}
	if !strings.Contains(out, "Approved") {
		t.Fatalf("the approval boundary stated nothing about the decision:\n%s", out)
	}
}

// The same rule for the autonomous approval boundary, the staged-DAG gate and
// the shell-command gate: all three are PERMISSIONS, and all three push their
// line synchronously inside the key handler — before the runtime command they
// issue has returned. The assertion is therefore on the handler itself: no
// success glyph may be emitted from it at all.
func TestApprovalKeyHandlersNeverEmitASuccessGlyph(t *testing.T) {
	src, ok := uiSourceFile(t, "keys.go")
	if !ok {
		t.Fatal("keys.go is unreadable")
	}
	for _, forbidden := range []string{
		`Icon.Success+" Approved`,
		`Icon.Success+" Plan authorized`,
		`Icon.Success+" Recovery selected`,
	} {
		if strings.Contains(src, forbidden) {
			t.Errorf("keys.go renders %q from a key handler, before the runtime operation it describes has resolved", forbidden)
		}
	}
}

// renderRecordsForTest flattens every pushed record into plain text so a test can
// assert on what the user actually read.
func renderRecordsForTest(m *model) string {
	var b strings.Builder
	for _, r := range m.records {
		b.WriteString(r.text)
		b.WriteByte('\n')
	}
	return b.String()
}

// uiSourceFile reads one production source file from this package's directory.
func uiSourceFile(t *testing.T, name string) (string, bool) {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		return "", false
	}
	return string(data), true
}
