package execution

// ── PHASE 15: STREAM TOKEN SYNCHRONIZATION, END TO END ───────────────────────
//
// The unit tests for StreamTokenAggregator prove the type is correct. These prove
// it is WIRED — that the executor's terminal account is settled on the paths where
// the artifact boundary, not the transport, is what ended the run.
//
// The defect being pinned is specific: a stream that completes and then carries
// prose is a CONTRACT failure, and the contract boundary is downstream of the
// stream. Before this phase each exit path re-derived its own telemetry, so a
// path that terminated on a contract error could return before the counts the
// provider had already billed were folded into the result. The aggregate then
// diverged from the invoice, and the divergence was largest exactly where the
// money was being spent.

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/events"
)

// proseStreamBody is a completed provider stream carrying a well-written paragraph
// and no artifact. It is the exact payload that reaches the artifact boundary and
// is refused with ErrZeroArtifactsParsed.
const proseStreamBody = "I have updated the file for you.\n\n" +
	"Here is a summary of what changed and why each change was needed for the " +
	"requested objective, together with the reasoning behind the chosen approach."

// TestPhase15_ContractFailureStillReportsProviderBilling is the acceptance test.
// The run fails at the artifact boundary; the provider's authoritative usage must
// still reach ExecutionResult.Completed, because the provider billed those
// tokens whether or not they produced a valid artifact.
func TestPhase15_ContractFailureStillReportsProviderBilling(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "note.md", "old content\n")

	r := &streamingReader{
		data: proseStreamBody,
		usage: ai.ProviderUsage{
			Known:            true,
			PromptTokens:     5883,
			CompletionTokens: 421,
			FinishReason:     "stop",
		},
	}
	bus := events.NewBus(events.DefaultBufferSize)
	x := phase4Executor(t, root, &streamingProvider{reader: r}, bus)

	res, err := x.Execute(context.Background(), ExecuteRequest{
		Prompt:   "$hot change note.md",
		Targets:  []string{"note.md"},
		Strategy: executionStrategyProfile(t, "note.md"),
	})
	if err == nil {
		t.Fatal("a prose-only artifact was accepted — the artifact boundary regressed")
	}
	if !errors.Is(err, ErrZeroArtifactsParsed) {
		t.Fatalf("err = %v, want ErrZeroArtifactsParsed", err)
	}
	if res == nil {
		t.Fatal("a failed execution returned no result to account against")
	}
	// The provider's real billing survived a contract failure.
	if !res.Completed.Known {
		t.Fatal("the terminal account is unknown after a contract failure — the " +
			"provider's billing was dropped because the artifact was rejected")
	}
	if res.Completed.InputTokens != 5883 {
		t.Errorf("input tokens = %d, want the provider-reported 5883", res.Completed.InputTokens)
	}
	if res.Completed.OutputTokens != 421 {
		t.Errorf("output tokens = %d, want the provider-reported 421", res.Completed.OutputTokens)
	}
	// And the invocation evidence carries the same counts, so an audit and the
	// footer cannot disagree.
	if len(res.ModelCalls) == 0 {
		t.Fatal("no model invocation was retained on the failure path")
	}
	last := res.ModelCalls[len(res.ModelCalls)-1]
	if !last.Known || last.TokenInput != 5883 || last.TokenOutput != 421 {
		t.Errorf("invocation evidence = %+v, want the provider-reported 5883/421", last)
	}
	// Nothing was written: the failure is a refusal, not a partial mutation.
	if got := readTargetFile(t, root, "note.md"); strings.Contains(got, "I have updated the file") {
		t.Fatalf("prose reached the workspace: %q", got)
	}
}

// phasedUsageReader serves a byte stream while cycling through a scripted sequence
// of usage readings, one per Read. It is the shape of a provider that reports an
// optimistic estimate at dispatch and the authoritative total only once the
// stream completes.
type phasedUsageReader struct {
	mu     sync.Mutex
	data   string
	offset int
	reads  int
	phases []ai.ProviderUsage
}

func (r *phasedUsageReader) Read(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.reads < len(r.phases) {
		r.reads++
	}
	if r.offset >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.offset:])
	r.offset += n
	return n, nil
}

func (r *phasedUsageReader) Close() error { return nil }

func (r *phasedUsageReader) Usage() ai.ProviderUsage {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.phases) == 0 {
		return ai.ProviderUsage{}
	}
	// The LAST scripted phase is held from the final read onward, so the small
	// estimate is genuinely the frame the executor sees last.
	idx := r.reads - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(r.phases) {
		idx = len(r.phases) - 1
	}
	return r.phases[idx]
}

// readTargetFile reads a workspace file for an end-state assertion.
func readTargetFile(t *testing.T, root, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(data)
}

