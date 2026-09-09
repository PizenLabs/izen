package compactor

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/PizenLabs/izen/internal/events"
)

// EstimateTokens is the fast local token estimator (~4 chars/token,
// rune-aware) used as middleware before any provider call.
func EstimateTokens(s string) int {
	if s == "" {
		return 0
	}
	return (len([]rune(s)) + 3) / 4
}

// Summarizer folds old turns into a single structured summary node. The
// production wiring injects a light LLM call; the default is a deterministic
// local digest so compaction never depends on spare context.
type Summarizer func(ctx context.Context, old []Turn) (string, error)

// Engine is the multi-stage context-window compaction pipeline.
type Engine struct {
	maxTokens      int
	thresholdRatio float64
	recentWindow   int
	summarizer     Summarizer
	bus            *events.Bus
}

// Option configures the Engine.
type Option func(*Engine)

// WithMaxTokens overrides the context window size.
func WithMaxTokens(n int) Option {
	return func(e *Engine) { e.maxTokens = n }
}

// WithThresholdRatio overrides the compaction trigger ratio.
func WithThresholdRatio(r float64) Option {
	return func(e *Engine) { e.thresholdRatio = r }
}

// WithRecentWindow overrides the uncompacted retention window.
func WithRecentWindow(n int) Option {
	return func(e *Engine) { e.recentWindow = n }
}

// WithSummarizer injects the history summarizer.
func WithSummarizer(s Summarizer) Option {
	return func(e *Engine) {
		if s != nil {
			e.summarizer = s
		}
	}
}

// WithEventBus wires lifecycle event emission (nil disables emission).
func WithEventBus(b *events.Bus) Option {
	return func(e *Engine) { e.bus = b }
}

// New returns an Engine with sensible defaults.
func New(opts ...Option) *Engine {
	e := &Engine{
		maxTokens:      DefaultMaxTokens,
		thresholdRatio: DefaultThresholdRatio,
		recentWindow:   RecentWindow,
		summarizer:     defaultSummarizer,
	}
	for _, o := range opts {
		o(e)
	}
	if e.maxTokens <= 0 {
		e.maxTokens = DefaultMaxTokens
	}
	if e.thresholdRatio <= 0 {
		e.thresholdRatio = DefaultThresholdRatio
	}
	if e.recentWindow <= 0 {
		e.recentWindow = RecentWindow
	}
	if e.summarizer == nil {
		e.summarizer = defaultSummarizer
	}
	return e
}

// Budget estimates the token breakdown for conv without mutating it.
func (e *Engine) Budget(conv *Conversation) TokenBudget {
	b := TokenBudget{
		MaxTokens:      e.maxTokens,
		ThresholdRatio: e.thresholdRatio,
	}
	if conv == nil {
		return b
	}
	b.SystemPromptTokens = EstimateTokens(conv.SystemPrompt) + EstimateTokens(conv.ToolDefs)
	b.SummaryTokens = EstimateTokens(conv.Summary)
	n := e.recentWindow
	var recent, older []Turn
	if len(conv.Turns) > n {
		older = conv.Turns[:len(conv.Turns)-n]
		recent = conv.Turns[len(conv.Turns)-n:]
	} else {
		recent = conv.Turns
	}
	for _, t := range recent {
		b.RecentTurnTokens += EstimateTokens(t.Role) + EstimateTokens(t.Content)
	}
	for _, t := range older {
		tok := EstimateTokens(t.Role) + EstimateTokens(t.Content)
		b.ToolOutputTokens += toolTokens(t, tok)
		b.CurrentTokens += tok
		_ = older // clarity: older contributes to current + tool split
	}
	b.CurrentTokens += b.SystemPromptTokens + b.SummaryTokens + b.RecentTurnTokens
	return b
}

func toolTokens(t Turn, tok int) int {
	if t.IsTool() {
		return tok
	}
	return 0
}

// NeedsCompaction reports whether conv is at or above the trigger threshold.
func (e *Engine) NeedsCompaction(conv *Conversation) bool {
	return e.Budget(conv).NeedsCompaction()
}

