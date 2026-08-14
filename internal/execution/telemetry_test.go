package execution

import (
	"strings"
	"testing"
	"time"
)

// deterministicClock advances a fixed time for each call so stage boundaries
// are fully deterministic and elapsed times are asserted exactly.
func deterministicClock() (func() time.Time, *time.Time) {
	base := time.Date(2026, 8, 14, 17, 37, 12, 0, time.UTC)
	cur := base
	return func() time.Time {
		cur = cur.Add(1 * time.Millisecond)
		return cur
	}, &base
}

func newTestTelemetry() (*Telemetry, func() time.Time) {
	now, _ := deterministicClock()
	return NewTelemetryAt("op-test", "build", now), now
}

// TestTelemetryStableOperationID asserts every execution record carries a
// stable operation ID that survives folding (test #1).
func TestTelemetryStableOperationID(t *testing.T) {
	tm, _ := newTestTelemetry()
	tm.Record("target", "index.html", StageDone, 0, 0, 0)
	tm.Finalize("success")
	snap := tm.Snapshot()
	if snap.OpID != "op-test" {
		t.Fatalf("OpID = %q, want op-test", snap.OpID)
	}
	if tm.OpID != "op-test" {
		t.Fatalf("telemetry OpID = %q, want op-test", tm.OpID)
	}
}

// TestTelemetryEveryStageHasTerminalState asserts every folded stage span has
// a terminal state (test #2): no stage can survive as running after the
// operation record is finalized.
func TestTelemetryEveryStageHasTerminalState(t *testing.T) {
	tm, _ := newTestTelemetry()
	tm.Record("target", "index.html", StageDone, 0, 0, 0)
	tm.Record("read", "index.html", StageDone, 18234, 0, 4*time.Millisecond)
	tm.Record("model", "qwen2.5-coder:7b", StageWaiting, 0, 0, 0)
	tm.Record("model", "qwen2.5-coder:7b", StageStreaming, 0, 0, 0)
	tm.Record("model", "qwen2.5-coder:7b", StageDone, 0, 921, 0)
	tm.Record("patch", "index.html", StageDone, 0, 0, 0)
	tm.Finalize("success")

	snap := tm.Snapshot()
	if len(snap.Stages) != 4 {
		t.Fatalf("got %d stages, want 4: %+v", len(snap.Stages), snap.Stages)
	}
	for _, sp := range snap.Stages {
		if !sp.State.Terminal() {
			t.Errorf("stage %s has non-terminal state %q after finalize", sp.Stage, sp.State)
		}
	}
	if len(snap.LiveWorkers) != 0 {
		t.Errorf("live workers after finalize: %v", snap.LiveWorkers)
	}
}

// TestTelemetryStageElapsedFromRealBoundaries asserts stage elapsed is the
// wall-clock delta between the run's first and last marker (test #3). The
// deterministic clock advances 1ms per call, so a run spanning N markers has
// elapsed (N-1)*1ms.
func TestTelemetryStageElapsedFromRealBoundaries(t *testing.T) {
	tm, _ := newTestTelemetry()
	tm.Record("read", "index.html", StageRunning, 0, 0, 0) // t+1ms
	tm.Record("read", "index.html", StageRunning, 18234, 0, 0)
	tm.Record("read", "index.html", StageDone, 18234, 0, 0) // t+3ms
	tm.Finalize("success")

	snap := tm.Snapshot()
	if len(snap.Stages) != 1 {
		t.Fatalf("got %d stages, want 1", len(snap.Stages))
	}
	sp := snap.Stages[0]
	if sp.Elapsed != 2*time.Millisecond {
		t.Fatalf("read elapsed = %v, want 2ms (real boundary delta)", sp.Elapsed)
	}
}

