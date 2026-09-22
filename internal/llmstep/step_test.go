package llmstep

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
)

var freeTierModels = []string{
	"cohere/north-mini-code:free",
	"meta-llama/llama-3.2-1b:free",
	"deepseek/deepseek-r1:free",
}

func TestResolveMaxTokens_ConstrainedFreeTierClampedToCeiling(t *testing.T) {
	for _, model := range freeTierModels {
		maxTokens, constrained := ResolveMaxTokens(model, DefaultAskRequestedTokens)
		if !constrained {
			t.Fatalf("%s: constrained = false, want true", model)
		}
		if maxTokens > capabilityConstrainedThreshold() {
			t.Fatalf("%s: maxTokens = %d, want <= constrained threshold", model, maxTokens)
		}
	}
}

func capabilityConstrainedThreshold() int {
	const threshold = 1024
	return threshold
}

func TestResolveMaxTokens_UnconstrainedKeepsRequested(t *testing.T) {
	maxTokens, constrained := ResolveMaxTokens("qwen2.5-coder:7b", 1200)
	if constrained {
		t.Fatalf("constrained = true, want false")
	}
	if maxTokens != 1200 {
		t.Fatalf("maxTokens = %d, want 1200", maxTokens)
	}
}

func TestResolveMaxTokens_RequestedBelowCeilingPreserved(t *testing.T) {
	maxTokens, constrained := ResolveMaxTokens("cohere/north-mini-code:free", 200)
	if !constrained {
		t.Fatalf("constrained = false, want true")
	}
	if maxTokens != 200 {
		t.Fatalf("maxTokens = %d, want 200 (below ceiling)", maxTokens)
	}
}

func TestResolveMaxTokens_NonPositiveRequestedFallsBackToDefault(t *testing.T) {
	maxTokens, constrained := ResolveMaxTokens("qwen2.5-coder:7b", 0)
	if constrained {
		t.Fatalf("constrained = true, want false")
	}
	if maxTokens != DefaultAskRequestedTokens {
		t.Fatalf("maxTokens = %d, want default %d", maxTokens, DefaultAskRequestedTokens)
	}
}

func TestSplitModelVendor(t *testing.T) {
	if vendor, model := SplitModelVendor("openai/gpt-4o"); vendor != "openai" || model != "gpt-4o" {
		t.Fatalf("openai/gpt-4o: vendor=%q model=%q", vendor, model)
	}
	if vendor, model := SplitModelVendor("qwen2.5-coder:7b"); vendor != "" || model != "qwen2.5-coder:7b" {
		t.Fatalf("bare: vendor=%q model=%q", vendor, model)
	}
}

func TestStepStateLifecycle(t *testing.T) {
	s := NewStepState("cohere/north-mini-code:free", true, 980, DefaultMaxContinuationSteps)
	if s.Ordinal() != 1 {
		t.Fatalf("Ordinal = %d, want 1", s.Ordinal())
	}
	if !s.CanContinue() || s.ContinuationsLeft() != DefaultMaxContinuationSteps {
		t.Fatalf("initial CanContinue/ContinuationsLeft mismatch: left=%d", s.ContinuationsLeft())
	}
	for i := 0; i < DefaultMaxContinuationSteps; i++ {
		s.Advance()
	}
	if s.Ordinal() != DefaultMaxContinuationSteps+1 {
		t.Fatalf("Ordinal = %d after all advances, want %d", s.Ordinal(), DefaultMaxContinuationSteps+1)
	}
	if s.CanContinue() {
		t.Fatalf("CanContinue = true after request budget consumed")
	}
	if s.ContinuationsLeft() != 0 {
		t.Fatalf("ContinuationsLeft = %d, want 0", s.ContinuationsLeft())
	}
}

func TestStepStateDefaultMaxSteps(t *testing.T) {
	s := NewStepState("m", false, 100, 0)
	if s.maxSteps != DefaultMaxContinuationSteps {
		t.Fatalf("maxSteps = %d, want default %d", s.maxSteps, DefaultMaxContinuationSteps)
	}
}

func TestContinuationUserTurn_NeverReplaysTranscript(t *testing.T) {
	turn := ContinuationUserTurn("base request text", []string{"delivered A", "delivered B"}, []string{"pending X"}, "markdown", 500)
	for _, want := range []string{"base request text", "delivered A", "delivered B", "pending X", "markdown", "500"} {
		if !strings.Contains(turn, want) {
			t.Fatalf("turn missing %q\n%s", want, turn)
		}
	}
	if strings.Count(turn, "OUTPUT BUDGET EXHAUSTED") != 1 {
		t.Fatalf("continuation instruction block duplicated:\n%s", turn)
	}
	again := ContinuationUserTurn("base request text", []string{"delivered A"}, []string{"pending Y"}, "", 300)
	if strings.Count(again, "OUTPUT BUDGET EXHAUSTED") != 1 {
		t.Fatalf("rebuild from bare base produced duplicated instruction block:\n%s", again)
	}
}

func TestIsOutputExhausted(t *testing.T) {
	if !IsOutputExhausted(&OutputExhaustedError{Step: 2, Hint: "x"}) {
		t.Fatalf("OutputExhaustedError not detected")
	}
	if !IsOutputExhausted(fmt.Errorf("wrap: %w", &OutputExhaustedError{Step: 1})) {
		t.Fatalf("wrapped OutputExhaustedError not detected")
	}
	if !IsOutputExhausted(ai.ErrPayloadTruncated) {
		t.Fatalf("ai.ErrPayloadTruncated not detected")
	}
	if !IsOutputExhausted(fmt.Errorf("wrap: %w", ai.ErrPayloadTruncated)) {
		t.Fatalf("wrapped ai.ErrPayloadTruncated not detected")
	}
	if IsOutputExhausted(errors.New("generic failure")) {
		t.Fatalf("generic error misclassified as exhausted")
	}
	if IsOutputExhausted(nil) {
		t.Fatalf("nil misclassified as exhausted")
	}
}

func TestCommittedSummaryBounds(t *testing.T) {
	s := NewStepState("m", false, 100, 1)
	s.RecordDelivered("one")
	s.RecordDelivered(strings.Repeat("x", 500))
	sum := s.CommittedSummary(80, 200)
	if len(sum) > 200+1 {
		t.Fatalf("summary too long: %d chars", len(sum))
	}
	if !strings.HasSuffix(sum, "…") {
		t.Fatalf("long committed entry not ellipsized: %q", sum)
	}
	if got := NewStepState("m", false, 100, 1).CommittedSummary(80, 200); got != "(none)" {
		t.Fatalf("empty committed summary = %q, want (none)", got)
	}
}
