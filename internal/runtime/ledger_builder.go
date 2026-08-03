package runtime

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/PizenLabs/izen/internal/events"
)

// eventSource is the subscription surface the LedgerBuilder consumes. The
// concrete infrastructure bus (internal/events.Bus) satisfies it structurally,
// so the builder stays decoupled from any concrete publisher.
type eventSource interface {
	Subscribe(eventType string, handler events.EventHandler) *events.Subscription
}

// CommandEntry records one submitted command.
type CommandEntry struct {
	Command string
	Mode    string
	At      time.Time
}

// IntentEntry records the latest classified intent.
type IntentEntry struct {
	Intent     string
	Raw        string
	Confidence float64
	At         time.Time
}

// PlanEntry records the latest staged plan.
type PlanEntry struct {
	TaskCount int
	Tasks     []string
	Stage     string
	At        time.Time
}

// PatchEntry records one applied patch with its line metrics.
type PatchEntry struct {
	File     string
	Strategy string
	LinesAdd int
	LinesDel int
	At       time.Time
}

// FailureEntry records one execution failure and its classification.
type FailureEntry struct {
	Stage          string
	Classification events.FailureClassification
	Error          string
	At             time.Time
}

// StageEntry records one completed pipeline stage.
type StageEntry struct {
	Stage    string
	Duration time.Duration
	Summary  string
	At       time.Time
}

// LedgerSnapshot is a deep, race-free copy of the projection state.
type LedgerSnapshot struct {
	Commands   []CommandEntry
	Intent     IntentEntry
	Phase      string
	Plan       PlanEntry
	Patches    []PatchEntry
	Failures   []FailureEntry
	Stages     []StageEntry
	Activities []string
	Approvals  int
	Events     int
	StartedAt  time.Time
	LastEvent  time.Time
}

// ContextLedger is a thread-safe projection of the domain event stream into a
// compact, current-state view optimized for LLM context injection. It is built
// by LedgerBuilder and is never persisted; it exists only for the lifetime of
// a workflow.
type ContextLedger struct {
	mu         sync.RWMutex
	commands   []CommandEntry
	intent     IntentEntry
	phase      string
	plan       PlanEntry
	patches    []PatchEntry
	failures   []FailureEntry
	stages     []StageEntry
	activities []string
	approvals  int
	events     int
	startedAt  time.Time
	lastEvent  time.Time
}

// NewContextLedger returns an empty ledger with the given start time.
func NewContextLedger() *ContextLedger {
	return &ContextLedger{startedAt: time.Now()}
}

// Apply projects a single domain event into the ledger. It is safe for
// concurrent use: the bus delivers events on its own goroutines.
func (l *ContextLedger) Apply(ev events.DomainEvent) {
	if l == nil || ev == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events++
	l.lastEvent = ev.Timestamp()

	switch p := ev.Payload().(type) {
	case events.CommandReceivedPayload:
		l.commands = append(l.commands, CommandEntry{Command: p.Command, Mode: p.Mode, At: ev.Timestamp()})
	case events.IntentParsedPayload:
		l.intent = IntentEntry{Intent: p.Intent, Raw: p.Raw, Confidence: p.Confidence, At: ev.Timestamp()}
	case events.IntentClassifiedPayload:
		l.intent = IntentEntry{Intent: p.Intent, Raw: p.Raw, Confidence: p.Confidence, At: ev.Timestamp()}
	case events.PlanStagedPayload:
		l.plan = PlanEntry{TaskCount: p.TaskCount, Tasks: append([]string(nil), p.Tasks...), Stage: p.Stage, At: ev.Timestamp()}
	case events.PhaseChangedPayload:
		l.phase = p.To
	case events.PatchAppliedPayload:
		l.patches = append(l.patches, PatchEntry{File: p.File, LinesAdd: p.LinesAdd, LinesDel: p.LinesDel, At: ev.Timestamp()})
	case events.ExecutionFailedPayload:
		l.failures = append(l.failures, FailureEntry{Stage: p.Stage, Classification: p.Classification, Error: p.Error, At: ev.Timestamp()})
	case events.StageCompletedPayload:
		l.stages = append(l.stages, StageEntry{Stage: p.Stage, Duration: p.Duration, Summary: p.Summary, At: ev.Timestamp()})
	case events.ActivityPayload:
		l.activities = append(l.activities, p.Line)
	case events.ApprovalRequestedPayload:
		l.approvals++
	}
}

