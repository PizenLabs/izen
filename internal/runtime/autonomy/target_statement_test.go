package autonomy

// ── EXPLICIT TARGET STATEMENT (CLI `-target` seam) ──────────────────────────
//
// `izen run -target <path>` states an explicit target set before the run. The
// migration binds that statement through the SAME authoritative seam a
// human-named target at a clarification boundary uses (Driver.SetTargets →
// bindAuthoritativeTargets → the canonical gateway): it is a statement, not a
// resolution, and the run may only bind it by resolving it. These tests pin that
// the statement reaches the run, is normalized, and is consumed exactly once.

import (
	"context"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/autonomy"
)

// TestSetTargetsBindsTheNextRunThroughTheAuthoritativeSeam proves an explicit
// target set is bound by the run, normalized, and applied to the held candidate
// — and that nothing is written before the human decision.
func TestSetTargetsBindsTheNextRunThroughTheAuthoritativeSeam(t *testing.T) {
	root, mock, a, _ := testHarness(t, []*ai.Response{{Content: sampleReplace}})
	d := NewDriver(a, nil, WithScope("$run"))
	d.SetTargets([]string{"note.txt", "note.txt", ""})

	term, err := d.Run(context.Background(), "change bar to qux")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if term != nil || d.State() != autonomy.RuntimeAwaitingHuman {
		t.Fatalf("run did not resolve the explicit target to an approval: term=%+v state=%s", term, d.State())
	}
	b := d.Boundary()
	if b == nil || b.Action != autonomy.HumanBoundaryApproval {
		t.Fatalf("boundary = %+v, want an approval over the explicit target", b)
	}
	if len(b.CandidateTargets) != 1 || b.CandidateTargets[0] != "note.txt" {
		t.Fatalf("candidate targets = %v, want [note.txt] (deduplicated, non-empty)", b.CandidateTargets)
	}
	if mock.calls() != 1 {
		t.Fatalf("provider calls = %d, want 1", mock.calls())
	}
	// Nothing is applied before the human decision: the review is a review.
	if got := readTarget(t, root, "note.txt"); got != sampleOriginal {
		t.Fatalf("the workspace changed before approval: %q", got)
	}
	// The statement is an input to THIS run, not a permanent override.
	if len(d.statedTargets) != 0 {
		t.Fatalf("stated targets survived the run: %v", d.statedTargets)
	}
}
