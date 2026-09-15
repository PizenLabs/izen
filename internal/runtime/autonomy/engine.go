package autonomy

import (
	"strings"

	baseautonomy "github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/core/domain"
)

// Autonomy Intent Masking Invariant: if UserIntent == ask, the AutonomyEngine
// MUST NOT evaluate risk as modification or request CapMutate / CapPropose
// capabilities. Autonomy decisions MUST resolve to DecisionAskReadOnly or
// PassThrough.
//
// This file is the explicit ask-ceiling enforcement surface for the bounded
// autonomous runtime. The Intent Classifier masking (HasExecutionMarker /
// ClassifyIntent in the application layer) runs BEFORE any Evaluate call;
// Evaluate re-enforces the ceiling defensively so a direct call with an ask
// context can never escalate via prompt semantics ("rewrite", "fix", ...).

// Decision is the autonomy evaluation verdict.
type Decision string

const (
	// DecisionAskReadOnly resolves an ask-intent objective to the read-only
	// ask pipeline: zero mutation capabilities, phase stays ask.
	DecisionAskReadOnly Decision = "ask_read_only"
	// DecisionPassThrough resolves an ask-intent objective with no capability
	// request at all (pure pass-through to read-only chat).
	DecisionPassThrough Decision = "pass_through"
	// DecisionBuild routes an explicitly authorized execution objective to
	// the build phase (only reachable with a non-ask intent).
	DecisionBuild Decision = "build"
)

// EvaluationContext carries the masked intent and the raw prompt text.
// Intent is the AUTHORITATIVE classifier verdict (domain IntentKind); Prompt
// is untrusted text that MUST NOT elevate the verdict.
type EvaluationContext struct {
	intent domain.IntentKind
	prompt string
	phase  string
}

// NewEvaluationContext builds the evaluation input. Phase defaults to "ask".
func NewEvaluationContext(intent domain.IntentKind, prompt string) EvaluationContext {
	return EvaluationContext{intent: intent, prompt: prompt, phase: "ask"}
}

// Intent returns the authoritative masked intent.
func (c EvaluationContext) Intent() domain.IntentKind { return c.intent }

// Prompt returns the raw (untrusted) prompt text.
func (c EvaluationContext) Prompt() string { return c.prompt }

// Phase returns the current phase ("ask" unless explicitly routed).
func (c EvaluationContext) Phase() string {
	if c.phase == "" {
		return "ask"
	}
	return c.phase
}

// EvaluationResult is the verdict of one Evaluate call.
type EvaluationResult struct {
	Decision     Decision
	Capabilities []string
	Phase        string
	Risk         string
}

// RequestsMutation reports whether the result requests any mutation
// capability (mutate/propose equivalents).
func (r EvaluationResult) RequestsMutation() bool {
	for _, c := range r.Capabilities {
		lower := strings.ToLower(strings.TrimSpace(c))
		switch lower {
		case "mutate", "capmutate", "cap_mutate", "propose", "cappropose", "cap_propose", "write", "patch":
			return true
		}
	}
	return false
}

// AutonomyEngine is the ask-ceiling-guarded evaluation surface. It never
// derives authority from prompt semantics: the masked Intent owns the
// verdict.
type AutonomyEngine struct{}

// NewAutonomyEngine returns the guarded engine.
func NewAutonomyEngine() *AutonomyEngine { return &AutonomyEngine{} }

// Evaluate resolves the decision for ctx. When ctx.Intent() == IntentAsk the
// engine short-circuits BEFORE any risk evaluation or capability resolution:
// risk is never "modification", zero mutation capabilities are requested, and
// the phase stays "ask" — regardless of prompt words like "rewrite file".
func (a *AutonomyEngine) Evaluate(ctx EvaluationContext) EvaluationResult {
	if ctx.Intent() == domain.IntentAsk {
		return EvaluationResult{
			Decision:     DecisionAskReadOnly,
			Capabilities: nil,
			Phase:        "ask",
			Risk:         "read_only",
		}
	}
	// Non-ask intents fall through to the capability-driven runtime. A prompt
	// without an explicit execution marker still cannot escalate here: the
	// caller MUST have applied Intent Classifier masking before Evaluate.
	// Defensive default for unmasked non-ask calls without a marker is to
	// stay read-only rather than escalate.
	if !HasExecutionMarker(ctx.Prompt()) {
		return EvaluationResult{
			Decision:     DecisionPassThrough,
			Capabilities: nil,
			Phase:        "ask",
			Risk:         "read_only",
		}
	}
	return EvaluationResult{
		Decision:     DecisionBuild,
		Capabilities: []string{"read", "analyze", "propose", "mutate"},
		Phase:        "build",
		Risk:         "bounded",
	}
}

// HasExecutionMarker reports whether raw input carries an explicit execution
// trigger ("$prompt"/"$hot" prefix or "/build" command, case-insensitive).
// Bare plain-text strictly routes to the read-only ask pipeline.
//
// Authority Static Invariant: CapMutate eligibility is governed EXCLUSIVELY
// by this Control Plane input parsing. Provider capabilities MUST NOT
// influence authorization; LLM prompt directives MUST NOT elevate authority.
func HasExecutionMarker(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return false
	}
	lower := strings.ToLower(trimmed)
	for _, marker := range []string{"$prompt", "$hot", "/build"} {
		if !strings.HasPrefix(lower, marker) {
			continue
		}
		if len(lower) == len(marker) {
			return true
		}
		c := lower[len(marker)]
		if c == ' ' || c == '\t' || c == '\n' {
			return true
		}
	}
	return false
}

// CapabilitiesForAsk returns the zero-mutation capability set for ask intent.
// It is the executable form of the masking invariant: ask NEVER carries
// CapMutate / CapPropose, only read-only capabilities.
func CapabilitiesForAsk() baseautonomy.CapabilitySet {
	return baseautonomy.CapabilitySet{baseautonomy.CapRead, baseautonomy.CapAnalyze}
}
