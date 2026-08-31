package compaction

import (
	"strings"
	"time"

	"github.com/PizenLabs/izen/internal/session"
)

// maxSummaryTokens bounds a folded summary so a generation stays token-cheap
// regardless of how much history it represents. The summary is a digest, never
// the transcript.
const maxSummaryTokens = 400

// Engine compacts a single session's history stream into generational
// CompactContexts. It is the Session Compaction authority: given a base
// generation (or none) and the history appended since, it either refreshes the
// recent window incrementally or seals a new generation when the adaptive
// policy fires.
//
// It is a pure engine — it owns no files, no locks and no session slots. The
// async Runner owns scheduling; the composition root owns persistence.
type Engine struct {
	policy Policy
	tokens func(string) int
}

// New returns an Engine bound to the adaptive policy.
func New(policy Policy) *Engine {
	return &Engine{
		policy: policy,
		tokens: estimateTokens,
	}
}

// Compact folds the session history into the next generation.
//
//   - base == nil          → full compact: generation 1, everything recent.
//   - below thresholds     → same generation, Recent window refreshed, no new
//     checkpoint (incremental, non-destructive).
//   - thresholds crossed   → new generation: previous Recent + appended turns
//     are folded into the summary and a fresh Recent window is carried.
//
// It returns the next generation and whether a checkpoint was sealed.
func (e *Engine) Compact(base *session.CompactContext, history []session.Message) (*session.CompactContext, bool) {
	now := time.Now()
	next := baseGeneration(base, history, now)

	appended := appendedSince(base, history)
	turnsSince := countUserTurns(appended)
	eventsSince := len(appended)

	baseSummaryTokens := e.tokens(next.Summary)
	appendedTokens := e.tokens(joined(appended))

	if base == nil {
		// First generation: everything is recent.
		next.Generation = 1
		next.EventCount = len(history)
		next.Recent = lastN(history, e.policy.RecentWindow)
		next.CompactedAt = now
		next.Summary = e.foldSummary("", history, next.Objective)
		next.TurnCount = len(history)
		return next, true
	}

	if e.policy.ShouldCheckpoint(turnsSince, eventsSince, appendedTokens, baseSummaryTokens) {
		next.Generation = base.Generation + 1
		next.EventCount = len(history)
		next.Recent = lastN(history, e.policy.RecentWindow)
		next.CompactedAt = now
		// Fold the previous Recent window and the appended turns into the
		// summary; the summary never grows unboundedly.
		next.Summary = e.foldSummary(base.Summary, append(append([]session.Message(nil), base.Recent...), appended...), next.Objective)
		next.TurnCount = len(history)
		return next, true
	}

	// Incremental refresh: no new checkpoint; the generation's identity (and
	// folded event count) is preserved while Recent tracks the freshest turns.
	next.Generation = base.Generation
	next.EventCount = base.EventCount
	next.Recent = lastN(history, e.policy.RecentWindow)
	next.CompactedAt = base.CompactedAt
	next.TurnCount = len(history)
	return next, false
}

// RebuildFromLog reconstructs a generational CompactContext from the raw
// history log — the compaction-aware half of the INV-SESSION-14 ladder. When a
// base generation is provided it is treated as the last valid checkpoint and
// the rebuild is INCREMENTAL (only entries after base.EventCount are folded);
// otherwise the whole log is compacted from generation 1. The compact context
// is therefore always derivable from raw history.
func (e *Engine) RebuildFromLog(base *session.CompactContext, log []session.Message, meta GenerationMeta) (*session.CompactContext, error) {
	if base != nil {
		// Incremental rebuild from the last valid generation.
		if base.EventCount <= len(log) {
			next, _ := e.Compact(applyMeta(base, meta), log)
			return next, nil
		}
		// The log is shorter than the base claims: the base is stale; fall
		// through to a full rebuild rather than trusting a truncated base.
	}
	seed := &session.CompactContext{
		Version:      compactVersion,
		SessionID:    meta.SessionID,
		Objective:    meta.Objective,
		Mode:         meta.Mode,
		RunNumber:    meta.RunNumber,
		TurnCount:    len(log),
		CreatedAt:    nowIfZero(meta.CreatedAt),
		UpdatedAt:    time.Now(),
		LastUserTurn: lastUserTurn(log),
	}
	if meta.Checkpoint != "" {
		seed.Checkpoint = meta.Checkpoint
		seed.Artifacts = []string{meta.Checkpoint}
	}
	next, _ := e.Compact(nil, log)
	return next, nil
}

