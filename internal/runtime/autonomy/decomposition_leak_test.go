package autonomy

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/execution/planner"
)

// ── stdio-leak fixtures & helpers ───────────────────────────────────────────

// htmlMonolithFixture renders a single contiguous top-level DOM section whose
// whole-file generation estimate trips the Boundary-2 preflight guard, so the
// driver stages a DECOMPOSITION_PROPOSAL built by the planner's line-slicing
// fallback (no natural sub-sections exist to split on).
func htmlMonolithFixture(lines int) []byte {
	var b strings.Builder
	b.WriteString(`<section id="monolith">` + "\n")
	for i := 1; i < lines-1; i++ {
		fmt.Fprintf(&b, `<div class="row-%d"><p>content block %d padded for the decomposition fixture body.</p></div>`+"\n", i, i)
	}
	b.WriteString("</section>\n")
	return []byte(b.String())
}

// captureStdio redirects the process stdout/stderr into drained pipes for the
// duration of a run. The returned closure restores both streams and yields
// whatever raw bytes leaked onto them while they were captured. Any raw byte
// on real stdio while Bubble Tea owns the altscreen corrupts the rendered
// frame (cursor jumps, dropped redraws, frozen screen).
func captureStdio(t *testing.T) func() (string, string) {
	t.Helper()
	origOut, origErr := os.Stdout, os.Stderr
	rOut, wOut, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	rErr, wErr, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout, os.Stderr = wOut, wErr
	outCh := make(chan string, 1)
	errCh := make(chan string, 1)
	go func() { b, _ := io.ReadAll(rOut); outCh <- string(b) }()
	go func() { b, _ := io.ReadAll(rErr); errCh <- string(b) }()
	return func() (string, string) {
		_ = wOut.Close()
		_ = wErr.Close()
		os.Stdout, os.Stderr = origOut, origErr
		stdoutLeak := <-outCh
		stderrLeak := <-errCh
		_ = rOut.Close()
		_ = rErr.Close()
		return stdoutLeak, stderrLeak
	}
}

// ── integration: fallback decomposition never spills raw text to stdio ──────