// Compact runs the multi-stage pipeline, mutating conv in place:
//
//	Phase 3 (guard): saturation above 95% → deterministic hard-crop first.
//	Phase 1: tool-log pruning on turns older than the recent window.
//	Phase 2: summarization of turns prior to the retention window.
//
// When neither the ratio trigger nor Force is set the conversation is left
// untouched and StrategyNone is reported.
func (e *Engine) Compact(ctx context.Context, conv *Conversation, opts ForceOptions) (CompactionResult, error) {
	start := time.Now()
	if conv == nil {
		return CompactionResult{}, fmt.Errorf("compactor: nil conversation")
	}
	before := e.Budget(conv)
	e.emit(events.NewCompactionStarted(before.CurrentTokens, before.MaxTokens, opts.Force))

	res := CompactionResult{
		StrategyUsed:     StrategyNone,
		TokensBefore:     before.CurrentTokens,
		TokensAfter:      before.CurrentTokens,
		UncompactedTurns: min(len(conv.Turns), e.recentWindow),
	}
	finish := func(strategy CompactionStrategy, stage string, err error) (CompactionResult, error) {
		after := e.Budget(conv)
		res.StrategyUsed = strategy
		res.TokensAfter = after.CurrentTokens
		res.FreedTokens = res.TokensBefore - res.TokensAfter
		if res.FreedTokens < 0 {
			res.FreedTokens = 0
		}
		res.Duration = time.Since(start)
		res.UncompactedTurns = min(len(conv.Turns), e.recentWindow)
		if err != nil {
			e.emit(events.NewCompactionFailed(err, stage))
			return res, err
		}
		e.emit(events.NewCompactionCompleted(string(strategy), res.TokensBefore, res.TokensAfter, res.FreedTokens, res.UncompactedTurns))
		return res, nil
	}

	triggered := opts.Force || before.NeedsCompaction()
	if !triggered {
		return finish(StrategyNone, "", nil)
	}

	// Phase 3 guard: saturate → hard-crop raw turns BEFORE any LLM call so
	// the summarization request itself can fit.
	if before.Saturated() {
		e.hardCrop(conv)
		after := e.Budget(conv)
		if !after.NeedsCompaction() {
			return finish(StrategyHardCrop, "", nil)
		}
		// Fall through to Phase 1/2 with the cropped window; the terminal
		// strategy still reports HARD_CROP when saturation forced the path.
		res.StrategyUsed = StrategyHardCrop
	}

	// Phase 1: lossless tool-log pruning outside the retention window.
	if pruned := pruneToolOutputs(conv, e.recentWindow); pruned > 0 {
		res.StrategyUsed = StrategyToolPrune
	}
	if after := e.Budget(conv); !after.NeedsCompaction() {
		strategy := res.StrategyUsed
		if strategy == StrategyNone {
			// Forced run with nothing to prune still reports cleanly.
			if opts.Force {
				strategy = StrategyToolPrune
			}
		}
		// Preserve a saturation-forced HARD_CROP label.
		if before.Saturated() && strategy != StrategyToolPrune {
			strategy = StrategyHardCrop
		}
		if strategy == StrategyNone && before.Saturated() {
			strategy = StrategyHardCrop
		}
		return finish(strategy, "", nil)
	}

	// Phase 2: lossy semantic compression of everything before the window.
	if len(conv.Turns) <= e.recentWindow {
		// Nothing eligible for summarization; Phase 1 is the final word.
		strategy := res.StrategyUsed
		if strategy == StrategyNone {
			strategy = StrategyToolPrune
		}
		return finish(strategy, "", nil)
	}
	if err := func() error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		split := len(conv.Turns) - e.recentWindow
		old := make([]Turn, split)
		copy(old, conv.Turns[:split])
		summary, err := e.summarizer(ctx, old)
		if err != nil {
			return err
		}
		conv.Summary = foldSummaries(conv.Summary, summary)
		remaining := make([]Turn, e.recentWindow)
		copy(remaining, conv.Turns[split:])
		conv.Turns = remaining
		return nil
	}(); err != nil {
		// Summarization failed → deterministic hard-crop so the caller still
		// makes progress instead of surfacing context_length_exceeded.
		e.hardCrop(conv)
		after := e.Budget(conv)
		_ = after
		return finish(StrategyHardCrop, "summarize", nil)
	}
	return finish(StrategySummarize, "", nil)
}

