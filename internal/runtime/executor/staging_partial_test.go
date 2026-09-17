package executor

import (
	"testing"

	"github.com/PizenLabs/izen/internal/core/domain/evidence"
)

// countingSink records workspace mutations for the isolation assertion.
type countingSink struct {
	patches int
	applied []string
}

func (s *countingSink) Apply(proposal string) (int, error) {
	s.patches++
	s.applied = append(s.applied, proposal)
	return 1, nil
}

// TestProposalStaging_PartialTruncationIsolation mocks a provider returning
// finish_reason=length mid-diff stream: the staging buffer must be
// discarded, zero patches may hit the workspace, and StepOutcomePartial with
// a continuation request must be emitted — without corrupting TaskState.
func TestProposalStaging_PartialTruncationIsolation(t *testing.T) {
	buf := NewProposalStagingBuffer("step-1-of-3")
	// Mid-diff stream: a plausible partial unified diff, cut off mid-hunk.
	buf.AppendString("diff --git a/main.go b/main.go\n")
	buf.AppendString("@@ -1,4 +1,6 @@\n")
	buf.AppendString("+func PartialFeature() {\n")
	if buf.Len() == 0 {
		t.Fatal("staging buffer must hold streamed bytes before finalize")
	}
	disp := buf.Finalize("length", false)
	if disp.Outcome != StepOutcomePartial {
		t.Fatalf("finish_reason=length outcome = %q, want partial", disp.Outcome)
	}
	if !disp.NeedsContinuation {
		t.Fatal("partial outcome must request scheduler continuation")
	}
	if !disp.Discarded {
		t.Fatal("partial outcome must discard the staging buffer")
	}
	if disp.Proposal != "" {
		t.Fatalf("partial proposal leaked %d bytes across the boundary", len(disp.Proposal))
	}
	if buf.Len() != 0 {
		t.Fatalf("staging buffer residue = %d bytes after discard, want 0", buf.Len())
	}
	// Even with passing evidence, a Partial disposition commits nothing.
	sink := &countingSink{}
	n, outcome, err := CommitGate(disp, evidence.VerdictPassed, sink)
	if err != nil {
		t.Fatalf("CommitGate: %v", err)
	}
	if n != 0 || sink.patches != 0 {
		t.Fatalf("partial committed %d patches to workspace, want 0", n)
	}
	if outcome != StepOutcomePartial {
		t.Fatalf("commit outcome = %q, want partial", outcome)
	}
	// A truncated transport flag forces Partial even with a "stop" reason.
	buf2 := NewProposalStagingBuffer("step-2-of-3")
	buf2.AppendString("complete-looking bytes")
	disp2 := buf2.Finalize("stop", true)
	if disp2.Outcome != StepOutcomePartial || !disp2.Discarded || disp2.Proposal != "" {
		t.Fatalf("transport truncation must yield discarded partial, got %+v", disp2)
	}
	// Control: a clean stop stream commits exactly once with verified
	// evidence (Invariant 5: execution truth, not model assertion).
	buf3 := NewProposalStagingBuffer("step-3-of-3")
	buf3.AppendString("full diff body")
	disp3 := buf3.Finalize("stop", false)
	if disp3.Outcome != StepOutcomeComplete || disp3.Proposal == "" {
		t.Fatalf("clean stop must yield committable proposal, got %+v", disp3)
	}
	n3, _, err := CommitGate(disp3, evidence.VerdictPassed, sink)
	if err != nil || n3 != 1 || sink.patches != 1 {
		t.Fatalf("complete+verified must apply 1 patch, got n=%d patches=%d err=%v", n3, sink.patches, err)
	}
	// Unverified evidence holds the proposal: zero additional patches.
	n4, _, _ := CommitGate(disp3, evidence.VerdictInconclusive, sink)
	if n4 != 0 || sink.patches != 1 {
		t.Fatalf("unverified evidence must not mutate workspace (patches=%d)", sink.patches)
	}
}
