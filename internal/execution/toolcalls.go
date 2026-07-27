package execution

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/PizenLabs/izen/internal/ai"
)

type ToolCallResult struct {
	File     string
	Original string
	Modified string
	IsNew    bool
}

type ToolCallResults struct {
	Results []ToolCallResult
}

func (r ToolCallResults) Summary() string {
	if len(r.Results) == 0 {
		return "No files modified."
	}
	parts := make([]string, 0, len(r.Results))
	for _, res := range r.Results {
		if res.IsNew {
			parts = append(parts, fmt.Sprintf("Created %s (%d bytes)", res.File, len(res.Modified)))
		} else {
			parts = append(parts, fmt.Sprintf("Modified %s", res.File))
		}
	}
	return strings.Join(parts, "\n")
}

func (r ToolCallResults) HasContent() bool {
	return len(r.Results) > 0
}

func (r ToolCallResults) LastFile() string {
	if len(r.Results) == 0 {
		return ""
	}
	return r.Results[len(r.Results)-1].File
}

func (r ToolCallResults) FirstResult() *ToolCallResult {
	if len(r.Results) == 0 {
		return nil
	}
	return &r.Results[0]
}

func DispatchToolCalls(tcs []ai.ToolCall, cwd string) (*ToolCallResults, error) {
	if len(tcs) == 0 {
		return &ToolCallResults{}, nil
	}

	results := make([]ToolCallResult, 0, len(tcs))
	for _, tc := range tcs {
		result, err := dispatchToolCall(tc, cwd)
		if err != nil {
			return &ToolCallResults{Results: results}, fmt.Errorf("tool call %q (%s): %w", tc.Function.Name, tc.ID, err)
		}
		results = append(results, *result)
	}
	return &ToolCallResults{Results: results}, nil
}

func dispatchToolCall(tc ai.ToolCall, cwd string) (*ToolCallResult, error) {
	switch tc.Function.Name {
	case ai.ToolWriteFile:
		return dispatchWriteFile(tc, cwd)
	case ai.ToolApplyPatch:
		return dispatchApplyPatch(tc, cwd)
	default:
		return nil, fmt.Errorf("unknown tool: %s", tc.Function.Name)
	}
}

func dispatchWriteFile(tc ai.ToolCall, cwd string) (*ToolCallResult, error) {
	var params ai.WriteFileParams
	if err := json.Unmarshal([]byte(tc.Function.Arguments), &params); err != nil {
		return nil, fmt.Errorf("parse write_file arguments: %w", err)
	}

	absPath := resolvePath(cwd, params.Path)

	var orig string
	if data, err := os.ReadFile(absPath); err == nil {
		orig = string(data)
	}

	if err := os.WriteFile(absPath, []byte(params.Content), 0644); err != nil {
		return nil, fmt.Errorf("write file %s: %w", params.Path, err)
	}

	return &ToolCallResult{
		File:     params.Path,
		Original: orig,
		Modified: params.Content,
		IsNew:    orig == "",
	}, nil
}

func dispatchApplyPatch(tc ai.ToolCall, cwd string) (*ToolCallResult, error) {
	var params ai.ApplyPatchParams
	if err := json.Unmarshal([]byte(tc.Function.Arguments), &params); err != nil {
		return nil, fmt.Errorf("parse apply_patch arguments: %w", err)
	}

	absPath := resolvePath(cwd, params.Path)

	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil, fmt.Errorf("read file %s: %w", params.Path, err)
	}
	orig := string(data)

	idx := strings.Index(orig, params.Search)
	if idx == -1 {
		return nil, fmt.Errorf("search text not found in %s", params.Path)
	}

	modified := strings.Replace(orig, params.Search, params.Replace, 1)

	if err := os.WriteFile(absPath, []byte(modified), 0644); err != nil {
		return nil, fmt.Errorf("write file %s: %w", params.Path, err)
	}

	return &ToolCallResult{
		File:     params.Path,
		Original: orig,
		Modified: modified,
		IsNew:    false,
	}, nil
}

func resolvePath(cwd, target string) string {
	if filepath.IsAbs(target) {
		return target
	}
	return filepath.Join(cwd, target)
}
