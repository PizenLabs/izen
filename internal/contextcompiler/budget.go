// Package contextcompiler is the Context Compilation authority. It owns the
// deterministic decision about which prompt material crosses a provider
// boundary and the token budget in which that material must fit.
//
// The compiler is intentionally independent from execution, authorization and
// provider transport. It consumes already-selected context facts and returns a
// bounded projection; it never reads the workspace, dispatches a model, or
// grants an operation authority.
package contextcompiler

import (
	"errors"
	"strings"

	"github.com/PizenLabs/izen/internal/protocol"
	"github.com/PizenLabs/izen/internal/providers/capability"
)

// ErrBudgetExceeded identifies a prompt whose critical material cannot fit the
// active model/context budget. Non-critical workspace material is truncated or
// dropped instead; only this error is returned when mandatory system/user
// instructions, output schema, tool descriptors, or explicitly required
// workspace material cannot fit.
var ErrBudgetExceeded = errors.New("contextcompiler: prompt token budget exceeded")

// Phase identifies the semantic step whose context is being compiled. The
// phase cap is a policy ceiling, not a provider capability claim; model limits
// may lower it but never raise it.
type Phase string

const (
	PhaseInvestigate Phase = "investigate"
	PhasePlan        Phase = "plan"
	PhaseExecute     Phase = "execute"

	// Short aliases make call sites read naturally while retaining the explicit
	// Phase* spellings for APIs that prefer them.
	Investigate = PhaseInvestigate
	Plan        = PhasePlan
	Execute     = PhaseExecute

	InvestigatePhase = PhaseInvestigate
	PlanPhase        = PhasePlan
	ExecutePhase     = PhaseExecute
)

// Normalize returns the canonical phase label, accepting case-insensitive
// input for configuration and embedding callers.
func (p Phase) Normalize() Phase {
	switch strings.ToLower(strings.TrimSpace(string(p))) {
	case string(PhaseInvestigate):
		return PhaseInvestigate
	case string(PhasePlan):
		return PhasePlan
	case string(PhaseExecute):
		return PhaseExecute
	default:
		return ""
	}
}

// ParsePhase is the string-oriented form of Phase.Normalize.
func ParsePhase(value string) Phase { return Phase(value).Normalize() }

// Valid reports whether p is one of the three semantic execution phases.
func (p Phase) Valid() bool { return p.Normalize() != "" }

// PhaseForContract maps a semantic interaction contract to its context phase.
// It is descriptive selection only; it never grants provider capability or
// execution authority.
func PhaseForContract(contract protocol.InteractionContract) Phase {
	switch contract {
	case protocol.StructuredCompletion:
		return PhasePlan
	case protocol.AgenticLoop, protocol.ToolEnabledCompletion:
		return PhaseExecute
	case protocol.DirectCompletion:
		return PhaseInvestigate
	default:
		return ""
	}
}

// ContextBudget returns the maximum prompt-context allocation justified by the
// phase before model-specific limits are applied.
func (p Phase) ContextBudget() int {
	switch p.Normalize() {
	case PhaseInvestigate:
		return InvestigateContextBudget
	case PhasePlan:
		return PlanContextBudget
	case PhaseExecute:
		return ExecuteContextBudget
	default:
		return 0
	}
}

const (
	// PhaseContextBudgets are the semantic ceilings before model limits.
	InvestigateContextBudget = 4000
	PlanContextBudget        = 8000
	ExecuteContextBudget     = 16000

	// DefaultOutputReserveTokens is used only when a model/provider does not
	// advertise either an output ceiling or a requested output budget.
	DefaultOutputReserveTokens = 1536
	// ContextSafetyMarginTokens leaves room for provider-specific message
	// framing and tokenization differences around the estimated prompt body.
	ContextSafetyMarginTokens = 256
)

// ModelLimits is the provider-neutral subset of model facts needed to compile
// a prompt. ContextWindow is the total input+output window; MaxOutputTokens is
// the advertised completion ceiling when known; RequestedOutputTokens is the
// actual per-step output request when it is smaller.
type ModelLimits struct {
	Provider              string
	Model                 string
	ContextWindow         int
	MaxOutputTokens       int
	RequestedOutputTokens int
}

