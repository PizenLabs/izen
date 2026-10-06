package progress

// ── R5 — the state-delta progress detector ──────────────────────────────────
//
// The runtime owns TWO independent progress engines:
//
//	1. execution.ReduceProgress  — a pure function of the CURRENT evidence,
//	   used by the autonomy loop to classify objective progress and to decide
//	   whether an incomplete objective may continue.
//	2. this Detector             — a STATEFUL run of consecutive snapshots,
//	   used by the behavioral repair loop to stop a loop that has stopped
//	   learning.
//
// R5 traced both. These tests pin the second engine's seed semantic: progress
// is an observable change of runtime state, and a run of identical states is
// NO_PROGRESS — never "activity", never a token count.
//
// The autonomy loop does NOT consult this detector today; that boundary is
// recorded in docs/report/R5_NON_PROGRESS_REPORT.md §"first missing transition".
// These tests prove the detector itself is correct and available.

import (
	"strings"
	"testing"
)

// TestR5_DetectorDistinguishesProgressFromStall is the state-delta proof: two
// runs that share a starting snapshot diverge into PROGRESS (state moved) and
// NO_PROGRESS (state frozen) purely on observed state, never on elapsed rounds.
func TestR5_DetectorDistinguishesProgressFromStall(t *testing.T) {
	base := Snapshot{
		Round:           1,
		Satisfied:       1,
		Unmet:           3,
		FingerprintKeys: []string{"defect-a"},
		EvidenceDigest:  "evidence-1",
		Mutations:       1,
	}

	// Genuine progress: a requirement is satisfied and a mutation lands.
	progressing := NewDetector(2)
	if got := progressing.Observe(base); got != VerdictUnknown {
		t.Fatalf("baseline verdict = %v, want UNKNOWN", got)
	}
	moved := base
	moved.Round = 2
	moved.Satisfied = 2
	moved.Unmet = 2
	moved.EvidenceDigest = "evidence-2"
	moved.Mutations = 2
	if got := progressing.Observe(moved); got != VerdictProgress {
		t.Fatalf("advanced state verdict = %v, want PROGRESS", got)
	}
	if progressing.Reason() != "" {
		t.Fatalf("a progress verdict must not carry a stop reason: %q", progressing.Reason())
	}

	// Stall: the same state repeats. Activity without a state change is not
	// progress, and the detector eventually says so.
	stalled := NewDetector(2)
	if got := stalled.Observe(base); got != VerdictUnknown {
		t.Fatalf("baseline verdict = %v, want UNKNOWN", got)
	}
	if got := stalled.Observe(base); got != VerdictNoProgress {
		t.Fatalf("identical state verdict = %v, want NO_PROGRESS", got)
	}
	if stalled.Reason() == "" || !strings.Contains(stalled.Reason(), "no progress") {
		t.Fatalf("NO_PROGRESS must carry an attributable reason, got %q", stalled.Reason())
	}
}

// TestR5_DetectorMutationAloneIsProgress proves a durable mutation is a
// legitimate state change even when the requirement counts are unchanged — the
// workspace the next round reads is not the workspace the last round read.
func TestR5_DetectorMutationAloneIsProgress(t *testing.T) {
	d := NewDetector(2)
	s := Snapshot{Round: 1, Satisfied: 1, Unmet: 1, EvidenceDigest: "e", Mutations: 1}
	d.Observe(s)
	s.Round = 2
	s.Mutations = 2 // nothing else changed
	if got := d.Observe(s); got != VerdictProgress {
		t.Fatalf("a new mutation = %v, want PROGRESS", got)
	}
}

// TestR5_DetectorMutationsAreCumulativeNotPerRound guards the accounting: a
// snapshot whose mutation COUNT is cumulative must compare against the previous
// count, not be treated as fresh progress every round.
func TestR5_DetectorMutationsAreCumulativeNotPerRound(t *testing.T) {
	d := NewDetector(2)
	s := Snapshot{Round: 1, Satisfied: 1, Unmet: 1, EvidenceDigest: "e", Mutations: 5}
	d.Observe(s)
	s.Round = 2
	// Same cumulative count, same everything: a stall, not progress.
	if got := d.Observe(s); got != VerdictNoProgress {
		t.Fatalf("an unchanged cumulative mutation count = %v, want NO_PROGRESS", got)
	}
}