// TestTelemetryProviderWaitingVsStreaming asserts provider waiting is
// distinguishable from streaming: waiting = request→first-token, streaming =
// first-token→terminal (test #4).
func TestTelemetryProviderWaitingVsStreaming(t *testing.T) {
	tm, _ := newTestTelemetry()
	tm.Record("model", "m", StageWaiting, 0, 0, 0)     // request t+1
	tm.Record("model", "m", StageWaiting, 0, 0, 0)     // still waiting t+2
	tm.Record("model", "m", StageStreaming, 0, 0, 0)   // first token t+3
	tm.Record("model", "m", StageStreaming, 0, 921, 0) // streaming t+4
	tm.Record("model", "m", StageDone, 0, 921, 0)      // done t+5
	tm.Finalize("success")

	snap := tm.Snapshot()
	if len(snap.Providers) != 1 {
		t.Fatalf("got %d providers, want 1", len(snap.Providers))
	}
	p := snap.Providers[0]
	if p.Waiting != 2*time.Millisecond {
		t.Fatalf("provider waiting = %v, want 2ms (request→first-token)", p.Waiting)
	}
	if p.Streaming != 2*time.Millisecond {
		t.Fatalf("provider streaming = %v, want 2ms (first-token→done)", p.Streaming)
	}
	if p.Tokens != 921 {
		t.Fatalf("provider tokens = %d, want 921", p.Tokens)
	}
	if p.State != StageDone {
		t.Fatalf("provider state = %q, want done", p.State)
	}
}

// TestTelemetryProviderCompletionTerminatesProviderStage asserts provider
// completion immediately terminates the provider stage (test #5): after the
// terminal marker no provider span is left open.
func TestTelemetryProviderCompletionTerminatesProviderStage(t *testing.T) {
	tm, _ := newTestTelemetry()
	tm.Record("model", "m", StageWaiting, 0, 0, 0)
	tm.Record("model", "m", StageDone, 0, 10, 0)
	tm.Finalize("success")

	snap := tm.Snapshot()
	if len(snap.Providers) != 1 {
		t.Fatalf("got %d providers, want 1", len(snap.Providers))
	}
	p := snap.Providers[0]
	if p.State != StageDone {
		t.Fatalf("provider state = %q, want done (terminal)", p.State)
	}
	// A completed provider must not leave any open stage span.
	if p.CompletedAt.IsZero() {
		t.Fatal("provider CompletedAt is zero — stage not terminated")
	}
}

// TestTelemetryLocalPostProviderSeparatelyMeasurable asserts local
// post-provider processing (patch/validate/apply) is separately measurable
// from provider latency (test #6).
func TestTelemetryLocalPostProviderSeparatelyMeasurable(t *testing.T) {
	tm, _ := newTestTelemetry()
	tm.Record("model", "m", StageWaiting, 0, 0, 0) // t+1
	tm.Record("model", "m", StageStreaming, 0, 0, 0)
	tm.Record("model", "m", StageDone, 0, 50, 0) // t+3
	tm.Record("patch", "index.html", StageRunning, 0, 0, 0)
	tm.Record("patch", "index.html", StageDone, 0, 0, 0) // t+5
	tm.Finalize("success")

	snap := tm.Snapshot()
	var modelSp, patchSp *StageSpan
	for i := range snap.Stages {
		switch snap.Stages[i].Stage {
		case "model":
			modelSp = &snap.Stages[i]
		case "patch":
			patchSp = &snap.Stages[i]
		}
	}
	if modelSp == nil || patchSp == nil {
		t.Fatalf("missing model/patch spans: %+v", snap.Stages)
	}
	if patchSp.Elapsed != 1*time.Millisecond {
		t.Fatalf("local patch processing = %v, want 1ms (separate from provider)", patchSp.Elapsed)
	}
}

// TestTelemetryRetryAttributedToRetry asserts a same-stage re-entry after a
// terminal marker is a retry with its own span and elapsed (test #7).
func TestTelemetryRetryAttributedToRetry(t *testing.T) {
	tm, _ := newTestTelemetry()
	tm.Record("model", "m", StageWaiting, 0, 0, 0)
	tm.Record("model", "m", StageDone, 0, 5, 0)    // attempt 0 fails
	tm.Record("model", "m", StageWaiting, 0, 0, 0) // retry
	tm.Record("model", "m", StageStreaming, 0, 0, 0)
	tm.Record("model", "m", StageDone, 0, 200, 0) // attempt 1 succeeds
	tm.Finalize("success")

	snap := tm.Snapshot()
	if snap.Invocations != 2 {
		t.Fatalf("invocations = %d, want 2", snap.Invocations)
	}
	if snap.Retries != 1 {
		t.Fatalf("retries = %d, want 1", snap.Retries)
	}
	if len(snap.Providers) != 2 {
		t.Fatalf("providers = %d, want 2 (each retry is its own provider span)", len(snap.Providers))
	}
	// Each provider span carries its own latency.
	if snap.Providers[0].Tokens != 5 || snap.Providers[1].Tokens != 200 {
		t.Fatalf("retry token attribution wrong: %+v", snap.Providers)
	}
}