// TestPhase15_CancelledStreamStillReportsBilledPartialWork pins the other
// termination. A stream cut short by cancellation consumed real prompt tokens on
// the provider's side, and zeroing them would under-report a spend that happened.
func TestPhase15_CancelledStreamStillReportsBilledPartialWork(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "note.md", "old content\n")

	r := &streamingReader{
		data: proseStreamBody,
		usage: ai.ProviderUsage{
			Known:        true,
			PromptTokens: 1200,
		},
	}
	bus := events.NewBus(events.DefaultBufferSize)
	x := phase4Executor(t, root, &streamingProvider{reader: r}, bus)

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel on the first read so the stream terminates mid-flight rather than at
	// EOF — the shape that used to lose the account.
	r.onRead = cancel

	res, _ := x.Execute(ctx, ExecuteRequest{
		Prompt:   "$hot change note.md",
		Targets:  []string{"note.md"},
		Strategy: executionStrategyProfile(t, "note.md"),
	})
	if res == nil {
		t.Fatal("a cancelled execution returned no result")
	}
	if !res.Completed.Known {
		t.Fatal("a cancelled stream reported an unknown account — partial work on a " +
			"dead stream is billed and must be recorded")
	}
	if res.Completed.InputTokens != 1200 {
		t.Errorf("input tokens = %d, want the provider-reported 1200", res.Completed.InputTokens)
	}
}

// TestPhase15_LateEstimateNeverClobbersTheAuthoritativeTotal pins the merge rule
// where it actually bit, and it is the closest thing here to the reported
// symptom: "aggregate token metrics diverge from provider billing logs".
//
// Several real providers report an OPTIMISTIC PROMPT ESTIMATE at dispatch and the
// authoritative total only at the end. Read last-write-wins, the final small
// estimate overwrites the real total and the aggregate ends up reporting the
// estimate as the bill — a number that is lower than what was actually charged,
// by an amount nobody can reconstruct afterwards.
//
// The phased reader below reproduces exactly that shape: an authoritative reading
// on the early frames, then a smaller estimated reading on the last one.
func TestPhase15_LateEstimateNeverClobbersTheAuthoritativeTotal(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "note.md", "old content\n")

	r := &phasedUsageReader{
		data: strings.Repeat("payload line\n", 400),
		phases: []ai.ProviderUsage{
			// The authoritative account, available while the stream is live.
			{Known: true, PromptTokens: 5883, CompletionTokens: 902, FinishReason: "stop"},
			// A late, smaller ESTIMATE — the frame that used to win.
			{Known: true, Estimated: true, PromptTokens: 2181},
		},
	}
	bus := events.NewBus(events.DefaultBufferSize)
	x := phase4Executor(t, root, &streamingProvider{reader: r}, bus)

	res, err := x.Execute(context.Background(), ExecuteRequest{
		Prompt:   "$hot change note.md",
		Targets:  []string{"note.md"},
		Strategy: executionStrategyProfile(t, "note.md"),
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.Completed.Known {
		t.Fatal("the terminal account is unknown")
	}
	if res.Completed.InputTokens != 5883 {
		t.Fatalf("input tokens = %d, want the authoritative 5883 — a late estimate "+
			"overwrote the provider's real total", res.Completed.InputTokens)
	}
	if res.Completed.OutputTokens != 902 {
		t.Fatalf("output tokens = %d, want the authoritative 902", res.Completed.OutputTokens)
	}
	if len(res.ModelCalls) == 0 {
		t.Fatal("no invocation evidence was retained")
	}
	last := res.ModelCalls[len(res.ModelCalls)-1]
	if last.TokenInput != 5883 || last.TokenOutput != 902 {
		t.Fatalf("invocation evidence = %d/%d, want 5883/902", last.TokenInput, last.TokenOutput)
	}
}

// TestPhase15_RepeatedCumulativeReadingsDoNotDoubleBill pins the merge rule at
// the integration level. A provider that re-reports the same cumulative counter on
// every chunk is normal; summing those frames would multiply the invoice.
func TestPhase15_RepeatedCumulativeReadingsDoNotDoubleBill(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "note.md", "old content\n")

	r := &streamingReader{
		data:  strings.Repeat("payload line\n", 400),
		usage: ai.ProviderUsage{Known: true, PromptTokens: 640, CompletionTokens: 210},
	}
	bus := events.NewBus(events.DefaultBufferSize)
	x := phase4Executor(t, root, &streamingProvider{reader: r}, bus)

	res, err := x.Execute(context.Background(), ExecuteRequest{
		Prompt:   "$hot change note.md",
		Targets:  []string{"note.md"},
		Strategy: executionStrategyProfile(t, "note.md"),
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Completed.InputTokens > 640 {
		t.Fatalf("input tokens = %d, want at most the cumulative 640 — repeated "+
			"readings of the same counter were summed", res.Completed.InputTokens)
	}
	if res.Completed.OutputTokens > 210 {
		t.Fatalf("output tokens = %d, want at most the cumulative 210", res.Completed.OutputTokens)
	}
	if !res.Completed.Known {
		t.Fatal("the account is unknown for a stream that reported usage")
	}
}
