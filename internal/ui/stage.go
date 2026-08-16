package ui

import (
	"fmt"
	"sync"
	"time"

	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/ui/status"
)

// ── Authoritative execution-stage record ────────────────────────────────────
//
// execStage is the SINGLE source of truth for "what is the runtime doing right
// now". It is updated ONLY at real execution boundaries — target resolution,
// file read, provider invocation, provider streaming, patch compilation,
// validation, mutation apply — and every progress indicator (loading dock,
// status line, processing dock) derives from it. The renderer never fabricates
// a stage: a stage exists only when the runtime actually reached it, so the UI
// can never imply work that is not happening.
//
// The mutex makes the record safe to update from worker command bodies (the
// patch-generation goroutines) and to read from the UI goroutine's View pass —
// the same concurrency pattern as ActivityTree.

// execStageState is the truthful runtime state of the active execution stage.
type execStageState string

const (
	// stageRunning: a local stage is actively executing on this runtime.
	stageRunning execStageState = "running"
	// stageWaiting: the runtime is blocked on an external dependency (a
	// provider round-trip before the first byte, a subprocess, a filesystem).
	stageWaiting execStageState = "waiting"
	// stageStreaming: provider tokens are actively arriving.
	stageStreaming execStageState = "streaming"
	// stageBlocked: the watchdog observed no runtime progress for a long
	// window — the stage is genuinely stalled, never merely animated.
	stageBlocked execStageState = "blocked"
	// Terminal states.
	stageDone      execStageState = "done"
	stageFailed    execStageState = "failed"
	stageCancelled execStageState = "cancelled"
)

// execStage is the authoritative single execution-stage record.
type execStage struct {
	mu    sync.Mutex
	Kind  OperationKind
	Label string // canonical stage name: target|read|model|patch|validate|apply
	// Target is the stage's concrete subject (file path or model name).
	Target string
	State  execStageState
	// LastTs is when the stage last changed state (or received progress). It
	// anchors the truthful "waiting · 4.2s" / "blocked" durations.
	LastTs time.Time
	// Bytes is the real bytes read for the read stage.
	Bytes int64
	// Elapsed is the real wall-clock duration of the last completed stage.
	Elapsed time.Duration
	// Tokens is the real provider-reported / streamed token count.
	Tokens int
	// Telemetry is the per-operation execution record fed from this stage's
	// boundaries. It is attached at beginOperation (UI goroutine) and read by
	// setStage/setStageMetrics/finishStage — including from worker goroutines —
	// so it lives under this stage's mutex to stay race-safe. It is never
	// detached mid-operation; Record is a no-op after the record is finalized.
	Telemetry *execution.Telemetry
}

// stageView is a lock-free snapshot of the authoritative stage consumed by the
// renderer. It carries no mutex so it can be copied freely across goroutines.
type stageView struct {
	Kind    OperationKind
	Label   string
	Target  string
	State   execStageState
	LastTs  time.Time
	Bytes   int64
	Elapsed time.Duration
	Tokens  int
}