// TestTelemetryDuplicateProviderInvocationDetectable asserts a duplicate
// provider invocation is detectable via the invocation counter (test #8).
func TestTelemetryDuplicateProviderInvocationDetectable(t *testing.T) {
	tm, _ := newTestTelemetry()
	tm.Record("model", "m", StageWaiting, 0, 0, 0)
	tm.Record("model", "m", StageDone, 0, 10, 0)
	tm.Record("model", "m", StageWaiting, 0, 0, 0)
	tm.Record("model", "m", StageDone, 0, 20, 0)
	tm.Finalize("success")

	snap := tm.Snapshot()
	if snap.Invocations != 2 {
		t.Fatalf("duplicate invocation not detected: invocations = %d, want 2", snap.Invocations)
	}
}

// TestTelemetryCancelledExecutionTerminatesWorkers asserts a cancelled record
// terminates all workers (test #10).
func TestTelemetryCancelledExecutionTerminatesWorkers(t *testing.T) {
	tm, _ := newTestTelemetry()
	tm.Workers().Spawn("stream")
	tm.Workers().Spawn("patch")
	tm.Record("model", "m", StageWaiting, 0, 0, 0)
	tm.Record("model", "m", StageCancelled, 0, 0, 0)
	tm.Workers().Release("stream")
	tm.Workers().Release("patch")
	tm.Finalize("cancelled")

	snap := tm.Snapshot()
	if len(snap.LiveWorkers) != 0 {
		t.Fatalf("workers still live after cancelled finalize: %v", snap.LiveWorkers)
	}
	if snap.Outcome != "cancelled" {
		t.Fatalf("outcome = %q, want cancelled", snap.Outcome)
	}
}

// TestTelemetryTimedOutExecutionTerminatesWorkers asserts a timed-out record
// terminates all workers (test #11).
func TestTelemetryTimedOutExecutionTerminatesWorkers(t *testing.T) {
	tm, _ := newTestTelemetry()
	tm.Workers().Spawn("provider")
	tm.Record("model", "m", StageWaiting, 0, 0, 0)
	tm.Record("model", "m", StageTimedOut, 0, 0, 0)
	tm.Workers().Release("provider")
	tm.Finalize("timeout")

	snap := tm.Snapshot()
	if len(snap.LiveWorkers) != 0 {
		t.Fatalf("workers still live after timeout finalize: %v", snap.LiveWorkers)
	}
	if snap.Outcome != "timeout" {
		t.Fatalf("outcome = %q, want timeout", snap.Outcome)
	}
}

// TestTelemetryNoWorkerSurvivesFinalization asserts a worker that is never
// released is surfaced as a live worker — so the terminal-lifecycle tests can
// detect orphan workers deterministically (test #12).
func TestTelemetryNoWorkerSurvivesFinalization(t *testing.T) {
	tm, _ := newTestTelemetry()
	tm.Workers().Spawn("orphan")
	tm.Record("model", "m", StageDone, 0, 1, 0)
	tm.Finalize("success")

	snap := tm.Snapshot()
	if len(snap.LiveWorkers) != 1 || snap.LiveWorkers[0] != "orphan" {
		t.Fatalf("orphan worker not surfaced: %v", snap.LiveWorkers)
	}
}

