package plan

import (
	"fmt"
	"strings"
)

// ModelTokenBudget maps model name prefixes to conservative token ceilings.
// These are deliberately conservative to prevent hallucination on small models.
var ModelTokenBudget = map[string]int{
	"qwen2.5-coder:7b": 4000,
	"qwen2.5-coder:":   6000,
	"llama3.1:":        8000,
	"codellama:":       8000,
	"deepseek-coder:":  8000,
	"phi3:":            4000,
	"phi4:":            8000,
	"gemma2:":          8000,
	"mistral:":         8000,
	"mixtral:":         16000,
	"claude-3-haiku":   8000,
	"claude-3-sonnet":  16000,
	"claude-sonnet-4":  16000,
	"gpt-4o-mini":      16000,
	"gpt-4o":           32000,
}

// DefaultTokenBudget is the fallback ceiling for unrecognised models.
const DefaultTokenBudget = 4000

// BudgetExceededError signals that the assembled context exceeds the model's
// token budget and the user must prune their workspace before retrying.
type BudgetExceededError struct {
	Model  string
	Budget int
	Actual int
	Diff   int
}

func (e *BudgetExceededError) Error() string {
	return fmt.Sprintf(
		"token budget exceeded for %s: budget=%d actual=%d (over by %d tokens)",
		e.Model, e.Budget, e.Actual, e.Diff,
	)
}

// BudgetActionHint returns a human-readable action prompt for the TUI.
func (e *BudgetExceededError) BudgetActionHint() string {
	return fmt.Sprintf(
		"context too large for %s (%d/%d tokens). Use /drop <path> to prune attached files",
		e.Model, e.Actual, e.Budget,
	)
}

// TokenBudgetForModel returns the conservative token ceiling for a model name.
// Lookup is prefix-based so "qwen2.5-coder:7b" matches "qwen2.5-coder:" first,
// then falls back to the exact prefix "qwen2.5-coder:7b".
func TokenBudgetForModel(model string) int {
	if budget, ok := ModelTokenBudget[model]; ok {
		return budget
	}
	for prefix, budget := range ModelTokenBudget {
		if strings.HasPrefix(model, prefix) {
			return budget
		}
	}
	return DefaultTokenBudget
}

// EstimateTokens provides a fast O(1) token approximation.
// Rule of thumb: ~4 chars per token for code, 3.5 for prose.
// We use 4 as a conservative divisor.
func EstimateTokens(text string) int {
	if text == "" {
		return 0
	}
	n := len(text)
	if n < 4 {
		return 1
	}
	return n / 4
}

// CheckTokenBudget verifies that a message fits within the model's token ceiling.
// Returns nil if within budget, or a descriptive error otherwise.
func CheckTokenBudget(model string, messageTokens int) *BudgetExceededError {
	budget := TokenBudgetForModel(model)
	if messageTokens > budget {
		return &BudgetExceededError{
			Model:  model,
			Budget: budget,
			Actual: messageTokens,
			Diff:   messageTokens - budget,
		}
	}
	return nil
}
