// PHASE 13 — GOLDEN SCENARIO, PRESENTATION HALF.
//
// The runtime half of the golden scenario lives in
// internal/execution/phase13_golden_portfolio_test.go: it drives the real
// RuntimeExecutor through the reported request and proves the workspace, the
// diff, the verification gate and the sealed evidence.
//
// This half replays the SAME semantic event stream through the real execution
// projection — the only path the TUI has to execution truth — and asserts the
// user-facing lifecycle:
//
//	MODIFICATION → AUTHORIZATION REQUIRED → USER AUTHORIZES → CONTEXT PREPARED
//	→ PROJECT UNDERSTOOD → ARTIFACT GENERATION → CANDIDATE READY
//	→ DIFF AVAILABLE → MUTATION → VERIFICATION → COMPLETED
//
// The acceptance properties under test are the ones a user would notice:
//
//	no false completion     the lifecycle only reaches COMPLETED on evidence
//	no fake diff            the ledger prints the boundary's own line metrics
//	no duplicate model state one owner per state
//	no context starvation   the compiled context is reported, not minimised
package presentation

import (
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/events"
)

// goldenPortfolioRequest is the reported request, verbatim.
const goldenPortfolioRequest = "Please review this project and redesign a professional personal portfolio page for me using HTML, CSS, and JS; the author's name is Tom Hunter, an AI Engineer."

// goldenTargets are the three artifacts the redesign touches.
var goldenTargets = []string{"index.html", "styles.css", "script.js"}

// goldenDiff is the measured diff the boundary reported for each target, taken
// from the runtime half of this scenario.
var goldenDiff = map[string][2]int{
	"index.html": {84, 12},
	"styles.css": {156, 31},
	"script.js":  {42, 8},
}

// goldenStream returns the canonical event sequence of the golden run, in the
// order the runtime emits them. The evidence is deliberately placed BEFORE the
// completion event — that ordering is the runtime's contract.
func goldenStream(includeEvidence bool) []events.DomainEvent {
	evs := []events.DomainEvent{
		events.NewExecutionStarted("golden-1", "build", goldenPortfolioRequest, ""),
		events.NewStrategySelected("golden-1", "targeted_mutation", true, "explicit target"),
		events.NewTargetResolved("golden-1", "index.html", true, "strategy"),
		// AUTHORIZATION REQUEST → USER AUTHORIZES. The request itself carries
		// no artifact, so nothing is renderable as a diff at this point.
		events.NewApprovalRequired("golden-1", "index.html", ""),
		// CONTEXT PREPARED — the compile happened; the figure is a layer-labelled
		// estimate of the assembled prompt, not a provider token count.
		events.NewContextPrepared("golden-1", []string{"user_intent", "target_content"}, 812),
		// PROJECT UNDERSTOOD → ARTIFACT GENERATION.
		events.NewModelInvoked("golden-1", "mock", 0, 0),
		events.NewProviderResponseWithTelemetry(events.ProviderResponsePayload{
			RequestID: "golden-1", Model: "mock",
			TokenInput: 1840, TokenOutput: 612, FinishReason: "stop",
		}),
		// CANDIDATE READY: a candidate exists per target, no mutation yet.
		events.NewArtifactProduced("golden-1", "patch", "index.html"),
		events.NewArtifactProduced("golden-1", "patch", "styles.css"),
		events.NewArtifactProduced("golden-1", "patch", "script.js"),
		// DIFF AVAILABLE → MUTATION: the boundary opened and each target's apply
		// completed with the compiled diff it actually produced.
		events.NewMutationStarted("golden-1", goldenTargets),
	}
	for _, target := range goldenTargets {
		d := goldenDiff[target]
		evs = append(evs, events.NewMutationCompletedWithEvidence("golden-1", events.MutationEvidence{
			Target: target, Outcome: "changed", ArtifactPresent: true,
			DiffPresent: true, DiffAdds: d[0], DiffRemoves: d[1],
			ApplyExecuted: true, FilesystemChanged: true,
		}))
	}
	// VERIFICATION.
	evs = append(evs, events.NewVerificationCompleted("golden-1", true, []string{"html", "css", "js"}))
	if includeEvidence {
		evs = append(evs, events.NewExecutionEvidence(events.ExecutionEvidencePayload{
			RequestID: "golden-1", ContractID: "cg-1", AttemptID: 1,
			Outcome: "COMMITTED", Tainted: false,
			Targets: goldenTargets, FilesMutated: 3,
		}))
	}
	return append(evs, events.NewExecutionFinished("golden-1", true, "changed"))
}