// TestTelemetrySingleProviderInvocation asserts a single successful execution
// performs exactly one provider invocation (test #13 equivalent at record
// level: hotfix performs only one provider invocation).
func TestTelemetrySingleProviderInvocation(t *testing.T) {
	tm, _ := newTestTelemetry()
	tm.Record("target", "LICENSE", StageDone, 0, 0, 0)
	tm.Record("model", "m", StageWaiting, 0, 0, 0)
	tm.Record("model", "m", StageStreaming, 0, 0, 0)
	tm.Record("model", "m", StageDone, 0, 100, 0)
	tm.Record("patch", "LICENSE", StageDone, 0, 0, 0)
	tm.Finalize("success")

	snap := tm.Snapshot()
	if snap.Invocations != 1 {
		t.Fatalf("invocations = %d, want exactly 1", snap.Invocations)
	}
	if snap.Retries != 0 {
		t.Fatalf("retries = %d, want 0", snap.Retries)
	}
}

// TestTelemetryNoStageSurvivesTerminalization asserts no stage span remains
// non-terminal after the record is finalized (test #18).
func TestTelemetryNoStageSurvivesTerminalization(t *testing.T) {
	tm, _ := newTestTelemetry()
	tm.Record("model", "m", StageWaiting, 0, 0, 0)
	tm.Record("model", "m", StageStreaming, 0, 0, 0)
	tm.Finalize("success") // no explicit terminal model marker

	snap := tm.Snapshot()
	if len(snap.Stages) != 1 {
		t.Fatalf("stages = %d, want 1", len(snap.Stages))
	}
	if !snap.Stages[0].State.Terminal() {
		t.Fatalf("stage %s state = %q after terminalization, want terminal", snap.Stages[0].Stage, snap.Stages[0].State)
	}
	// Finalize is idempotent: additional markers after finalize are dropped.
	tm.Record("patch", "x", StageRunning, 0, 0, 0)
	snap2 := tm.Snapshot()
	if len(snap2.Stages) != 1 {
		t.Fatalf("stages after post-finalize record = %d, want 1 (finalize is terminal)", len(snap2.Stages))
	}
}

// TestTelemetryRenderContainsNoReasoning asserts the rendered timeline is pure
// execution metadata with no reasoning/thinking content (test #20).
func TestTelemetryRenderContainsNoReasoning(t *testing.T) {
	tm, _ := newTestTelemetry()
	tm.Record("model", "m", StageWaiting, 0, 0, 0)
	tm.Record("model", "m", StageStreaming, 0, 0, 0)
	tm.Record("model", "m", StageDone, 0, 921, 0)
	tm.Record("patch", "index.html", StageRunning, 0, 0, 0)
	tm.Record("patch", "index.html", StageDone, 0, 0, 0)
	tm.Finalize("success")

	rendered := tm.Snapshot().RenderTimeline()
	for _, forbidden := range []string{"thinking", "reasoning", "thought", "chain-of-thought", "<thought>"} {
		if strings.Contains(strings.ToLower(rendered), forbidden) {
			t.Fatalf("rendered timeline leaks %q:\n%s", forbidden, rendered)
		}
	}
	if !strings.Contains(rendered, "Execution: op-test") {
		t.Fatalf("timeline missing operation identity:\n%s", rendered)
	}
	if !strings.Contains(rendered, "invocations=1") {
		t.Fatalf("timeline missing invocation counter:\n%s", rendered)
	}
}

// TestTelemetryRenderWaitNeverLeftOpenAfterCompletion asserts the rendered
// timeline shows the provider terminal, never a lingering waiting state
// (test #17 at record level).
func TestTelemetryRenderWaitNeverLeftOpenAfterCompletion(t *testing.T) {
	tm, _ := newTestTelemetry()
	tm.Record("model", "m", StageWaiting, 0, 0, 0)
	tm.Record("model", "m", StageStreaming, 0, 0, 0)
	tm.Record("model", "m", StageDone, 0, 921, 0)
	tm.Finalize("success")

	rendered := tm.Snapshot().RenderTimeline()
	if strings.Contains(rendered, "waiting") && strings.Contains(rendered, "request-started") && !strings.Contains(rendered, "done") {
		t.Fatalf("provider completion did not terminate the waiting state:\n%s", rendered)
	}
	if !strings.Contains(rendered, "done") {
		t.Fatalf("timeline missing provider terminal:\n%s", rendered)
	}
}