func (e *Engine) emit(ev events.DomainEvent) {
	if e.bus == nil || ev == nil {
		return
	}
	e.bus.Publish(ev)
}

// pruneToolOutputs truncates tool turns older than the last keepN turns to
// head/tail markers. It returns the number of turns rewritten.
func pruneToolOutputs(conv *Conversation, keepN int) int {
	if conv == nil || len(conv.Turns) == 0 {
		return 0
	}
	cutoff := len(conv.Turns) - keepN
	if cutoff < 0 {
		cutoff = 0
	}
	pruned := 0
	for i := 0; i < cutoff; i++ {
		t := &conv.Turns[i]
		if !t.IsTool() {
			continue
		}
		if isPruned(t.Content) {
			continue
		}
		t.Content = truncateToolOutput(t.Content)
		pruned++
	}
	return pruned
}

func isPruned(s string) bool {
	return strings.Contains(s, "[Output truncated:")
}

// truncateToolOutput keeps the head (5 lines) and tail (5 lines) of a tool
// dump with an omission marker. Short outputs pass through untouched.
func truncateToolOutput(s string) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= 12 {
		return s
	}
	const head, tail = 5, 5
	omitted := len(lines) - head - tail
	var b strings.Builder
	b.WriteString(strings.Join(lines[:head], "\n"))
	fmt.Fprintf(&b, "\n[Output truncated: %d lines omitted]\n", omitted)
	b.WriteString(strings.Join(lines[len(lines)-tail:], "\n"))
	return b.String()
}

// hardCrop deterministically drops the oldest turns until the window fits or
// only a single turn remains. Callers re-estimate afterwards.
func (e *Engine) hardCrop(conv *Conversation) {
	for len(conv.Turns) > 1 {
		if b := e.Budget(conv); !b.Saturated() {
			return
		}
		conv.Turns = conv.Turns[1:]
	}
	// Last resort: truncate the surviving content to the hard limit.
	if b := e.Budget(conv); b.Saturated() && len(conv.Turns) > 0 {
		allowance := b.HardLimit() - (b.SystemPromptTokens + b.SummaryTokens)
		if allowance < 0 {
			allowance = 0
		}
		budget := allowance
		for i := range conv.Turns {
			tok := EstimateTokens(conv.Turns[i].Content)
			if tok <= budget {
				budget -= tok
				continue
			}
			conv.Turns[i].Content = cropToTokens(conv.Turns[i].Content, budget)
			break
		}
	}
}

func cropToTokens(s string, budget int) string {
	if budget <= 0 {
		return "[Output truncated: content cropped under saturation guard]"
	}
	// ~4 chars/token estimator → chars allowance.
	maxChars := budget * 4
	r := []rune(s)
	if len(r) <= maxChars {
		return s
	}
	return string(r[:maxChars]) + "\n[Output truncated: content cropped under saturation guard]"
}

func foldSummaries(prev, next string) string {
	next = strings.TrimSpace(next)
	if next == "" {
		return prev
	}
	if strings.TrimSpace(prev) == "" {
		return next
	}
	return strings.TrimSpace(prev) + "\n" + next
}

// defaultSummarizer is the deterministic local digest used when no LLM is
// injected: it preserves user requests verbatim (bounded) and assistant/tool
// markers as digests, capped so the summary node stays cheap.
func defaultSummarizer(_ context.Context, old []Turn) (string, error) {
	var b strings.Builder
	b.WriteString("[Compacted summary]")
	for _, t := range old {
		content := strings.TrimSpace(t.Content)
		if content == "" {
			continue
		}
		switch t.Role {
		case "user":
			b.WriteString(" user: " + clip(content, 160))
		case "assistant":
			b.WriteString(" | assistant: " + clip(content, 80))
		case "tool", "system":
			b.WriteString(" | " + t.Role + ": " + clip(content, 80))
		default:
			b.WriteString(" | " + clip(content, 80))
		}
	}
	return b.String(), nil
}

func clip(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	flat := strings.Join(strings.Fields(string(r)), " ")
	r = []rune(flat)
	if len(r) <= n {
		return flat
	}
	return string(r[:n]) + "…"
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
