package execution

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// ── Execution Telemetry (Phase 3) ──────────────────────────────────────────
//
// Telemetry is the authoritative per-operation execution record. It attributes
// wall-clock latency to real runtime stages by logging every stage boundary
// (target, read, context, model/provider, patch, validate, apply) exactly as
// the runtime reaches it — never fabricated, never inferred from a spinner.
//
// The record is a log of markers. At Snapshot time the markers are folded into
// stage spans: a span runs from the first marker of a contiguous stage run to
// its last marker, so started_at/completed_at/elapsed are measured from real
// boundaries. Provider (model) runs are further split into request → waiting →
// first-token → streaming → terminal so provider latency is distinguishable
// from local post-provider processing.
//
// The record is race-safe (mutex-guarded) because workers on several
// goroutines publish into it while the UI goroutine reads it. It carries an
// operation ID so every event is traceable to its operation, an invocation
// counter so duplicate provider calls are detectable, and a retry counter so
// retry latency is attributed to the retry.

// StageState is the truthful state of a stage at a marker boundary.
type StageState string

const (
	// StageRunning: a local stage is actively executing.
	StageRunning StageState = "running"
	// StageWaiting: the runtime is blocked on an external dependency (a
	// provider round-trip before the first byte, a subprocess, a filesystem).
	StageWaiting StageState = "waiting"
	// StageStreaming: provider tokens are actively arriving.
	StageStreaming StageState = "streaming"
	// Terminal states.
	StageDone      StageState = "done"
	StageFailed    StageState = "failed"
	StageCancelled StageState = "cancelled"
	StageTimedOut  StageState = "timedout"
)

// Terminal reports whether the state is a terminal marker.
func (s StageState) Terminal() bool {
	switch s {
	case StageDone, StageFailed, StageCancelled, StageTimedOut:
		return true
	}
	return false
}

// marker is one real execution-boundary observation. Elapsed carries a
// caller-measured wall-clock duration when the boundary has one (e.g. a file
// read's elapsed, a provider's reported duration); zero otherwise.
type marker struct {
	At      time.Time
	Stage   string
	Target  string
	State   StageState
	Bytes   int64
	Tokens  int
	Elapsed time.Duration
}

// StageSpan is the folded, terminal stage record of one execution stage.
type StageSpan struct {
	Stage       string        `json:"stage"`
	Target      string        `json:"target,omitempty"`
	Attempt     int           `json:"attempt"`
	State       StageState    `json:"state"`
	StartedAt   time.Time     `json:"started_at"`
	CompletedAt time.Time     `json:"completed_at"`
	Elapsed     time.Duration `json:"elapsed"`
	Bytes       int64         `json:"bytes,omitempty"`
	Tokens      int           `json:"tokens,omitempty"`
}

// ProviderSpan is the folded provider-invocation record for one model run. It
// splits the invocation into request → waiting → first token → streaming →
// terminal so provider latency is attributed precisely.
type ProviderSpan struct {
	Model        string        `json:"model"`
	Attempt      int           `json:"attempt"`
	RequestAt    time.Time     `json:"request_at"`
	FirstTokenAt time.Time     `json:"first_token_at,omitempty"`
	CompletedAt  time.Time     `json:"completed_at"`
	Waiting      time.Duration `json:"waiting"`
	Streaming    time.Duration `json:"streaming"`
	State        StageState    `json:"state"`
	Tokens       int           `json:"tokens,omitempty"`
}

// TelemetrySnapshot is an immutable copy of a Telemetry record, safe to render
// from any goroutine.
type TelemetrySnapshot struct {
	OpID        string         `json:"op_id"`
	Kind        string         `json:"kind"`
	StartedAt   time.Time      `json:"started_at"`
	CompletedAt time.Time      `json:"completed_at"`
	Outcome     string         `json:"outcome"`
	Stages      []StageSpan    `json:"stages"`
	Providers   []ProviderSpan `json:"providers"`
	Invocations int            `json:"invocations"`
	Retries     int            `json:"retries"`
	LiveWorkers []string       `json:"live_workers"`
	Elapsed     time.Duration  `json:"elapsed"`
}

// Telemetry is the authoritative per-operation execution record. It is safe
// for concurrent use.
type Telemetry struct {
	mu        sync.Mutex
	now       func() time.Time
	OpID      string
	Kind      string
	StartedAt time.Time

	markers     []marker
	finalized   bool
	outcome     string
	completedAt time.Time

	workers *WorkerTracker
}

