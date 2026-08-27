package telemetry

import (
	"sync"
	"time"
)

// ── High-precision latency instrumentation ───────────────────────────────────
//
// Three canonical latencies are measured at nanosecond precision:
//
//   prompt_submit_latency : time spent inside submit_prompt (target <10ms)
//   preflight_latency     : duration of BackgroundPreflight worker execution
//   first_stream_latency  : total elapsed from user Enter to first LLM token

// Recorder is a thread-safe high-precision latency sink.
type Recorder struct {
	mu sync.Mutex

	promptSubmitLatency time.Duration
	preflightLatency    time.Duration
	firstStreamLatency  time.Duration
	firstStreamStart    time.Time
	promptSubmitCount   int64
	preflightCount      int64
}

// NewRecorder returns an empty recorder.
func NewRecorder() *Recorder { return &Recorder{} }

var defaultRecorder = NewRecorder()

// Default returns the process-wide recorder.
func Default() *Recorder { return defaultRecorder }

// RecordPromptSubmit records prompt_submit_latency.
func (r *Recorder) RecordPromptSubmit(d time.Duration) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.promptSubmitLatency = d
	r.promptSubmitCount++
}

// RecordPreflight records preflight_latency.
func (r *Recorder) RecordPreflight(d time.Duration) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.preflightLatency = d
	r.preflightCount++
}

// MarkPromptEntered marks the wall-clock entry of user Enter for
// first_stream_latency accounting.
func (r *Recorder) MarkPromptEntered(t time.Time) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.firstStreamStart = t
}

// RecordFirstToken records first_stream_latency as the delta from the last
// MarkPromptEntered.
func (r *Recorder) RecordFirstToken(at time.Time) time.Duration {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.firstStreamStart.IsZero() {
		return 0
	}
	d := at.Sub(r.firstStreamStart)
	r.firstStreamLatency = d
	return d
}

// Snapshot returns the current latencies.
func (r *Recorder) Snapshot() (promptSubmit, preflight, firstStream time.Duration) {
	if r == nil {
		return 0, 0, 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.promptSubmitLatency, r.preflightLatency, r.firstStreamLatency
}

// Reset clears recorded latencies.
func (r *Recorder) Reset() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.promptSubmitLatency = 0
	r.preflightLatency = 0
	r.firstStreamLatency = 0
	r.firstStreamStart = time.Time{}
}

// Global helpers that delegate to the default recorder.

// RecordPromptSubmit records prompt_submit_latency on the default recorder.
func RecordPromptSubmit(d time.Duration) { defaultRecorder.RecordPromptSubmit(d) }

// RecordPreflight records preflight_latency on the default recorder.
func RecordPreflight(d time.Duration) { defaultRecorder.RecordPreflight(d) }

// MarkPromptEntered marks user Enter on the default recorder.
func MarkPromptEntered(t time.Time) { defaultRecorder.MarkPromptEntered(t) }

// RecordFirstToken records first_stream_latency on the default recorder.
func RecordFirstToken(at time.Time) time.Duration { return defaultRecorder.RecordFirstToken(at) }
