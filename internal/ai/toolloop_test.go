package ai

import (
	"context"
	"encoding/json"
	"testing"
)

type fakeToolRunner struct {
	calls []ToolCall
	out   map[string]string
	err   error
}

func (f *fakeToolRunner) Run(_ context.Context, call ToolCall) (string, error) {
	f.calls = append(f.calls, call)
	if f.err != nil {
		return "", f.err
	}
	return f.out[call.Function.Name], nil
}

// TestReadOnlyToolsAreAuthentic pins the canonical read-only set and that every
// declared tool has a well-formed JSON schema and is recognized as read-only.
func TestReadOnlyToolsAreAuthentic(t *testing.T) {
	tools := ReadOnlyTools()
	if len(tools) != 4 {
		t.Fatalf("ReadOnlyTools() = %d tools, want 4", len(tools))
	}
	byName := map[string]ToolDefinition{}
	for _, td := range tools {
		if td.Type != "function" {
			t.Errorf("tool %q type = %q, want function", td.Function.Name, td.Type)
		}
		if !json.Valid(td.Function.Parameters) {
			t.Errorf("tool %q parameters are not valid JSON: %s", td.Function.Name, td.Function.Parameters)
		}
		if !IsReadOnlyToolName(td.Function.Name) {
			t.Errorf("tool %q is not recognized as read-only", td.Function.Name)
		}
		byName[td.Function.Name] = td
	}
	for _, want := range []string{ToolReadFile, ToolListDirectory, ToolSearchCodebase, ToolSymbolLookup} {
		if _, ok := byName[want]; !ok {
			t.Errorf("ReadOnlyTools() missing %q", want)
		}
	}
	if IsReadOnlyToolName(ToolWriteFile) || IsReadOnlyToolName(ToolApplyPatch) {
		t.Error("mutating tools must never be classified read-only")
	}
}

// TestRunReadOnlyToolLoopExecutesAndCompletes: the model requests a read-only
// tool, the runtime executes it, feeds the result back, and returns the final
// textual answer.
func TestRunReadOnlyToolLoopExecutesAndCompletes(t *testing.T) {
	turn := 0
	var sawAssistantToolCalls, sawToolResult bool
	shot := func(_ context.Context, req Request) (*Response, error) {
		turn++
		if turn == 1 {
			return &Response{
				FinishReason: "tool_calls",
				ToolCalls: []ToolCall{{
					ID: "call-1", Type: "function",
					Function: ToolCallFunction{Name: ToolReadFile, Arguments: `{"path":"note.txt"}`},
				}},
			}, nil
		}
		for _, m := range req.Messages {
			if m.Role == "assistant" && len(m.ToolCalls) == 1 && m.ToolCalls[0].ID == "call-1" {
				sawAssistantToolCalls = true
			}
			if m.Role == "tool" && m.ToolCallID == "call-1" && m.Content == "FILE-CONTENT" {
				sawToolResult = true
			}
		}
		return &Response{Content: "final answer", FinishReason: "stop"}, nil
	}
	runner := &fakeToolRunner{out: map[string]string{ToolReadFile: "FILE-CONTENT"}}

	resp, err := RunReadOnlyToolLoop(context.Background(), shot, runner,
		Request{Messages: []Message{{Role: "user", Content: "read note.txt"}}}, ToolLoopOptions{})
	if err != nil {
		t.Fatalf("RunReadOnlyToolLoop: %v", err)
	}
	if resp == nil || resp.Content != "final answer" {
		t.Fatalf("final response = %+v, want 'final answer'", resp)
	}
	if len(runner.calls) != 1 || runner.calls[0].Function.Name != ToolReadFile {
		t.Fatalf("runner calls = %+v, want one read_file call", runner.calls)
	}
	if !sawAssistantToolCalls {
		t.Error("second turn must carry the assistant tool_calls message")
	}
	if !sawToolResult {
		t.Error("second turn must carry the tool result message")
	}
}

// TestRunReadOnlyToolLoopRejectsMutatingTool: a model request for a mutating
// tool receives a bounded error result and is never executed.
func TestRunReadOnlyToolLoopRejectsMutatingTool(t *testing.T) {
	turn := 0
	var toolResult string
	shot := func(_ context.Context, req Request) (*Response, error) {
		turn++
		if turn == 1 {
			return &Response{
				FinishReason: "tool_calls",
				ToolCalls: []ToolCall{{
					ID: "danger", Type: "function",
					Function: ToolCallFunction{Name: ToolWriteFile, Arguments: `{"path":"x","content":"y"}`},
				}},
			}, nil
		}
		for _, m := range req.Messages {
			if m.Role == "tool" {
				toolResult = m.Content
			}
		}
		return &Response{Content: "done", FinishReason: "stop"}, nil
	}
	runner := &fakeToolRunner{}
	if _, err := RunReadOnlyToolLoop(context.Background(), shot, runner, Request{}, ToolLoopOptions{}); err != nil {
		t.Fatalf("loop error: %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("mutating tool must never reach the runner, got %+v", runner.calls)
	}
	if toolResult == "" || toolResult == "done" {
		t.Fatalf("mutating tool must return a bounded error result, got %q", toolResult)
	}
}

// TestRunReadOnlyToolLoopBounded: a model that keeps calling tools is bounded
// and still returns its last response.
func TestRunReadOnlyToolLoopBounded(t *testing.T) {
	calls := 0
	shot := func(context.Context, Request) (*Response, error) {
		calls++
		return &Response{
			FinishReason: "tool_calls",
			ToolCalls: []ToolCall{{
				ID: "loop", Type: "function",
				Function: ToolCallFunction{Name: ToolListDirectory, Arguments: `{}`},
			}},
		}, nil
	}
	runner := &fakeToolRunner{out: map[string]string{ToolListDirectory: "a\nb"}}
	if _, err := RunReadOnlyToolLoop(context.Background(), shot, runner, Request{}, ToolLoopOptions{MaxIterations: 2}); err != nil {
		t.Fatalf("loop error: %v", err)
	}
	if calls != 2 {
		t.Fatalf("loop invoked provider %d times, want bounded 2", calls)
	}
}
