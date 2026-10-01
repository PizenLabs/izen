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

// TargetEvidence is the observed per-target execution state of one mutation
// target. Every field is an observed fact carried by a canonical runtime event —
// nothing here is computed for display.
//
// This is the ONLY source of per-file diff statistics in the presentation
// layer. DiffAdds / DiffRemoves are meaningful exclusively when DiffPresent is
// true: they are the line metrics of the unified diff the mutation boundary
// actually compiled. When no diff exists there are no statistics to render, and
// a renderer must render nothing rather than a fabricated zero.
type TargetEvidence struct {
	// Target is the mutation target path.
	Target string
	// Candidate reports that a mutation artifact exists for this target (an
	// artifact.produced record, or the execution entering the mutation boundary
	// with this target).
	Candidate bool
	// ArtifactPresent reports that the apply boundary observed a concrete
	// mutation artifact for this target.
	ArtifactPresent bool
	// Outcome is the semantic mutation outcome from mutation.completed ("" when
	// no outcome was observed for this target yet).
	Outcome string
	// DiffPresent reports whether an actual compiled diff exists for the target.
	DiffPresent bool
	// DiffAdds / DiffRemoves are the measured compiled-diff line metrics.
	DiffAdds    int
	DiffRemoves int
	// ApplyExecuted reports that the apply step ran against the filesystem.
	ApplyExecuted bool
	// FilesystemChanged reports the boundary's observed post-apply result: the
	// content actually differs from the pre-apply content.
	FilesystemChanged bool
}

// Mutated reports whether the target's boundary evidence represents a real
// filesystem change: the apply ran AND the content actually changed. This is
// the only combination that may be counted as a mutated file.
func (t TargetEvidence) Mutated() bool {
	return t.ApplyExecuted && t.FilesystemChanged
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
	// ContextTokens is the COMPILED CONTEXT token estimate: the ~4-chars/token
	// accounting of the assembled prompt the compiler actually sent. It is NOT
	// the provider's prompt-token count (that is TokenInput), NOT a workspace
	// size, and NOT an execution budget. Every surface that prints it must name
	// the layer, because the user must never have to infer what a token number
	// measures.
	ContextTokens int
	// ContextCacheHit reports that the compiled context was reused from the
	// compiler's fingerprint cache. It is a fact about the CONTEXT COMPILER's
	// cache, never about the workspace/structural cache.
	ContextCacheHit bool
	// Model is the resolved provider model.
	Model string
	// ProviderCalls counts the model invocations observed for this execution.
	ProviderCalls int
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
	// FinishReason is the normalized provider finish reason of the last
	// observed response. A MODEL INVOCATION completing is not a TASK completing:
	// "stop" and "length" describe the generation, never the objective.
	FinishReason string
	// StartedAt / FinishedAt bound the execution window.
	StartedAt  time.Time
	FinishedAt time.Time
	// Artifacts lists the semantically-typed artifacts produced.
	Artifacts []ArtifactView
	// Targets is the observed per-target ledger: candidate state, mutation
	// outcome, and the real compiled-diff metrics.
	Targets []TargetEvidence
	// MutatedFiles counts targets whose boundary evidence proves a real
	// filesystem change. It is the only "N files updated" the UI may render.
	MutatedFiles int
	// CandidateCount counts targets that have a candidate artifact.
	CandidateCount int
	// VerificationRan / VerificationPassed are the verifier gate's real state.
	// VerificationRan=false means the gate never executed — which is NOT a pass.
	VerificationRan    bool
	VerificationPassed bool
	// VerificationSteps are the executed step names the verifier reported.
	VerificationSteps []string
	// EvidenceObserved / EvidenceOutcome / EvidenceTainted mirror the sealed
	// terminal evidence record. On a mutation execution this is the ONLY
	// authority for a completion claim.
	EvidenceObserved bool
	EvidenceOutcome  string
	EvidenceTainted  bool
	// FilesMutated is the file count the sealed evidence recorded.
	FilesMutated int
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

// BytesChanged returns the total measured added+removed diff lines across the
// targets for which an actual compiled diff exists, and whether ANY such diff
// was observed. A false second result means there is no diff evidence at all,
// and a renderer must render no diff statistics whatsoever.
func (d ExecutionDetails) BytesChanged() (int, bool) {
	total, any := 0, false
	for _, t := range d.Targets {
		if !t.DiffPresent {
			continue
		}
		any = true
		total += t.DiffAdds + t.DiffRemoves
	}
	return total, any
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
