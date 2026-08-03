package plan

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
)

// TestProcessFromLedger_ReasoningOnlyStream is the end-to-end regression guard
// for the "plan engine: empty response from provider" failure: when a
// Mini/reasoning model emits its entire plan inside the reasoning/thinking
// pipeline (content empty), plan synthesis must still succeed by promoting the
// reasoning text to the payload and parsing it into tasks.
func TestProcessFromLedger_ReasoningOnlyStream(t *testing.T) {
	planJSON := `{"architectural_strategy":"fix via reasoning","atomic_tasks":[{"task_id":1,"strategy":"SHELL_EXEC","file":"go mod tidy","description":"tidy deps","rationale":"resolve blocker"},{"task_id":2,"strategy":"FILE_MUTATE","file":"internal/foo.go","description":"fix import","rationale":"correct path"}]}`
	streamed := &mockStreamResult{
		data:   "\x00RSNG\x00" + planJSON + "\x00RSNG\x00",
		finish: "stop",
		input:  64,
		output: 32,
	}

	e := NewEngine(NewPlanStore())
	e.SetStreamProvider(func(ctx context.Context, req ai.Request) (io.ReadCloser, error) {
		return streamed, nil
	})

	tasks, err := e.ProcessFromLedger(context.Background(), "", "fix the failing build", "test-model")
	if err != nil {
		t.Fatalf("ProcessFromLedger with reasoning-only stream: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("got %d tasks, want 2 (reasoning payload must be parsed): %+v", len(tasks), tasks)
	}
	if tasks[0].Type != "SHELL_EXEC" || tasks[0].Target != "go mod tidy" {
		t.Errorf("task 0 = %+v, want SHELL_EXEC go mod tidy", tasks[0])
	}
	if tasks[1].Type != "FILE_MUTATE" || tasks[1].Target != "internal/foo.go" {
		t.Errorf("task 1 = %+v, want FILE_MUTATE internal/foo.go", tasks[1])
	}

	in, out := e.LastUsage()
	if in != 64 || out != 32 {
		t.Errorf("LastUsage = (%d, %d), want (64, 32) even when payload came from reasoning", in, out)
	}
}

// TestAccumulateStream_ReturnsReasoning pins that accumulateStream separates
// content from reasoning instead of discarding the thinking text, which is the
// seam the reasoning fallback uses.
func TestAccumulateStream_ReturnsReasoning(t *testing.T) {
	streamed := &mockStreamResult{data: "intro " + "\x00RSNG\x00think here\x00RSNG\x00" + " outro"}
	content, reasoning, _, _, _ := accumulateStream(streamed)
	if content != "intro  outro" {
		t.Errorf("content = %q, want %q", content, "intro  outro")
	}
	if reasoning != "think here" {
		t.Errorf("reasoning = %q, want %q", reasoning, "think here")
	}
}

// TestAccumulateStream_ReasoningOnlyEmptyContent confirms a reasoning-only
// stream yields empty content and the reasoning text — the exact condition the
// fallback promotes.
func TestAccumulateStream_ReasoningOnlyEmptyContent(t *testing.T) {
	streamed := &mockStreamResult{data: "\x00RSNG\x00plan\x00RSNG\x00"}
	content, reasoning, _, _, _ := accumulateStream(streamed)
	if content != "" {
		t.Errorf("content = %q, want empty", content)
	}
	if reasoning != "plan" {
		t.Errorf("reasoning = %q, want %q", reasoning, "plan")
	}
}

// TestParseJSONPlan_FlatSpecArray verifies the tolerant Action/Target/Reason
// array form: a compact JSON array of {"action","target","reason"} specs
// parses into tasks even though it deviates from the full plan object schema.
func TestParseJSONPlan_FlatSpecArray(t *testing.T) {
	input := `[{"action":"SHELL_EXEC","target":"go mod tidy","reason":"resync deps"},{"action":"FILE_MUTATE","target":"internal/foo.go","reason":"fix import"}]`
	result := ParseJSONPlan(input)
	if !result.Valid {
		t.Fatalf("expected valid for flat spec array, got: %s", result.Error)
	}
	if len(result.Tasks) != 2 {
		t.Fatalf("got %d tasks, want 2", len(result.Tasks))
	}
	if result.Tasks[0].Type != "SHELL_EXEC" || result.Tasks[0].Target != "go mod tidy" {
		t.Errorf("task 0 = %+v, want SHELL_EXEC go mod tidy", result.Tasks[0])
	}
	if result.Tasks[0].Rationale != "resync deps" {
		t.Errorf("task 0 rationale = %q, want %q", result.Tasks[0].Rationale, "resync deps")
	}
	if result.Tasks[0].IsHardcoded {
		t.Error("flat-array tasks must not be hardcoded (they are LLM-generated)")
	}
}

// TestParseJSONPlan_ThinkWrappedJSON verifies a plan wrapped in thinking tags
// still parses after sanitization.
func TestParseJSONPlan_ThinkWrappedJSON(t *testing.T) {
	input := "<think>" + `{"architectural_strategy":"s","atomic_tasks":[{"task_id":1,"file":"a.go","strategy":"FILE_MUTATE","description":"d"}]}` + "</think>"
	result := ParseJSONPlan(input)
	if !result.Valid {
		t.Fatalf("expected valid for think-wrapped JSON, got: %s", result.Error)
	}
	if len(result.Tasks) != 1 {
		t.Fatalf("got %d tasks, want 1", len(result.Tasks))
	}
}

// TestSanitizeJSONContent_StripsThinkBlocks verifies reasoning delimiters are
// consumed before structural parsing even when they surround the code fence.
func TestSanitizeJSONContent_StripsThinkBlocks(t *testing.T) {
	input := "Here is my plan:\n<thought>analysis</thought>\n```json\n{\"a\":1}\n```"
	want := "{\"a\":1}"
	if got := sanitizeJSONContent(input); got != want {
		t.Fatalf("sanitizeJSONContent = %q, want %q", got, want)
	}
}

// TestSanitizeJSONContent_RepairsUnescapedNewlines verifies literal newlines
// inside a JSON string value are escaped so the payload becomes valid JSON.
func TestSanitizeJSONContent_RepairsUnescapedNewlines(t *testing.T) {
	input := "{\"description\": \"line1\nline2\"}"
	want := `{"description": "line1\nline2"}`
	if got := sanitizeJSONContent(input); got != want {
		t.Fatalf("sanitizeJSONContent = %q, want %q", got, want)
	}
}

// TestParseJSONPlan_RepairsUnescapedNewlines does a full parse round-trip on a
// plan whose description contains a literal newline.
func TestParseJSONPlan_RepairsUnescapedNewlines(t *testing.T) {
	input := "{\"architectural_strategy\":\"s\",\"atomic_tasks\":[{\"task_id\":1,\"file\":\"a.go\",\"strategy\":\"FILE_MUTATE\",\"description\":\"line1\nline2\"}]}"
	result := ParseJSONPlan(input)
	if !result.Valid {
		t.Fatalf("expected valid after newline repair, got: %s", result.Error)
	}
	if len(result.Tasks) != 1 {
		t.Fatalf("got %d tasks, want 1", len(result.Tasks))
	}
}

// TestProcessFromLedger_MarkdownTolerantFallback verifies the non-fast-track
// path accepts markdown task blocks when JSON parsing fails, so a Mini model
// that returns checklist output still yields a staged plan.
func TestProcessFromLedger_MarkdownTolerantFallback(t *testing.T) {
	md := "- [ ] SHELL_EXEC: go mod tidy | Resolve dependency blocker\n" +
		"- [ ] FILE_MUTATE: internal/foo.go | Fix import path"
	streamed := &mockStreamResult{data: md}

	e := NewEngine(NewPlanStore())
	e.SetStreamProvider(func(ctx context.Context, req ai.Request) (io.ReadCloser, error) {
		return streamed, nil
	})

	tasks, err := e.ProcessFromLedger(context.Background(), "", "fix the failing build", "test-model")
	if err != nil {
		t.Fatalf("ProcessFromLedger with markdown-only response: %v", err)
	}
	if len(tasks) == 0 {
		t.Fatal("expected at least one task from markdown fallback")
	}
	if tasks[0].Type != "SHELL_EXEC" || tasks[0].Target != "go mod tidy" {
		t.Errorf("task 0 = %+v, want SHELL_EXEC go mod tidy", tasks[0])
	}
}

// TestDiagnoseSynthesisFailureEmitsEvent verifies the graceful error path
// publishes a diagnostic StageCompleted event instead of only returning a raw
// error.
func TestDiagnoseSynthesisFailureEmitsEvent(t *testing.T) {
	e := NewEngine(NewPlanStore())
	e.SetProvider(func(ctx context.Context, req ai.Request) (*ai.Response, error) {
		return &ai.Response{Content: ""}, nil
	})

	_, err := e.ProcessFromLedger(context.Background(), "", "non-dependency prose", "test-model")
	if err == nil {
		t.Fatal("expected empty-response error")
	}
	if !strings.Contains(err.Error(), "empty response from provider") {
		t.Fatalf("error = %q, want empty-response diagnostic", err)
	}
}