func projectGolden(includeEvidence bool) *ExecutionProjection {
	p := NewExecutionProjection()
	p.Begin("golden-1")
	for _, ev := range goldenStream(includeEvidence) {
		p.Project(ev)
	}
	return p
}

// TestGoldenProjection_ReachesCompletedOnEvidence is the positive golden
// assertion: the full lifecycle, ending in COMPLETED, with the artifact ledger
// and the real diff metrics in place.
func TestGoldenProjection_ReachesCompletedOnEvidence(t *testing.T) {
	p := projectGolden(true)
	st := p.State()

	if st.Phase != PhaseCompleted || st.Outcome != "changed" {
		t.Fatalf("final state = %+v, want completed/changed", st)
	}
	if !st.Valid() {
		t.Fatalf("terminal state failed validation: %+v", st)
	}

	// The human lifecycle, in order, from the canonical transitions only. Each
	// mutated file is named individually: a three-file apply reports three
	// changes, not one generic "applied" line.
	got := p.HumanTimeline()
	want := []string{
		"Reading index.html",
		"Waiting for approval",
		"Gathering context",
		"Analyzing",
		"Preparing result",
		"Applying changes",
		"Applied change to index.html",
		"Applied change to styles.css",
		"Applied change to script.js",
		"Verified changes",
		"Completed",
	}
	if len(got) != len(want) {
		t.Fatalf("human timeline = %v, want %v", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("timeline[%d] = %q, want %q (full: %v)", i, got[i], w, got)
		}
	}

	d := st.Details
	if d.CandidateCount != 3 {
		t.Errorf("CandidateCount = %d, want 3", d.CandidateCount)
	}
	if d.MutatedFiles != 3 {
		t.Errorf("MutatedFiles = %d, want 3", d.MutatedFiles)
	}
	if !d.VerificationRan || !d.VerificationPassed {
		t.Errorf("verification = ran:%t passed:%t, want true/true", d.VerificationRan, d.VerificationPassed)
	}
	if !d.EvidenceObserved || d.EvidenceTainted {
		t.Errorf("evidence = observed:%t tainted:%t, want true/false", d.EvidenceObserved, d.EvidenceTainted)
	}
	if d.FilesMutated != 3 {
		t.Errorf("FilesMutated = %d, want 3", d.FilesMutated)
	}
	// Budget observability: the model computation is measurable.
	if d.ProviderCalls != 1 {
		t.Errorf("ProviderCalls = %d, want 1 — no duplicate invocation may be introduced", d.ProviderCalls)
	}
	if d.TokenInput != 1840 || d.TokenOutput != 612 {
		t.Errorf("provider tokens = %d/%d, want 1840/612", d.TokenInput, d.TokenOutput)
	}
	// No context starvation: the compiled context is reported and substantial.
	if d.ContextTokens != 812 {
		t.Errorf("ContextTokens = %d, want 812", d.ContextTokens)
	}
	if len(d.ContextChannels) != 2 {
		t.Errorf("ContextChannels = %v, want the two compiled channels", d.ContextChannels)
	}
}

// TestGoldenProjection_NoEvidenceNoCompletion is the headline acceptance
// property, isolated: the identical stream, minus the sealed evidence, must NOT
// reach COMPLETED. The golden run's success rests on the evidence — not on the
// provider having returned, not on the loop having ended, and not on the absence
// of an error.
func TestGoldenProjection_NoEvidenceNoCompletion(t *testing.T) {
	st := projectGolden(false).State()
	if st.Phase == PhaseCompleted {
		t.Fatalf("the same stream without sealed evidence must not complete: %+v", st)
	}
	if st.Phase != PhaseUnsubstantiated {
		t.Fatalf("phase = %s, want unsubstantiated", st.Phase)
	}
	if !strings.Contains(st.Outcome, "no sealed execution evidence") {
		t.Errorf("the refusal must name the missing evidence, got %q", st.Outcome)
	}
}

