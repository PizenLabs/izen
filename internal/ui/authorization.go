package ui

// ── EVIDENCE-BOUND EXECUTION PROJECTION (Phase 16) ─────────────────────────
//
// Authorization is a PERMISSION. Execution is a FACT. The authorization card
// used to blur them, and it blurred them in the most dangerous direction
// possible: it rendered a static checklist of steps the runtime intended to take.
//
//	✓ inspect target
//	✓ analyze structure
//	✓ propose change
//	✓ apply mutation
//
// Every one of those checkmarks was a lie. The card was raised BEFORE anything
// was inspected, nothing was analyzed, no change was proposed, and nothing was
// applied — and the UI said otherwise, in the same visual language it used for
// work that had actually happened. A user glancing at a redacted scrollback
// could not tell a plan from a record, and "apply mutation ✓" is exactly the
// line nobody re-reads carefully before believing a tool touched their files.
//
// So this file establishes the whole vocabulary, and it is smaller than what it
// replaces:
//
//	AUTHORIZED:  a static PERMISSION DECLARATION. What the runtime is now
//	             permitted to do inside the granted boundary. It is not a
//	             status, it has no checkmark, and it is emitted at the moment
//	             the grant is issued — never before, never for work that has
//	             not started.
//
//	Execution     steps are rendered ONLY on consuming a runtime event.
//	steps         EventStepStarted  → "● <step>"
//	              EventStepCompleted → "✓ <step>"
//
// There is no third source. A step that no runtime event announced does not
// exist on screen, which means the UI cannot drift ahead of the runtime: the
// only way a checkmark appears is if the runtime said the work finished.
//
// ARCHITECTURAL NOTE: this file consumes plain values — step names and
// lifecycle states. It imports no execution runtime package and holds no
// executor result, which is what keeps the projection testable headless.

import (
	"fmt"
	"strings"
	"sync"
)

// StepLifecycle is the evidence state of one execution step, and it is
// deliberately CLOSED. A projection that can hold a fourth, invented state
// ("probably done", "assumed complete") is how a hardcoded checklist came back.
type StepLifecycle string

const (
	// StepActive: EventStepStarted was consumed and no completion has arrived.
	StepActive StepLifecycle = "active"
	// StepCompleted: EventStepCompleted was consumed for this step.
	StepCompleted StepLifecycle = "completed"
)

// ExecutionStep is one runtime-attested step.
type ExecutionStep struct {
	// Key is the step's IDENTITY as the runtime reported it — the bounded-step
	// ordinal. Identity and display are separate because the start event carries
	// more descriptive context (a model id) than the completion event does:
	// collapsing the two into one string would make the start and the
	// completion of the SAME step look like two unrelated steps, and a ledger
	// that cannot pair them is a ledger that renders one step twice.
	Key string
	// Name is the display label. It is the runtime's own context, never
	// friendlier text the UI invented — a friendlier label is a step the
	// runtime never reported.
	Name string
	// Lifecycle is the evidence state, and it only ever moves forward.
	Lifecycle StepLifecycle
	// Ordinal preserves the order the runtime announced the steps in.
	Ordinal int
}

// Glyph returns the marker for a step's lifecycle state.
//
// The distinction is the whole point: "●" means the runtime STARTED this step,
// "✓" means it FINISHED. Neither glyph is ever produced without the
// corresponding event.
func (s ExecutionStep) Glyph() string {
	return s.Lifecycle.Glyph()
}

// Glyph returns the marker for a lifecycle state on its own. It is a method on
// the closed vocabulary rather than a field on the step so that no projector can
// attach a glyph to a state the runtime never reported.
func (l StepLifecycle) Glyph() string {
	if l == StepCompleted {
		return "✓"
	}
	return "●"
}

// Line renders one attested step as "<glyph> <name>".
func (s ExecutionStep) Line() string {
	return s.Glyph() + " " + s.Name
}

// ExecutionStepLedger is the UI's record of runtime-attested execution steps.
//
// It is the ONLY source of execution-step rendering. It is fed by the domain
// event stream and read by the authorization surface, which is what makes the
// ordering guarantee structural: the card cannot render a step the ledger has
// not been told about, because the card has no other list to read.
type ExecutionStepLedger struct {
	mu    sync.RWMutex
	steps []ExecutionStep
	index map[string]int
}

// NewExecutionStepLedger returns an empty ledger. An empty ledger renders
// nothing at all, which is the correct and desired state before any runtime
// event has arrived.
func NewExecutionStepLedger() *ExecutionStepLedger {
	return &ExecutionStepLedger{index: make(map[string]int)}
}