// NewTelemetry starts a fresh execution record for the given operation.
func NewTelemetry(opID, kind string) *Telemetry {
	return NewTelemetryAt(opID, kind, time.Now)
}

// NewTelemetryAt starts a record with an injectable clock (test seam).
func NewTelemetryAt(opID, kind string, now func() time.Time) *Telemetry {
	if now == nil {
		now = time.Now
	}
	return &Telemetry{
		now:       now,
		OpID:      opID,
		Kind:      kind,
		StartedAt: now(),
		workers:   NewWorkerTracker(),
	}
}

// Workers returns the worker-lifetime tracker owned by this record.
func (t *Telemetry) Workers() *WorkerTracker {
	if t == nil {
		return nil
	}
	return t.workers
}

// Record appends one execution-boundary marker. It is a no-op after the record
// is finalized. bytes/tokens/elapsed may be zero to omit the metric.
func (t *Telemetry) Record(stage, target string, state StageState, bytes int64, tokens int, elapsed time.Duration) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.finalized {
		return
	}
	t.markers = append(t.markers, marker{
		At:      t.now(),
		Stage:   stage,
		Target:  target,
		State:   state,
		Bytes:   bytes,
		Tokens:  tokens,
		Elapsed: elapsed,
	})
}

// Finalize closes the record with a terminal outcome. It is idempotent. Any
// stage still in flight (non-terminal last marker) is coerced to a terminal
// state derived from the outcome so no stage can survive the operation.
func (t *Telemetry) Finalize(outcome string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.finalized {
		return
	}
	t.finalized = true
	t.outcome = outcome
	t.completedAt = t.now()

	// Coerce any in-flight stage to the outcome's terminal state.
	if len(t.markers) > 0 {
		last := &t.markers[len(t.markers)-1]
		if !last.State.Terminal() {
			term := terminalStateForOutcome(outcome)
			// Emit a synthetic terminal marker at the true completion time so
			// the stage span's elapsed stays measured from real boundaries.
			last.State = term
		}
	}
}

// terminalStateForOutcome maps an operation outcome onto the terminal stage
// state used to close any stage left in flight at finalization.
func terminalStateForOutcome(outcome string) StageState {
	switch outcome {
	case "cancelled":
		return StageCancelled
	case "timeout", "timedout":
		return StageTimedOut
	case "failure", "failed":
		return StageFailed
	default:
		return StageDone
	}
}

