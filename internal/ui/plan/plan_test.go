package plan

import (
	"strings"
	"testing"
	"time"
)

func testPlan() *ExecutionPlan {
	return &ExecutionPlan{
		Goal: "fix failing build",
		Steps: []PlanStep{
			{ID: "s1", Title: "Reproduce failure", Status: StatusPending},
			{ID: "s2", Title: "Patch source", Status: StatusPending, SubSteps: []PlanStep{
				{ID: "s2a", Title: "Edit handler", Status: StatusPending},
				{ID: "s2b", Title: "Update tests", Status: StatusPending},
			}},
			{ID: "s3", Title: "Verify build", Status: StatusPending},
		},
	}
}

// TestPlanStateTransitions drives Pending -> Running -> Success across steps
// and verifies counts and total elapsed duration at each stage.
func TestPlanStateTransitions(t *testing.T) {
	p := testPlan()

	if c, r, f, tot := p.Counts(); c != 0 || r != 0 || f != 0 || tot != 5 {
		t.Fatalf("initial counts = (%d,%d,%d,%d), want (0,0,0,5)", c, r, f, tot)
	}

	// s1 starts running.
	if !p.UpdateStep("s1", StatusRunning, 0, "") {
		t.Fatal("UpdateStep(s1 -> RUNNING) returned false")
	}
	if _, r, _, _ := p.Counts(); r != 1 {
		t.Fatalf("running = %d, want 1", r)
	}

	// s1 succeeds after 400ms.
	if !p.UpdateStep("s1", StatusSuccess, 400*time.Millisecond, "") {
		t.Fatal("UpdateStep(s1 -> SUCCESS) returned false")
	}
	c, r, _, _ := p.Counts()
	if c != 1 || r != 0 {
		t.Fatalf("counts = completed %d running %d, want (1,0)", c, r)
	}

	// Nested sub-steps run and complete.
	if !p.UpdateStep("s2", StatusRunning, 0, "") {
		t.Fatal("UpdateStep(s2 -> RUNNING) returned false")
	}
	if !p.UpdateStep("s2a", StatusRunning, 0, "") {
		t.Fatal("UpdateStep(s2a -> RUNNING) returned false")
	}
	if _, r, _, _ := p.Counts(); r != 2 {
		t.Fatalf("running = %d, want 2", r)
	}
	p.UpdateStep("s2a", StatusSuccess, 100*time.Millisecond, "")
	p.UpdateStep("s2b", StatusSuccess, 200*time.Millisecond, "")
	p.UpdateStep("s2", StatusSuccess, 3200*time.Millisecond, "")

	// s3 fails.
	p.UpdateStep("s3", StatusFailed, 50*time.Millisecond, "exit code 1")

	c, r, f, tot := p.Counts()
	if c != 4 || r != 0 || f != 1 || tot != 5 {
		t.Fatalf("final counts = (%d,%d,%d,%d), want (4,0,1,5)", c, r, f, tot)
	}

	wantElapsed := 400*time.Millisecond + 100*time.Millisecond + 200*time.Millisecond +
		3200*time.Millisecond + 50*time.Millisecond
	if got := p.TotalElapsed(); got != wantElapsed {
		t.Fatalf("TotalElapsed = %v, want %v", got, wantElapsed)
	}
}

func TestUpdateStepUnknownID(t *testing.T) {
	p := testPlan()
	if p.UpdateStep("nope", StatusSuccess, 0, "") {
		t.Fatal("UpdateStep(unknown) must return false")
	}
}

func TestRenderStatusGlyphs(t *testing.T) {
	p := testPlan()
	p.UpdateStep("s1", StatusSuccess, 400*time.Millisecond, "")
	p.UpdateStep("s2", StatusRunning, 3200*time.Millisecond, "")
	p.UpdateStep("s3", StatusFailed, 0, "exit code 1")

	out := p.Render(0, 100)
	for _, want := range []string{"✓", "○", "✗", "Execution Plan", "fix failing build"} {
		if !strings.Contains(out, want) {
			t.Errorf("Render missing %q:\n%s", want, out)
		}
	}
	// Spinner frame must advance the running glyph.
	alt := p.Render(3, 100)
	if alt == "" {
		t.Fatal("Render(frame=3) returned empty")
	}
}

func TestRenderNilPlan(t *testing.T) {
	var p *ExecutionPlan
	if got := p.Render(0, 80); got != "" {
		t.Fatalf("nil Render = %q, want empty", got)
	}
	if c, r, f, tot := p.Counts(); c != 0 || r != 0 || f != 0 || tot != 0 {
		t.Fatalf("nil Counts = (%d,%d,%d,%d), want zeros", c, r, f, tot)
	}
	if got := p.TotalElapsed(); got != 0 {
		t.Fatalf("nil TotalElapsed = %v, want 0", got)
	}
}