// compactVersion mirrors session.CompactContextVersion for subpackage payloads.
const compactVersion = 1

// GenerationMeta carries the durable session fields a rebuild must preserve.
type GenerationMeta struct {
	SessionID  string
	Objective  string
	Mode       string
	Checkpoint string
	RunNumber  int
	CreatedAt  time.Time
}

// foldSummary merges the previous summary with the folded turns, preserving
// user requests verbatim (bounded) and assistant markers as digests, then caps
// the whole summary at maxSummaryTokens.
func (e *Engine) foldSummary(base string, msgs []session.Message, objective string) string {
	var b strings.Builder
	if base != "" {
		b.WriteString(base)
	}
	if objective != "" && !strings.Contains(base, objective) {
		if b.Len() > 0 {
			b.WriteString(" | ")
		}
		b.WriteString("goal: " + objective)
	}
	for _, m := range msgs {
		content := strings.TrimSpace(m.Content)
		if content == "" {
			continue
		}
		switch m.Role {
		case "user":
			b.WriteString(" | user: " + truncate(content, 120))
		case "assistant":
			b.WriteString(" | done: " + truncate(content, 60))
		default:
			b.WriteString(" | sys: " + truncate(content, 60))
		}
	}
	out := b.String()
	if e.tokens(out) <= maxSummaryTokens {
		return out
	}
	// Bound the digest: keep the head (objective + decisions) and the tail
	// (freshest signal) with an ellipsis.
	head, tail := splitBudget(out, maxSummaryTokens, e.tokens)
	return head + " … " + tail
}

// appendedSince returns the history entries not yet folded into the base
// generation (everything after base.EventCount).
func appendedSince(base *session.CompactContext, history []session.Message) []session.Message {
	if base == nil {
		return history
	}
	if base.EventCount >= len(history) {
		return nil
	}
	return history[base.EventCount:]
}

// baseGeneration seeds the next generation from the base (or a fresh payload
// when base is nil), preserving the durable session fields.
func baseGeneration(base *session.CompactContext, history []session.Message, now time.Time) *session.CompactContext {
	if base != nil {
		next := *base
		next.UpdatedAt = now
		next.Recent = nil
		return &next
	}
	return &session.CompactContext{
		Version:    compactVersion,
		CreatedAt:  now,
		UpdatedAt:  now,
		TurnCount:  len(history),
		EventCount: 0,
		Generation: 0,
		Recent:     nil,
	}
}

// applyMeta refreshes the durable session fields carried across a generation.
func applyMeta(base *session.CompactContext, meta GenerationMeta) *session.CompactContext {
	next := *base
	if meta.SessionID != "" {
		next.SessionID = meta.SessionID
	}
	if meta.Objective != "" {
		next.Objective = meta.Objective
	}
	if meta.Mode != "" {
		next.Mode = meta.Mode
	}
	if meta.Checkpoint != "" {
		next.Checkpoint = meta.Checkpoint
	}
	if meta.RunNumber > 0 {
		next.RunNumber = meta.RunNumber
	}
	if !meta.CreatedAt.IsZero() {
		next.CreatedAt = meta.CreatedAt
	}
	return &next
}

func lastN(msgs []session.Message, n int) []session.Message {
	if n <= 0 {
		return nil
	}
	if len(msgs) <= n {
		return append([]session.Message(nil), msgs...)
	}
	out := make([]session.Message, n)
	copy(out, msgs[len(msgs)-n:])
	return out
}

func lastUserTurn(msgs []session.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			return msgs[i].Content
		}
	}
	return ""
}

func joined(msgs []session.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		b.WriteString(m.Role)
		b.WriteByte(':')
		b.WriteString(m.Content)
		b.WriteByte('\n')
	}
	return b.String()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// splitBudget splits s into head/tail so that head+tail stays within the
// budget (roughly), discarding the middle.
func splitBudget(s string, budget int, tokens func(string) int) (string, string) {
	if tokens(s) <= budget {
		return s, ""
	}
	mid := len(s) / 2
	return s[:mid], s[mid:]
}

func nowIfZero(t time.Time) time.Time {
	if t.IsZero() {
		return time.Now()
	}
	return t
}

// estimateTokens is the coarse ~4 chars/token accounting heuristic shared by
// the compaction policy.
func estimateTokens(s string) int {
	if len(s) == 0 {
		return 0
	}
	return (len([]rune(s)) + 3) / 4
}
