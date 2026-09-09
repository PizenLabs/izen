package compactor

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/events"
)

func bigToolOutput(lines, width int) string {
	var b strings.Builder
	for i := 0; i < lines; i++ {
		b.WriteString(strings.Repeat("x", width))
		b.WriteString("\n")
	}
	return b.String()
}

func TestPhase1ToolPruning(t *testing.T) {
	// Synthetic ~50k token tool output (~200k chars at ~4 chars/token).
	huge := bigToolOutput(2000, 100)
	conv := &Conversation{
		SystemPrompt: "sys",
		Turns: []Turn{
			{Role: "tool", Content: huge, IsToolOutput: true},
			{Role: "user", Content: "keep me 1"},
			{Role: "assistant", Content: "keep me 2"},
			{Role: "user", Content: "keep me 3"},
		},
	}
	e := New(WithMaxTokens(200000), WithThresholdRatio(0.80))
	before := e.Budget(conv)
	if before.ToolOutputTokens < 40000 {
		t.Fatalf("fixture too small: tool tokens = %d", before.ToolOutputTokens)
	}
	res, err := e.Compact(context.Background(), conv, ForceOptions{Force: true})
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if res.StrategyUsed != StrategyToolPrune && res.StrategyUsed != StrategySummarize {
		t.Fatalf("strategy = %s, want TOOL_PRUNE (or SUMMARIZE)", res.StrategyUsed)
	}
	if !strings.Contains(conv.Turns[0].Content, "[Output truncated:") {
		t.Fatal("old tool output was not truncated")
	}
	// Recent window untouched.
	for i, want := range []string{"keep me 1", "keep me 2", "keep me 3"} {
		if conv.Turns[len(conv.Turns)-3+i].Content != want {
			t.Fatalf("recent turn %d mutated: %q", i, conv.Turns[len(conv.Turns)-3+i].Content)
		}
	}
	if res.FreedTokens <= 0 {
		t.Fatalf("freed = %d, want > 0", res.FreedTokens)
	}
}

func TestPhase2RetentionWindowInvariant(t *testing.T) {
	var turns []Turn
	for i := 0; i < 10; i++ {
		turns = append(turns, Turn{Role: "user", Content: strings.Repeat("a", 4000)})
		turns = append(turns, Turn{Role: "assistant", Content: strings.Repeat("b", 4000)})
	}
	// Snapshot last 3 turns byte-for-byte.
	want := make([]Turn, 3)
	copy(want, turns[len(turns)-3:])
	conv := &Conversation{SystemPrompt: "sys", Turns: turns}
	e := New(WithMaxTokens(30000), WithThresholdRatio(0.5))
	res, err := e.Compact(context.Background(), conv, ForceOptions{Force: true})
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if res.StrategyUsed != StrategySummarize {
		t.Fatalf("strategy = %s, want LLM_SUMMARIZE", res.StrategyUsed)
	}
	if len(conv.Turns) != 3 {
		t.Fatalf("turns after = %d, want 3", len(conv.Turns))
	}
	for i := range want {
		if conv.Turns[i] != want[i] {
			t.Fatalf("recent turn %d not byte-identical", i)
		}
	}
	if strings.TrimSpace(conv.Summary) == "" {
		t.Fatal("summary node empty after Phase 2")
	}
	if res.UncompactedTurns != 3 {
		t.Fatalf("UncompactedTurns = %d, want 3", res.UncompactedTurns)
	}
}

func TestPhase3HardCropFallback(t *testing.T) {
	// Simulate 99% saturation with a tiny window so even the summary cannot fit.
	var turns []Turn
	for i := 0; i < 20; i++ {
		turns = append(turns, Turn{Role: "user", Content: strings.Repeat("z", 4000)})
	}
	conv := &Conversation{SystemPrompt: strings.Repeat("s", 4000), Turns: turns}
	e := New(
		WithMaxTokens(8000),
		WithThresholdRatio(0.80),
		WithSummarizer(func(_ context.Context, _ []Turn) (string, error) {
			return "", context.DeadlineExceeded // force fallback path
		}),
	)
	before := e.Budget(conv)
	if float64(before.CurrentTokens)/float64(before.MaxTokens) < 0.99 {
		t.Fatalf("fixture saturation = %.2f, want >= 0.99", float64(before.CurrentTokens)/float64(before.MaxTokens))
	}
	res, err := e.Compact(context.Background(), conv, ForceOptions{Force: true})
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if res.StrategyUsed != StrategyHardCrop {
		t.Fatalf("strategy = %s, want HARD_CROP", res.StrategyUsed)
	}
	after := e.Budget(conv)
	if after.CurrentTokens >= before.CurrentTokens {
		t.Fatalf("hard crop freed nothing: before=%d after=%d", before.CurrentTokens, after.CurrentTokens)
	}
}

func TestNoopBelowThreshold(t *testing.T) {
	conv := &Conversation{Turns: []Turn{{Role: "user", Content: "hi"}}}
	e := New(WithMaxTokens(200000))
	snap := conv.Clone()
	res, err := e.Compact(context.Background(), conv, ForceOptions{})
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if res.StrategyUsed != StrategyNone {
		t.Fatalf("strategy = %s, want NONE", res.StrategyUsed)
	}
	if len(conv.Turns) != len(snap.Turns) || conv.Summary != snap.Summary {
		t.Fatal("conversation mutated on clean no-op run")
	}
}

func TestLifecycleEvents(t *testing.T) {
	bus := events.NewBus(64)
	defer bus.Close()
	var mu sync.Mutex
	var got []string
	done := make(chan struct{}, 8)
	collect := func(ev events.DomainEvent) {
		mu.Lock()
		got = append(got, ev.Type())
		mu.Unlock()
		done <- struct{}{}
	}
	bus.Subscribe(events.EventCompactionStarted, collect)
	bus.Subscribe(events.EventCompactionCompleted, collect)
	e := New(WithEventBus(bus))
	conv := &Conversation{Turns: []Turn{{Role: "user", Content: "hi"}}}
	if _, err := e.Compact(context.Background(), conv, ForceOptions{Force: true}); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	count := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(got)
	}
	timeout := time.After(2 * time.Second)
	for count() < 2 {
		select {
		case <-done:
		case <-timeout:
			mu.Lock()
			defer mu.Unlock()
			t.Fatalf("events = %v, want started+completed", got)
		}
	}
}
