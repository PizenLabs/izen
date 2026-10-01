// PHASE 13 — main-UI convergence: one owner per state, one narrative per
// execution.
//
// The failures these tests pin are the ones that made the TUI feel like it was
// guessing rather than reporting:
//
//   - The loading dock and the execution narrative panel both rendered the SAME
//     current step, from the SAME projection, in the same frame — and the
//     runtime stage rendered a THIRD wording of the same provider event
//     ("Model ● streaming" beside "Model responding").
//   - The EXECUTING header titled from a different source than the panel, so
//     the chrome and the body could disagree.
//   - Diff statistics rendered where no diff existed.
package ui

import (
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/presentation"
)

// phase13PanelModel returns a model with the execution projection mounted and
// in flight, so the surfaces under test are actually live.
func phase13PanelModel(t *testing.T) *model {
	t.Helper()
	m := readyChatModel(newTestModel())
	m.execView = presentation.NewExecutionProjection()
	m.execView.Begin("g1")
	m.executionResolving = true
	m.execVisibility = presentation.VisibilityNormal
	m.telemetryDemuxer = NewTelemetryDemuxer()
	return m
}

// TestNoDuplicateModelStateInOneFrame is the core convergence test: with a live
// execution step, exactly ONE main-UI surface may state it.
//
// The narrative panel is that surface. The loading dock must render its glyph
// and tip but no status text, and it must not substitute the runtime stage under
// a different wording.
func TestNoDuplicateModelStateInOneFrame(t *testing.T) {
	m := phase13PanelModel(t)
	m.startShimmer("Waiting for model...", "analyze")

	m.handleDomainEvent(events.NewExecutionStarted("g1", "build", "redesign portfolio", ""))
	m.handleDomainEvent(events.NewTargetResolved("g1", "index.html", true, "strategy"))
	m.handleDomainEvent(events.NewProviderFirstToken("g1", "mock", 120))

	step := m.execView.HumanStep()
	if step == "" {
		t.Fatal("the projection must have a live step for this test to be meaningful")
	}

	// The dock renders no status text while the panel owns the step.
	if got := m.composeDockText(); got != "" {
		t.Fatalf("loading dock restated the current step: %q (panel already shows %q)", got, step)
	}
	// The dock still renders — glyph plus tip — so progress is never silent.
	dock := stripANSITest(m.renderLoadingDock())
	if m.loadingTip != "" && !strings.Contains(dock, m.loadingTip) {
		t.Errorf("the dock lost its tip line: %q", dock)
	}
	if !strings.Contains(dock, "Tip:") {
		t.Errorf("the dock must keep a visible tip while the panel owns the step: %q", dock)
	}
	// The narrative panel is the single owner of the step.
	panel := stripANSITest(m.renderExecutionLayered())
	if !strings.Contains(panel, step) {
		t.Fatalf("narrative panel %q does not carry the canonical step %q", panel, step)
	}
	// The stage must not appear beside it under a second wording.
	if strings.Contains(dock, "streaming") {
		t.Errorf("the runtime stage re-stated the provider state beside the panel: %q", dock)
	}
}

// TestDockOwnsTheStepWhenNoPanelIsMounted proves the de-duplication did not
// silence the legacy paths: with no execution panel mounted, the dock falls back
// to the runtime stage and then the shimmer text.
func TestDockOwnsTheStepWhenNoPanelIsMounted(t *testing.T) {
	m := phase13PanelModel(t)
	m.execView = nil // conversational / legacy path

	m.startShimmer("Analyzing project structure", "analyze")
	if got := m.composeDockText(); !strings.Contains(got, "Analyzing project structure") {
		t.Fatalf("with no panel mounted the dock must own the status text, got %q", got)
	}
}

// TestHeaderAgreesWithThePanel proves the EXECUTING header and the narrative
// panel cannot disagree. Both title the same event-derived step, so the chrome
// and the body always say the same thing.
func TestHeaderAgreesWithThePanel(t *testing.T) {
	m := phase13PanelModel(t)
	// A shimmer text that would CONTRADICT the live step if the header used it.
	m.startShimmer("Waiting for model...", "analyze")
	m.state = StateProcessing
	m.handleDomainEvent(events.NewExecutionStarted("g1", "build", "redesign portfolio", ""))
	m.handleDomainEvent(events.NewTargetResolved("g1", "index.html", true, "strategy"))
	m.handleDomainEvent(events.NewProviderFirstToken("g1", "mock", 120))

	step := m.execView.HumanStep()
	header := stripANSITest(m.renderTopBar(120))
	if header == "" {
		t.Fatal("the EXECUTING header must render while executing")
	}
	if !strings.Contains(strings.ToLower(header), strings.ToLower(step)) {
		t.Fatalf("header %q disagrees with the canonical step %q", header, step)
	}
	if strings.Contains(header, "Waiting for model...") {
		t.Fatalf("header used the stale shimmer text instead of the canonical step: %q", header)
	}
}

