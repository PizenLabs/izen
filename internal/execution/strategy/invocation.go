package strategy

import (
	"fmt"
	"strings"
)

// InvocationContract is the explicit contract for a single provider call. It
// answers, before the call, every question the invocation must answer: why the
// model is being called, the exact decision it must make, what context it
// receives and what is intentionally excluded, what artifact it must return,
// the maximum output, the reasoning budget, what constitutes success and
// invalid output, and the deterministic fallback. Retries are bound by this
// contract: a malformed artifact may justify a corrected retry; a wrong
// contract never does.
type InvocationContract struct {
	// Number is the ordinal invocation for this operation (1-based).
	Number int
	// Reason is why the model is being called.
	Reason string
	// Decision is the exact decision the model must make.
	Decision string
	// Input describes what context the model receives.
	Input string
	// Excluded lists what is intentionally NOT provided.
	Excluded []string
	// Artifact is the artifact the model must return.
	Artifact ArtifactContract
	// MaxOutput is the maximum expected artifact size in tokens.
	MaxOutput int
	// ReasoningBudget is the thinking budget justified by complexity.
	ReasoningBudget int
	// Success is the deterministic success criterion.
	Success string
	// InvalidOutput describes what output is rejected.
	InvalidOutput string
	// DeterministicFallback is what the engine does when the model fails.
	DeterministicFallback string
}

// For builds the invocation contract for the operation described by the
// profile. number is the 1-based invocation ordinal. When the profile requires
// no model, a zero contract is returned (callers must not invoke).
func For(p ExecutionStrategyProfile, number int) InvocationContract {
	if !p.ModelRequired {
		return InvocationContract{}
	}
	c := InvocationContract{
		Number:                number,
		Reason:                p.StrategyReason,
		Decision:              p.ModelDecision,
		Input:                 inputFor(p),
		Excluded:              excludedFor(p),
		Artifact:              p.Artifact,
		MaxOutput:             p.MaxOutputTokens,
		ReasoningBudget:       p.ReasoningBudget,
		Success:               successFor(p),
		InvalidOutput:         invalidFor(p),
		DeterministicFallback: fallbackFor(p),
	}
	if c.Reason == "" {
		c.Reason = "model reasoning required by execution strategy " + p.Strategy.String()
	}
	return c
}

// String renders the contract compactly for $inspect.
func (c InvocationContract) String() string {
	if c.Number == 0 {
		return "invocation: none (no model required)"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "invocation#%d: %s decision=%q", c.Number, c.Reason, c.Decision)
	if c.Input != "" {
		b.WriteString(" input=" + c.Input)
	}
	if len(c.Excluded) > 0 {
		b.WriteString(" excluded=" + strings.Join(c.Excluded, ","))
	}
	fmt.Fprintf(&b, " artifact=%s max_output=%d reasoning=%d", c.Artifact.Kind, c.MaxOutput, c.ReasoningBudget)
	if c.Success != "" {
		b.WriteString(" success=" + c.Success)
	}
	return b.String()
}

// inputFor names the context the model receives for the strategy.
func inputFor(p ExecutionStrategyProfile) string {
	switch p.Strategy {
	case TargetedMutation:
		return "explicit target content only"
	case TargetedReasoning:
		return "explicit target content only"
	case RepositoryInvestigation:
		return "repository evidence discovered by the engine"
	case MultiFilePlanning:
		return "governed repository evidence selected by the engine"
	case DirectDeterministic:
		return "none (deterministic)"
	case HumanClarification:
		return "none (no model invocation)"
	default:
		return "governed evidence selected by the engine"
	}
}

// excludedFor lists the context intentionally withheld from the call.
func excludedFor(p ExecutionStrategyProfile) []string {
	switch p.Strategy {
	case TargetedMutation, TargetedReasoning:
		return []string{
			"repository-wide scan",
			"unrelated file content",
			"conversation history",
			"tool schemas",
		}
	case RepositoryInvestigation, MultiFilePlanning:
		return []string{"conversation history", "unrelated file content"}
	default:
		return nil
	}
}

// successFor names the deterministic success criterion.
func successFor(p ExecutionStrategyProfile) string {
	switch p.Strategy {
	case TargetedMutation, TargetedReasoning:
		return "artifact parses and applies cleanly to the target"
	case RepositoryInvestigation:
		return "evidence set is complete enough to conclude"
	case MultiFilePlanning:
		return "plan tasks validate against the workspace"
	default:
		return "artifact satisfies the contract"
	}
}

// invalidFor names the output the engine rejects.
func invalidFor(p ExecutionStrategyProfile) string {
	switch p.Strategy {
	case TargetedMutation:
		return "full-file rewrite of an existing real-content file; unanchored prose"
	case RepositoryInvestigation:
		return "fabricated evidence or guessed file paths"
	case MultiFilePlanning:
		return "non-JSON plan; invented tasks without workspace evidence"
	default:
		return "non-parseable artifact"
	}
}

// fallbackFor names the deterministic fallback when the model fails.
func fallbackFor(p ExecutionStrategyProfile) string {
	switch p.Strategy {
	case TargetedMutation:
		return "engine-side fuzzy patch repair; then human approval surface"
	case RepositoryInvestigation:
		return "evidence re-discovery; bounded provider retry"
	case MultiFilePlanning:
		return "deterministic plan fast-tracks; bounded provider retry"
	default:
		return "bounded provider retry, then human clarification"
	}
}
