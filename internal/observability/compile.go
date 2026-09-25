// Package observability contains provider-neutral, content-free metric
// projections shared by the context compiler, runtime execution and telemetry
// adapters. It intentionally has no dependency on execution or engine layers.
package observability

import (
	"strings"

	"github.com/PizenLabs/izen/internal/protocol"
)

// CompileResult is the bounded, content-free result of one context
// compilation. It retains structural accounting for audit/telemetry while
// dropping rendered sections, prompt text and file contents.
type CompileResult struct {
	Phase              Phase  `json:"phase,omitempty"`
	Provider           string `json:"provider,omitempty"`
	Model              string `json:"model,omitempty"`
	Scope              string `json:"scope,omitempty"`
	FittedContextScope string `json:"fitted_context_scope,omitempty"`
	FittedScope        string `json:"fitted_scope,omitempty"`
	Policy             string `json:"policy,omitempty"`
	Lineage            string `json:"lineage,omitempty"`

	BudgetTotal     int `json:"budget_tokens,omitempty"`
	ReservedTokens  int `json:"reserved_tokens,omitempty"`
	AvailableTokens int `json:"available_tokens,omitempty"`
	UsedTokens      int `json:"used_tokens,omitempty"`
	ContextTokens   int `json:"context_tokens,omitempty"`
	SystemTokens    int `json:"system_tokens,omitempty"`
	SchemaTokens    int `json:"schema_tokens,omitempty"`
	ToolTokens      int `json:"tool_tokens,omitempty"`

	Truncated          bool     `json:"truncated,omitempty"`
	TruncatedFiles     []string `json:"truncated_files,omitempty"`
	TruncatedFileCount int      `json:"truncated_file_count,omitempty"`
	DropCount          int      `json:"drop_count,omitempty"`
	DroppedCount       int      `json:"dropped_count,omitempty"`
	Dropped            int      `json:"dropped,omitempty"`
	SectionCount       int      `json:"section_count,omitempty"`
	Sources            []Source `json:"sources,omitempty"`

	PromptChars       int    `json:"prompt_chars,omitempty"`
	PromptFingerprint string `json:"prompt_fingerprint,omitempty"`
}

// Phase and Source aliases keep the projection source-compatible with the
// compiler vocabulary without importing the compiler package.
type Phase = string
type Source = string

// Normalize returns a detached, bounded copy suitable for queueing or audit
// persistence.
func (r CompileResult) Normalize() CompileResult {
	r.Scope = strings.TrimSpace(r.Scope)
	r.FittedContextScope = strings.TrimSpace(r.FittedContextScope)
	r.TruncatedFiles = append([]string(nil), r.TruncatedFiles...)
	r.Sources = append([]Source(nil), r.Sources...)
	return r
}

// CompileTelemetry is a compatibility alias for integrations that call the
// projection telemetry.
type CompileTelemetry = CompileResult

// Fingerprint delegates to the protocol hash helper for callers that need a
// structural fingerprint while constructing this package's metrics.
func Fingerprint(parts ...string) string { return protocol.Fingerprint(parts...) }
