package ui

// ── TELEMETRY DOMAIN ISOLATION (Phase 16.1, I14) ───────────────────────────
//
// The TUI used to have ONE undifferentiated notion of "busy": a single spinner
// and a single loading flag that any event could move. So a workspace-indexer
// progress report — background work that has nothing to do with the user's
// execution — advanced the same execution spinner that means "the model is
// running your task". The user saw an execution spinner for an indexer, and the
// spinner's state stopped being a fact about execution.
//
// The repair is OWNERSHIP, and it is enforced at the type level:
//
//	BackgroundTelemetryState   — workspace/indexer/telemetry facts. It has NO
//	                             ExecutionSpinner field, so no code reachable
//	                             from it can instantiate or update one.
//
//	ExecutionNarrativeState    — execution-narrative facts. It OWNS the
//	                             ExecutionSpinner. Only an EXECUTION domain
//	                             event may advance it.
//
// The reducer in model.go is the single dispatch point: an indexer event can
// only reach BackgroundTelemetryState, and an execution event can only reach
// ExecutionNarrativeState. The boundary is structural, not a convention.

import (
	"sync"

	"github.com/PizenLabs/izen/internal/events"
)

// ExecutionSpinner is the execution-narrative activity indicator. It exists
// ONLY inside ExecutionNarrativeState; nothing in the background telemetry
// domain can reach it.
//
// It counts starts and updates as EVIDENCE: a test can assert that a
// background event produced zero instantiations and zero updates without
// inspecting a rendered frame.
type ExecutionSpinner struct {
	mu      sync.Mutex
	active  bool
	frame   int
	starts  int
	updates int
}

// NewExecutionSpinner returns an inert spinner. Construction alone does not
// start it; only an execution event does.
func NewExecutionSpinner() *ExecutionSpinner {
	return &ExecutionSpinner{}
}

// Start marks the spinner active. The first activation is counted as a START;
// re-activating an active spinner is idempotent.
func (s *ExecutionSpinner) Start() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.active {
		s.starts++
	}
	s.active = true
}

// Update advances the spinner one frame. A frame advance is evidence that the
// execution domain observed an execution event.
func (s *ExecutionSpinner) Update() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updates++
	s.frame++
}

// Stop marks the spinner inactive. It does not clear the counters.
func (s *ExecutionSpinner) Stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active = false
}

// Active reports whether the spinner is currently running.
func (s *ExecutionSpinner) Active() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active
}

// Snapshot returns the spinner's observable evidence.
func (s *ExecutionSpinner) Snapshot() (active bool, frame, starts, updates int) {
	if s == nil {
		return false, 0, 0, 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active, s.frame, s.starts, s.updates
}

// BackgroundTelemetryState is the BACKGROUND telemetry domain: workspace
// indexing, graph progress and other non-execution telemetry.
//
// It deliberately has NO ExecutionSpinner field and no method that reaches one.
// That absence is the enforcement: I14 is a structural property here, not a
// rule a future caller has to remember.
type BackgroundTelemetryState struct {
	mu              sync.Mutex
	indexerStage    string
	filesIndexed    int
	totalFiles      int
	progressReports int
}

// NewBackgroundTelemetryState returns an empty background telemetry domain.
func NewBackgroundTelemetryState() *BackgroundTelemetryState {
	return &BackgroundTelemetryState{}
}

// ObserveIndexerProgress records a workspace-indexer progress report. It exists
// ONLY on the background domain, so an indexer event has no execution surface
// to write to.
func (b *BackgroundTelemetryState) ObserveIndexerProgress(p events.IndexerProgressPayload) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.indexerStage = p.Stage
	b.filesIndexed = p.FilesIndexed
	b.totalFiles = p.TotalFiles
	b.progressReports++
}

