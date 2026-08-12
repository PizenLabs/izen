package execution

import (
	"strings"
	"sync/atomic"
	"testing"
)

func TestHasReasoningLeak(t *testing.T) {
	for _, leaky := range []string{
		"Thought for a second, then I wrote the code",
		"let me think step by step: first the parser",
		"<thought>this is my private reasoning</thought>",
		"<thinking>should I refactor?</thinking>",
		"prefix <thought>mid</thought> suffix",
	} {
		if !HasReasoningLeak(leaky) {
			t.Errorf("reasoning leak not detected in %q", leaky)
		}
	}
	for _, clean := range []string{
		"",
		"just plain code output",
		"package main\nfunc main() {}\n",
		"Think different.",
		"thought", // bare word, not a marker
	} {
		if HasReasoningLeak(clean) {
			t.Errorf("false positive on clean output %q", clean)
		}
	}
}

func TestReasoningLeakObserverEmitsTelemetry(t *testing.T) {
	var warnings int64
	obs := NewReasoningLeakObserver(func(format string, args ...interface{}) {
		atomic.AddInt64(&warnings, 1)
		msg := format
		if len(args) > 0 {
			msg = strings.ReplaceAll(msg, "%s", args[0].(string))
		}
		if !strings.Contains(msg, "reasoning leakage detected") {
			t.Errorf("warning must carry the reasoning leakage message, got %q", msg)
		}
	})

	obs.Inspect("here is my output with <thinking>internal reasoning</thinking>")
	obs.Inspect("clean output")
	obs.Inspect("Another Thought for marker")
	obs.Wait()

	if got := atomic.LoadInt64(&warnings); got != 2 {
		t.Errorf("warnings = %d, want 2 (leaked only)", got)
	}
	if obs.LeakCount() != 2 {
		t.Errorf("LeakCount = %d, want 2", obs.LeakCount())
	}
	if obs.Inspected() != 3 {
		t.Errorf("Inspected = %d, want 3", obs.Inspected())
	}
}

func TestReasoningLeakObserverNeverBlocks(t *testing.T) {
	obs := NewReasoningLeakObserver(nil)
	// A huge payload must not make Inspect block; it returns immediately and
	// the scan drains asynchronously.
	obs.Inspect(strings.Repeat("x", 1<<20) + "Thought for a moment")
	obs.Wait()
	if obs.LeakCount() != 1 {
		t.Errorf("LeakCount = %d, want 1", obs.LeakCount())
	}
}

func TestReasoningLeakObserverNilLoggerSafe(t *testing.T) {
	obs := NewReasoningLeakObserver(nil)
	obs.Inspect("<thought>leak</thought>")
	obs.Wait()
	if obs.LeakCount() != 1 {
		t.Errorf("nil logger must still count leaks, got %d", obs.LeakCount())
	}
}

func TestReasoningLeakObserverCloseDrains(t *testing.T) {
	obs := NewReasoningLeakObserver(nil)
	obs.Inspect("<thinking>x</thinking>")
	obs.Close()
	if obs.LeakCount() != 1 {
		t.Errorf("Close must drain pending inspections, LeakCount = %d", obs.LeakCount())
	}
	// After Close, new inspections are refused.
	obs.Inspect("<thought>y</thought>")
	obs.Wait()
	if obs.Inspected() != 1 {
		t.Errorf("Inspected after Close = %d, want 1 (refused)", obs.Inspected())
	}
}
