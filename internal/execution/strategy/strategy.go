// Package strategy implements Izen's engine-first execution-strategy layer.
//
// The layer owns one decision and one decision only: BEFORE any model is
// invoked, the engine classifies the requested operation into a small, stable
// set of execution strategies and records the full reasoning behind that
// choice. The model is never asked to decide what kind of execution a request
// requires, which files it targets, or whether repository discovery is needed
// — those are deterministic engine questions.
//
// The taxonomy is intentionally bounded (six strategies). It is not a generic
// agent planner: every strategy maps onto an existing execution path, so the
// runtime converges on the smallest sufficient path instead of treating
// investigate/plan/build as mandatory stages.
//
// Every decision is observable: an ExecutionStrategyProfile carries the
// strategy, the resolved targets and their resolution status, the
// execution-factor complexity, the context kinds the operation needs, the
// artifact contract, and the reasoning/output budgets the operation justifies.
// $inspect renders it so a user can answer "why did Izen call the model?",
// "why did it read this file?", and "why did it need /plan?" — without ever
// exposing private model reasoning.
package strategy

import (
	"fmt"
	"strings"
)

// ExecutionStrategy is the bounded set of engine-first execution strategies.
// It is selected deterministically before model invocation and must stay small
// and stable: adding a strategy is an architectural decision, not a patch.
type ExecutionStrategy string

const (
	// DirectDeterministic runs with ZERO model invocations: the engine can
	// resolve the mutation deterministically (e.g. a trivial template create,
	// a known-convention file operation) or the request is a hard fact the
	// engine already holds.
	DirectDeterministic ExecutionStrategy = "direct_deterministic"

	// TargetedMutation is a mutation confined to explicit, resolved target
	// file(s). It receives only the required file context and at most one
	// bounded model invocation. This is the $hot contract and the correct
	// strategy for a simple "$prompt fix X in @index.html".
	TargetedMutation ExecutionStrategy = "targeted_mutation"

	// TargetedReasoning is a read-only understanding request about an explicit
	// target ("explain the auth flow in @auth.go"). It needs target context but
	// never a mutation path, approval surface, or MutationSet.
	TargetedReasoning ExecutionStrategy = "targeted_reasoning"

	// RepositoryInvestigation is a diagnostic request ("why is the build
	// failing?") that requires repository evidence discovery before any
	// reasoning. The forensic engine owns this path.
	RepositoryInvestigation ExecutionStrategy = "repository_investigation"

	// MultiFilePlanning is a broad, multi-file or architectural request with no
	// explicit target set. It is the only strategy that legitimately expands
	// into investigate/plan/build — because repository evidence proved it
	// necessary, not because a mode existed.
	MultiFilePlanning ExecutionStrategy = "multi_file_planning"

	// DirectResponse is casual conversation / a greeting that is answered
	// directly with a single bounded read-only invocation and ZERO repository
	// context. It must never trigger workspace planning, never load repository
	// context, and never scan the workspace: "hi" is not a coding task.
	DirectResponse ExecutionStrategy = "direct_response"

	// HumanClarification stops the runtime before any model invocation: target
	// resolution is ambiguous or unresolved and the syntax clearly indicates a
	// file target. The human is the authority; no file is read into a prompt
	// and no mutation is proposed.
	HumanClarification ExecutionStrategy = "human_clarification"
)

// String returns the canonical machine-readable strategy name.
func (s ExecutionStrategy) String() string { return string(s) }

// TargetStatus is the engine-first target-resolution status. A target is never
// silently forwarded to an LLM when deterministic resolution can classify it.
type TargetStatus string

const (
	// TargetExplicit is a target the user named with @ syntax.
	TargetExplicit TargetStatus = "explicit"
	// TargetResolved is a target deterministically matched to an existing file.
	TargetResolved TargetStatus = "resolved"
	// TargetInferred is a bare-filename target matched heuristically.
	TargetInferred TargetStatus = "inferred"
	// TargetUnresolved is a target whose syntax clearly names a file but no
	// existing file matches deterministically.
	TargetUnresolved TargetStatus = "unresolved"
	// TargetAmbiguous is a target that resolves to more than one candidate.
	TargetAmbiguous TargetStatus = "ambiguous"
)

// Target is the resolution record of a single referenced file target. It
// answers "how did the engine resolve this target and why" — deterministically.
type Target struct {
	// Raw is the target as the user wrote it (the @ scope token or bare name).
	Raw string
	// Resolved is the workspace-relative path the engine confirmed ("" when
	// unresolvable).
	Resolved string
	// Status classifies the resolution outcome.
	Status TargetStatus
	// Exists reports whether the resolved path is present on disk.
	Exists bool
	// Source names where the target was discovered: "@scope", "bare-filename",
	// or "keyword".
	Source string
	// Explicit reports whether the target was named with explicit @ syntax by
	// the user. It is independent of the resolution outcome: a deliberately
	// created new file is explicit even though it does not yet exist.
	Explicit bool
	// Reason explains the resolution decision.
	Reason string
}