// IndexerProgress returns the last observed indexer state.
func (b *BackgroundTelemetryState) IndexerProgress() (stage string, filesIndexed, totalFiles, reports int) {
	if b == nil {
		return "", 0, 0, 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.indexerStage, b.filesIndexed, b.totalFiles, b.progressReports
}

// ExecutionNarrativeState is the EXECUTION narrative domain. It owns the
// ExecutionSpinner, and the spinner advances ONLY when an execution domain
// event is consumed through this type.
type ExecutionNarrativeState struct {
	mu              sync.Mutex
	spinner         *ExecutionSpinner
	model           string
	streamEvents    int
	executionEvents int
}

// NewExecutionNarrativeState returns an execution narrative with no spinner
// yet. The spinner is created on first EXECUTION event, so a session in which
// only background telemetry arrives never instantiates one.
func NewExecutionNarrativeState() *ExecutionNarrativeState {
	return &ExecutionNarrativeState{}
}

// Spinner returns the execution spinner, creating it on first use. It is the
// ONLY accessor that instantiates the spinner; reachable only from the
// execution domain.
func (n *ExecutionNarrativeState) Spinner() *ExecutionSpinner {
	if n == nil {
		return nil
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.spinner == nil {
		n.spinner = NewExecutionSpinner()
	}
	return n.spinner
}

// spinnerIfAny returns the spinner only when an execution event already created
// it. It never instantiates, which is what makes "did background telemetry
// create a spinner?" answerable.
func (n *ExecutionNarrativeState) spinnerIfAny() *ExecutionSpinner {
	if n == nil {
		return nil
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.spinner
}

// IsExecutionDomainEvent reports whether ev belongs to the execution narrative
// domain. It is the closed allow-list the spinner ownership rule is built on.
func IsExecutionDomainEvent(ev events.DomainEvent) bool {
	if ev == nil {
		return false
	}
	switch ev.Type() {
	case events.EventExecutionStarted,
		events.EventModelInvoked,
		events.EventModelStreaming,
		events.EventProviderWaiting,
		events.EventProviderFirstToken,
		events.EventProviderStreamDelta,
		events.EventProviderUsageUpdate,
		events.EventProviderResponse,
		events.EventArtifactProduced,
		events.EventMutationStarted,
		events.EventMutationCompleted,
		events.EventVerificationCompleted,
		events.EventExecutionEvidence,
		events.EventExecutionFinished:
		return true
	default:
		return false
	}
}

// ObserveExecutionEvent advances the execution narrative for one execution
// domain event. A non-execution event is IGNORED: it has no business mutating
// execution rendering. This is the single write path to the spinner.
func (n *ExecutionNarrativeState) ObserveExecutionEvent(ev events.DomainEvent) {
	if n == nil || !IsExecutionDomainEvent(ev) {
		return
	}
	n.mu.Lock()
	n.executionEvents++
	n.mu.Unlock()

	if p, ok := ev.Payload().(events.ModelStreamingPayload); ok {
		n.observeModelStreaming(p)
		return
	}
	switch ev.Type() {
	case events.EventExecutionStarted, events.EventModelInvoked,
		events.EventProviderWaiting, events.EventProviderFirstToken,
		events.EventProviderStreamDelta:
		n.Spinner().Start()
		n.Spinner().Update()
	case events.EventExecutionFinished, events.EventExecutionEvidence:
		n.Spinner().Stop()
	}
}

// observeModelStreaming records a model-streaming report and advances the
// spinner. Streaming content is execution narrative evidence, so it is exactly
// the class of event that may do so.
func (n *ExecutionNarrativeState) observeModelStreaming(p events.ModelStreamingPayload) {
	if n == nil {
		return
	}
	n.mu.Lock()
	n.streamEvents++
	n.model = p.Model
	n.mu.Unlock()
	spinner := n.Spinner()
	if p.Streaming {
		spinner.Start()
		spinner.Update()
	} else {
		spinner.Stop()
	}
}

// StreamEvents returns how many model-streaming reports were consumed.
func (n *ExecutionNarrativeState) StreamEvents() int {
	if n == nil {
		return 0
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.streamEvents
}

// ExecutionEvents returns how many execution domain events were consumed.
func (n *ExecutionNarrativeState) ExecutionEvents() int {
	if n == nil {
		return 0
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.executionEvents
}

// ── model accessors ────────────────────────────────────────────────────────

// backgroundTelemetryState returns the background telemetry domain, creating it
// on first use.
func (m *model) backgroundTelemetryState() *BackgroundTelemetryState {
	if m == nil {
		return nil
	}
	if m.backgroundTelemetry == nil {
		m.backgroundTelemetry = NewBackgroundTelemetryState()
	}
	return m.backgroundTelemetry
}

// executionNarrativeState returns the execution narrative domain, creating it
// on first use.
func (m *model) executionNarrativeState() *ExecutionNarrativeState {
	if m == nil {
		return nil
	}
	if m.executionNarrative == nil {
		m.executionNarrative = NewExecutionNarrativeState()
	}
	return m.executionNarrative
}

// executionSpinnerIfAny returns the execution spinner only when an execution
// domain event already created it. Background telemetry can never cause it to
// exist, so a nil result here is proof that no execution event was consumed.
func (m *model) executionSpinnerIfAny() *ExecutionSpinner {
	if m == nil || m.executionNarrative == nil {
		return nil
	}
	return m.executionNarrative.spinnerIfAny()
}

// routeExecutionDomainEvent dispatches one event to exactly one domain. It is
// the boundary the reducer calls before doing any rendering:
//
//   - background event  → BackgroundTelemetryState (never the spinner)
//   - execution event   → ExecutionNarrativeState (may advance the spinner)
//
// Everything else is ignored by the domain router.
func (m *model) routeExecutionDomainEvent(ev events.DomainEvent) {
	if m == nil || ev == nil {
		return
	}
	switch p := ev.Payload().(type) {
	case events.IndexerProgressPayload:
		// I14: an indexer progress report is background telemetry. This arm
		// cannot reach ExecutionNarrativeState or ExecutionSpinner.
		m.backgroundTelemetryState().ObserveIndexerProgress(p)
	default:
		if IsExecutionDomainEvent(ev) {
			m.executionNarrativeState().ObserveExecutionEvent(ev)
		}
	}
}
