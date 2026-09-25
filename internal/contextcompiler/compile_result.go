package contextcompiler

import (
	"github.com/PizenLabs/izen/internal/observability"
	"github.com/PizenLabs/izen/internal/protocol"
)

// CompileResult is the content-free compiler projection consumed by telemetry
// and audit sinks. The alias keeps the compiler API descriptive while the
// underlying type remains in a dependency-neutral observability package.
type CompileResult = observability.CompileResult
type CompileTelemetry = observability.CompileResult
type ContextCompilationMetrics = observability.CompileResult

// Metrics projects a compiled context into its safe telemetry form. The
// projection is deliberately structural: it contains paths and counts, never
// rendered sections or prompt text.
func (c *CompiledContext) Metrics() CompileResult {
	if c == nil {
		return CompileResult{}
	}
	result := CompileResult{
		Phase:              string(c.Phase),
		Provider:           c.Provider,
		Model:              c.Model,
		Scope:              c.Scope,
		FittedContextScope: c.Scope,
		FittedScope:        c.Scope,
		Policy:             c.Policy,
		Lineage:            c.Lineage,
		BudgetTotal:        c.Budget.Total,
		ReservedTokens:     c.Budget.Reserved,
		AvailableTokens:    c.Budget.Available,
		UsedTokens:         c.UsedTokens,
		ContextTokens:      c.ContextTokens,
		SystemTokens:       c.SystemTokens,
		SchemaTokens:       c.SchemaTokens,
		ToolTokens:         c.ToolTokens,
		Truncated:          c.Truncated,
		TruncatedFiles:     append([]string(nil), c.TruncatedFiles...),
		TruncatedFileCount: len(c.TruncatedFiles),
		DropCount:          c.Dropped,
		DroppedCount:       c.Dropped,
		Dropped:            c.Dropped,
		SectionCount:       len(c.Sections),
	}
	seen := make(map[Source]struct{}, len(c.Sections))
	for _, section := range c.Sections {
		if _, ok := seen[section.Source]; ok {
			continue
		}
		seen[section.Source] = struct{}{}
		result.Sources = append(result.Sources, string(section.Source))
	}
	rendered := c.Assemble()
	result.PromptChars = len(rendered)
	result.PromptFingerprint = protocol.Fingerprint(rendered)
	return result
}

// CompileResult is the explicit method spelling for callers that use the
// result type as a noun. It intentionally delegates to Metrics.
func (c *CompiledContext) CompileResult() CompileResult { return c.Metrics() }

// CompileMetrics and Telemetry are descriptive method aliases for metric
// consumers that do not use the result noun.
func (c *CompiledContext) CompileMetrics() CompileResult { return c.Metrics() }
func (c *CompiledContext) Telemetry() CompileResult      { return c.Metrics() }

// CompileResult projects an AgentContext facade to the same safe telemetry
// result. A nil or incomplete facade still returns a valid zero projection.
func (a *AgentContext) CompileResult() CompileResult {
	if a == nil {
		return CompileResult{}
	}
	if a.Compiled != nil {
		return a.Compiled.Metrics()
	}
	if a.Context != nil {
		return a.Context.Metrics()
	}
	return CompileResult{}
}

func (a *AgentContext) CompileMetrics() CompileResult { return a.CompileResult() }
func (a *AgentContext) Telemetry() CompileResult      { return a.CompileResult() }

// NormalizeCompileResult is a defensive copy helper for callers that persist or
// queue a result across a boundary.
func NormalizeCompileResult(r CompileResult) CompileResult { return r.Normalize() }