// TestGoldenProjection_LedgerCarriesOnlyRealDiff proves the artifact ledger's
// diff figures are the boundary's own numbers, verbatim, and that a target with
// no diff would render no numbers at all.
func TestGoldenProjection_LedgerCarriesOnlyRealDiff(t *testing.T) {
	d := projectGolden(true).State().Details
	if len(d.Targets) != 3 {
		t.Fatalf("target ledger = %d rows, want 3", len(d.Targets))
	}
	for _, tgt := range d.Targets {
		want, ok := goldenDiff[tgt.Target]
		if !ok {
			t.Fatalf("unexpected ledger row %q", tgt.Target)
		}
		if !tgt.DiffPresent {
			t.Errorf("%s: DiffPresent = false, want true", tgt.Target)
		}
		if tgt.DiffAdds != want[0] || tgt.DiffRemoves != want[1] {
			t.Errorf("%s: diff = +%d -%d, want +%d -%d", tgt.Target, tgt.DiffAdds, tgt.DiffRemoves, want[0], want[1])
		}
		if !tgt.Mutated() {
			t.Errorf("%s: boundary evidence must prove an applied filesystem change", tgt.Target)
		}
	}
	total := 84 + 12 + 156 + 31 + 42 + 8
	if n, ok := d.BytesChanged(); !ok || n != total {
		t.Errorf("BytesChanged = %d (present=%t), want %d/true", n, ok, total)
	}
}

// TestGoldenProjection_CandidateReadyRendersNoDiff is the pre-approval half of
// the golden scenario: once candidates exist but the boundary has not applied
// anything, there is no diff and no mutation to report. "0 bytes written" must
// stay truthful.
func TestGoldenProjection_CandidateReadyRendersNoDiff(t *testing.T) {
	p := NewExecutionProjection()
	p.Begin("held")
	for _, ev := range []events.DomainEvent{
		events.NewExecutionStarted("held", "build", goldenPortfolioRequest, ""),
		events.NewApprovalRequired("held", "index.html", ""),
		events.NewContextPrepared("held", []string{"user_intent", "target_content"}, 812),
		events.NewArtifactProduced("held", "patch", "index.html"),
		events.NewArtifactProduced("held", "patch", "styles.css"),
		events.NewArtifactProduced("held", "patch", "script.js"),
		events.NewMutationStarted("held", goldenTargets),
	} {
		p.Project(ev)
	}
	d := p.State().Details
	if d.CandidateCount != 3 {
		t.Errorf("CandidateCount = %d, want 3", d.CandidateCount)
	}
	if d.MutatedFiles != 0 {
		t.Errorf("MutatedFiles = %d, want 0 — nothing was applied", d.MutatedFiles)
	}
	if n, ok := d.BytesChanged(); ok {
		t.Errorf("no diff may be reported before a mutation, got %d lines", n)
	}
	if d.VerificationRan {
		t.Error("verification must not be reported before it ran")
	}
	if d.EvidenceObserved {
		t.Error("a held execution is not terminated, so no evidence may be claimed")
	}
}

// TestGoldenProjection_ModelInvocationIsSeparatedFromTaskCompletion pins the
// separation at the user-facing layer: the finish reason and the token usage
// are recorded as invocation facts, and the completed verdict comes from a
// different source entirely.
func TestGoldenProjection_ModelInvocationIsSeparatedFromTaskCompletion(t *testing.T) {
	d := projectGolden(true).State().Details
	if d.FinishReason != "stop" {
		t.Errorf("FinishReason = %q, want stop — the generation fact must be preserved", d.FinishReason)
	}
	if d.ProviderState != "done" {
		t.Errorf("ProviderState = %q, want done", d.ProviderState)
	}
	// The invocation completed; the TASK verdict came from the evidence gate,
	// which is recorded in its own field and nowhere else.
	if !d.EvidenceObserved {
		t.Error("the task verdict must be traceable to the evidence, not to the invocation")
	}
}

// TestGoldenProjection_FailedVerificationNeverCompletes is the negative golden
// variant: identical up to verification, which fails and taints the record.
func TestGoldenProjection_FailedVerificationNeverCompletes(t *testing.T) {
	p := NewExecutionProjection()
	p.Begin("golden-1")
	stream := goldenStream(true)
	for _, ev := range stream {
		switch ev.Type() {
		case events.EventVerificationCompleted:
			p.Project(events.NewVerificationCompleted("golden-1", false, []string{"html"}))
		case events.EventExecutionEvidence:
			p.Project(events.NewExecutionEvidence(events.ExecutionEvidencePayload{
				RequestID: "golden-1", Outcome: "COMMITTED", Tainted: true, FilesMutated: 3,
			}))
		default:
			p.Project(ev)
		}
	}
	st := p.State()
	if st.Phase == PhaseCompleted {
		t.Fatalf("failed verification must never complete: %+v", st)
	}
	if !st.Details.VerificationRan || st.Details.VerificationPassed {
		t.Errorf("verification = ran:%t passed:%t, want true/false",
			st.Details.VerificationRan, st.Details.VerificationPassed)
	}
}