// ModelLimitsFor applies the existing provider capability heuristics. Callers
// with a catalog record should copy the record's explicit values over the
// returned limits; the compiler always treats explicit values as authoritative.
func ModelLimitsFor(provider, model string, requestedOutput int) ModelLimits {
	maxOutput := capability.MaxOutputTokensFor(provider, model)
	if capability.IsFreeTierModelID(model) && maxOutput > capability.ConstrainedOutputThreshold {
		maxOutput = capability.ConstrainedOutputThreshold
	}
	return ModelLimits{
		Provider:              provider,
		Model:                 model,
		ContextWindow:         capability.ContextWindowFor(model),
		MaxOutputTokens:       maxOutput,
		RequestedOutputTokens: requestedOutput,
	}
}

// TokenBudget is the effective, model-aware input allowance for one phase.
// ContextWindow - OutputReserve - ContextSafetyMargin is the hard model bound;
// PhaseLimit is the semantic ceiling. Available is the remaining allowance
// after the non-context reservations.
type TokenBudget struct {
	Phase               Phase
	ContextWindow       int
	OutputReserve       int
	SafetyMargin        int
	PhaseLimit          int
	Available           int
	Total               int
	RequestedOutput     int
	AdvertisedMaxOutput int
}

// ResolveTokenBudget calculates the strict effective prompt budget for a
// phase/model pair. Unknown model limits fall back to the phase ceiling rather
// than inventing a provider-specific window.
func ResolveTokenBudget(phase Phase, limits ModelLimits) TokenBudget {
	phase = phase.Normalize()
	if !phase.Valid() {
		phase = PhaseExecute
	}
	phaseLimit := phase.ContextBudget()
	if phaseLimit <= 0 {
		phaseLimit = DefaultMaxTokens
	}

	outputReserve := limits.MaxOutputTokens
	if limits.RequestedOutputTokens > 0 && (outputReserve <= 0 || limits.RequestedOutputTokens < outputReserve) {
		outputReserve = limits.RequestedOutputTokens
	}
	if outputReserve <= 0 {
		outputReserve = DefaultOutputReserveTokens
	}

	available := phaseLimit
	if limits.ContextWindow > 0 {
		available = limits.ContextWindow - outputReserve - ContextSafetyMarginTokens
		if available < 0 {
			available = 0
		}
		if available > phaseLimit {
			available = phaseLimit
		}
	}
	if available < 0 {
		available = 0
	}
	return TokenBudget{
		Phase:               phase,
		ContextWindow:       limits.ContextWindow,
		OutputReserve:       outputReserve,
		SafetyMargin:        ContextSafetyMarginTokens,
		PhaseLimit:          phaseLimit,
		Available:           available,
		Total:               available,
		RequestedOutput:     limits.RequestedOutputTokens,
		AdvertisedMaxOutput: limits.MaxOutputTokens,
	}
}

// CalculateTokenBudget is a descriptive alias for ResolveTokenBudget.
func CalculateTokenBudget(phase Phase, limits ModelLimits) TokenBudget {
	return ResolveTokenBudget(phase, limits)
}

// ResolveContextBudget is an explicit context-oriented alias for
// ResolveTokenBudget.
func ResolveContextBudget(phase Phase, limits ModelLimits) TokenBudget {
	return ResolveTokenBudget(phase, limits)
}

// CalculateBudget is a compact compatibility alias for budget callers.
func CalculateBudget(phase Phase, limits ModelLimits) TokenBudget {
	return ResolveTokenBudget(phase, limits)
}

// BudgetForPhase is a compact helper for callers that only need the effective
// context-token cap.
func BudgetForPhase(phase Phase, contextWindow, maxOutputTokens, requestedOutput int) int {
	return ResolveTokenBudget(phase, ModelLimits{
		ContextWindow:         contextWindow,
		MaxOutputTokens:       maxOutputTokens,
		RequestedOutputTokens: requestedOutput,
	}).Total
}

