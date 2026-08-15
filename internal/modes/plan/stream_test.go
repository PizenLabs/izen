package plan

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/core/stream"
	"github.com/PizenLabs/izen/internal/events"
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

func (m *mockStreamResult) Usage() ai.ProviderUsage {
	return ai.ProviderUsage{
		PromptTokens:     m.input,
		CompletionTokens: m.output,
		TotalTokens:      m.input + m.output,
		FinishReason:     m.finish,
		Known:            true,
	}
}

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
	content, _, finishReason, in, out := accumulateStream(streamed)
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

	got, _, _, _, _ := accumulateStream(&oneByteAtATime{s})
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

// TestProcessFromLedgerNilResponseOnRetryNoPanic is the regression guard for
// the nil-pointer dereference panic in the emergency fallback: when the
// provider returns a non-empty (but unparseable) first response and then
// returns a nil response with a nil error on every retry, the retry loop
// exhausts with resp == nil. The engine must never dereference the nil
// response — with the heuristic fallback hard-killed it returns an explicit
// error instead of panicking or fabricating a plan.
func TestProcessFromLedgerNilResponseOnRetryNoPanic(t *testing.T) {
	calls := 0
	e := NewEngine(NewPlanStore())
	e.SetProvider(func(ctx context.Context, req ai.Request) (*ai.Response, error) {
		calls++
		if calls == 1 {
			// Non-empty content that fails both JSON parsing and markdown task
			// extraction, forcing the loop into the retry path.
			return &ai.Response{Content: "prose, not json, not tasks"}, nil
		}
		// Every retry returns a nil response with no error.
		return nil, nil
	})

	tasks, err := e.ProcessFromLedger(context.Background(), "", "no parseable plan", "test-model")
	if err == nil {
		t.Fatal("expected an explicit error (heuristic fallback is hard-killed), got nil")
	}
	if len(tasks) != 0 {
		t.Fatalf("got %d tasks, want 0: %+v", len(tasks), tasks)
	}
	if calls < 3 {
		t.Errorf("provider called %d times, want at least 3 (initial + retries)", calls)
	}
}

// waitForReasoningDelivery polls the bus-delivered reasoning stream until the
// expected terminal event and at least one chunk have arrived (delivery is
// asynchronous). Returns the concatenated chunk text.
func waitForReasoningDelivery(t *testing.T, mu *sync.Mutex, chunks *[]string, complete *bool) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		done := *complete
		joined := strings.Join(*chunks, "")
		mu.Unlock()
		if done && joined != "" {
			return joined
		}
		if time.Now().After(deadline) {
			return joined
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestProcessFromLedgerPublishesReasoningStreamFromSentinels verifies that
// reasoning/thinking tokens embedded as sentinel markers in the raw stream
// (the OpenRouter transport) are continuously published to the event bus as
// EventReasoningStream chunks during plan synthesis, and that a terminal
// IsComplete event closes the block — the UI can then render live thinking.
func TestProcessFromLedgerPublishesReasoningStreamFromSentinels(t *testing.T) {
	bus := events.NewBus(64)
	defer bus.Close()

	var mu sync.Mutex
	var chunks []string
	complete := false
	bus.Subscribe(events.EventReasoningStream, func(ev events.DomainEvent) {
		p := ev.Payload().(events.ReasoningPayload)
		mu.Lock()
		defer mu.Unlock()
		if p.IsComplete {
			complete = true
			return
		}
		chunks = append(chunks, p.Chunk)
	})

	streamed := &mockStreamResult{
		data: stream.ReasoningSentinel + "thinking hard " + stream.ReasoningSentinel + validPlanJSON,
	}
	e := NewEngine(NewPlanStore()).WithEventBus(bus)
	e.SetStreamProvider(func(ctx context.Context, req ai.Request) (io.ReadCloser, error) {
		return streamed, nil
	})

	tasks, err := e.ProcessFromLedger(context.Background(), "", "fix the plan", "test-model")
	if err != nil {
		t.Fatalf("ProcessFromLedger: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("got %d tasks, want 2", len(tasks))
	}

	joined := waitForReasoningDelivery(t, &mu, &chunks, &complete)
	if joined != "thinking hard " {
		t.Errorf("reasoning chunks = %q, want %q", joined, "thinking hard ")
	}
	mu.Lock()
	done := complete
	mu.Unlock()
	if !done {
		t.Error("terminal reasoning event (IsComplete) not published")
	}
}

// TestProcessFromLedgerPublishesReasoningViaHandler verifies the provider-side
// reasoning channel: providers (OpenAI/Claude/Gemini/Ollama/Groq) route
// reasoning deltas through req.ReasoningHandler. The plan engine must wire that
// handler to the event bus so thinking is forwarded even when it never appears
// in the raw stream.
func TestProcessFromLedgerPublishesReasoningViaHandler(t *testing.T) {
	bus := events.NewBus(64)
	defer bus.Close()

	var mu sync.Mutex
	var chunks []string
	complete := false
	bus.Subscribe(events.EventReasoningStream, func(ev events.DomainEvent) {
		p := ev.Payload().(events.ReasoningPayload)
		mu.Lock()
		defer mu.Unlock()
		if p.IsComplete {
			complete = true
			return
		}
		chunks = append(chunks, p.Chunk)
	})

	e := NewEngine(NewPlanStore()).WithEventBus(bus)
	e.SetStreamProvider(func(ctx context.Context, req ai.Request) (io.ReadCloser, error) {
		if req.ReasoningHandler == nil {
			t.Error("plan engine did not wire the request ReasoningHandler")
		} else {
			// OpenAI-style: reasoning routed exclusively via the handler.
			_ = req.ReasoningHandler("handler reasoning ")
		}
		return &mockStreamResult{data: validPlanJSON}, nil
	})

	tasks, err := e.ProcessFromLedger(context.Background(), "", "fix the plan", "test-model")
	if err != nil {
		t.Fatalf("ProcessFromLedger: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("got %d tasks, want 2", len(tasks))
	}

	joined := waitForReasoningDelivery(t, &mu, &chunks, &complete)
	if joined != "handler reasoning " {
		t.Errorf("reasoning chunks = %q, want %q", joined, "handler reasoning ")
	}
	mu.Lock()
	done := complete
	mu.Unlock()
	if !done {
		t.Error("terminal reasoning event (IsComplete) not published")
	}
}
