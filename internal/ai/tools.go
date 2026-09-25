package ai

import (
	"context"
	"encoding/json"
)

const (
	ToolWriteFile  = "write_file"
	ToolApplyPatch = "apply_patch"

	// Read-only inspection tools. These are authentic, executable IZEN
	// capabilities (see internal/execution readonly tool runner) — never
	// dummy/shadow schemas. They are attached to agentic-harness models so the
	// provider accepts the invocation while the runtime stays non-mutating.
	ToolReadFile       = "read_file"
	ToolListDirectory  = "list_directory"
	ToolSearchCodebase = "search_codebase"
	ToolSymbolLookup   = "symbol_lookup"
)

// ToolRunner executes one authentic read-only tool call and returns its textual
// output. The execution pipeline implements it against the live workspace; the
// provider adapters never touch the filesystem themselves.
type ToolRunner interface {
	Run(ctx context.Context, call ToolCall) (string, error)
}

// ReadOnlyToolNames is the closed set of read-only tool names.
var ReadOnlyToolNames = []string{
	ToolReadFile,
	ToolListDirectory,
	ToolSearchCodebase,
	ToolSymbolLookup,
}

type ToolDefinition struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

type ToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type ToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function ToolCallFunction `json:"function"`
}

type ToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type WriteFileParams struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type ApplyPatchParams struct {
	Path    string `json:"path"`
	Search  string `json:"search"`
	Replace string `json:"replace"`
}

func NewWriteFileTool() ToolDefinition {
	return ToolDefinition{
		Type: "function",
		Function: ToolFunction{
			Name:        ToolWriteFile,
			Description: "Create or overwrite a file with the given content. Use for new files or when you need to replace the entire file contents. The path is relative to the project root.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"path": {
						"type": "string",
						"description": "Relative file path from project root (e.g. src/index.html)"
					},
					"content": {
						"type": "string",
						"description": "Complete file content to write"
					}
				},
				"required": ["path", "content"]
			}`),
		},
	}
}

func NewApplyPatchTool() ToolDefinition {
	return ToolDefinition{
		Type: "function",
		Function: ToolFunction{
			Name:        ToolApplyPatch,
			Description: "Apply a targeted search/replace modification to an existing file. Use for modifying specific portions of an existing file without rewriting the entire file.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"path": {
						"type": "string",
						"description": "Relative file path from project root (e.g. src/index.html)"
					},
					"search": {
						"type": "string",
						"description": "Exact text to search for in the file"
					},
					"replace": {
						"type": "string",
						"description": "Text to replace the search text with"
					}
				},
				"required": ["path", "search", "replace"]
			}`),
		},
	}
}

func FileMutationTools() []ToolDefinition {
	return []ToolDefinition{
		NewWriteFileTool(),
		NewApplyPatchTool(),
	}
}

// ── Read-only inspection tools ──────────────────────────────────────────────

// ReadFileParams are the read_file tool parameters.
type ReadFileParams struct {
	Path      string `json:"path"`
	StartLine int    `json:"start_line,omitempty"`
	EndLine   int    `json:"end_line,omitempty"`
}

// ListDirectoryParams are the list_directory tool parameters.
type ListDirectoryParams struct {
	Path string `json:"path,omitempty"`
}

// SearchCodebaseParams are the search_codebase tool parameters.
type SearchCodebaseParams struct {
	Query      string `json:"query"`
	Path       string `json:"path,omitempty"`
	MaxResults int    `json:"max_results,omitempty"`
}

// SymbolLookupParams are the symbol_lookup tool parameters.
type SymbolLookupParams struct {
	Symbol string `json:"symbol"`
	Path   string `json:"path,omitempty"`
}

// NewReadFileTool defines the read_file inspection capability.
func NewReadFileTool() ToolDefinition {
	return ToolDefinition{
		Type: "function",
		Function: ToolFunction{
			Name:        ToolReadFile,
			Description: "Read the contents of a text file in the workspace. Read-only; never modifies the file. Optionally restrict to a 1-based inclusive line range.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"path": {"type": "string", "description": "Relative file path from project root"},
					"start_line": {"type": "integer", "description": "Optional 1-based first line to include"},
					"end_line": {"type": "integer", "description": "Optional 1-based last line to include"}
				},
				"required": ["path"]
			}`),
		},
	}
}

// NewListDirectoryTool defines the list_directory inspection capability.
func NewListDirectoryTool() ToolDefinition {
	return ToolDefinition{
		Type: "function",
		Function: ToolFunction{
			Name:        ToolListDirectory,
			Description: "List the entries of a workspace directory (non-recursive). Read-only workspace structure lookup. Defaults to the project root.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"path": {"type": "string", "description": "Relative directory path from project root (default: root)"}
				}
			}`),
		},
	}
}

// NewSearchCodebaseTool defines the search_codebase inspection capability.
func NewSearchCodebaseTool() ToolDefinition {
	return ToolDefinition{
		Type: "function",
		Function: ToolFunction{
			Name:        ToolSearchCodebase,
			Description: "Search workspace text files for a literal query and return matching file:line results. Read-only.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"query": {"type": "string", "description": "Literal text to search for"},
					"path": {"type": "string", "description": "Optional relative directory to search (default: root)"},
					"max_results": {"type": "integer", "description": "Optional maximum number of matches (default 50)"}
				},
				"required": ["query"]
			}`),
		},
	}
}

// NewSymbolLookupTool defines the symbol_lookup inspection capability.
func NewSymbolLookupTool() ToolDefinition {
	return ToolDefinition{
		Type: "function",
		Function: ToolFunction{
			Name:        ToolSymbolLookup,
			Description: "Locate a code symbol (function, type, class) by name across workspace source files. Read-only AST/text lookup.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"symbol": {"type": "string", "description": "Symbol name to locate"},
					"path": {"type": "string", "description": "Optional relative directory to search (default: root)"}
				},
				"required": ["symbol"]
			}`),
		},
	}
}

// ReadOnlyTools is the canonical set of authentic, non-mutating inspection
// capabilities IZEN exposes to agentic-harness models. Every tool maps to a
// real execution-pipeline handler — no dummy/shadow definitions.
func ReadOnlyTools() []ToolDefinition {
	return []ToolDefinition{
		NewReadFileTool(),
		NewListDirectoryTool(),
		NewSearchCodebaseTool(),
		NewSymbolLookupTool(),
	}
}
