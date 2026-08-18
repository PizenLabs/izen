package build

import (
	"strings"
	"testing"
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

	if !strings.Contains(out, "**⛟  BUILD MUTATION SUMMARY**") {
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
