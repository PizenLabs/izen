package ai

import "encoding/json"

const (
	ToolWriteFile  = "write_file"
	ToolApplyPatch = "apply_patch"
)

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
