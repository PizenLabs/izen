// Human presentation layers: the UI never renders runtime internals by
// default. The presentation layer computes an ExecutionFrame per Visibility —
// NORMAL (human narrative only), EXPANDED (+ runtime metadata), DEBUG (+ full
// event stream) — and the renderer formats whatever the frame carries. The
// renderer never interprets: it is visual output only.
package presentation

import "time"

// Visibility is the human-facing presentation layer of an execution.
type Visibility uint8

const (
	// VisibilityNormal is the default layer: the human narrative only
	// (current action + completed milestones). Providers, strategies, token
	// counts, and raw event names are never surfaced here.
	VisibilityNormal Visibility = iota
	// VisibilityExpanded adds execution details (strategy, context policy,
	// model, token usage, duration, artifacts). The renderer formats the
	// metadata; the projection decides what belongs here.
	VisibilityExpanded
	// VisibilityDebug surfaces the full runtime event stream (every canonical
	// machine event in order).
	VisibilityDebug
)

// Valid reports whether the visibility is a known presentation layer.
func (v Visibility) Valid() bool {
	return v >= VisibilityNormal && v <= VisibilityDebug
}

// String renders the canonical visibility name.
func (v Visibility) String() string {
	switch v {
	case VisibilityExpanded:
		return "expanded"
	case VisibilityDebug:
		return "debug"
	default:
		return "normal"
	}
}

// NarrativeStep is one deterministic human narrative milestone of an
// execution. Current is true for the live in-flight step (running/waiting),
// false for completed milestones.
type NarrativeStep struct {
	// Transition is the canonical ExecutionGraph transition this step derives
	// from (e.g. "strategy.selected").
	Transition string
	// Sentence is the derived human sentence.
	Sentence string
	// Current marks the live step.
	Current bool
}

// ExecutionDetails is the runtime metadata the EXPANDED and DEBUG layers
// expose. It is a pure accumulation of the observed event payloads — never a
// UI-invented value.
type ExecutionDetails struct {
	// Strategy is the selected execution strategy.
	Strategy string
	// ContextChannels are the context-policy channels compiled before the
	// model invocation.
	ContextChannels []string
	// ContextTokens is the compiled context token count.
	ContextTokens int
	// Model is the resolved provider model.
	Model string
	// TokenInput / TokenOutput are the authoritative provider-reported usage.
	TokenInput  int
	TokenOutput int
	// ReasoningTokens is the provider-reported reasoning token count (0 when
	// the provider reported none).
	ReasoningTokens int
	// ReasoningDuration is the measured wall-clock reasoning window (0 when no
	// reasoning was observed).
	ReasoningDuration time.Duration
	// ProviderState is the truthful live provider phase of the model stage:
	// "" (not yet invoked), "waiting" (round-trip in flight), "streaming"
	// (provider bytes arriving), or "done". It is derived ONLY from the
	// canonical provider events — never inferred by the renderer.
	ProviderState string
	// StartedAt / FinishedAt bound the execution window.
	StartedAt  time.Time
	FinishedAt time.Time
	// Artifacts lists the semantically-typed artifacts produced.
	Artifacts []ArtifactView
}

// Duration returns the wall-clock execution window (0 when unstarted).
func (d ExecutionDetails) Duration() time.Duration {
	if d.StartedAt.IsZero() {
		return 0
	}
	end := d.FinishedAt
	if end.IsZero() {
		end = d.StartedAt
	}
	return end.Sub(d.StartedAt)
}

// Empty reports whether no runtime metadata was observed yet.
func (d ExecutionDetails) Empty() bool {
	return d.Strategy == "" && d.Model == "" && len(d.ContextChannels) == 0 && len(d.Artifacts) == 0 &&
		d.ProviderState == "" && d.ReasoningDuration == 0
}

// ExecutionFrame is the renderer-ready, visibility-scoped presentation of one
// execution. It is a pure function of ExecutionViewState + ExecutionNarrative:
// the presentation layer decides what belongs in each layer, the renderer
// formats it.
type ExecutionFrame struct {
	// Visibility is the layer this frame was computed for.
	Visibility Visibility
	// State is the canonical execution view state.
	State ExecutionViewState
	// Steps are the human narrative milestones (NORMAL + EXPANDED).
	Steps []NarrativeStep
	// Details is the runtime metadata (EXPANDED + DEBUG).
	Details ExecutionDetails
	// Events is the full machine event stream (DEBUG).
	Events []string
}

// Terminal reports whether the framed execution reached a terminal phase.
func (f ExecutionFrame) Terminal() bool {
	return f.State.Phase.Terminal()
}
