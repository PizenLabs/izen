// PHASE 13 — artifact-centric execution observability, and DIFF AS EVIDENCE.
//
// The rules pinned here:
//
//   - Diff statistics come from the mutation boundary's OWN compiled diff. There
//     is no display-time diff engine, and no estimate.
//   - A target with no compiled diff renders NO statistics. Never "+0 -0",
//     which would read as a measured empty diff rather than an absent one.
//   - A file is only counted as mutated when the boundary proves the apply ran
//     AND the content actually changed.
//   - "0 bytes written" stays truthful: an execution whose candidate was never
//     applied reports no diff at all.
package presentation

import "testing"

// TestTargetEvidence_DiffIsAbsentWhenNoDiffWasCompiled proves the absent-diff
// case at the data layer. This is the case that would otherwise let a renderer
// print a fabricated "+0 -0".
func TestTargetEvidence_DiffIsAbsentWhenNoDiffWasCompiled(t *testing.T) {
	d := ExecutionDetails{Targets: []TargetEvidence{{
		Target: "index.html", Candidate: true, Outcome: "changed",
		DiffPresent: false, ApplyExecuted: true, FilesystemChanged: true,
	}}}
	if n, ok := d.BytesChanged(); ok {
		t.Fatalf("BytesChanged reported %d lines with no compiled diff — the absent case must stay absent", n)
	}
}

// TestTargetEvidence_DiffMetricsAreVerbatim proves the projection transports the
// boundary's numbers without adjustment.
func TestTargetEvidence_DiffMetricsAreVerbatim(t *testing.T) {
	p := project(
		events_ExecutionStarted(),
		events_MutationStarted("r1", []string{"index.html", "styles.css", "script.js"}),
		events_MutationCompleted("r1", "index.html", "changed", true, 84, 12),
		events_MutationCompleted("r1", "styles.css", "changed", true, 156, 31),
		events_MutationCompleted("r1", "script.js", "changed", true, 42, 8),
	)
	d := p.State().Details
	if d.MutatedFiles != 3 {
		t.Fatalf("MutatedFiles = %d, want 3", d.MutatedFiles)
	}
	if n, ok := d.BytesChanged(); !ok || n != 84+12+156+31+42+8 {
		t.Errorf("BytesChanged = %d (present=%t), want 333/true", n, ok)
	}
	want := map[string][2]int{
		"index.html": {84, 12},
		"styles.css": {156, 31},
		"script.js":  {42, 8},
	}
	for _, tgt := range d.Targets {
		w, ok := want[tgt.Target]
		if !ok {
			continue
		}
		if !tgt.DiffPresent {
			t.Errorf("%s: DiffPresent = false, want true", tgt.Target)
		}
		if tgt.DiffAdds != w[0] || tgt.DiffRemoves != w[1] {
			t.Errorf("%s: diff = +%d -%d, want +%d -%d", tgt.Target, tgt.DiffAdds, tgt.DiffRemoves, w[0], w[1])
		}
	}
}

// TestTargetEvidence_NoProgressIsNotAMutation proves an unchanged target is
// never counted as mutated even when the apply step ran — the apply having run
// is not the same as the content having changed.
func TestTargetEvidence_NoProgressIsNotAMutation(t *testing.T) {
	p := project(
		events_ExecutionStarted(),
		events_MutationStarted("r1", []string{"index.html"}),
		events_MutationCompleted("r1", "index.html", "nochange", false, 0, 0),
	)
	d := p.State().Details
	if d.MutatedFiles != 0 {
		t.Errorf("MutatedFiles = %d, want 0 — a no-change apply mutated nothing", d.MutatedFiles)
	}
	if d.Targets[0].Mutated() {
		t.Error("Mutated() must be false when the filesystem did not change")
	}
	if d.Targets[0].DiffPresent {
		t.Error("a no-change target can never carry a compiled diff")
	}
}

// TestTargetEvidence_CandidateWithoutApplyHasNoDiff is the "0 bytes written"
// invariant: a candidate was generated, the boundary opened, and nothing was
// applied — so there is no diff to report anywhere.
func TestTargetEvidence_CandidateWithoutApplyHasNoDiff(t *testing.T) {
	p := project(
		events_ExecutionStarted(),
		events_ArtifactProduced("r1", "index.html"),
		events_MutationStarted("r1", []string{"index.html"}),
	)
	d := p.State().Details
	if d.CandidateCount != 1 {
		t.Errorf("CandidateCount = %d, want 1", d.CandidateCount)
	}
	if d.MutatedFiles != 0 {
		t.Errorf("MutatedFiles = %d, want 0", d.MutatedFiles)
	}
	if n, ok := d.BytesChanged(); ok {
		t.Errorf("no apply ran, so no diff may be reported (got %d)", n)
	}
}

// TestTargetEvidence_RepeatedCompletionDoesNotInflateCounts proves the counters
// are derived from the ledger rather than incremented per event, so a duplicated
// or replayed event cannot inflate a count.
func TestTargetEvidence_RepeatedCompletionDoesNotInflateCounts(t *testing.T) {
	ev := events_MutationCompleted("r1", "index.html", "changed", true, 10, 2)
	p := project(
		events_ExecutionStarted(),
		events_MutationStarted("r1", []string{"index.html"}),
		ev, ev, ev,
	)
	d := p.State().Details
	if d.MutatedFiles != 1 {
		t.Errorf("MutatedFiles = %d, want 1 after three identical events", d.MutatedFiles)
	}
	if n, _ := d.BytesChanged(); n != 12 {
		t.Errorf("BytesChanged = %d, want 12 — a replayed event must not re-count", n)
	}
}

// TestProviderCallsAreCounted pins the observability denominator: provider
// invocations are counted from observed model.invoked events, so "useful
// outcome / model computation" is measurable rather than guessed.
func TestProviderCallsAreCounted(t *testing.T) {
	p := project(
		events_ExecutionStarted(),
		events_ModelInvoked("r1"),
		events_ProviderResponse("r1", 120, 900, "stop"),
		events_ModelInvoked("r1"),
		events_ProviderResponse("r1", 60, 400, "length"),
	)
	d := p.State().Details
	if d.ProviderCalls != 2 {
		t.Errorf("ProviderCalls = %d, want 2", d.ProviderCalls)
	}
	if d.TokenInput != 60 || d.TokenOutput != 400 {
		t.Errorf("provider tokens = %d/%d, want the latest report 60/400", d.TokenInput, d.TokenOutput)
	}
	if d.FinishReason != "length" {
		t.Errorf("FinishReason = %q, want length", d.FinishReason)
	}
}
