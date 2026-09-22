// Package llmstep defines the shared bounded LLM step boundary primitive.
//
// Every production LLM invocation in Izen flows through a capability-aware
// output budget. The provider's ACTUAL ceiling (a constrained/free-tier model
// cuts generation at ~980 output tokens) is the binding constraint, never a
// global constant. When the provider cuts a response off at that ceiling
// (finish_reason="length" → OUTPUT_EXHAUSTED), the step runtime does NOT re-run
// the same full-scope prompt: it preserves the durable committed state and
// schedules a smaller bounded continuation step that ADVANCES the work (a
// compact summary of what was already delivered), never replaying the full
// transcript.
//
// This is the ONE budget resolution and the ONE bounded-step lifecycle shared
// by every invocation path:
//
//   - plan synthesis (internal/modes/plan) — the original bounded-step repair,
//   - executor read-only / ASK direct_response (internal/execution),
//   - executor targeted mutation budget resolution,
//   - investigate classifier invocations.
//
// Mode-specific semantics (task salvage, adaptive granularity, artifact
// validation) stay in their owning packages; this package owns only the shared
// step boundary contract: budget resolution, step ordinal/request-budget
// accounting, advance-state continuation prompts, and the typed
// OUTPUT_EXHAUSTED condition.
package llmstep

import (
	"errors"
	"fmt"
	"strings"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/providers/capability"
)

// Requested output budgets are PER-MODE REQUESTS, never capability claims.
// Every one of them is clamped against the provider's real ceiling by
// ResolveMaxTokens before any provider request crosses the boundary. The
// values only matter when the provider advertises an unknown/unlimited
// ceiling — an unconstrained model may keep the requested budget verbatim.
const (
	// DefaultAskRequestedTokens is the requested ASK / read-only budget for
	// unconstrained models.
	DefaultAskRequestedTokens = 1536
	// DefaultMutationRequestedTokens is the requested targeted-mutation budget
	// for unconstrained models.
	DefaultMutationRequestedTokens = 1200
	// DefaultClassifyRequestedTokens is the requested budget for small
	// classifier-style invocations (investigate dispatch, intent routing).
	DefaultClassifyRequestedTokens = 256
	// DefaultMaxContinuationSteps is the request budget: the maximum number of
	// bounded continuation invocations a single step may schedule after the
	// initial OUTPUT_EXHAUSTED step. It bounds the whole sequence so no mode
	// can retry forever under a ceiling it already proved insufficient.
	DefaultMaxContinuationSteps = 3
)

// ResolveMaxTokens derives the maximal safe output budget for ANY LLM
// invocation against a specific model. A model whose ceiling is unknown or
// above the constrained threshold keeps the requested budget; a constrained or
// free-tier model is clamped to its ceiling (980) so the request never asks
// for a budget the provider must silently cut — the primary driver of
// finish_reason="length" plus blind same-scope retry. The second return
// reports whether the model was classified as constrained. This is the single
// capability-aware budget resolution: modes and the executor MUST NOT ship a
// separate model-output-budget resolver.
func ResolveMaxTokens(modelName string, requested int) (maxTokens int, constrained bool) {
	if requested <= 0 {
		requested = DefaultAskRequestedTokens
	}
	vendor, model := SplitModelVendor(modelName)
	ceiling := capability.MaxOutputTokensFor(vendor, model)
	if capability.IsFreeTierModelID(modelName) {
		if ceiling <= 0 || ceiling > capability.ConstrainedOutputThreshold {
			ceiling = capability.ConstrainedOutputThreshold
		}
		return capability.ClampMaxTokensForBudget(requested, ceiling), true
	}
	if ceiling > 0 && ceiling <= capability.ConstrainedOutputThreshold {
		return capability.ClampMaxTokensForBudget(requested, ceiling), true
	}
	return requested, false
}

// SplitModelVendor splits an OpenRouter-style "vendor/model:id" identifier
// into its vendor prefix and bare model id. A bare identifier yields vendor ""
// and the full string as the model.
func SplitModelVendor(modelID string) (vendor, model string) {
	trimmed := strings.TrimSpace(modelID)
	if i := strings.Index(trimmed, "/"); i >= 0 {
		return strings.TrimSpace(trimmed[:i]), strings.TrimSpace(trimmed[i+1:])
	}
	return "", trimmed
}

// StepState is the durable, bounded continuity state of one LLM step sequence.
// It preserves the task identity (the active model), the step ordinal, the
// request budget (max continuation steps), the capability-aware output budget,
// and the compact committed-state summaries — so each continuation step
// advances work instead of replaying it. The field names are unexported so the
// lifecycle can only be advanced through the invariant-preserving methods.
type StepState struct {
	modelName     string
	constrained   bool
	maxTokens     int
	step          int
	continuations int
	maxSteps      int
	committed     []string
}

// NewStepState opens a bounded step sequence bound to one model.
func NewStepState(modelName string, constrained bool, maxTokens, maxSteps int) *StepState {
	if maxSteps <= 0 {
		maxSteps = DefaultMaxContinuationSteps
	}
	return &StepState{
		modelName:   modelName,
		constrained: constrained,
		maxTokens:   maxTokens,
		step:        1,
		maxSteps:    maxSteps,
	}
}

// Model returns the provider model id this step sequence is bound to.
func (s *StepState) Model() string { return s.modelName }

