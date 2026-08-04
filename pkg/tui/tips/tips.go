// Package tips provides a contextual tip engine for the TUI loading state. It
// selects actionable, phase-aware tips based on the current execution phase
// (Analyze, Plan, Execute, Validate) and the active strategy, and renders the
// chosen tip below the shimmer/spinner line with a tree-branch prefix
// ("└ Tip: ...").
package tips

import (
	"math/rand"
	"strings"
)

// Built-in strategy names matched by Tips. They mirror the runtime strategy
// registry so strategy-specific tips fire when the engine can report the
// strategy it picked for the current run.
const (
	// StrategyChat is the DirectChatStrategy name (single-pass, no workspace
	// scan).
	StrategyChat = "direct_chat"
	// StrategyDirect is the DirectGenerationStrategy name.
	StrategyDirect = "direct_generation"
	// StrategyIterative is the IterativeStrategy name.
	StrategyIterative = "iterative"
)

// Phase identifies one of the canonical execution phases the tip engine keys
// on. They map onto the runtime state machine's Receive -> Analyze -> Plan ->
// Execute -> Validate lifecycle.
type Phase int

const (
	// PhaseAnalyze covers intent parsing, context gathering, workspace
	// scanning and root-cause investigation.
	PhaseAnalyze Phase = iota
	// PhasePlan covers task-graph synthesis and declarative policy
	// evaluation.
	PhasePlan
	// PhaseExecute covers strategy execution and workspace mutation.
	PhaseExecute
	// PhaseValidate covers verification and review of the produced changes.
	PhaseValidate
)

// String returns the canonical phase name.
func (p Phase) String() string {
	switch p {
	case PhaseAnalyze:
		return "analyze"
	case PhasePlan:
		return "plan"
	case PhaseExecute:
		return "execute"
	case PhaseValidate:
		return "validate"
	default:
		return "analyze"
	}
}

// PhaseFromString maps a state-machine phase string onto a Phase. It accepts
// the runtime engine's canonical stage names ("analyzed", "planned",
// "policy_evaluated", "executing", "validating") as well as the TUI's shorter
// aliases and mode names. ok is false for unknown inputs (the caller may fall
// back to PhaseAnalyze).
func PhaseFromString(s string) (Phase, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "analyze", "analyzed", "ask", "investigate", "investigating", "indexing", "receive", "received":
		return PhaseAnalyze, true
	case "plan", "planned", "policy_evaluated", "policy", "synthesizing", "blueprinting":
		return PhasePlan, true
	case "execute", "executing", "build", "building", "repair", "repairing", "hotfix":
		return PhaseExecute, true
	case "validate", "validating", "verify", "verifying", "review", "reviewing":
		return PhaseValidate, true
	default:
		return PhaseAnalyze, false
	}
}

// Tip is a single contextual tip. An empty Phases or Strategies slice means
// "match anything" for that dimension.
type Tip struct {
	// Phases narrows the tip to the listed phases; empty matches any phase.
	Phases []Phase
	// Strategies narrows the tip to the listed strategy names; empty matches
	// any strategy.
	Strategies []string
	// Text is the human-readable tip body (without the "└ Tip:" prefix).
	Text string
}

// Provider holds the tip corpus and the rotation state used to avoid
// immediately repeating the previously shown tip.
type Provider struct {
	tips     []Tip
	lastPick int
}

// New returns an empty provider. Populate it with Add, or use Default for the
// curated corpus.
func New() *Provider {
	return &Provider{lastPick: -1}
}

// Add appends one or more tips to the provider's corpus.
func (p *Provider) Add(tips ...Tip) {
	p.tips = append(p.tips, tips...)
}

// TipFor returns the best matching tip for the given phase and strategy.
// Matching priority: strategy+phase exact matches, then strategy-only, then
// phase-only, then universal tips. Empty strategy means "any strategy".
// It returns "" only when the provider has no matching tip at all.
func (p *Provider) TipFor(phase Phase, strategy string) string {
	if p == nil {
		return ""
	}
	strategy = strings.ToLower(strings.TrimSpace(strategy))

	var exact, strategyOnly, phaseOnly, universal []int
	for i, t := range p.tips {
		sMatch := matchesStrategy(t, strategy)
		pMatch := matchesPhase(t, phase)
		switch {
		case sMatch && pMatch && len(t.Strategies) > 0 && len(t.Phases) > 0:
			exact = append(exact, i)
		case sMatch && len(t.Strategies) > 0 && len(t.Phases) == 0:
			strategyOnly = append(strategyOnly, i)
		case pMatch && len(t.Phases) > 0 && len(t.Strategies) == 0:
			phaseOnly = append(phaseOnly, i)
		case len(t.Phases) == 0 && len(t.Strategies) == 0:
			universal = append(universal, i)
		}
	}

	var pool []int
	switch {
	case len(exact) > 0:
		pool = exact
	case len(strategyOnly) > 0:
		pool = strategyOnly
	case len(phaseOnly) > 0:
		pool = phaseOnly
	case len(universal) > 0:
		pool = universal
	default:
		return ""
	}
	return p.tips[p.rotate(pool)].Text
}

