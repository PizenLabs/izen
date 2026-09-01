// Package preflight implements the intent-aware preflight engine: target
// resolution, risk assessment, and context compilation orchestration. A
// PreflightRequest is routed through a target.Resolver, converted into a risk
// classification and an intent confidence, and compiled into a
// CompiledRequest carrying a context.CompiledContext and a final XML prompt
// payload. The engine is deterministic: no LLM is consulted here.
package preflight

import (
	"github.com/PizenLabs/izen/pkg/runtime/context"
	"github.com/PizenLabs/izen/pkg/runtime/target"
)

// RiskLevel classifies the blast radius of mutating a resolved target.
type RiskLevel int

const (
	// RiskLow is reserved for read-only / non-destructive targets. No current
	// assessment rule emits it; it exists so read-only intents can be routed
	// distinctly in the future.
	RiskLow RiskLevel = iota
	// RiskMedium marks modifying an existing non-critical tracked or
	// filesystem-resolved source file.
	RiskMedium
	// RiskHigh marks creating files, deleting files, modifying build
	// manifests (go.mod, package.json, Makefile), or unresolved raw targets.
	RiskHigh
)

// String returns a stable lowercase label for the risk level.
func (r RiskLevel) String() string {
	switch r {
	case RiskLow:
		return "low"
	case RiskMedium:
		return "medium"
	case RiskHigh:
		return "high"
	default:
		return "unknown"
	}
}

// PreflightRequest is the input to the preflight engine: the raw user intent,
// the working directory it is relative to, the context token budget, and the
// candidate context units available for compilation.
type PreflightRequest struct {
	RawInput       string
	WorkDir        string
	TokenBudget    int
	CandidateUnits []context.ContextUnit
}

// CompiledRequest is the output of the preflight engine: the resolved target,
// the compiled context, the assessed risk, and the formatted XML prompt.
type CompiledRequest struct {
	TargetRef       *target.TargetRef
	Context         *context.CompiledContext
	Risk            RiskLevel
	FormattedPrompt string
}