// Constrained reports whether the bound model is capability-locked.
func (s *StepState) Constrained() bool { return s.constrained }

// MaxTokens returns the capability-aware output budget enforced per step.
func (s *StepState) MaxTokens() int { return s.maxTokens }

// Ordinal returns the 1-based bounded-step ordinal.
func (s *StepState) Ordinal() int { return s.step }

// CanContinue reports whether the request budget allows another continuation.
func (s *StepState) CanContinue() bool { return s.continuations < s.maxSteps }

// ContinuationsLeft returns the number of continuation steps still available.
func (s *StepState) ContinuationsLeft() int { return s.maxSteps - s.continuations }

// Advance records one scheduled continuation: the step ordinal increments and
// the request-budget counter consumes one unit.
func (s *StepState) Advance() {
	s.continuations++
	s.step++
}

// RecordDelivered records one compact delivered-state summary. Only validated
// atomic results may be recorded; a delivered entry is never repeated by a
// continuation prompt. (Named to avoid colliding with the authority-gated
// transaction commit selector that architectural guards treat as
// Apply/Approve-exclusive.)
func (s *StepState) RecordDelivered(summary string) {
	s.committed = append(s.committed, summary)
}

// Committed returns the committed-state summaries (never the transcript).
func (s *StepState) Committed() []string { return s.committed }

// CommittedSummary flattens the committed summaries into one bounded line.
func (s *StepState) CommittedSummary(perEntryLimit, totalLimit int) string {
	if len(s.committed) == 0 {
		return "(none)"
	}
	var b strings.Builder
	for i, c := range s.committed {
		if c == "" {
			continue
		}
		if n := len(c); n > perEntryLimit {
			c = c[:perEntryLimit] + "…"
		}
		if i > 0 {
			b.WriteString("; ")
		}
		b.WriteString(c)
	}
	out := b.String()
	if n := len(out); n > totalLimit {
		out = out[:totalLimit] + "…"
	}
	if out == "" {
		return "(none)"
	}
	return out
}

// ContinuationUserTurn rebuilds the user turn for a continuation step from the
// ORIGINAL base prompt. It never replays the transcript: it appends a compact
// instruction that the previous output was exhausted, that the committed
// summaries are already delivered (never repeat them), which topics remain
// pending, and that THIS step must stay within the re-budgeted output ceiling.
// It is rebuilt from the bare base each time so a long continuation cannot
// accumulate duplicate instruction blocks.
func ContinuationUserTurn(base string, committed []string, pending []string, responseFormat string, stepMaxTokens int) string {
	var b strings.Builder
	b.WriteString(base)
	b.WriteString("\n\n[SYSTEM: OUTPUT BUDGET EXHAUSTED — BOUNDED CONTINUATION]\n")
	b.WriteString("The previous response was cut off at the provider's output ceiling (finish_reason=length); it was NOT complete.\n")
	if len(committed) > 0 {
		b.WriteString("Already delivered — DO NOT repeat: ")
		b.WriteString(abbrevAll(committed, 80, 200))
		b.WriteString(".\n")
	}
	if len(pending) > 0 {
		b.WriteString("Still pending: ")
		b.WriteString(strings.Join(pending, ", "))
		b.WriteString(".\n")
	}
	b.WriteString("Continue ONLY with the REMAINING part of the answer.")
	if responseFormat != "" {
		b.WriteString(" Keep the response format: ")
		b.WriteString(responseFormat)
		b.WriteString(".")
	}
	if stepMaxTokens > 0 {
		fmt.Fprintf(&b, " Keep this whole response under %d output tokens.", stepMaxTokens)
	}
	return b.String()
}

func abbrevAll(items []string, per, total int) string {
	var b strings.Builder
	first := true
	for _, it := range items {
		if it == "" {
			continue
		}
		if n := len(it); n > per {
			it = it[:per] + "…"
		}
		if !first {
			b.WriteString(", ")
		}
		first = false
		b.WriteString(it)
	}
	out := b.String()
	if out == "" {
		return "(none)"
	}
	if len(out) > total {
		return out[:total] + "…"
	}
	return out
}

// OutputExhaustedError is the typed, recoverable OUTPUT_EXHAUSTED condition
// every bounded-step path uses to signal "the provider cut generation at its
// output ceiling and the continuation budget was consumed". It is distinct
// from a failed task: downstream recovery treats it as a bounded-step outcome
// and schedules a successor step that changes the contract materially.
type OutputExhaustedError struct {
	Step int
	Hint string
}

func (e *OutputExhaustedError) Error() string {
	msg := fmt.Sprintf("bounded LLM step failed: output exhausted (step %d)", e.Step)
	if e.Hint != "" {
		msg += ": " + e.Hint
	}
	return msg
}

// Unwrap satisfies the errors unwrap contract (no wrapped cause).
func (e *OutputExhaustedError) Unwrap() error { return nil }

// IsOutputExhausted reports whether err is a bounded-step OUTPUT_EXHAUSTED
// outcome — a recoverable exhaustion, distinct from a failed invocation. It
// matches the typed llmstep condition and the ai transport truncation signal
// (ErrPayloadTruncated), so callers can route the same recovery policy for
// both the executor gate error and the engine-level typed error.
func IsOutputExhausted(err error) bool {
	if err == nil {
		return false
	}
	var e *OutputExhaustedError
	if errors.As(err, &e) {
		return true
	}
	return errors.Is(err, ai.ErrPayloadTruncated)
}
