package domain

import "time"

// ExecutionEnvironment is the snapshot of the world the Runtime observes
// BEFORE selecting a strategy. It is read-only after construction.
type ExecutionEnvironment struct {
	Model     ModelEnvironment      `json:"model"`
	Provider  ProviderEnvironment   `json:"provider"`
	Workspace WorkspaceCapabilities `json:"workspace"`
	Budget    ResourceBudget        `json:"budget"`
	Source    SourceState           `json:"source"`
	Evidence  EvidenceSnapshot      `json:"evidence"`
}

type ModelEnvironment struct {
	ContextWindow    int  `json:"context_window"`
	MaxOutputTokens  int  `json:"max_output_tokens"`
	ReasoningCapable bool `json:"reasoning_capable"`
	ToolSupport      bool `json:"tool_support"`
}

type ProviderEnvironment struct {
	RateLimitRPM    int           `json:"rate_limit_rpm"`
	RateLimitTPM    int           `json:"rate_limit_tpm"`
	CacheCapable    bool          `json:"cache_capable"`
	LatencyP50      time.Duration `json:"latency_p50"`
	CostPer1KTokens float64       `json:"cost_per_1k_tokens"`
}

type WorkspaceCapabilities struct {
	HasParser      bool `json:"has_parser"`
	HasSymbolGraph bool `json:"has_symbol_graph"`
	HasLSP         bool `json:"has_lsp"`
	HasFormatter   bool `json:"has_formatter"`
	HasLinter      bool `json:"has_linter"`
	HasCompiler    bool `json:"has_compiler"`
	HasTestRunner  bool `json:"has_test_runner"`
	HasGit         bool `json:"has_git"`
	IsCold         bool `json:"is_cold"`
}

type ResourceBudget struct {
	MaxInputTokens   int           `json:"max_input_tokens"`
	MaxOutputTokens  int           `json:"max_output_tokens"`
	MaxRequests      int           `json:"max_requests"`
	MaxAttempts      int           `json:"max_attempts"`
	MaxFiles         int           `json:"max_files"`
	MaxDiffLines     int           `json:"max_diff_lines"`
	MaxShellCommands int           `json:"max_shell_commands"`
	MaxLatency       time.Duration `json:"max_latency"`
}

type SourceState struct {
	CommitHash string            `json:"commit_hash"`
	FileHashes map[string]string `json:"file_hashes"`
	Dirty      bool              `json:"dirty"`
}

type EvidenceSnapshot struct {
	Level   EvidenceLevel `json:"level"`
	Summary string        `json:"summary"`
}
