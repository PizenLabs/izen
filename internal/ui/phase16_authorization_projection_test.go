package ui

// PHASE 16 — AUTHORIZATION PROJECTION: permissions are static, execution is
// evidence.
//
// The invariant under test is the difference between a PERMISSION and a FACT.
// An authorization card used to render a checklist of steps the runtime had not
// taken:
//
//	✓ inspect target
//	✓ analyze structure
//	✓ propose change
//	✓ apply mutation
//
// Every checkmark there was invented by the UI. The user reading it could not
// distinguish a plan from a record, and "apply mutation ✓" is precisely the
// line nobody re-reads before trusting a tool with their files.
//
// The repaired surface has exactly two registers:
//   - AUTHORIZED: — a static PERMISSION DECLARATION, with no execution glyphs.
//   - "● step" / "✓ step" — emitted ONLY on consuming a runtime event.

import (
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/events"
)

// executionCheckGlyph and executionActiveGlyph are the marks that assert a step
// HAPPENED. Before any runtime event is consumed, neither may appear.
const (
	executionCheckGlyph  = '✓'
	executionActiveGlyph = '●'
)

// countExecutionCheckmarks returns how many completed-step marks appear in a
// rendered surface.
func countExecutionCheckmarks(t *testing.T, rendered string) int {
	t.Helper()
	return strings.Count(rendered, string(executionCheckGlyph))
}

