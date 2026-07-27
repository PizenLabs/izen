package execution

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/PizenLabs/izen/internal/ai"
)

// ── ToolCallBuffer ────────────────────────────────────────────────────────────
// ToolCallBuffer is the in-memory interceptor for native LLM tool calls.
// When the LLM responds with finish_reason: "tool_calls", the calls are buffered
// here and NOT applied to disk. A unified diff preview is generated for each call,
// and the user must explicitly approve before any disk mutation occurs.

// BufferedToolCall holds a single intercepted tool call with its computed diff.
type BufferedToolCall struct {
	ID       string
	Name     string
	Path     string
	Original string
	Modified string
	Diff     string
	IsNew    bool
	Approved bool
}

// ToolCallBuffer intercepts and buffers tool calls, generating diff previews.
// Zero value is ready to use.
type ToolCallBuffer struct {
	mu      sync.Mutex
	calls   []BufferedToolCall
	cwd     string
	applied bool
}

// NewToolCallBuffer creates a buffer for the given working directory.
func NewToolCallBuffer(cwd string) *ToolCallBuffer {
	return &ToolCallBuffer{cwd: cwd}
}

// Buffer parses tool call arguments, reads original file content from disk,
// computes the modified content and a unified diff, and stores everything
// in memory without touching the filesystem. Returns an error if the tool
// call arguments cannot be parsed.
func (b *ToolCallBuffer) Buffer(tc ai.ToolCall) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	btc, err := bufferToolCall(tc, b.cwd)
	if err != nil {
		return fmt.Errorf("buffer tool call %q (%s): %w", tc.Function.Name, tc.ID, err)
	}
	b.calls = append(b.calls, *btc)
	return nil
}

// BufferAll buffers multiple tool calls in a single call.
func (b *ToolCallBuffer) BufferAll(tcs []ai.ToolCall) error {
	for _, tc := range tcs {
		if err := b.Buffer(tc); err != nil {
			return err
		}
	}
	return nil
}

// Pending returns all unapproved tool calls.
func (b *ToolCallBuffer) Pending() []BufferedToolCall {
	b.mu.Lock()
	defer b.mu.Unlock()
	var pending []BufferedToolCall
	for _, c := range b.calls {
		if !c.Approved {
			pending = append(pending, c)
		}
	}
	return pending
}

// All returns all buffered tool calls.
func (b *ToolCallBuffer) All() []BufferedToolCall {
	b.mu.Lock()
	defer b.mu.Unlock()
	result := make([]BufferedToolCall, len(b.calls))
	copy(result, b.calls)
	return result
}

// Count returns the total number of buffered calls.
func (b *ToolCallBuffer) Count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.calls)
}

// Approve marks a single buffered call as approved by index.
func (b *ToolCallBuffer) Approve(index int) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if index < 0 || index >= len(b.calls) {
		return fmt.Errorf("tool call index %d out of range (0-%d)", index, len(b.calls)-1)
	}
	b.calls[index].Approved = true
	return nil
}

// ApproveAll marks all buffered calls as approved.
func (b *ToolCallBuffer) ApproveAll() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i := range b.calls {
		b.calls[i].Approved = true
	}
}

// Reject removes all buffered calls without applying them.
func (b *ToolCallBuffer) Reject() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls = nil
}

// ApplyApproved writes all approved buffered calls to disk.
// Returns ToolCallResults describing what was applied.
func (b *ToolCallBuffer) ApplyApproved() (*ToolCallResults, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.applied {
		return &ToolCallResults{}, nil
	}

	var results []ToolCallResult
	for i := range b.calls {
		if !b.calls[i].Approved {
			continue
		}
		absPath := resolvePath(b.cwd, b.calls[i].Path)
		dir := filepath.Dir(absPath)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return &ToolCallResults{Results: results}, fmt.Errorf("mkdir %s: %w", dir, err)
		}
		if err := os.WriteFile(absPath, []byte(b.calls[i].Modified), 0644); err != nil {
			return &ToolCallResults{Results: results}, fmt.Errorf("write %s: %w", b.calls[i].Path, err)
		}
		results = append(results, ToolCallResult{
			File:     b.calls[i].Path,
			Original: b.calls[i].Original,
			Modified: b.calls[i].Modified,
			IsNew:    b.calls[i].IsNew,
		})
	}
	b.applied = true
	return &ToolCallResults{Results: results}, nil
}