// view returns a consistent lock-free snapshot of the stage.
func (s *execStage) view() stageView {
	if s == nil {
		return stageView{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return stageView{
		Kind:    s.Kind,
		Label:   s.Label,
		Target:  s.Target,
		State:   s.State,
		LastTs:  s.LastTs,
		Bytes:   s.Bytes,
		Elapsed: s.Elapsed,
		Tokens:  s.Tokens,
	}
}

// active reports whether the snapshot belongs to an in-flight execution.
func (v stageView) active() bool {
	switch v.State {
	case stageDone, stageFailed, stageCancelled:
		return false
	}
	return true
}

// resetStage (re)starts the authoritative stage for a new operation. It is
// called from beginOperation on the UI goroutine.
func (m *model) resetStage(kind OperationKind) {
	if m.stage == nil {
		m.stage = &execStage{}
	}
	now := time.Now()
	m.stage.mu.Lock()
	m.stage.Kind = kind
	m.stage.Label = ""
	m.stage.Target = ""
	m.stage.State = stageRunning
	m.stage.LastTs = now
	m.stage.Bytes = 0
	m.stage.Elapsed = 0
	m.stage.Tokens = 0
	m.stage.mu.Unlock()
}

// setStage records a real execution-stage transition. It is safe to call from
// worker goroutines. target is optional (pass "" to keep the previous target).
//
// It also feeds the authoritative per-operation execution telemetry so stage
// latency is attributed to the real runtime stage: a provider round-trip
// records as the "model" stage with waiting/streaming sub-phases, a local
// stage records as running→done. See execution_telemetry.go.
func (m *model) setStage(label, target string, state execStageState) {
	if m.stage == nil {
		m.stage = &execStage{}
	}
	now := time.Now()
	m.stage.mu.Lock()
	m.stage.Label = label
	if target != "" {
		m.stage.Target = target
	}
	m.stage.State = state
	m.stage.LastTs = now
	te := m.stage.Telemetry
	m.stage.mu.Unlock()
	if ts := stageStateToTelemetry(state); ts != "" && te != nil {
		te.Record(label, target, ts, 0, 0, 0)
	}
}

// stageStateToTelemetry maps the truthful UI stage state onto the telemetry
// stage state. Synthetic/animation states (blocked) map to running so the
// telemetry only ever records real runtime conditions.
func stageStateToTelemetry(s execStageState) execution.StageState {
	switch s {
	case stageRunning:
		return execution.StageRunning
	case stageWaiting:
		return execution.StageWaiting
	case stageStreaming:
		return execution.StageStreaming
	case stageDone:
		return execution.StageDone
	case stageFailed:
		return execution.StageFailed
	case stageCancelled:
		return execution.StageCancelled
	case stageBlocked:
		return execution.StageRunning
	default:
		return ""
	}
}

// spawnOpWorker registers a live worker under the active operation's
// telemetry so the terminal-lifecycle tests can assert no worker survives the
// operation. It is a strict no-op when no operation/telemetry is attached.
// Callers MUST pair it with a defer releaseOpWorker(label).
func (m *model) spawnOpWorker(label string) {
	if m.stage == nil {
		return
	}
	m.stage.mu.Lock()
	te := m.stage.Telemetry
	m.stage.mu.Unlock()
	if te != nil {
		te.Workers().Spawn(label)
	}
}

// releaseOpWorker unregisters a live worker under the active operation's
// telemetry. Paired with spawnOpWorker.
func (m *model) releaseOpWorker(label string) {
	if m.stage == nil {
		return
	}
	m.stage.mu.Lock()
	te := m.stage.Telemetry
	m.stage.mu.Unlock()
	if te != nil {
		te.Workers().Release(label)
	}
}

// setStageMetrics attaches real runtime metrics to the active stage.

// setStageMetrics attaches real runtime metrics to the active stage.
func (m *model) setStageMetrics(bytes int64, elapsed time.Duration, tokens int) {
	if m.stage == nil {
		return
	}
	m.stage.mu.Lock()
	if bytes > 0 {
		m.stage.Bytes = bytes
	}
	if elapsed > 0 {
		m.stage.Elapsed = elapsed
	}
	if tokens >= 0 {
		m.stage.Tokens = tokens
	}
	label := m.stage.Label
	target := m.stage.Target
	te := m.stage.Telemetry
	m.stage.mu.Unlock()
	// Feed the real metrics into the per-operation execution telemetry so the
	// stage span carries the true bytes/tokens/elapsed observed on disk or
	// from the provider.
	if te != nil {
		te.Record(label, target, stageStateToTelemetry(stageRunning), bytes, tokens, elapsed)
	}
}

// finishStage maps a terminal operation outcome onto the stage so the stage can
// never outlive the operation as a live indicator. The terminal state is also
// recorded into the per-operation execution telemetry so the stage span has a
// real terminal marker.
func (m *model) finishStage(outcome OperationOutcome) {
	if m.stage == nil {
		return
	}
	m.stage.mu.Lock()
	var state execStageState
	switch outcome {
	case OpOutcomeSuccess:
		state = stageDone
	case OpOutcomeCancelled:
		state = stageCancelled
	default:
		state = stageFailed
	}
	m.stage.State = state
	label := m.stage.Label
	target := m.stage.Target
	te := m.stage.Telemetry
	m.stage.mu.Unlock()
	if te != nil {
		if ts := stageStateToTelemetry(state); ts != "" && label != "" {
			te.Record(label, target, ts, 0, 0, 0)
		}
	}
}

// stageSnapshot returns a consistent lock-free snapshot of the authoritative
// stage.
func (m *model) stageSnapshot() stageView {
	if m.stage == nil {
		return stageView{}
	}
	return m.stage.view()
}

// stageWaitBlockedAfter is how long a provider-waiting stage must show zero
// progress before the indicator truthfully labels it BLOCKED. It mirrors the
// operation watchdog's stall window so the two signals never disagree.
const stageWaitBlockedAfter = opWatchdogStuckAfter

// stageDisplayLabel maps a canonical stage name to its display label.
func stageDisplayLabel(label string) string {
	switch label {
	case "target":
		return "Target"
	case "read":
		return "Read"
	case "context":
		return "Context"
	case "model":
		return "Model"
	case "patch":
		return "Patch"
	case "validate":
		return "Validate"
	case "apply":
		return "Apply"
	case "shell":
		return "Shell"
	default:
		return label
	}
}

// renderStageStatus renders the current stage as a truthful one-line status.
// It NEVER claims work that is not happening: a provider wait with zero tokens
// renders as "waiting", never as "thinking"/"processing".
func renderStageStatus(st stageView) string {
	target := st.Target
	label := stageDisplayLabel(st.Label)
	if label == "" {
		label = "Working"
	}

	switch st.State {
	case stageWaiting:
		wait := time.Since(st.LastTs)
		if wait >= stageWaitBlockedAfter {
			return fmt.Sprintf("Model ● blocked · %s", formatDurationElapsed(wait))
		}
		return fmt.Sprintf("Model ● waiting · %s", formatDurationElapsed(wait))
	case stageStreaming:
		// The token count is ONLY ever the authoritative provider-reported
		// count (fed via setStageMetrics from the stream's ProviderUsage).
		// When no authoritative usage has arrived, the indicator stays plain
		// "streaming" — it never fabricates a number from a buffer length.
		if st.Tokens > 0 {
			return fmt.Sprintf("Model ● streaming · %s tok", status.FormatTokens(st.Tokens))
		}
		return "Model ● streaming"
	case stageBlocked:
		return fmt.Sprintf("Model ● blocked · %s", formatDurationElapsed(time.Since(st.LastTs)))
	case stageRunning:
		// A running stage with no real label/target carries no truthful claim
		// (e.g. a freshly begun operation before any execution boundary) —
		// render nothing rather than a fabricated "Working".
		if label == "Working" && target == "" {
			return ""
		}
		if target != "" {
			return fmt.Sprintf("%s ● %s", label, target)
		}
		return fmt.Sprintf("%s ● running", label)
	case stageDone:
		if target != "" {
			return fmt.Sprintf("✓ %s %s", label, target)
		}
		return fmt.Sprintf("✓ %s", label)
	case stageFailed:
		return fmt.Sprintf("✖ %s", label)
	case stageCancelled:
		return "✕ cancelled"
	}
	return ""
}

// renderStageLine renders the authoritative stage as a plain one-line status
// (no snowflake, no spinner). It returns "" when no active stage exists so
// callers can fall back to their own truthful text.
func (m *model) renderStageLine() string {
	if m.stage == nil {
		return ""
	}
	st := m.stageSnapshot()
	if !st.active() {
		return ""
	}
	return renderStageStatus(st)
}

// formatDurationElapsed renders a duration like "4.2s" / "1m 3s".
func formatDurationElapsed(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < time.Minute {
		return d.Round(100 * time.Millisecond).String()
	}
	return fmt.Sprintf("%dm %s", int(d.Minutes()), (d % time.Minute).Round(time.Second))
}