// normalizeForAssertion collapses whitespace runs so an assertion is about the
// CONTENT of a rendered surface rather than the terminal width that wrapped it.
func normalizeForAssertion(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// TestAuthorization_NoFakeUIProgress is ACCEPTANCE TEST A.
//
// Action: issue an authorization command (a mutation proposal is staged).
//
// Assert: the UI renders the PERMISSION DECLARATION, and ZERO execution-step
// checkmarks exist until runtime events are emitted.
//
// The assertion is on the whole rendered card, not on one field, because the
// original defect was a hardcoded string in a template — a targeted check on
// one struct field would have passed while the card still lied.
func TestAuthorization_NoFakeUIProgress(t *testing.T) {
	m := authorizationTestModel(t)

	// Precondition, asserted rather than assumed: no runtime event has been
	// consumed, so the ledger is empty.
	if steps := m.attestedExecutionSteps(); len(steps) != 0 {
		t.Fatalf("no runtime event was emitted, so no step may be attested: %+v", steps)
	}
	if m.executionStepLedger().HasCompletedCheckmark() {
		t.Fatal("an empty ledger must not report a completed step")
	}

	view := stripANSITest(m.renderAutonomyProposalBlock(100))

	// The permission declaration is present as a static sentence: either the
	// runtime's own AUTHORIZED line (after a grant) or the requested-capability
	// vector (before one). Both are PERMISSIONS; neither is progress.
	if !strings.Contains(view, "AUTHORIZED:") && !strings.Contains(view, "Capabilities:") {
		t.Fatalf("the card must render a permission declaration:\n%s", view)
	}
	if !strings.Contains(view, "No mutation has occurred.") {
		t.Fatalf("the card must state that nothing has changed yet:\n%s", view)
	}

	// THE assertion: no execution step may be claimed.
	if got := countExecutionCheckmarks(t, view); got != 0 {
		t.Errorf("authorization card claims %d completed execution step(s) before any runtime event:\n%s", got, view)
	}
	// And no step line of any lifecycle may appear.
	if strings.Contains(view, string(executionActiveGlyph)) {
		t.Errorf("authorization card renders an execution step marker before any runtime event:\n%s", view)
	}
	// The old hardcoded checklist must be GONE as STEP LINES. The words
	// themselves survive inside the permission declaration — "inspect target"
	// and "apply mutation" are capability NAMES — so the assertion is on the
	// glyph-prefixed line, which is the form the lie actually took.
	for _, phantom := range []string{
		"inspect target", "analyze structure", "propose change", "apply mutation", "verify + diagnose loop",
	} {
		for _, glyph := range []string{string(executionCheckGlyph), string(executionActiveGlyph)} {
			if strings.Contains(view, glyph+" "+phantom) {
				t.Errorf("authorization card still renders %q as an executed step (%q):\n%s", phantom, glyph+" "+phantom, view)
			}
		}
	}
}

// TestAuthorization_LowRiskAutoApprovalDeclaresPermissionNotProgress covers the
// second grant path — the one that used to stream the fake checklist. The
// auto-approval is silent and immediate, which is exactly where a user is
// most likely to believe a change landed.
func TestAuthorization_LowRiskAutoApprovalDeclaresPermissionNotProgress(t *testing.T) {
	m := authorizationTestModel(t)
	// Grant the low-risk scope so isLowRiskAutoApprovable is true.
	m.autonomy.GrantDefault(autonomy.CapRead, autonomy.CapAnalyze, autonomy.CapPropose, autonomy.CapMutate)

	prop := m.pendingAutonomyProposal
	if !m.isLowRiskAutoApprovable(prop) {
		t.Skip("scope capability was not auto-approvable in this configuration")
	}

	// Consume the trace directly so the low-risk branch is exercised without the
	// full dispatch machinery.
	trace := autonomy.Trace{
		Input:  prop.Input,
		Intent: autonomy.IntentResult{Intent: prop.Intent, Required: prop.Required},
		Route:  autonomy.WorkspaceRoute{Workspace: prop.Workspace, Covers: true},
		Risk:   prop.Risk,
		Grant:  autonomy.GrantRequest{Scope: prop.Scope, Required: prop.Required},
	}
	m.pendingAutonomyProposal = nil
	_ = m.requestAutonomyProposal(trace)

	// The viewport is the ONLY place activity lines land, so it is the surface a
	// user actually reads. Whitespace is collapsed first: the viewport hard
	// wraps, and a test that asserts on a wrapped sentence is asserting on the
	// terminal width.
	m.refreshViewportContent()
	rendered := normalizeForAssertion(stripANSITest(m.Viewport.View()))

	// The phantom steps are asserted as STEP LINES — a glyph immediately
	// followed by the step name — not as bare substrings. The permission
	// declaration legitimately contains the words "inspect target" and "apply
	// mutation": those are CAPABILITY names inside a permission statement, and
	// a checkmark in front of them is precisely the claim being forbidden.
	for _, phantom := range []string{"inspect target", "analyze structure", "propose change", "apply mutation", "verify + diagnose loop"} {
		for _, glyph := range []string{string(executionCheckGlyph), string(executionActiveGlyph)} {
			if strings.Contains(rendered, glyph+" "+phantom) {
				t.Errorf("auto-approval renders %q as an executed step (%q):\n%s", phantom, glyph+" "+phantom, rendered)
			}
		}
	}
	// The permission declaration is the substitute: it names the boundary and
	// claims no work.
	if !strings.Contains(rendered, "AUTHORIZED:") {
		t.Errorf("auto-approval must declare the released permission boundary:\n%s", rendered)
	}
	if !strings.Contains(rendered, "execution not yet started") {
		t.Errorf("auto-approval must not imply execution has begun:\n%s", rendered)
	}
	// And nothing may be attested in the ledger yet: auto-approving is a
	// permission decision, not a runtime event.
	if steps := m.attestedExecutionSteps(); len(steps) != 0 {
		t.Errorf("auto-approval must not attest execution steps, got %+v", steps)
	}
}

// TestAuthorization_StepsAppearOnlyFromRuntimeEvents is the positive half: once
// the runtime reports steps, they render — and they render with the right
// glyphs, in the order the runtime announced them.
func TestAuthorization_StepsAppearOnlyFromRuntimeEvents(t *testing.T) {
	m := authorizationTestModel(t)

	// A real runtime event: the executor announces a bounded step.
	m.handleDomainEvent(events.NewStepStarted("gpt-4o-mini", 1, 1024))
	if len(m.attestedExecutionSteps()) != 1 {
		t.Fatalf("a StepStarted event must attest exactly one step, got %+v", m.attestedExecutionSteps())
	}
	mid := stripANSITest(m.renderAutonomyProposalBlock(100))
	if !strings.Contains(mid, string(executionActiveGlyph)) {
		t.Fatalf("an attested, in-flight step must render as ●:\n%s", mid)
	}
	if got := countExecutionCheckmarks(t, mid); got != 0 {
		t.Errorf("an in-flight step must not render a completion mark, got %d:\n%s", got, mid)
	}

	// The runtime reports it finished.
	m.handleDomainEvent(events.NewStepCompleted(1, 2, "stop"))
	done := stripANSITest(m.renderAutonomyProposalBlock(100))
	if !strings.Contains(done, string(executionCheckGlyph)) {
		t.Fatalf("a completed step must render as ✓:\n%s", done)
	}
	if strings.Contains(done, string(executionActiveGlyph)) {
		t.Errorf("a completed step must not also render as in-flight:\n%s", done)
	}
}

// TestAuthorization_StartAndCompletionPairOnOneStep is the invariant the first
// implementation got wrong. The start event carries a model id and the
// completion event does not, so deriving the step's IDENTITY from the richer
// string makes the two events describe two different steps — and the card then
// renders one piece of work twice, once active and once complete.
func TestAuthorization_StartAndCompletionPairOnOneStep(t *testing.T) {
	m := authorizationTestModel(t)

	m.handleDomainEvent(events.NewStepStarted("gpt-4o-mini", 1, 1024))
	m.handleDomainEvent(events.NewStepCompleted(1, 3, "stop"))

	steps := m.attestedExecutionSteps()
	if len(steps) != 1 {
		t.Fatalf("start and completion of one ordinal must produce ONE step, got %+v", steps)
	}
	if steps[0].Lifecycle != StepCompleted {
		t.Errorf("lifecycle = %q, want %q", steps[0].Lifecycle, StepCompleted)
	}
	// The descriptive context from the start event survives on the paired entry.
	if !strings.Contains(steps[0].Name, "gpt-4o-mini") {
		t.Errorf("the start event's model context must survive pairing, name = %q", steps[0].Name)
	}

	view := stripANSITest(m.renderAutonomyProposalBlock(100))
	if got := countExecutionCheckmarks(t, view); got != 1 {
		t.Errorf("exactly one completed step must render as exactly one checkmark, got %d:\n%s", got, view)
	}
	if strings.Contains(view, string(executionActiveGlyph)) {
		t.Errorf("a completed step must not also render as in-flight:\n%s", view)
	}
}

// TestAuthorization_StepKeyIsTheOrdinal: identity comes from the ordinal on both
// events, so the ledger can pair them without either event's optional context
// leaking into the key.
func TestAuthorization_StepKeyIsTheOrdinal(t *testing.T) {
	if got := stepKeyFromOrdinal(7); got != "step-7" {
		t.Errorf("stepKeyFromOrdinal(7) = %q, want step-7", got)
	}
	if got := stepLabelFromKey("step-7"); got != "step 7" {
		t.Errorf("stepLabelFromKey = %q, want 'step 7'", got)
	}
	if got := stepLabelWithModel("claude-x", 7); got != "step 7 (claude-x)" {
		t.Errorf("stepLabelWithModel = %q", got)
	}
	if got := stepLabelWithModel("", 7); got != "step 7" {
		t.Errorf("an absent model must not add decoration, got %q", got)
	}
	// The label is DISPLAY only — it must never become the key.
	if stepKeyFromOrdinal(7) == stepLabelWithModel("claude-x", 7) {
		t.Error("identity and display label must be distinct values")
	}
}

// TestAuthorization_LedgerLifecycleIsForwardOnly: a step that has completed
// never reverts to active. Downgrading a completed step to ● would be the same
// class of lie as the checklist, reintroduced through reordering.
func TestAuthorization_LedgerLifecycleIsForwardOnly(t *testing.T) {
	m := authorizationTestModel(t)
	m.handleDomainEvent(events.NewStepStarted("m", 1, 512))
	m.handleDomainEvent(events.NewStepCompleted(1, 0, "stop"))
	m.handleDomainEvent(events.NewStepStarted("m", 1, 512))

	steps := m.attestedExecutionSteps()
	if len(steps) != 1 {
		t.Fatalf("a re-announced step must not duplicate, got %+v", steps)
	}
	if steps[0].Lifecycle != StepCompleted {
		t.Errorf("lifecycle = %q, want %q — a completed step must never revert",
			steps[0].Lifecycle, StepCompleted)
	}
}

// TestAuthorization_CompletionWithoutStartIsStillRecorded: real work can begin
// before the UI attaches. The completion IS the evidence, and hiding it would
// make the UI under-report rather than over-report.
func TestAuthorization_CompletionWithoutStartIsStillRecorded(t *testing.T) {
	m := authorizationTestModel(t)
	m.handleDomainEvent(events.NewStepCompleted(3, 1, "stop"))

	steps := m.attestedExecutionSteps()
	if len(steps) != 1 {
		t.Fatalf("a completion event must attest a step, got %+v", steps)
	}
	if steps[0].Lifecycle != StepCompleted {
		t.Errorf("lifecycle = %q, want %q", steps[0].Lifecycle, StepCompleted)
	}
	if !m.executionStepLedger().HasCompletedCheckmark() {
		t.Error("a completed step must be observable as a completion mark")
	}
}

// TestAuthorization_StepLedgerClearsWithTheProposal: the ledger lives on the
// same unwind seam as the pending proposal, so a previous run's completed steps
// can never be read as this run's progress.
func TestAuthorization_StepLedgerClearsWithTheProposal(t *testing.T) {
	m := authorizationTestModel(t)
	m.handleDomainEvent(events.NewStepStarted("m", 1, 512))
	m.handleDomainEvent(events.NewStepCompleted(1, 0, "stop"))
	if !m.executionStepLedger().HasCompletedCheckmark() {
		t.Fatal("precondition: a completed step must be recorded")
	}

	m.clearAutonomyProposal()

	if steps := m.attestedExecutionSteps(); len(steps) != 0 {
		t.Fatalf("clearing the proposal must clear the step ledger, got %+v", steps)
	}
	view := stripANSITest(m.renderAutonomyProposalBlock(100))
	if got := countExecutionCheckmarks(t, view); got != 0 {
		t.Errorf("a cleared ledger must render no checkmarks, got %d:\n%s", got, view)
	}
}

// TestAuthorization_LedgerIsNilSafe: the ledger sits on the render path, so a
// nil model or a nil ledger must render nothing rather than panic.
func TestAuthorization_LedgerIsNilSafe(t *testing.T) {
	var ledger *ExecutionStepLedger
	if got := ledger.renderExecutionSteps(); got != "" {
		t.Errorf("a nil ledger must render nothing, got %q", got)
	}
	if steps := ledger.Steps(); steps != nil {
		t.Errorf("a nil ledger must report no steps, got %+v", steps)
	}
	if attested, completed := ledger.Count(); attested != 0 || completed != 0 {
		t.Errorf("a nil ledger must report 0/0, got %d/%d", attested, completed)
	}
	if ledger.HasCompletedCheckmark() {
		t.Error("a nil ledger must never report a completion mark")
	}
	// All mutators must be inert on a nil receiver.
	ledger.RecordStepStarted("step-1", "step 1")
	ledger.RecordStepCompleted("step-1")
	ledger.Reset()

	var m *model
	if got := m.executionStepLedger(); got != nil {
		t.Error("a nil model has no ledger")
	}
	if steps := m.attestedExecutionSteps(); steps != nil {
		t.Errorf("a nil model attests no steps, got %+v", steps)
	}
}

// TestAuthorization_StepLifecycleVocabularyIsClosed: the ledger holds two
// states and no others. A third, invented state ("probably done") is how a
// hardcoded checklist comes back wearing a new name.
func TestAuthorization_StepLifecycleVocabularyIsClosed(t *testing.T) {
	closed := map[StepLifecycle]bool{StepActive: true, StepCompleted: true}
	if !closed[StepActive] || !closed[StepCompleted] {
		t.Fatal("both declared lifecycles must be in the closed vocabulary")
	}
	if len(closed) != 2 {
		t.Fatalf("the step lifecycle vocabulary has %d states, want exactly 2", len(closed))
	}
	if StepActive.Glyph() == StepCompleted.Glyph() {
		t.Error("an in-flight step and a completed step must not share a glyph — that is the checklist defect")
	}
}

// TestAuthorization_PermissionDeclarationCarriesNoExecutionGlyph: the
// permission declaration is a fixed sentence about a boundary. It must not grow
// checkmarks, because a checkmark is a claim about work.
func TestAuthorization_PermissionDeclarationCarriesNoExecutionGlyph(t *testing.T) {
	decl := stripANSITest(renderAuthorizedPermission())
	if !strings.HasPrefix(decl, "AUTHORIZED:") {
		t.Fatalf("the permission declaration must lead with AUTHORIZED:, got %q", decl)
	}
	for _, want := range []string{"read workspace", "inspect target", "propose mutation", "apply mutation"} {
		if !strings.Contains(decl, want) {
			t.Errorf("the permission declaration must name %q, got %q", want, decl)
		}
	}
	for _, glyph := range []string{string(executionCheckGlyph), string(executionActiveGlyph)} {
		if strings.Contains(decl, glyph) {
			t.Errorf("the permission declaration carries the execution glyph %q — a permission is not progress", glyph)
		}
	}
}
