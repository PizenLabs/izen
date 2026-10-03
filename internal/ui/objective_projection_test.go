package ui

// ── Objective lifecycle projection ──────────────────────────────────────────
//
// §16: the trace must make the real execution state legible, and it must be a
// PROJECTION of runtime events rather than a checklist the UI assembles.
//
// The runtime emits the objective lifecycle as machine activity lines (see
// internal/runtime/autonomy/objective_lifecycle.go). These tests pin that:
//
//  1. those lines reach the trace surface verbatim and are kept OUT of the
//     narrative — the objective contract is telemetry, not a user-facing claim;
//  2. no completion glyph is ever attached to an objective line the runtime did
//     not emit, so the UI cannot render "done" for an objective the Control Plane
//     refused;
//  3. a machine line that reports an objective state stays machine content in
//     every stage, including when it says PROVEN — the UI does not re-interpret
//     it as a user-facing success.

import (
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/execution"
)

// newObjectiveProjectionModel returns a model wired with the single ingestion
// reducer the Phase 15 tests established.
func newObjectiveProjectionModel(t *testing.T) *model {
	t.Helper()
	m := readyChatModel(newTestModel())
	m.telemetryDemuxer = NewTelemetryDemuxer()
	m.narrative = nil
	return m
}

// traceText returns the Trace buffer's own view of what it ingested. This is the
// AUTHORITATIVE projection input: the rendered overlay is a width-bounded view of
// it, and asserting on the view would assert on the terminal width.
func traceText(m *model) string {
	steps := m.telemetryDemuxer.Steps()
	lines := make([]string, 0, len(steps))
	for _, s := range steps {
		lines = append(lines, s.Message)
	}
	return strings.Join(lines, "\n")
}

// traceOverlayText renders the Trace overlay, which is where the objective
// lifecycle is legible to a human.
func traceOverlayText(m *model) string {
	return normalizeForAssertion(stripANSITest(m.telemetryDemuxer.RenderOverlay(120, 60)))
}

func TestObjectiveProjection_LifecycleLinesReachTheTraceVerbatim(t *testing.T) {
	m := newObjectiveProjectionModel(t)

	lines := []string{
		`[objective] id=run-1 kind=PATCH progress=UNDERSTOOD scope=[index.html,styles.css] clauses=2 conditions=6`,
		`[objective] requirements admitted=2 rejected=1 r1=ADMITTED r2=ADMITTED r3=REJECTED`,
		`[intent] user_intent=modification command_mode=(not-carried) interaction_contract=agentic_loop execution_intent=PATCH objective=run-1 scope=$prompt`,
		`[objective] progress=PARTIALLY_SATISFIED continuation=CONTINUE satisfied=[cond-observed] unresolved=[cond-scope-mutated,cond-r2]`,
		`[objective] unmet completion conditions (3/6): cond-scope-mutated, cond-post-mutation-reinspected, cond-r2`,
	}
	for _, line := range lines {
		m.logActivity("%s", line)
	}

	// Every objective/intent line is trace content and NOTHING is narrative
	// content: an objective contract line is machine state, and letting it into
	// the narrative is how a runtime fact turns into a user-facing claim the UI
	// then owns.
	if len(m.records) != 0 {
		t.Fatalf("objective lifecycle lines leaked into the narrative: %+v", m.records)
	}
	if got := m.telemetryDemuxer.StepCount(); got != len(lines) {
		t.Fatalf("trace steps = %d, want %d", got, len(lines))
	}

	// The rendered trace must contain the facts verbatim. A projection that
	// reformatted or dropped the obligation ids would make the trace useless for
	// the only question it exists to answer: what is still unmet?
	rendered := traceText(m)
	for _, want := range []string{
		"cond-scope-mutated",
		"cond-post-mutation-reinspected",
		"PARTIALLY_SATISFIED",
		"user_intent=modification",
		"execution_intent=PATCH",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("the projection dropped %q from the trace:\n%s", want, rendered)
		}
	}
}

func TestObjectiveProjection_NeverRendersACompletionGlyph(t *testing.T) {
	m := newObjectiveProjectionModel(t)

	// A refusal. The runtime says PROVE-NOT; the UI must not draw a checkmark.
	m.logActivity(`[objective] progress=PARTIALLY_SATISFIED unresolved=[cond-scope-mutated]`)
	overlay := traceOverlayText(m)
	buffer := traceText(m)

	// No completion glyph anywhere, in the buffer or in the rendering.
	for _, rendered := range []string{overlay, buffer} {
		if strings.Contains(rendered, string(executionCheckGlyph)) {
			t.Fatalf("an incomplete objective was rendered with a completion glyph:\n%s", rendered)
		}
	}
	// The words themselves must survive: the trace has to say what is missing.
	if !strings.Contains(buffer, "cond-scope-mutated") {
		t.Fatalf("the refusal lost the obligation it names:\n%s", buffer)
	}
}

func TestObjectiveProjection_ProvenIsStillMachineContent(t *testing.T) {
	m := newObjectiveProjectionModel(t)

	// A PROVEN verdict is a real runtime event, and it renders as one. It does not
	// become a narrative success line, because the Control Plane owns that claim
	// and the UI only projects it.
	m.logActivity(`[objective] progress=PROVEN continuation=COMPLETE`)
	rendered := traceText(m)

	if !strings.Contains(rendered, string(execution.ProgressProven)) {
		t.Fatalf("a PROVEN objective was not projected verbatim:\n%s", rendered)
	}
	if len(m.records) != 0 {
		t.Fatalf("a PROVEN objective line leaked into the narrative: %+v", m.records)
	}
}

func TestObjectiveProjection_NoStaticChecklistIsEverAssembled(t *testing.T) {
	m := newObjectiveProjectionModel(t)

	// Feed NOTHING. The projection must render no execution step at all — an
	// empty runtime emits no steps, and a UI that manufactures any is lying.
	m.logActivity("%s", "[objective] id=run-1 kind=PATCH progress=DISCOVERED")
	rendered := traceOverlayText(m)

	for _, glyph := range []string{string(executionCheckGlyph), string(executionActiveGlyph)} {
		if strings.Contains(rendered, glyph) {
			t.Fatalf("the projection manufactured an execution step glyph %q from no runtime events:\n%s", glyph, rendered)
		}
	}
	for _, phantom := range []string{
		"inspect target", "analyze structure", "propose change",
		"apply mutation", "verify + diagnose loop",
	} {
		for _, glyph := range []string{string(executionCheckGlyph), string(executionActiveGlyph)} {
			if strings.Contains(rendered, glyph+" "+phantom) {
				t.Fatalf("the projection rendered %q as an executed step", phantom)
			}
		}
	}
}