// TestDriver_DecompositionFallbackKeepsStdioSilentAndParksCleanly drives a
// preflight-infeasible single-DOM-section objective through the full
// line-slicing fallback path and asserts:
//
//  1. ZERO raw bytes reach os.Stdout/os.Stderr across BOTH the staging and
//     the approved atomic execution of every sub-task (the TUI frame can
//     never be corrupted by runtime diagnostics);
//  2. the standard library logger — redirected exactly like ui.runProgram
//     does while the altscreen is active — carries NO [boundary2]/[boundary5]
//     lines anymore: autonomy telemetry is routed through the injected
//     DiagnosticSink instead of the global logger;
//  3. the sink actually receives the structured telemetry (wiring is live);
//  4. the loop state machine transitions cleanly to the typed
//     HumanBoundaryDecomposition (decomposition_proposal) park without any
//     UI-facing side effect.
func TestDriver_DecompositionFallbackKeepsStdioSilentAndParksCleanly(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "index.html", string(htmlMonolithFixture(120)))

	p := &dagProvider{root: root, target: "index.html"}
	bus := events.NewBus(events.DefaultBufferSize)
	x := testExecutor(t, root, p, bus)
	driver := NewDriver(NewExecutorAdapter(root, execution.NewIntentGateway(root), x), bus)

	// Injected diagnostic sink, wired exactly like compose.Wire does.
	var mu sync.Mutex
	var diag []string
	SetDiagnosticLog(func(format string, args ...interface{}) {
		mu.Lock()
		defer mu.Unlock()
		diag = append(diag, fmt.Sprintf(format, args...))
	})
	defer SetDiagnosticLog(nil)

	// Standard-logger redirect, mirroring ui.runProgram under altscreen:
	// anything still logged through the global logger lands here instead of
	// the terminal, where we can prove autonomy no longer uses it.
	var logMu sync.Mutex
	var stdLoggerLines []string
	origLog := log.Writer()
	defer func() { log.SetOutput(origLog) }()
	log.SetOutput(writerFunc(func(p []byte) (int, error) {
		logMu.Lock()
		defer logMu.Unlock()
		stdLoggerLines = append(stdLoggerLines, strings.TrimSpace(string(p)))
		return len(p), nil
	}))

	getLeaks := captureStdio(t)

	term, err := driver.Run(context.Background(), "restyle every row @index.html")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// ── clean park at the typed DECOMPOSITION_PROPOSAL boundary ──────────
	if term != nil {
		t.Fatalf("run terminated (%+v) instead of parking at the proposal", term)
	}
	if driver.State() != autonomy.RuntimeAwaitingHuman {
		t.Fatalf("state = %s, want awaiting_human", driver.State())
	}
	history := driver.History()
	if len(history) == 0 || history[len(history)-1].To != autonomy.RuntimeAwaitingHuman {
		t.Fatalf("loop history must converge on awaiting_human, got %+v", history)
	}
	b := driver.Boundary()
	if b == nil || b.Action != autonomy.HumanBoundaryDecomposition {
		t.Fatalf("boundary = %+v, want %s", b, autonomy.HumanBoundaryDecomposition)
	}
	dag := b.Proposal
	if dag == nil {
		t.Fatal("parked boundary carries no DECOMPOSITION_PROPOSAL")
	}
	if dag.Status != planner.PlanStaged {
		t.Fatalf("plan status = %s, want %s", dag.Status, planner.PlanStaged)
	}
	if len(dag.SubTasks) < 3 {
		t.Fatalf("sub-tasks = %d, want >= 3 line-sliced windows", len(dag.SubTasks))
	}
	if err := dag.Validate(); err != nil {
		t.Fatalf("staged DAG failed Validate: %v", err)
	}
	for _, st := range dag.SubTasks {
		if st.Kind != planner.SplitBoundedLines {
			t.Errorf("%s kind = %s, want %s", st.ID, st.Kind, planner.SplitBoundedLines)
		}
	}

	// ── approved atomic execution of every sub-task, still silent ────────
	before := readTarget(t, root, "index.html")
	term2, err := driver.ResumeApproveProposal(context.Background())
	if err != nil {
		t.Fatalf("ResumeApproveProposal: %v", err)
	}
	if term2 == nil || term2.State != autonomy.RuntimeCompleted {
		t.Fatalf("termination = %+v, want completed", term2)
	}
	if driver.Plan().Status != planner.DagExecutionCompleted {
		t.Fatalf("plan status = %s, want %s", driver.Plan().Status, planner.DagExecutionCompleted)
	}
	after := readTarget(t, root, "index.html")
	if after == before || !strings.Contains(after, "patched-by-st-1") {
		t.Fatal("approved line-sliced sub-tasks never mutated the workspace")
	}

	stdoutLeak, stderrLeak := getLeaks()
	if stdoutLeak != "" {
		t.Errorf("fallback decomposition leaked raw bytes to stdout (corrupts TUI frame): %q", stdoutLeak)
	}
	if stderrLeak != "" {
		t.Errorf("fallback decomposition leaked raw bytes to stderr (corrupts TUI frame): %q", stderrLeak)
	}

	// Structured routing proof: the injected sink carried the staging and
	// per-sub-task telemetry instead of any raw stream.
	mu.Lock()
	defer mu.Unlock()
	joined := strings.Join(diag, "\n")
	for _, want := range []string{"DECOMPOSITION_PROPOSAL staged", "sub-task st-1 applied"} {
		if !strings.Contains(joined, want) {
			t.Errorf("diagnostic sink missing %q; captured:\n%s", want, joined)
		}
	}

	// The global standard logger must carry NO autonomy boundary lines.
	logMu.Lock()
	defer logMu.Unlock()
	for _, line := range stdLoggerLines {
		if strings.Contains(line, "[boundary2]") || strings.Contains(line, "[boundary5]") {
			t.Errorf("autonomy telemetry reached the standard logger (leaks under unredirected TUI): %q", line)
		}
	}
}

// writerFunc adapts a func to io.Writer.
type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }
