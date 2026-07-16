package build

import (
	"strings"
	"testing"

	izenctx "github.com/PizenLabs/izen/internal/context"
)

func TestRenderExecutionSummarySuccess(t *testing.T) {
	s := ExecutionSummary{
		Success:        true,
		Mutations:      []MutationRecord{{File: "internal/foo/bar.go", Strategy: "ATOMIC_REPLACE"}},
		ContextID:      "#ctx-go-123-r1",
		GuardrailPass:  true,
		GuardrailCount: 0,
		GuardrailLimit: 3,
	}
	out := RenderExecutionSummary(s)

	if !strings.Contains(out, "**🚀 BUILD MUTATION SUMMARY**") {
		t.Fatalf("missing header:\n%s", out)
	}
	if !strings.Contains(out, "- **Status:** SUCCESS") {
		t.Fatalf("missing success status:\n%s", out)
	}
	if !strings.Contains(out, "- **Files Mutated:** `internal/foo/bar.go` (strategy: ATOMIC_REPLACE)") {
		t.Fatalf("missing mutated file line:\n%s", out)
	}
	if !strings.Contains(out, "- **Context Scope:** [#ctx-go-123-r1]") {
		t.Fatalf("missing context scope:\n%s", out)
	}
	if !strings.Contains(out, "- **Guardrail Status:** PASS (0/3 mutations)") {
		t.Fatalf("missing guardrail status:\n%s", out)
	}
}

func TestRenderExecutionSummaryFailed(t *testing.T) {
	s := ExecutionSummary{
		Success:        false,
		ErrorLink:      "err://patch-rejected",
		ContextID:      "#ctx-go-9-r2",
		GuardrailPass:  false,
		GuardrailCount: 3,
		GuardrailLimit: 3,
	}
	out := RenderExecutionSummary(s)
	if !strings.Contains(out, "- **Status:** FAILED (err://patch-rejected)") {
		t.Fatalf("expected failed status with link:\n%s", out)
	}
	if !strings.Contains(out, "- **Guardrail Status:** TRIGGERED (3/3 mutations)") {
		t.Fatalf("expected triggered guardrail:\n%s", out)
	}
}

func TestEngineRecordPatchUpdatesLedger(t *testing.T) {
	ledger := izenctx.NewTaskLedger()
	e := NewEngine()
	e.SetLedger(ledger)
	e.SetContextID("#ctx-go-1-r1")

	summary := e.RecordPatch(3, "internal/foo.go", "DIFF_PATCH")

	if summary.Success != true {
		t.Fatal("expected success summary")
	}
	if !ledger.IsCompleted(3) {
		t.Fatal("expected task 3 to be Completed in the shared ledger")
	}
	if ledger.IsCompleted(99) {
		t.Fatal("unrelated task must remain pending")
	}
	if !strings.Contains(e.RenderExecutionSummary(), "BUILD MUTATION SUMMARY") {
		t.Fatal("expected RenderExecutionSummary to expose the stored summary")
	}
	if !strings.Contains(e.RenderExecutionSummary(), "(strategy: DIFF_PATCH)") {
		t.Fatal("expected strategy in rendered summary")
	}
}

func TestEngineRecordPatchWithoutTaskID(t *testing.T) {
	ledger := izenctx.NewTaskLedger()
	e := NewEngine()
	e.SetLedger(ledger)

	// taskID 0 means no ledger update (e.g. a recovery rewrite, not a plan task).
	e.RecordPatch(0, "internal/foo.go", "")

	if ledger.IsCompleted(0) {
		t.Fatal("taskID 0 must not mutate the ledger")
	}
	if !strings.Contains(e.RenderExecutionSummary(), "(strategy: ATOMIC_REPLACE)") {
		t.Fatal("expected default strategy when unset")
	}
}

func TestEngineRecordPatchGuardrailCounter(t *testing.T) {
	e := NewEngine()
	e.RecordPatch(1, "a.go", "")
	e.RecordPatch(2, "b.go", "")
	out := e.RenderExecutionSummary()
	if !strings.Contains(out, "PASS (2/3 mutations)") {
		t.Fatalf("expected running guardrail count 2/3, got:\n%s", out)
	}
}