// RecordStepStarted records an EventStepStarted under the step's runtime
// identity. name is optional display context; when empty the identity-derived
// label is used.
//
// Re-announcing a known step is idempotent: a retried dispatch re-opens a step
// rather than appending a duplicate, because two lines for one step read as two
// pieces of work. It is also forward-only — a step that already completed does
// not revert to active, because re-rendering a finished step as running is the
// same class of lie as the checklist this ledger replaced.
func (l *ExecutionStepLedger) RecordStepStarted(key, name string) {
	if l == nil {
		return
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.index[key]; ok {
		return
	}
	l.index[key] = len(l.steps)
	l.steps = append(l.steps, ExecutionStep{
		Key:       key,
		Name:      firstNonEmptyName(name, stepLabelFromKey(key)),
		Lifecycle: StepActive,
		Ordinal:   len(l.steps) + 1,
	})
}

// RecordStepCompleted records an EventStepCompleted under the same identity the
// start event used.
//
// Completing a step the ledger never saw start still records it: the completion
// IS the evidence, and discarding it would hide real work that happened before
// this ledger existed. A ledger that under-reports is safer than one that
// over-reports, but it is still wrong.
func (l *ExecutionStepLedger) RecordStepCompleted(key string) {
	if l == nil {
		return
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if at, ok := l.index[key]; ok {
		l.steps[at].Lifecycle = StepCompleted
		return
	}
	l.index[key] = len(l.steps)
	l.steps = append(l.steps, ExecutionStep{
		Key:       key,
		Name:      stepLabelFromKey(key),
		Lifecycle: StepCompleted,
		Ordinal:   len(l.steps) + 1,
	})
}

// Steps returns a snapshot of the attested steps in announcement order.
func (l *ExecutionStepLedger) Steps() []ExecutionStep {
	if l == nil {
		return nil
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	if len(l.steps) == 0 {
		return nil
	}
	out := make([]ExecutionStep, len(l.steps))
	copy(out, l.steps)
	return out
}

// Count returns how many steps are attested and how many have completed.
func (l *ExecutionStepLedger) Count() (attested, completed int) {
	if l == nil {
		return 0, 0
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	for _, s := range l.steps {
		attested++
		if s.Lifecycle == StepCompleted {
			completed++
		}
	}
	return attested, completed
}

// HasCompletedCheckmark reports whether any attested step has completed.
//
// It exists for the acceptance test that matters most: before any runtime event
// arrives, this must be false, and a hardcoded checklist could never satisfy
// that.
func (l *ExecutionStepLedger) HasCompletedCheckmark() bool {
	_, completed := l.Count()
	return completed > 0
}

// Reset clears the ledger. It runs at session start, /clear and mode
// transitions — the same unwind seam that clears the pending proposal, so a
// previous run's completed steps can never be read as this run's progress.
func (l *ExecutionStepLedger) Reset() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.steps = nil
	l.index = make(map[string]int)
}

// renderExecutionSteps projects the ledger as an indented block. It returns ""
// when nothing has been attested, so an authorization card before the first
// runtime event shows no execution section at all — not an empty one, not a
// planned one.
func (l *ExecutionStepLedger) renderExecutionSteps() string {
	steps := l.Steps()
	if len(steps) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n")
	for _, s := range steps {
		line := "  " + s.Glyph() + " " + s.Name
		if s.Lifecycle == StepCompleted {
			b.WriteString(greenStyle.Render(line))
		} else {
			b.WriteString(infoStyle.Render(line))
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// ── Permission declaration ────────────────────────────────────────────────

// authorizedPermissionLabel is the static PERMISSION DECLARATION rendered after
// a capability grant is issued.
//
// It is a fixed sentence on purpose. A permission declaration must not vary
// with the objective, or it becomes a second place where the runtime asserts
// what it is about to do. The list names the boundary that was released, and
// nothing else: no step, no checkmark, no claim about work in progress.
const authorizedPermissionLabel = "AUTHORIZED: read workspace, inspect target, propose mutation, apply mutation"

// renderAuthorizedPermission renders the permission declaration. It is emitted
// at the moment the grant is issued and carries NO execution glyphs, so it can
// never be mistaken for a record of work.
func renderAuthorizedPermission() string {
	return mutedStyle.Render(authorizedPermissionLabel)
}

// stepKeyFromOrdinal is the runtime-identity of a bounded step.
//
// The ordinal is the ONLY stable identifier across the two events: the start
// payload adds a model id, the completion payload does not. Deriving the key
// from the ordinal on both sides is what makes the start and the completion of
// one step land on ONE ledger entry — otherwise every finished step renders
// twice, once active and once complete, which reads as two pieces of work.
func stepKeyFromOrdinal(ordinal int) string {
	return fmt.Sprintf("step-%d", ordinal)
}

// stepLabelFromKey renders a display label for a step identity on its own.
func stepLabelFromKey(key string) string {
	if idx := strings.LastIndex(key, "-"); idx > 0 {
		return "step " + key[idx+1:]
	}
	return key
}

// stepLabelWithModel builds the display label for a step start, which is the one
// event carrying a model id. It is DISPLAY context only — never the key.
func stepLabelWithModel(model string, ordinal int) string {
	label := stepLabelFromKey(stepKeyFromOrdinal(ordinal))
	model = strings.TrimSpace(model)
	if model == "" {
		return label
	}
	return label + " (" + model + ")"
}

// firstNonEmptyName returns the first non-blank candidate, or "".
func firstNonEmptyName(candidates ...string) string {
	for _, c := range candidates {
		if strings.TrimSpace(c) != "" {
			return c
		}
	}
	return ""
}