// Snapshot returns a deep copy of the current projection for race-free reads.
func (l *ContextLedger) Snapshot() LedgerSnapshot {
	if l == nil {
		return LedgerSnapshot{}
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	return LedgerSnapshot{
		Commands:   append([]CommandEntry(nil), l.commands...),
		Intent:     l.intent,
		Phase:      l.phase,
		Plan:       l.plan,
		Patches:    append([]PatchEntry(nil), l.patches...),
		Failures:   append([]FailureEntry(nil), l.failures...),
		Stages:     append([]StageEntry(nil), l.stages...),
		Activities: append([]string(nil), l.activities...),
		Approvals:  l.approvals,
		Events:     l.events,
		StartedAt:  l.startedAt,
		LastEvent:  l.lastEvent,
	}
}

// Render produces a compact, signal-dense view of the projection for LLM
// context injection. It is token-efficient and omits empty sections.
func (l *ContextLedger) Render() string {
	snap := l.Snapshot()
	var sb strings.Builder
	fmt.Fprintf(&sb, "context ledger: %d events", snap.Events)
	if !snap.StartedAt.IsZero() {
		fmt.Fprintf(&sb, " over %s", time.Since(snap.StartedAt).Round(time.Millisecond))
	}
	if len(snap.Commands) > 0 {
		last := snap.Commands[len(snap.Commands)-1]
		fmt.Fprintf(&sb, " | last command %q", last.Command)
		if last.Mode != "" {
			fmt.Fprintf(&sb, " (mode %s)", last.Mode)
		}
	}
	if snap.Intent.Intent != "" {
		fmt.Fprintf(&sb, " | intent %q (conf %.2f)", snap.Intent.Intent, snap.Intent.Confidence)
	}
	if snap.Phase != "" {
		fmt.Fprintf(&sb, " | phase %s", snap.Phase)
	}
	if snap.Plan.TaskCount > 0 {
		fmt.Fprintf(&sb, " | plan %d tasks (%s)", snap.Plan.TaskCount, snap.Plan.Stage)
	}
	if n := len(snap.Patches); n > 0 {
		last := snap.Patches[n-1]
		fmt.Fprintf(&sb, " | %d patch(es), last %s (+%d -%d)", n, last.File, last.LinesAdd, last.LinesDel)
	}
	if n := len(snap.Failures); n > 0 {
		last := snap.Failures[n-1]
		fmt.Fprintf(&sb, " | %d failure(s), last %s (%s)", n, last.Stage, last.Classification)
	}
	if n := len(snap.Stages); n > 0 {
		names := make([]string, 0, n)
		for _, s := range snap.Stages {
			names = append(names, s.Stage)
		}
		fmt.Fprintf(&sb, " | stages %s", strings.Join(names, ","))
	}
	if len(snap.Activities) > 0 {
		fmt.Fprintf(&sb, " | %d activities", len(snap.Activities))
	}
	if snap.Approvals > 0 {
		fmt.Fprintf(&sb, " | %d approvals requested", snap.Approvals)
	}
	return sb.String()
}

// projectedEventTypes are the domain events the ledger projects. Unknown or
// high-frequency stream events are intentionally omitted to keep the ledger
// compact.
var projectedEventTypes = []string{
	events.EventCommandReceived,
	events.EventIntentParsed,
	events.EventIntentClassified,
	events.EventPlanStaged,
	events.EventPhaseChanged,
	events.EventPatchApplied,
	events.EventExecutionFailed,
	events.EventStageCompleted,
	events.EventActivity,
	events.EventApprovalRequested,
}

// LedgerBuilder subscribes to a domain event source and projects the event
// stream into an unpersisted ContextLedger. It is the Application-layer
// counterpart of the event translator: events in, a compact context view out.
type LedgerBuilder struct {
	mu      sync.Mutex
	source  eventSource
	ledger  *ContextLedger
	subs    []*events.Subscription
	started bool
}

// NewLedgerBuilder returns a builder bound to source. The source may be nil;
// Start then becomes a no-op.
func NewLedgerBuilder(source eventSource) *LedgerBuilder {
	return &LedgerBuilder{
		source: source,
		ledger: NewContextLedger(),
	}
}

// Start subscribes to every projected event type. It is idempotent and safe
// for concurrent use.
func (b *LedgerBuilder) Start() {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.started || b.source == nil {
		return
	}
	for _, typ := range projectedEventTypes {
		if sub := b.source.Subscribe(typ, b.ledger.Apply); sub != nil {
			b.subs = append(b.subs, sub)
		}
	}
	b.started = true
}

// Ledger returns the projection being built.
func (b *LedgerBuilder) Ledger() *ContextLedger {
	if b == nil {
		return nil
	}
	return b.ledger
}

// Close cancels all subscriptions, stopping further projection. It is
// idempotent.
func (b *LedgerBuilder) Close() {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, sub := range b.subs {
		sub.Cancel()
	}
	b.subs = nil
	b.started = false
}