// ComplexityLevel is the coarse execution-complexity classification that
// steers context scope, reasoning budget, strategy and verification depth.
type ComplexityLevel int

const (
	// ComplexityLow is a small single-file change (low context + small budget).
	ComplexityLow ComplexityLevel = iota
	// ComplexityMedium is a moderate single- or few-file change.
	ComplexityMedium
	// ComplexityHigh is an architectural or multi-file change.
	ComplexityHigh
)

// String returns the canonical complexity label.
func (c ComplexityLevel) String() string {
	switch c {
	case ComplexityLow:
		return "low"
	case ComplexityMedium:
		return "medium"
	default:
		return "high"
	}
}

// Factor is one weighted input to the complexity estimate, with the concrete
// reason it contributed.
type Factor struct {
	Name   string
	Score  int
	Weight int
	Reason string
}

// Complexity is the execution-factor complexity estimate. It is NOT a keyword
// classification: every factor is a measurable execution property (target
// count, file count, dependency count, ambiguity, scope, artifact size,
// verification depth, cross-file coupling).
type Complexity struct {
	Level   ComplexityLevel
	Score   int
	Factors []Factor
}

// ArtifactContract is the exact artifact the model must return for the chosen
// strategy. It bounds the output budget: a SEARCH/REPLACE block never needs the
// budget of a multi-file plan, and a real-content file is never forced to
// re-emit its full contents.
type ArtifactContract struct {
	// Kind is one of: create_file, replace_block, replace_file, plan,
	// investigation, explanation.
	Kind string
	// Bounded reports whether the artifact is anchored to a located block
	// rather than a full-file rewrite.
	Bounded bool
	// MaxLines bounds the expected artifact size in lines (0 = unbounded).
	MaxLines int
	// Description names the artifact for humans and $inspect.
	Description string
}

// ContextKind is one of the context channels the operation requires. The
// context compiler uses it to decide what crosses to the model and what is
// intentionally excluded.
type ContextKind string

const (
	// ContextUserIntent is the user's goal, normalized.
	ContextUserIntent ContextKind = "user_intent"
	// ContextExplicitTargets is the resolved target set.
	ContextExplicitTargets ContextKind = "explicit_targets"
	// ContextTargetContent is the required target-file content.
	ContextTargetContent ContextKind = "target_content"
	// ContextStructuralEvidence is the located defect / target block.
	ContextStructuralEvidence ContextKind = "structural_evidence"
	// ContextDependencyEvidence is cross-file dependency evidence.
	ContextDependencyEvidence ContextKind = "dependency_evidence"
	// ContextRelevantHistory is prior conversation directly relevant to the
	// decision (never included merely because it exists).
	ContextRelevantHistory ContextKind = "relevant_history"
	// ContextPriorExecution is evidence from a previous execution ($fix
	// continuity: failure output, test logs).
	ContextPriorExecution ContextKind = "prior_execution_evidence"
	// ContextRepositoryConstraints is absolute workspace knowledge (stack,
	// capability, policy).
	ContextRepositoryConstraints ContextKind = "repository_constraints"
	// ContextArtifactContract is the artifact the model must return.
	ContextArtifactContract ContextKind = "artifact_contract"
	// ContextVerificationContract is the deterministic verification gate.
	ContextVerificationContract ContextKind = "verification_contract"
)

// contextLabels renders the context kinds a strategy requires as a compact,
// stable, order-insensitive list for $inspect.
var contextLabels = map[ContextKind]string{
	ContextUserIntent:            "intent",
	ContextExplicitTargets:       "targets",
	ContextTargetContent:         "content",
	ContextStructuralEvidence:    "structural",
	ContextDependencyEvidence:    "deps",
	ContextRelevantHistory:       "history",
	ContextPriorExecution:        "prior",
	ContextRepositoryConstraints: "constraints",
	ContextArtifactContract:      "artifact",
	ContextVerificationContract:  "verify",
}

// Label returns the compact label for a context kind.
func (k ContextKind) Label() string {
	if l, ok := contextLabels[k]; ok {
		return l
	}
	return string(k)
}

// ContextPolicy is the STRATEGY-OWNED context contract. The strategy decides
// the minimum sufficient context — never a generic compiler. It answers
// "what may the runtime read before any model invocation?":
//
//	none             — zero context: no workspace scan, no repository context,
//	                   no file channels (casual chat / direct response).
//	target_file_only — exactly the resolved target file(s) and their content,
//	                   nothing else (targeted mutation / reasoning).
//	repository       — repository evidence: symbol graph, relevant files and
//	                   dependency context (investigation / multi-file planning).
type ContextPolicy string

const (
	// ContextPolicyNone is the zero-context policy. It forbids any workspace
	// scan, any repository context and any file channel.
	ContextPolicyNone ContextPolicy = "none"
	// ContextPolicyTargetFileOnly confines context to the resolved target
	// file(s) and their content.
	ContextPolicyTargetFileOnly ContextPolicy = "target_file_only"
	// ContextPolicyRepository admits repository evidence (symbol graph,
	// relevant files, dependency context) before reasoning.
	ContextPolicyRepository ContextPolicy = "repository"
)