// EffectiveContextBudget is an explicit-name alias for BudgetForPhase.
func EffectiveContextBudget(phase Phase, contextWindow, maxOutputTokens, requestedOutput int) int {
	return BudgetForPhase(phase, contextWindow, maxOutputTokens, requestedOutput)
}

// ContextBudgetForPhase is a descriptive alias for BudgetForPhase.
func ContextBudgetForPhase(phase Phase, contextWindow, maxOutputTokens, requestedOutput int) int {
	return BudgetForPhase(phase, contextWindow, maxOutputTokens, requestedOutput)
}

// Source identifies one context source the compiler may admit. The first three
// sources are critical prompt contracts: they are reserved before any optional
// workspace context is considered and are never silently truncated.
type Source string

const (
	SourceSystemInstructions Source = "system_instructions"
	SourceSchemaOverlay      Source = "schema_overlay"
	SourceToolDescriptors    Source = "tool_descriptors"
	SourceUserRequest        Source = "user_request"
	SourceWorkflow           Source = "workflow_state"
	SourceRecentTurns        Source = "recent_turns"
	SourceSessionCompact     Source = "session_compact"
	SourceArtifacts          Source = "artifacts"
	SourceProjectKnowledge   Source = "project_knowledge"

	SourceSchema = SourceSchemaOverlay
	SourceTools  = SourceToolDescriptors
	SourceFiles  = SourceArtifacts
)

// priorityOrder is the admission order: critical contracts first, then the
// user request and workflow, and finally lower-priority project knowledge.
var priorityOrder = []Source{
	SourceSystemInstructions,
	SourceSchemaOverlay,
	SourceToolDescriptors,
	SourceUserRequest,
	SourceWorkflow,
	SourceRecentTurns,
	SourceSessionCompact,
	SourceArtifacts,
	SourceProjectKnowledge,
}

// defaultShares is the dynamic per-source token allocation for non-critical
// material (percent, normalized at allocation time).
var defaultShares = map[Source]int{
	SourceUserRequest:      20,
	SourceWorkflow:         10,
	SourceRecentTurns:      25,
	SourceSessionCompact:   20,
	SourceArtifacts:        10,
	SourceProjectKnowledge: 15,
}

// DefaultMaxTokens is the legacy/default context ceiling used when no phase
// or model-specific budget is supplied.
const DefaultMaxTokens = 4000

// Budget is the dynamic token budget for one compilation. Total is the hard
// input-prompt cap; BySource contains the effective allowance for each source.
type Budget struct {
	Total         int
	BySource      map[Source]int
	Phase         Phase
	ContextWindow int
	OutputReserve int
	Reserved      int
	Available     int
}

// ContextBudget is the descriptive facade name for a compiled source budget.
// It aliases Budget so embedders can use the context-oriented vocabulary
// without maintaining a second budget representation.
type ContextBudget = Budget

// Source returns the effective per-source token share.
func (b Budget) Source(src Source) int {
	if b.BySource == nil {
		return 0
	}
	return b.BySource[src]
}

// Allocate derives per-source shares from a total cap and an allocation table.
// A non-positive cap falls back to DefaultMaxTokens; missing shares default to
// 0. Percentages that do not sum to 100 are normalized.
func Allocate(total int, shares map[Source]int) Budget {
	if total <= 0 {
		total = DefaultMaxTokens
	}
	sum := 0
	for _, pct := range shares {
		if pct > 0 {
			sum += pct
		}
	}
	if sum <= 0 {
		shares = defaultShares
		sum = 100
	}
	by := make(map[Source]int, len(shares))
	for src, pct := range shares {
		if pct <= 0 {
			by[src] = 0
			continue
		}
		by[src] = total * pct / sum
	}
	return Budget{Total: total, BySource: by}
}

// EstimateTokens is the coarse ~4 characters/token accounting heuristic. It is
// the compiler's single token oracle, used for budgeting, fitting and
// telemetry; provider-reported usage remains authoritative for billing.
func EstimateTokens(s string) int {
	if len(s) == 0 {
		return 0
	}
	return (len([]rune(s)) + 3) / 4
}