// Finalized reports whether the record was closed.
func (t *Telemetry) Finalized() bool {
	if t == nil {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.finalized
}

// Snapshot folds the marker log into immutable stage/provider spans.
func (t *Telemetry) Snapshot() TelemetrySnapshot {
	if t == nil {
		return TelemetrySnapshot{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	snap := TelemetrySnapshot{
		OpID:        t.OpID,
		Kind:        t.Kind,
		StartedAt:   t.StartedAt,
		CompletedAt: t.completedAt,
		Outcome:     t.outcome,
	}
	if !snap.CompletedAt.IsZero() {
		snap.Elapsed = snap.CompletedAt.Sub(snap.StartedAt)
		if snap.Elapsed < 0 {
			snap.Elapsed = 0
		}
	}
	if t.workers != nil {
		snap.LiveWorkers = t.workers.Live()
	}

	stages, providers, invocations := foldMarkers(t.markers)
	snap.Stages = stages
	snap.Providers = providers
	snap.Invocations = invocations
	if invocations > 1 {
		snap.Retries = invocations - 1
	}
	return snap
}

// stageRun is a contiguous run of markers sharing the same stage, terminated
// by a terminal marker. A new attempt of the same stage (a retry) opens a new
// run even when no intervening stage was recorded.
type stageRun struct {
	stage  string
	target string
	start  marker
	last   marker
	bytes  int64
	tokens int
	first  int // index into markers of the run start
}

// foldMarkers groups marker runs into stage spans and splits model runs into
// provider spans. Invocations is the number of model runs (each is one
// provider call). A same-stage re-entry after a terminal marker is a retry and
// produces a separate span, so retry latency is attributed to the retry.
func foldMarkers(ms []marker) (stages []StageSpan, providers []ProviderSpan, invocations int) {
	if len(ms) == 0 {
		return nil, nil, 0
	}

	var runs []stageRun
	for i, m := range ms {
		if len(runs) == 0 || runs[len(runs)-1].stage != m.Stage || runs[len(runs)-1].last.State.Terminal() {
			runs = append(runs, stageRun{stage: m.Stage, target: m.Target, start: m, last: m, first: i})
			continue
		}
		r := &runs[len(runs)-1]
		r.last = m
		if m.Bytes > 0 {
			r.bytes = m.Bytes
		}
		if m.Tokens > 0 {
			r.tokens = m.Tokens
		}
	}

	for i := range runs {
		r := &runs[i]
		span := StageSpan{
			Stage:       r.stage,
			Target:      r.target,
			Attempt:     attemptOf(runs[:i], r.stage),
			State:       r.last.State,
			StartedAt:   r.start.At,
			CompletedAt: r.last.At,
			Elapsed:     r.last.At.Sub(r.start.At),
			Bytes:       r.bytes,
			Tokens:      r.tokens,
		}
		if span.Elapsed < 0 {
			span.Elapsed = 0
		}
		stages = append(stages, span)

		if r.stage == "model" {
			invocations++
			providers = append(providers, foldProvider(r, ms))
		}
	}
	return stages, providers, invocations
}

// attemptOf returns the zero-based retry attempt for a stage run: the number
// of earlier runs of the same stage.
func attemptOf(earlier []stageRun, stage string) int {
	n := 0
	for i := range earlier {
		if earlier[i].stage == stage {
			n++
		}
	}
	return n
}

// foldProvider splits a model run into the provider invocation phases:
// request → waiting → first-token → streaming → terminal.
func foldProvider(r *stageRun, all []marker) ProviderSpan {
	p := ProviderSpan{
		Model:       r.target,
		RequestAt:   r.start.At,
		CompletedAt: r.last.At,
		State:       r.last.State,
		Tokens:      r.tokens,
	}

	firstTokenAt := p.CompletedAt
	streamingFrom := p.CompletedAt
	// Prefer a caller-measured elapsed marker: some paths report the actual
	// round-trip duration directly.
	if r.last.Elapsed > 0 {
		p.Waiting = r.last.Elapsed
	}
	// Scan the run's markers for the streaming (first-token) boundary.
	for j := r.first; j < len(all); j++ {
		m := all[j]
		if m.Stage != "model" {
			break
		}
		if m.State == StageStreaming {
			firstTokenAt = m.At
			streamingFrom = m.At
			break
		}
	}
	p.FirstTokenAt = firstTokenAt
	if p.Waiting <= 0 {
		p.Waiting = firstTokenAt.Sub(p.RequestAt)
	}
	if p.Waiting < 0 {
		p.Waiting = 0
	}
	if p.CompletedAt.After(streamingFrom) {
		p.Streaming = p.CompletedAt.Sub(streamingFrom)
	}
	if p.Streaming < 0 {
		p.Streaming = 0
	}
	return p
}

// ── Worker lifetime tracking ───────────────────────────────────────────────

// WorkerTracker is a race-safe registry of the live workers an operation
// spawned. The terminal-lifecycle tests assert that every spawned worker is
// released before the operation finalizes, so no worker can outlive it.
type WorkerTracker struct {
	mu   sync.Mutex
	live map[string]int
}

// NewWorkerTracker returns an empty tracker.
func NewWorkerTracker() *WorkerTracker {
	return &WorkerTracker{live: make(map[string]int)}
}

// Spawn registers one live worker under a label.
func (w *WorkerTracker) Spawn(label string) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.live[label]++
}

// Release unregisters one live worker under a label. It is a no-op when the
// label has no live count (double-release safety).
func (w *WorkerTracker) Release(label string) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.live[label] > 0 {
		w.live[label]--
		if w.live[label] == 0 {
			delete(w.live, label)
		}
	}
}

// Live returns the sorted labels of workers still running.
func (w *WorkerTracker) Live() []string {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]string, 0, len(w.live))
	for label, n := range w.live {
		if n > 0 {
			out = append(out, label)
		}
	}
	sort.Strings(out)
	return out
}

