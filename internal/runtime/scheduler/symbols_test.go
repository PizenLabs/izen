package scheduler

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/core/domain/evidence"
	"github.com/PizenLabs/izen/internal/runtime/executor"
)

func TestEvidence_RedundantSymbolRejection(t *testing.T) {
	spec, state, sink := recoverySpec(t)
	baseline := "package main\n\ntype widget struct { n int }\nfunc Exported() {}\nfunc existingHelper(x int) int { if x < 0 { return -x }; return x }\nvar limit = 3\n"
	if err := os.WriteFile(sink.path, []byte(baseline), 0600); err != nil { t.Fatal(err) }
	refreshDisk(t, &spec, sink)
	spec.Operations["main.go"] = "MODIFY"
	proposal := baseline + "func duplicateHelper(value int) int { if value < 0 { return -value }; return value }\n"
	var staged *executor.ProposalStagingBuffer
	worker := func(_ context.Context, _ ExecutionStep, slice ContextSlice, buffer *executor.ProposalStagingBuffer) (StreamResult, error) {
		for _, name := range []string{"AVAILABLE SYMBOLS", "widget", "Exported", "existingHelper", "limit"} {
			if !strings.Contains(slice.RenderPrompt(), name) { t.Fatalf("missing scope symbol %s", name) }
		}
		staged = buffer
		buffer.AppendString(proposal)
		return StreamResult{FinishReason: "stop"}, nil
	}
	result, err := NewStepScheduler().RunNext(t.Context(), spec, state, worker, evidence.VerdictPassed, sink)
	if err != nil { t.Fatal(err) }
	if result.Outcome != StepOutcomePartial || result.Reason != string(executor.RedundantSymbolReason) || result.Patches != 0 || !result.NeedsContinuation || sink.calls != 0 || staged.Len() != 0 {
		t.Fatalf("redundant proposal crossed boundary: %+v calls=%d staged=%d", result, sink.calls, staged.Len())
	}
	actual, err := os.ReadFile(sink.path)
	if err != nil || string(actual) != baseline { t.Fatalf("workspace changed: %q %v", actual, err) }
	if len(state.RecoveryContext.ReuseSymbols) != 1 || state.RecoveryContext.ReuseSymbols[0] != "existingHelper" { t.Fatalf("missing reuse guidance: %+v", state.RecoveryContext) }
	continuation := func(_ context.Context, _ ExecutionStep, slice ContextSlice, buffer *executor.ProposalStagingBuffer) (StreamResult, error) {
		if !strings.Contains(slice.RecoveryInstructions, "existingHelper") || !strings.Contains(slice.RecoveryInstructions, "REDUNDANT_SYMBOL") { t.Fatalf("missing actionable continuation: %s", slice.RenderPrompt()) }
		buffer.AppendString(strings.Replace(baseline, "return -x", "return x * -1", 1))
		return StreamResult{FinishReason: "stop"}, nil
	}
	second, err := NewStepScheduler().RunNext(t.Context(), spec, state, continuation, evidence.VerdictPassed, sink)
	if err != nil || second.Outcome != StepOutcomeComplete || second.Patches != 1 || sink.calls != 1 { t.Fatalf("existing helper modification rejected: %+v %v", second, err) }
	if state.RecoveryContext.Reason != "" { t.Fatalf("recovery not cleared after commit: %+v", state.RecoveryContext) }
}

func TestEvidence_PublicDeclarationAllowed(t *testing.T) {
	spec, state, sink := recoverySpec(t)
	baseline := "package main\nfunc existingHelper() int { return 1 }\n"
	spec.TargetASTs = map[string]string{"main.go": baseline}
	spec.Operations["main.go"] = "MODIFY"
	spec.DiskSizes["main.go"] = int64(len(baseline))
	spec.StateFingerprints["main.go"] = fmt.Sprintf("%x", sha256.Sum256([]byte(baseline)))
	result, err := NewStepScheduler().RunNext(t.Context(), spec, state, streamWorker(baseline+"func PublicAPI() int { return 1 }\n", "stop", 30), evidence.VerdictPassed, sink)
	if err != nil || result.Outcome != StepOutcomeComplete || sink.calls != 1 { t.Fatalf("public API rejected: %+v %v", result, err) }
}