// TipForPhaseString is TipFor with a state-machine phase string; unknown phase
// strings degrade to PhaseAnalyze.
func (p *Provider) TipForPhaseString(phase, strategy string) string {
	ph, _ := PhaseFromString(phase)
	return p.TipFor(ph, strategy)
}

// rotate picks a candidate index from pool, preferring a different tip than
// the previous pick so tips visibly rotate instead of stuttering on one line.
func (p *Provider) rotate(pool []int) int {
	if len(pool) == 1 {
		p.lastPick = pool[0]
		return pool[0]
	}
	cands := pool
	if p.lastPick >= 0 {
		filtered := make([]int, 0, len(pool))
		for _, idx := range pool {
			if idx != p.lastPick {
				filtered = append(filtered, idx)
			}
		}
		if len(filtered) > 0 {
			cands = filtered
		}
	}
	idx := cands[rand.Intn(len(cands))]
	p.lastPick = idx
	return idx
}

// matchesPhase reports whether t applies to the given phase.
func matchesPhase(t Tip, phase Phase) bool {
	if len(t.Phases) == 0 {
		return true
	}
	for _, p := range t.Phases {
		if p == phase {
			return true
		}
	}
	return false
}

// matchesStrategy reports whether t applies to the given strategy. An empty
// strategy on the tip means "any strategy"; an empty requested strategy is
// treated as a non-match so the more specific buckets win.
func matchesStrategy(t Tip, strategy string) bool {
	if len(t.Strategies) == 0 {
		return true
	}
	if strategy == "" {
		return false
	}
	for _, s := range t.Strategies {
		if strings.EqualFold(s, strategy) {
			return true
		}
	}
	return false
}

// Render formats a tip body with the canonical tree-branch prefix used below
// the shimmer/spinner loading line. It returns "" for an empty body.
func Render(text string) string {
	if text == "" {
		return ""
	}
	return "└ Tip: " + text
}

// Default returns a Provider pre-populated with the curated contextual tips
// covering every phase, the built-in strategies, and universal fallbacks.
func Default() *Provider {
	p := New()
	p.Add(
		// Phase: Analyze
		Tip{Phases: []Phase{PhaseAnalyze}, Text: "Use /investigate to trace code paths and collect diagnostics before fixing bugs."},
		Tip{Phases: []Phase{PhaseAnalyze}, Text: "Use @file in your prompt to attach specific files for context."},
		Tip{Phases: []Phase{PhaseAnalyze}, Text: "Use /ask for read-only questions — the sandbox indicator stays RO (read-only)."},

		// Phase: Plan
		Tip{Phases: []Phase{PhasePlan}, Text: "Use declarative policies in izen.yaml to enforce custom execution boundaries."},
		Tip{Phases: []Phase{PhasePlan}, Text: "Use /plan to decompose complex tasks into structured steps before mutation."},
		Tip{Phases: []Phase{PhasePlan}, Text: "Policies in izen.yaml gate every phase transition — declare allow/deny rules up front."},

		// Phase: Execute
		Tip{Phases: []Phase{PhaseExecute}, Text: "Press Ctrl+C to interrupt long-running executions."},
		Tip{Phases: []Phase{PhaseExecute}, Text: "Use /undo to revert the last build operation."},
		Tip{Phases: []Phase{PhaseExecute}, Text: "Use /checkpoint to manually save the current workspace state before risky builds."},

		// Phase: Validate
		Tip{Phases: []Phase{PhaseValidate}, Text: "Use /review to get a structured analysis of your code before committing."},
		Tip{Phases: []Phase{PhaseValidate}, Text: "Run /commit to auto-generate a semantic commit message from staged changes."},

		// Strategy-specific
		Tip{Strategies: []string{StrategyChat}, Text: "DirectChatStrategy bypasses workspace file scans for instant answers."},
		Tip{Strategies: []string{StrategyDirect}, Text: "DirectGenerationStrategy fast-tracks small, low-fanout tasks without staging."},
		Tip{Strategies: []string{StrategyIterative}, Text: "IterativeStrategy decomposes large scope into staged, verifiable steps."},

		// Universal
		Tip{Text: "Run /help or /? at any time to see all available commands."},
		Tip{Text: "Use Ctrl+Up/Down to scroll through command history without re-typing."},
	)
	return p
}
