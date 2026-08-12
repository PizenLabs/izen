package execution

import (
	"strings"
	"sync"
)

// reasoningMarkers are the observable traces a model's chain-of-thought may
// leak into its public output. Detection is conservative: a match is a
// substring hit on any marker, lower-cased on both sides.
var reasoningMarkers = []string{
	"thought for",
	"<thought>",
	"</thought>",
	"<thinking>",
	"</thinking>",
	"let me think step by step",
}

// HasReasoningLeak reports whether raw contains any reasoning marker. It is a
// pure, cheap scan used by the observer and by tests.
func HasReasoningLeak(raw string) bool {
	if raw == "" {
		return false
	}
	lower := strings.ToLower(raw)
	for _, m := range reasoningMarkers {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return false
}

// ReasoningLeakObserver monitors raw LLM output for leaked chain-of-thought
// reasoning markers and emits a telemetry warning for provider quality
// tracking. It is strictly observational:
//
//   - Inspect returns immediately; the scan runs in a background goroutine.
//   - It never returns an error, never cancels, never retries, and never
//     influences the pipeline's accept/reject decision.
//   - A nil logger silently drops the warning; the counters still track.
type ReasoningLeakObserver struct {
	mu        sync.Mutex
	wg        sync.WaitGroup
	logFn     func(format string, args ...interface{})
	leaked    int
	inspected int
	closed    bool
}

// NewReasoningLeakObserver returns an observer that logs warnings through
// logFn. A nil logFn falls back to the package-wide activity logger; when that
// is also unset the warning is dropped silently.
func NewReasoningLeakObserver(logFn func(format string, args ...interface{})) *ReasoningLeakObserver {
	return &ReasoningLeakObserver{logFn: logFn}
}

// Inspect schedules an asynchronous scan of raw. It NEVER blocks the caller:
// the scan runs on its own goroutine and the method returns immediately.
func (o *ReasoningLeakObserver) Inspect(raw string) {
	o.mu.Lock()
	if o.closed {
		o.mu.Unlock()
		return
	}
	o.inspected++
	o.wg.Add(1)
	o.mu.Unlock()

	go func() {
		defer o.wg.Done()
		if HasReasoningLeak(raw) {
			o.record()
		}
	}()
}

// record increments the leak counter and emits exactly one telemetry warning.
func (o *ReasoningLeakObserver) record() {
	o.mu.Lock()
	o.leaked++
	logFn := o.logFn
	o.mu.Unlock()

	if logFn == nil {
		logFn = globalActivityLog
	}
	if logFn != nil {
		logFn("[TELEMETRY] warning: reasoning leakage detected")
	}
}

// LeakCount returns the number of outputs flagged so far.
func (o *ReasoningLeakObserver) LeakCount() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.leaked
}

// Inspected returns the number of inspections scheduled so far.
func (o *ReasoningLeakObserver) Inspected() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.inspected
}

// Wait blocks until every in-flight inspection has finished. Used by tests and
// by pipeline shutdown; it is never called from the hot path.
func (o *ReasoningLeakObserver) Wait() {
	o.wg.Wait()
}

// Close prevents further inspections. Pending inspections still drain.
func (o *ReasoningLeakObserver) Close() {
	o.mu.Lock()
	o.closed = true
	o.mu.Unlock()
	o.wg.Wait()
}