// TestArtifactLedgerRendersOnlyRealEvidence pins the artifact-centric view and
// its truthfulness rules: targets come from observed events, mutations are
// counted only from boundary proof, and a target with no compiled diff gets NO
// numbers.
func TestArtifactLedgerRendersOnlyRealEvidence(t *testing.T) {
	m := phase13PanelModel(t)
	m.handleDomainEvent(events.NewExecutionStarted("g1", "build", "redesign portfolio", ""))
	m.handleDomainEvent(events.NewArtifactProduced("g1", "patch", "index.html"))
	m.handleDomainEvent(events.NewMutationStarted("g1", []string{"index.html", "styles.css", "script.js"}))

	// Generating: index.html has a candidate, the other two are announced but
	// have none.
	generating := stripANSITest(renderArtifactLedger(m.execView.State().Details))
	for _, want := range []string{"generating", "index.html", "styles.css", "script.js"} {
		if !strings.Contains(generating, want) {
			t.Errorf("ledger missing %q:\n%s", want, generating)
		}
	}
	if strings.Contains(generating, "+") || strings.Contains(generating, "-0") {
		t.Errorf("no diff was compiled, so the ledger must show no diff statistics:\n%s", generating)
	}

	// Mutation: real boundary evidence, verbatim diff metrics.
	m.handleDomainEvent(events.NewMutationCompletedWithEvidence("g1", events.MutationEvidence{
		Target: "index.html", Outcome: "changed", ArtifactPresent: true,
		DiffPresent: true, DiffAdds: 84, DiffRemoves: 12,
		ApplyExecuted: true, FilesystemChanged: true,
	}))
	m.handleDomainEvent(events.NewMutationCompletedWithEvidence("g1", events.MutationEvidence{
		Target: "styles.css", Outcome: "changed", ArtifactPresent: true,
		DiffPresent: true, DiffAdds: 156, DiffRemoves: 31,
		ApplyExecuted: true, FilesystemChanged: true,
	}))
	// script.js: the apply ran but the content did not change → not a mutation,
	// and it carries no diff.
	m.handleDomainEvent(events.NewMutationCompletedWithEvidence("g1", events.MutationEvidence{
		Target: "script.js", Outcome: "nochange",
		ApplyExecuted: true, FilesystemChanged: false,
	}))

	mutated := stripANSITest(renderArtifactLedger(m.execView.State().Details))
	for _, want := range []string{"mutation", "+84", "-12", "+156", "-31", "2 file(s) updated"} {
		if !strings.Contains(mutated, want) {
			t.Errorf("mutation ledger missing %q:\n%s", want, mutated)
		}
	}
	if strings.Contains(mutated, "3 file(s) updated") {
		t.Errorf("an unchanged target must not be counted as mutated:\n%s", mutated)
	}
	if !strings.Contains(mutated, "nochange") {
		t.Errorf("an unchanged target must render its real outcome:\n%s", mutated)
	}
}

// TestArtifactLedgerRendersNothingWithoutTargets proves the ledger is empty
// rather than speculative: an execution that announced no target shows no
// artifact list at all.
func TestArtifactLedgerRendersNothingWithoutTargets(t *testing.T) {
	if got := renderArtifactLedger(presentation.ExecutionDetails{}); got != "" {
		t.Fatalf("ledger must render nothing without observed targets, got %q", got)
	}
}