// ApplyPending approves all pending calls and applies them in one step.
func (b *ToolCallBuffer) ApplyPending() (*ToolCallResults, error) {
	b.ApproveAll()
	return b.ApplyApproved()
}

// Reset clears the buffer for reuse.
func (b *ToolCallBuffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls = nil
	b.applied = false
}

// HasPending returns true if there are calls that have not been approved.
func (b *ToolCallBuffer) HasPending() bool {
	return len(b.Pending()) > 0
}

func bufferToolCall(tc ai.ToolCall, cwd string) (*BufferedToolCall, error) {
	switch tc.Function.Name {
	case ai.ToolWriteFile:
		return bufferWriteFile(tc, cwd)
	case ai.ToolApplyPatch:
		return bufferApplyPatch(tc, cwd)
	default:
		return nil, fmt.Errorf("unknown tool: %s", tc.Function.Name)
	}
}

func bufferWriteFile(tc ai.ToolCall, cwd string) (*BufferedToolCall, error) {
	var params ai.WriteFileParams
	if err := json.Unmarshal([]byte(tc.Function.Arguments), &params); err != nil {
		return nil, fmt.Errorf("parse write_file arguments: %w", err)
	}

	absPath := resolvePath(cwd, params.Path)
	var orig string
	if data, err := os.ReadFile(absPath); err == nil {
		orig = string(data)
	}

	diff := buildDiff(orig, params.Content, params.Path)
	isNew := orig == ""

	return &BufferedToolCall{
		ID:       tc.ID,
		Name:     tc.Function.Name,
		Path:     params.Path,
		Original: orig,
		Modified: params.Content,
		Diff:     diff,
		IsNew:    isNew,
	}, nil
}

func bufferApplyPatch(tc ai.ToolCall, cwd string) (*BufferedToolCall, error) {
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
	diff := buildDiff(orig, modified, params.Path)

	return &BufferedToolCall{
		ID:       tc.ID,
		Name:     tc.Function.Name,
		Path:     params.Path,
		Original: orig,
		Modified: modified,
		Diff:     diff,
		IsNew:    false,
	}, nil
}

// buildDiff generates a minimal unified diff between old and new content.
func buildDiff(oldContent, newContent, filePath string) string {
	if oldContent == "" && newContent != "" {
		return fmt.Sprintf("--- a/%s\n+++ b/%s\n@@ -0,0 +1,%d @@\n%s",
			filePath, filePath, len(strings.Split(newContent, "\n")),
			addPlusPrefix(newContent))
	}
	if oldContent != "" && newContent == "" {
		return fmt.Sprintf("--- a/%s\n+++ b/%s\n@@ -1,%d +0,0 @@\n",
			filePath, filePath, len(strings.Split(oldContent, "\n")))
	}
	if oldContent == newContent {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "--- a/%s\n+++ b/%s\n", filePath, filePath)
	oldLines := strings.Split(oldContent, "\n")
	newLines := strings.Split(newContent, "\n")
	fmt.Fprintf(&b, "@@ -1,%d +1,%d @@\n", len(oldLines), len(newLines))
	for _, line := range oldLines {
		b.WriteString("-" + line + "\n")
	}
	for _, line := range newLines {
		b.WriteString("+" + line + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func addPlusPrefix(content string) string {
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		lines[i] = "+" + line
	}
	return strings.Join(lines, "\n")
}

// ── Existing ToolCallResult / Dispatch (kept for backward compatibility) ──────

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