// Count returns the total number of live worker registrations.
func (w *WorkerTracker) Count() int {
	if w == nil {
		return 0
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	n := 0
	for _, c := range w.live {
		n += c
	}
	return n
}

// ── Timeline rendering (debug / inspect view) ──────────────────────────────

// RenderTimeline formats the snapshot as a compact execution telemetry
// timeline: operation identity, state, elapsed, and per-stage/provider rows
// with real started/completed offsets. It carries NO reasoning content — only
// execution metadata.
func (s TelemetrySnapshot) RenderTimeline() string {
	var b strings.Builder
	state := "RUNNING"
	if !s.CompletedAt.IsZero() {
		switch s.Outcome {
		case "success", "":
			state = "DONE"
		case "cancelled":
			state = "CANCELLED"
		case "timeout":
			state = "TIMED OUT"
		case "failure":
			state = "FAILED"
		default:
			state = strings.ToUpper(s.Outcome)
		}
	}
	fmt.Fprintf(&b, "Execution: %s\n", s.OpID)
	fmt.Fprintf(&b, "State: %s\n", state)
	if !s.StartedAt.IsZero() {
		fmt.Fprintf(&b, "Elapsed: %s\n", formatTelemetryDuration(s.Elapsed))
	}

	origin := s.StartedAt
	if origin.IsZero() {
		origin = time.Now()
	}

	type row struct {
		off  time.Duration
		line string
	}
	var rows []row

	hasProviders := len(s.Providers) > 0
	for _, sp := range s.Stages {
		// Provider rows carry the precise request/waiting/first-token/
		// streaming attribution; suppress the generic model stage rows so the
		// timeline never shows the same stage twice.
		if sp.Stage == "model" && hasProviders {
			continue
		}
		started := sp.StartedAt.Sub(origin)
		if started < 0 {
			started = 0
		}
		rows = append(rows, row{started, fmt.Sprintf("%-9s started", sp.Stage)})
		completed := sp.CompletedAt.Sub(origin)
		if completed < 0 {
			completed = 0
		}
		detail := fmt.Sprintf("%-9s %-11s %s", sp.Stage, sp.State, formatTelemetryDuration(sp.Elapsed))
		if sp.Bytes > 0 {
			detail += fmt.Sprintf("  bytes=%d", sp.Bytes)
		}
		if sp.Tokens > 0 {
			detail += fmt.Sprintf("  tokens=%d", sp.Tokens)
		}
		rows = append(rows, row{completed, detail})
	}

	// Provider rows carry the precise request/waiting/first-token/streaming
	// attribution, superseding the generic model stage rows.
	for _, p := range s.Providers {
		reqOff := p.RequestAt.Sub(origin)
		if reqOff < 0 {
			reqOff = 0
		}
		rows = append(rows, row{reqOff, fmt.Sprintf("%-9s request-started  model=%s", "model", labelOrModel(p.Model))})
		if !p.FirstTokenAt.IsZero() {
			ftOff := p.FirstTokenAt.Sub(origin)
			if ftOff < 0 {
				ftOff = 0
			}
			rows = append(rows, row{ftOff, fmt.Sprintf("%-9s first-token     waiting=%s", "model", formatTelemetryDuration(p.Waiting))})
		}
		doneOff := p.CompletedAt.Sub(origin)
		if doneOff < 0 {
			doneOff = 0
		}
		rows = append(rows, row{doneOff, fmt.Sprintf("%-9s %-11s tokens=%d streaming=%s", "model", p.State, p.Tokens, formatTelemetryDuration(p.Streaming))})
	}

	sort.SliceStable(rows, func(i, j int) bool {
		return rows[i].off < rows[j].off
	})

	for _, r := range rows {
		fmt.Fprintf(&b, "%07.3f  %s\n", r.off.Seconds(), r.line)
	}

	if len(s.Providers) > 0 {
		b.WriteString("\nprovider:")
		for _, p := range s.Providers {
			wait := p.Waiting
			if wait < 0 {
				wait = 0
			}
			stream := p.Streaming
			if stream < 0 {
				stream = 0
			}
			fmt.Fprintf(&b, "\n  %s  request→first-token %s  first-token→done %s  tokens=%d  state=%s",
				labelOrModel(p.Model), formatTelemetryDuration(wait), formatTelemetryDuration(stream), p.Tokens, p.State)
		}
	}
	fmt.Fprintf(&b, "\ninvocations=%d retries=%d live-workers=%d",
		s.Invocations, s.Retries, len(s.LiveWorkers))
	if len(s.LiveWorkers) > 0 {
		fmt.Fprintf(&b, " [%s]", strings.Join(s.LiveWorkers, ","))
	}
	return b.String()
}

func labelOrModel(s string) string {
	if s == "" {
		return "(model)"
	}
	return s
}

// formatTelemetryDuration renders a duration like "4ms" / "4.2s" / "1m 3s".
func formatTelemetryDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Millisecond:
		return d.String()
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return d.Round(10 * time.Millisecond).String()
	default:
		return fmt.Sprintf("%dm %s", int(d.Minutes()), (d % time.Minute).Round(time.Second))
	}
}