// TestArtifactLedger_DistinguishesActiveFromPending pins the distinction that
// makes the ledger worth reading: a candidate that EXISTS is active, and a
// target the boundary merely ANNOUNCED is pending.
//
// mutation.started lists the targets the apply will cover. It does not prove a
// candidate was generated for each — treating the announcement as a candidate
// would mark work as underway that never began.
func TestArtifactLedger_DistinguishesActiveFromPending(t *testing.T) {
	m := phase13PanelModel(t)
	m.handleDomainEvent(events.NewExecutionStarted("g1", "build", "redesign portfolio", ""))
	m.handleDomainEvent(events.NewArtifactProduced("g1", "patch", "index.html"))
	m.handleDomainEvent(events.NewMutationStarted("g1", []string{"index.html", "styles.css", "script.js"}))

	ledger := stripANSITest(renderArtifactLedger(m.execView.State().Details))
	rows := map[string]string{}
	for _, line := range strings.Split(ledger, "\n") {
		for _, target := range []string{"index.html", "styles.css", "script.js"} {
			if strings.Contains(line, target) {
				rows[target] = strings.TrimSpace(line)
			}
		}
	}
	active := stripANSITest(mustLedgerRow(t, rows, "index.html"))
	pendingCss := stripANSITest(mustLedgerRow(t, rows, "styles.css"))
	pendingJs := stripANSITest(mustLedgerRow(t, rows, "script.js"))
	if active == pendingCss || active == pendingJs {
		t.Fatalf("an active candidate and a pending target must render differently: %q / %q", active, pendingCss)
	}
	if d := m.execView.State().Details; d.CandidateCount != 1 {
		t.Errorf("CandidateCount = %d, want 1 — only index.html has a candidate", d.CandidateCount)
	}
	// The active row carries the active glyph; the pending rows carry the
	// pending glyph. No diff numbers appear, because no apply has run.
	if !strings.Contains(active, "●") {
		t.Errorf("index.html has a candidate and must render active: %q", active)
	}
	for name, row := range map[string]string{"styles.css": pendingCss, "script.js": pendingJs} {
		if !strings.Contains(row, "○") {
			t.Errorf("%s has no candidate and must render pending: %q", name, row)
		}
	}
	if strings.Contains(ledger, "+") || strings.Contains(ledger, "-0") {
		t.Errorf("no apply ran, so the ledger must show no diff statistics:\n%s", ledger)
	}
}

// mustLedgerRow returns the rendered row for a target, failing if absent.
func mustLedgerRow(t *testing.T, rows map[string]string, target string) string {
	t.Helper()
	row, ok := rows[target]
	if !ok {
		t.Fatalf("ledger has no row for %q: %v", target, rows)
	}
	return row
}

// TestVerificationIsReportedOnlyWhenObserved proves the panel never implies a
// verification verdict it did not receive.
func TestVerificationIsReportedOnlyWhenObserved(t *testing.T) {
	m := phase13PanelModel(t)
	m.execVisibility = presentation.VisibilityExpanded
	m.handleDomainEvent(events.NewExecutionStarted("g1", "build", "redesign portfolio", ""))
	m.handleDomainEvent(events.NewTargetResolved("g1", "index.html", true, "strategy"))
	m.handleDomainEvent(events.NewContextPrepared("g1", []string{"user_intent"}, 40))
	m.handleDomainEvent(events.NewModelInvoked("g1", "mock", 0, 0))
	m.handleDomainEvent(events.NewVerificationCompleted("g1", true, []string{"html", "css"}))

	details := stripANSITest(renderExecutionDetails(m.execView.State().Details))
	if !strings.Contains(details, "verification:") || !strings.Contains(details, "passed") {
		t.Fatalf("a real verification pass must be reported:\n%s", details)
	}
	if !strings.Contains(details, "html, css") {
		t.Fatalf("the executed verification steps must be reported:\n%s", details)
	}

	// Reset to a state with no verification observed.
	m2 := phase13PanelModel(t)
	m2.execVisibility = presentation.VisibilityExpanded
	m2.handleDomainEvent(events.NewExecutionStarted("g1", "build", "redesign portfolio", ""))
	m2.handleDomainEvent(events.NewTargetResolved("g1", "index.html", true, "strategy"))
	m2.handleDomainEvent(events.NewContextPrepared("g1", []string{"user_intent"}, 40))
	m2.handleDomainEvent(events.NewModelInvoked("g1", "mock", 0, 0))

	panel := stripANSITest(renderExecutionDetails(m2.execView.State().Details))
	if strings.Contains(panel, "verification:") {
		t.Fatalf("no verification was observed, so none may be reported:\n%s", panel)
	}

}

// TestSealedEvidenceIsReportedWithItsVerdict proves the evidence record reaches
// the user as the authority it is — outcome and taint both visible.
func TestSealedEvidenceIsReportedWithItsVerdict(t *testing.T) {
	m := phase13PanelModel(t)
	m.execVisibility = presentation.VisibilityExpanded
	m.handleDomainEvent(events.NewExecutionStarted("g1", "build", "redesign portfolio", ""))
	m.handleDomainEvent(events.NewMutationStarted("g1", []string{"index.html"}))
	m.handleDomainEvent(events.NewContextPrepared("g1", []string{"user_intent"}, 40))
	m.handleDomainEvent(events.NewModelInvoked("g1", "mock", 0, 0))
	m.handleDomainEvent(events.NewExecutionEvidence(events.ExecutionEvidencePayload{
		RequestID: "g1", Outcome: "COMMITTED", Tainted: true, FilesMutated: 1,
	}))

	panel := stripANSITest(renderExecutionFrame(m.execView.Frame(presentation.VisibilityExpanded)))
	if !strings.Contains(panel, "sealed evidence:") || !strings.Contains(panel, "tainted") {
		t.Fatalf("a tainted record must be surfaced with its taint flag:\n%s", panel)
	}
}
