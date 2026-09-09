// Package compactor implements the context-window compaction engine: it
// monitors token usage and reclaims budget in three deterministic stages
// (tool-log pruning, history summarization, hard-crop fallback) without
// losing the recent conversational state.
package compactor

import "time"

// CompactionStrategy names the stage that reclaimed budget on a run.
type CompactionStrategy string

const (
	// StrategyToolPrune truncated historical tool/stdout outputs.
	StrategyToolPrune CompactionStrategy = "TOOL_PRUNE"
	// StrategySummarize folded old turns into a compacted summary node.
	StrategySummarize CompactionStrategy = "LLM_SUMMARIZE"
	// StrategyHardCrop applied deterministic tail-cropping under saturation.
	StrategyHardCrop CompactionStrategy = "HARD_CROP"
	// StrategyNone reports that no compaction work was needed.
	StrategyNone CompactionStrategy = "NONE"
)

// Defaults for the compaction pipeline.
const (
	// DefaultMaxTokens is the default context window size.
	DefaultMaxTokens = 200000
	// DefaultThresholdRatio triggers compaction at 80% of the window.
	DefaultThresholdRatio = 0.80
	// DefaultHardCropRatio is the saturation guard: above 95% even the
	// summarization call may not fit, so hard-crop first.
	DefaultHardCropRatio = 0.95
	// RecentWindow is the uncompacted retention window: the last N turns are
	// always preserved byte-identical.
	RecentWindow = 3
)

// TokenBudget is the token accounting breakdown estimated locally before any
// provider call.
type TokenBudget struct {
	MaxTokens          int
	CurrentTokens      int
	SystemPromptTokens int
	RecentTurnTokens   int
	SummaryTokens      int
	ToolOutputTokens   int
	ThresholdRatio     float64 // Default: 0.80 (80%)
}

// Threshold returns the effective ratio (falls back to the default when unset).
func (b TokenBudget) Threshold() float64 {
	if b.ThresholdRatio <= 0 {
		return DefaultThresholdRatio
	}
	return b.ThresholdRatio
}

// Limit returns the token count at which compaction triggers.
func (b TokenBudget) Limit() int {
	return int(float64(b.MaxTokens) * b.Threshold())
}

// HardLimit returns the saturation guard above which hard-crop applies.
func (b TokenBudget) HardLimit() int {
	return int(float64(b.MaxTokens) * DefaultHardCropRatio)
}

// NeedsCompaction reports whether the budget is at or above the threshold.
func (b TokenBudget) NeedsCompaction() bool {
	if b.MaxTokens <= 0 {
		return false
	}
	return b.CurrentTokens >= b.Limit()
}

// Saturated reports whether the budget is above the hard-crop guard.
func (b TokenBudget) Saturated() bool {
	if b.MaxTokens <= 0 {
		return false
	}
	return b.CurrentTokens >= b.HardLimit()
}

// UsageRatio returns Current/Max in [0, +inf).
func (b TokenBudget) UsageRatio() float64 {
	if b.MaxTokens <= 0 {
		return 0
	}
	return float64(b.CurrentTokens) / float64(b.MaxTokens)
}

// CompactionResult is the terminal record of one Compact run.
type CompactionResult struct {
	StrategyUsed     CompactionStrategy
	TokensBefore     int
	TokensAfter      int
	FreedTokens      int
	Duration         time.Duration
	UncompactedTurns int
}

// Turn is one conversation turn in the compactable window.
type Turn struct {
	Role         string // "user", "assistant", "system", "tool"
	Content      string
	IsToolOutput bool // marks ToolChunk/stdout telemetry eligible for pruning
}

// IsTool reports whether the turn carries tool output.
func (t Turn) IsTool() bool {
	return t.IsToolOutput || t.Role == "tool"
}

// Conversation is the mutable compactable state. Turns holds the live window
// (oldest first); Summary holds the folded digest of compacted turns;
// SystemPrompt and ToolDefs are pinned and never compacted.
type Conversation struct {
	SystemPrompt string
	ToolDefs     string
	Summary      string
	Turns        []Turn
}

// Clone returns a deep copy of the conversation.
func (c *Conversation) Clone() *Conversation {
	if c == nil {
		return nil
	}
	out := &Conversation{
		SystemPrompt: c.SystemPrompt,
		ToolDefs:     c.ToolDefs,
		Summary:      c.Summary,
		Turns:        make([]Turn, len(c.Turns)),
	}
	copy(out.Turns, c.Turns)
	return out
}

// ForceOptions forces a compaction run regardless of the token ratio.
type ForceOptions struct {
	Force bool
}
