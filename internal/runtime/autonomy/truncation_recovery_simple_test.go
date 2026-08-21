package autonomy

import (
	"context"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
)

func TestTruncationRecoveryChangesStrategySimple(t *testing.T) {
	obs := autonomy.Observation{
		Target:          "index.html",
		Outcome:         autonomy.OutcomeTruncated,
		FinishReason:    "length",
		MaxOutputTokens: 1024,
		AttemptNum:      1,
		InputTokens:     2180,
		OutputTokens:    1021,
		UsageKnown:      true,
	}
	req := autonomy.LoopRequest{
		Prompt:  "check @index.html and rewrite it",
		Targets: []string{"index.html"},
		Evidence: "initial evidence",
	}
	next, err := defaultRepair(obs, req)
	if err != nil {
		t.Fatalf("repair: %v", err)
	}
	if next.RecoveryStrategy != "bounded_patch" {
		t.Fatalf("RecoveryStrategy = %q, want bounded_patch", next.RecoveryStrategy)
	}
	if next.Evidence == req.Evidence {
		t.Fatal("Evidence unchanged after truncation recovery")
	}
	if !strings.Contains(next.Evidence, "bounded_patch") && !strings.Contains(next.Evidence, "SEARCH/REPLACE") {
		t.Fatalf("Evidence missing bounded_patch hint: %q", next.Evidence)
	}
	if next.Prompt == req.Prompt && next.Evidence == req.Evidence && next.MaxOutputTokens == req.MaxOutputTokens {
		t.Fatal("next LoopRequest is identical to previous — not a material contract change")
	}
}

func TestBoundedRecoveryExhaustion(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "index.html", strings.Repeat("x", 7780))
	trunc := &ai.Response{
		Content: "x",
		Usage:   ai.ProviderUsage{PromptTokens: 2180, CompletionTokens: 1024, Known: true, FinishReason: "length"},
	}
	mock := &mockProvider{responses: []*ai.Response{trunc, trunc, trunc, trunc}}
	bus := events.NewBus(events.DefaultBufferSize)
	x := testExecutor(t, root, mock, bus)
	adapter := NewExecutorAdapter(root, execution.NewIntentGateway(root), x)
	driver := NewDriver(adapter, bus, WithLoopBounds(autonomy.LoopBounds{
		MaxAttempts:           3,
		MaxRecoveryCycles:     2,
		MaxExecutionSteps:     10,
		MaxIdenticalDecisions: 10,
		MaxTotalTokens:        100000,
	}))
	_, err := driver.Run(context.Background(), "check @index.html and rewrite it")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if mock.calls() > 3 {
		t.Fatalf("calls = %d, want <=3 bounded", mock.calls())
	}
	st := driver.State()
	if st != autonomy.RuntimeAwaitingHuman && st != autonomy.RuntimeAborted {
		t.Fatalf("state = %v, want awaiting_human or aborted", st)
	}
}

func TestUsageAcrossRecoverySimple(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "index.html", strings.Repeat("x", 7780))
	r1 := &ai.Response{Content: "<<<<<<< SEARCH\na\n=======\nb\n>>>>>>>", Usage: ai.ProviderUsage{PromptTokens: 2180, CompletionTokens: 1021, Known: true, FinishReason: "length"}, TokenInput: 2180, TokenOutput: 1021}
	r2 := &ai.Response{Content: "<<<<<<< SEARCH\na\n=======\nb\n>>>>>>>", Usage: ai.ProviderUsage{PromptTokens: 2176, CompletionTokens: 1024, Known: true, FinishReason: "stop"}, TokenInput: 2176, TokenOutput: 1024}
	mock := &mockProvider{responses: []*ai.Response{r1, r2}}
	bus := events.NewBus(events.DefaultBufferSize)
	x := testExecutor(t, root, mock, bus)
	adapter := NewExecutorAdapter(root, execution.NewIntentGateway(root), x)
	driver := NewDriver(adapter, bus, WithLoopBounds(autonomy.LoopBounds{
		MaxAttempts: 5, MaxRecoveryCycles: 5, MaxExecutionSteps: 10, MaxIdenticalDecisions: 10, MaxTotalTokens: 100000,
	}))
	_, err := driver.Run(context.Background(), "check @index.html and rewrite it")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	in, out, known := driver.AggregatedUsage()
	if !known {
		t.Fatal("aggregated not known")
	}
	if mock.calls() == 2 && (in != 4356 || out != 2045) {
		t.Fatalf("aggregate for 2 calls = %d/%d, want 4356/2045", in, out)
	}
	if in < 2180 || out < 1021 {
		t.Fatalf("aggregate %d/%d less than first attempt 2180/1021", in, out)
	}
}

func TestFinishReasonLengthIsTruncated(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "index.html", strings.Repeat("x", 100))
	mock := &mockProvider{responses: []*ai.Response{{
		Content: "full file content that would be truncated",
		Usage:   ai.ProviderUsage{PromptTokens: 10, CompletionTokens: 1024, Known: true, FinishReason: "length"},
	}}}
	bus := events.NewBus(events.DefaultBufferSize)
	x := testExecutor(t, root, mock, bus)
	adapter := NewExecutorAdapter(root, execution.NewIntentGateway(root), x)
	obs, err := adapter.Execute(context.Background(), autonomy.LoopRequest{Prompt: "check @index.html and rewrite it", Targets: []string{"index.html"}})
	if err != nil {
		t.Fatalf("Execute err: %v", err)
	}
	if obs.Outcome != autonomy.OutcomeTruncated {
		t.Fatalf("outcome = %q, want truncated", obs.Outcome)
	}
	if obs.FinishReason != "length" {
		t.Fatalf("FinishReason = %q, want length", obs.FinishReason)
	}
}
