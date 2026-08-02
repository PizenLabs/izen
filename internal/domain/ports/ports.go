// Package ports defines the provider interfaces that separate the pure domain
// layer from the outer adapter layers (runtime, infrastructure, presentation).
//
// Every concrete implementation of a port lives outside internal/domain; the
// domain core depends only on these abstractions so that business logic stays
// testable and decoupled from files, processes, network, and LLM backends.
package ports

import "context"

// PatchPayload is the normalized input a PatchPort consumes. It carries the
// target file coordinate, the current content (when the file exists), the
// proposed modification, and execution metadata.
type PatchPayload struct {
	// File is the workspace-relative path of the target file.
	File string
	// Original holds the exact bytes currently on disk, if any.
	Original string
	// Modified holds the proposed new content or a structured diff payload.
	Modified string
	// IsFullRewrite marks an explicit full-file replacement that bypasses
	// hunk-matching safety checks.
	IsFullRewrite bool
	// TaskID links the patch to a task in the execution graph (0 = ad-hoc).
	TaskID int
}

// PatchResult reports the outcome of applying a patch.
type PatchResult struct {
	// File is the workspace-relative path that was targeted.
	File string
	// Applied is true when the patch was written to disk.
	Applied bool
	// LinesAdded is the net number of lines introduced.
	LinesAdded int
	// LinesRemoved is the net number of lines removed.
	LinesRemoved int
}

// PatchPort abstracts patch parsing, validation, and application. Adapters in
// the infrastructure layer implement it over the concrete patch engine.
type PatchPort interface {
	// Parse turns a raw payload into a normalized PatchPayload. It must reject
	// malformed input without side effects.
	Parse(ctx context.Context, payload string) (PatchPayload, error)
	// Validate checks that a patch can be safely applied against the current
	// file content. It must be side-effect free.
	Validate(ctx context.Context, patch PatchPayload, current string) error
	// Apply writes the patch to disk and returns the result.
	Apply(ctx context.Context, patch PatchPayload) (PatchResult, error)
}

// ShellResult is the combined output of a shell command execution.
type ShellResult struct {
	// Stdout holds the command's standard output.
	Stdout string
	// Stderr holds the command's standard error.
	Stderr string
	// ExitCode is the process exit status; zero means success.
	ExitCode int
}

// ShellPort abstracts command execution in the workspace. Adapters implement
// it over the OS process API.
type ShellPort interface {
	// Execute runs a command in the default working directory.
	Execute(ctx context.Context, command string) (ShellResult, error)
	// ExecuteIn runs a command in the given working directory.
	ExecuteIn(ctx context.Context, dir, command string) (ShellResult, error)
}

// GitStatusEntry describes a single working-tree change reported by git.
type GitStatusEntry struct {
	// Path is the repository-relative path of the changed file.
	Path string
	// Staging is the two-letter staging flag (e.g. "M", "A", "??").
	Staging string
	// Worktree is the worktree flag character.
	Worktree string
}

// GitPort abstracts version-control operations. Adapters implement it over the
// git CLI or a VCS library.
type GitPort interface {
	// Status returns the current working-tree changes.
	Status(ctx context.Context) ([]GitStatusEntry, error)
	// Diff returns the unstaged diff of the working tree.
	Diff(ctx context.Context) (string, error)
	// DiffFile returns the diff of a single file.
	DiffFile(ctx context.Context, path string) (string, error)
	// Commit records a new commit with the given subject and body.
	Commit(ctx context.Context, subject, body string) error
	// CurrentHash returns the short hash of the current HEAD commit.
	CurrentHash(ctx context.Context) (string, error)
	// Branch returns the name of the current branch.
	Branch(ctx context.Context) (string, error)
}

// FilePort abstracts filesystem access for the workspace. Adapters implement
// it over the OS filesystem, allowing domain tests to run against memory.
type FilePort interface {
	// Read returns the full content of the file at path.
	Read(ctx context.Context, path string) (string, error)
	// Write persists content to the file at path, creating parents as needed.
	Write(ctx context.Context, path string, content string) error
	// List returns the directory entries directly under dir.
	List(ctx context.Context, dir string) ([]string, error)
	// Exists reports whether the file at path exists.
	Exists(ctx context.Context, path string) bool
}

// Message is a single conversational turn sent to an LLM.
type Message struct {
	// Role is one of "system", "user", or "assistant".
	Role string
	// Content is the message body.
	Content string
}

// LLMRequest describes a single generation request.
type LLMRequest struct {
	// Model identifies the model to use.
	Model string
	// System is the system prompt, if any.
	System string
	// Messages holds the conversation turns.
	Messages []Message
	// MaxTokens bounds the generated output.
	MaxTokens int
	// Temperature controls sampling randomness.
	Temperature float64
}

// LLMResponse is the result of a generation request.
type LLMResponse struct {
	// Content is the generated text.
	Content string
	// TokenInput counts input tokens consumed.
	TokenInput int
	// TokenOutput counts output tokens produced.
	TokenOutput int
	// TotalCostUSD is the estimated monetary cost of the call.
	TotalCostUSD float64
}

// StreamHandler receives incremental text chunks as they are generated.
type StreamHandler func(chunk string) error

// LLMPort abstracts model inference. Adapters implement it over concrete model
// providers (OpenAI, Ollama, etc.).
type LLMPort interface {
	// Generate produces a complete response for the request.
	Generate(ctx context.Context, req LLMRequest) (LLMResponse, error)
	// Stream produces a response incrementally, delivering chunks to handler.
	Stream(ctx context.Context, req LLMRequest, handler StreamHandler) (LLMResponse, error)
}