// String returns the canonical context-policy name.
func (p ContextPolicy) String() string { return string(p) }

// ExecutionStrategyProfile is the observable, immutable record of the
// engine-first decision for one operation. It is produced deterministically
// BEFORE any model invocation and answers every "why" question about the
// execution path without exposing model reasoning.
type ExecutionStrategyProfile struct {
	// Intent is the normalized user goal the engine will execute.
	Intent string
	// Strategy is the selected execution strategy.
	Strategy ExecutionStrategy
	// StrategyReason explains the deterministic selection.
	StrategyReason string
	// ModelRequired reports whether this strategy requires any model reasoning.
	ModelRequired bool
	// ModelDecision names the exact decision the model must make ("" when no
	// model is required). It is never a broad "figure this out" prompt.
	ModelDecision string
	// Targets is the resolved target set with per-target resolution status.
	Targets []Target
	// Complexity is the execution-factor complexity estimate.
	Complexity Complexity
	// ContextKinds is the minimum sufficient context channel set.
	ContextKinds []ContextKind
	// ContextPolicy is the strategy-owned context contract that governs what
	// the runtime may read before any model invocation. The strategy decides
	// the minimum sufficient context — the compiler never does. A zero value
	// is normalized to ContextPolicyNone for zero-context strategies.
	ContextPolicy ContextPolicy
	// Artifact is the artifact contract the model must satisfy.
	Artifact ArtifactContract
	// ReasoningBudget is the thinking budget justified by complexity (0 = none).
	ReasoningBudget int
	// MaxOutputTokens is the output budget justified by the artifact contract.
	MaxOutputTokens int
	// Deterministic reports whether the engine can resolve the operation
	// without any model invocation.
	Deterministic bool
	// Escalation reports whether context expansion / phase escalation is
	// required beyond the initial strategy.
	Escalation bool
	// EscalationReason names why escalation is required.
	EscalationReason string
}

// TargetCount returns the number of resolved targets (explicit or confirmed).
func (p ExecutionStrategyProfile) TargetCount() int {
	n := 0
	for _, t := range p.Targets {
		if t.Status == TargetExplicit || t.Status == TargetResolved || t.Status == TargetInferred {
			n++
		}
	}
	return n
}

// FileCount returns the number of distinct existing files involved.
func (p ExecutionStrategyProfile) FileCount() int {
	n := 0
	for _, t := range p.Targets {
		if t.Exists {
			n++
		}
	}
	return n
}

// HasContext reports whether the profile requires the given context kind.
func (p ExecutionStrategyProfile) HasContext(k ContextKind) bool {
	for _, c := range p.ContextKinds {
		if c == k {
			return true
		}
	}
	return false
}

// Policy returns the effective context policy. The zero value normalizes to
// ContextPolicyNone so a strategy that omits the policy is never accidentally
// granted repository context.
func (p ExecutionStrategyProfile) Policy() ContextPolicy {
	if p.ContextPolicy == "" {
		return ContextPolicyNone
	}
	return p.ContextPolicy
}

// HasUnresolvedTarget reports whether any target could not be resolved to an
// existing file — the trigger for HumanClarification.
func (p ExecutionStrategyProfile) HasUnresolvedTarget() bool {
	for _, t := range p.Targets {
		if t.Status == TargetUnresolved || t.Status == TargetAmbiguous {
			return true
		}
	}
	return false
}

// String renders the profile as a compact, single-purpose inspectable record
// (the same shape $inspect projects).
func (p ExecutionStrategyProfile) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "strategy=%s", p.Strategy)
	if p.StrategyReason != "" {
		b.WriteString(" reason=" + p.StrategyReason)
	}
	fmt.Fprintf(&b, " complexity=%s", p.Complexity.Level)
	if len(p.Complexity.Factors) > 0 {
		b.WriteString(" score=" + fmt.Sprint(p.Complexity.Score))
	}
	if p.ModelRequired {
		fmt.Fprintf(&b, " model=yes decision=%q", p.ModelDecision)
	} else {
		b.WriteString(" model=no")
	}
	if p.Deterministic {
		b.WriteString(" deterministic=yes")
	}
	if p.Artifact.Kind != "" {
		b.WriteString(" artifact=" + p.Artifact.Kind)
	}
	if p.MaxOutputTokens > 0 {
		fmt.Fprintf(&b, " output=%d", p.MaxOutputTokens)
	}
	if p.ReasoningBudget > 0 {
		fmt.Fprintf(&b, " reasoning=%d", p.ReasoningBudget)
	}
	if len(p.ContextKinds) > 0 {
		b.WriteString(" context=" + contextKindsString(p.ContextKinds))
	}
	if p.Escalation {
		b.WriteString(" escalated=" + p.EscalationReason)
	}
	return b.String()
}

// contextKindsString renders the required context kinds in a stable label list.
func contextKindsString(kinds []ContextKind) string {
	labels := make([]string, 0, len(kinds))
	for _, k := range kinds {
		labels = append(labels, k.Label())
	}
	return strings.Join(labels, ",")
}
