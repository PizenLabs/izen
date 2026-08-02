package plan

import (
	"context"
	"io"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
)

// mockStreamResult is a test double for ai.Provider.ExecuteStream results: an
// io.ReadCloser that also reports provider usage and the terminal finish
// reason, exactly like the OpenRouter SSE reader used in production.
type mockStreamResult struct {
	data   string
	pos    int
	finish string
	input  int
	output int
}

func (m *mockStreamResult) Read(p []byte) (int, error) {
	if m.pos >= len(m.data) {
		return 0, io.EOF
	}
	n := copy(p, m.data[m.pos:])
	m.pos += n
	return n, nil
}

func (m *mockStreamResult) Close() error { return nil }

func (m *mockStreamResult) Usage() (int, int) { return m.input, m.output }

func (m *mockStreamResult) FinishReason() string { return m.finish }

var _ ai.FinishReasonProvider = (*mockStreamResult)(nil)

const validPlanJSON = `{"context_anchor":{"source":"ledger","target_packages":["internal/modes/plan"]},"architectural_strategy":"retain truncated stream buffer","strategic_overview":{"root_core_factor":"empty response on length","impact_domain":"plan","risk_evaluation":"low","verification_vector":"go test ./internal/modes/plan/"},"atomic_tasks":[{"task_id":1,"file":"internal/modes/plan/engine.go","strategy":"FILE_MUTATE","description":"retain partial stream content on finish_reason length"},{"task_id":2,"file":"go build ./...","strategy":"SHELL_EXEC","description":"compile the plan engine"}]}`

// TestProcessFromLedgerTruncatedStream is the regression guard for the empty
// response bug: when the provider truncates the response (finish_reason
// "length") the accumulated streaming buffer must still be returned as valid
// content, so the plan engine parses tasks instead of failing with
// "plan engine: empty response from provider". Provider-reported usage must
// also be committed via LastUsage despite the truncation.
func TestProcessFromLedgerTruncatedStream(t *testing.T) {
	streamed := &mockStreamResult{
		data:   validPlanJSON,
		finish: "length",
		input:  128,
		output: 96,
	}

	e := NewEngine(NewPlanStore())
	e.SetStreamProvider(func(ctx context.Context, req ai.Request) (io.ReadCloser, error) {
		if !req.Stream {
			t.Error("streaming request did not set Stream=true")
		}
		return streamed, nil
	})

	tasks, err := e.ProcessFromLedger(context.Background(), "", "plan engine returns empty response on truncation", "test-model")
	if err != nil {
		t.Fatalf("ProcessFromLedger with finish_reason=length: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("got %d tasks, want 2 (truncated content must be parsed): %+v", len(tasks), tasks)
	}

	in, out := e.LastUsage()
	if in != 128 || out != 96 {
		t.Errorf("LastUsage = (%d, %d), want (128, 96) even though stream was truncated", in, out)
	}
}

// TestAccumulateStreamTruncationAgnostic pins the core contract of
// accumulateStream: a stream that ends with finish_reason "length" yields its
// partial buffer as content — never an empty string — and reports both the
// finish reason and provider usage.
func TestAccumulateStreamTruncationAgnostic(t *testing.T) {
	streamed := &mockStreamResult{
		data:   validPlanJSON,
		finish: "length",
		input:  64,
		output: 32,
	}
	content, finishReason, in, out := accumulateStream(streamed)
	if content == "" {
		t.Fatal("accumulateStream returned empty content for a truncated stream")
	}
	if content != validPlanJSON {
		t.Errorf("accumulateStream corrupted content:\nwant %s\ngot  %s", validPlanJSON, content)
	}
	if finishReason != "length" {
		t.Errorf("FinishReason = %q, want %q", finishReason, "length")
	}
	if in != 64 || out != 32 {
		t.Errorf("usage = (%d, %d), want (64, 32)", in, out)
	}
}

// TestAccumulateStreamPartialRunesAcrossReads ensures the rune-safe buffer
// reassembles a UTF-8 sequence split across multiple Read calls instead of
// emitting replacement characters.
func TestAccumulateStreamPartialRunesAcrossReads(t *testing.T) {
	content := `{"context_anchor":{"source":"测试","target_packages":["内部"]},"architectural_strategy":"保留流缓冲","atomic_tasks":[{"task_id":1,"file":"内/文件.go","strategy":"FILE_MUTATE","description":"保留截断内容"}]}`
	s := &mockStreamResult{data: content}
	var partial mockStreamResult
	partial.data = content

	got, _, _, _ := accumulateStream(&oneByteAtATime{s})
	if got != content {
		t.Errorf("chunked read corrupted multibyte runes:\nwant %s\ngot  %s", content, got)
	}
}

// oneByteAtATime forces every Read to return a single byte so the RuneBuffer
// path for split UTF-8 sequences is exercised.
type oneByteAtATime struct {
	inner io.Reader
}

func (o *oneByteAtATime) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	one := make([]byte, 1)
	n, err := o.inner.Read(one)
	if n > 0 {
		p[0] = one[0]
	}
	return n, err
}
