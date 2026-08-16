// Package handlers holds the Application Layer command handlers (RFC v1.0
// section 2). Each handler is the single coordinator for one canonical
// RuntimeCommand: it owns the domain side effects (phase transitions, intent
// classification, plan staging, approval resolution) and publishes the domain
// events that the LedgerBuilder and EventTranslator project downstream.
//
// Handlers are engine-agnostic: they never invoke concrete infrastructure or
// the presentation layer. Everything they need flows in through HandlerDeps.
package handlers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/PizenLabs/izen/internal/domain/workflow"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/runtime"
)

// Sentinel errors returned by the real handlers.
var (
	// ErrInvalidMode is returned when a command carries an unknown mode/phase
	// name.
	ErrInvalidMode = errors.New("handlers: invalid mode")
	// ErrEmptyPrompt is returned when a SubmitPromptCmd carries a blank prompt.
	ErrEmptyPrompt = errors.New("handlers: empty prompt")
	// ErrNoWorkflow is returned when a command requires the domain
	// WorkflowRuntime but none was injected.
	ErrNoWorkflow = errors.New("handlers: no workflow runtime")
)

// HandlerDeps are the Application-layer dependencies shared by the runtime
// command handlers. Every dependency is optional: a nil Bus disables event
// emission and a nil WorkflowRuntime makes phase-dependent commands a no-op
// on the domain state (they still publish telemetry where possible).
type HandlerDeps struct {
	// Workflow is the domain phase machine the handlers drive. Phase
	// transitions fail when the target violates the configured rules.
	Workflow workflow.WorkflowRuntime
	// Bus is the domain event bus handlers publish onto. Nil disables
	// emission so headless/CLI harnesses can execute commands silently.
	Bus *events.Bus
	// Approver resolves a pending approval by ID. It is the TEST/fallback
	// seam only: production approval is routed through Executor.Approve/Reject
	// and never fabricates a mutation record.
	Approver PatchApprover
	// Executor is the RuntimeExecutor authority boundary. When wired, the
	// approval commands resolve a REAL pending mutation through it (apply,
	// verification, commit) instead of emitting a fabricated PatchApplied
	// event. A nil executor degrades approval to the injected Approver (tests)
	// or a deterministic error — never a fake mutation.
	Executor *execution.RuntimeExecutor
	// Now is an injectable clock for deterministic duration telemetry.
	// Defaults to time.Since at the handler level.
	Now func() time.Time
}

// ApprovalResult reports the file a resolved approval maps to and its net
// line delta, mirroring the domain PatchResult contract.
type ApprovalResult struct {
	File     string
	LinesAdd int
	LinesDel int
}

// PatchApprover is the seam the handlers use to resolve a pending approval.
// The concrete implementation lives in the wiring/application layer; a nil
// approver degrades to a deterministic in-memory record.
type PatchApprover interface {
	// Resolve answers whether a pending approval should be applied. Approve is
	// true for an accept, false for a reject carrying a reason.
	Resolve(ctx context.Context, patchID string, approve bool, reason string) (ApprovalResult, error)
}

// handlerBase carries the shared dependencies and the emit helper.
type handlerBase struct {
	deps HandlerDeps
}

// now returns the injectable clock, defaulting to time.Now.
func (b *handlerBase) now() time.Time {
	if b.deps.Now != nil {
		return b.deps.Now()
	}
	return time.Now()
}

// emit publishes a domain event when a bus is wired.
func (b *handlerBase) emit(ev events.DomainEvent) {
	if b.deps.Bus != nil && ev != nil {
		b.deps.Bus.Publish(ev)
	}
}

// workflow returns the injected domain WorkflowRuntime.
func (b *handlerBase) workflow() workflow.WorkflowRuntime {
	return b.deps.Workflow
}

// ParsePhase maps a canonical mode/phase name onto the domain Phase. It is the
// single translation point between the presentation command vocabulary and the
// domain lifecycle.
func ParsePhase(s string) (workflow.Phase, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "ask":
		return workflow.PhaseAsk, true
	case "investigate":
		return workflow.PhaseInvestigate, true
	case "plan":
		return workflow.PhasePlan, true
	case "build":
		return workflow.PhaseBuild, true
	case "review":
		return workflow.PhaseReview, true
	default:
		return workflow.PhaseAsk, false
	}
}

// ── SubmitPromptHandler ──────────────────────────────────────────────────────

// SubmitPromptHandler classifies an incoming prompt, drives the domain phase
// transition when a target mode is carried, stages a plan for plan-mode
// prompts, and publishes the resulting domain events.
type SubmitPromptHandler struct {
	handlerBase
}

// Command returns the command type this handler serves.
func (h *SubmitPromptHandler) Command() runtime.CommandType { return runtime.CommandSubmitPrompt }

// Handle implements runtime.CommandHandler.
func (h *SubmitPromptHandler) Handle(ctx context.Context, cmd runtime.RuntimeCommand) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c, ok := cmd.(runtime.SubmitPromptCmd)
	if !ok {
		return fmt.Errorf("handlers: unexpected command %T", cmd)
	}
	if strings.TrimSpace(c.Prompt) == "" {
		return ErrEmptyPrompt
	}

	start := h.now()

	// 1. Intent classification (deterministic, no LLM dependency).
	intent, confidence := ClassifyIntent(c.Prompt, c.Mode)
	h.emit(events.NewIntentParsed(intent, c.Prompt, confidence))

	// 2. Phase routing: an explicit target mode transitions the domain phase.
	target := intent
	if ph, ok := ParsePhase(c.Mode); ok {
		target = ph.String()
	} else if c.Mode != "" {
		return fmt.Errorf("%w: %q", ErrInvalidMode, c.Mode)
	}
	if err := h.enterPhase(ctx, target); err != nil {
		return err
	}

	h.emit(events.NewStageCompleted("submit_prompt", time.Since(start),
		fmt.Sprintf("prompt routed to %s", target)))
	return nil
}

// enterPhase transitions the domain WorkflowRuntime into target when it
// differs from the current phase and a runtime is wired.
func (h *SubmitPromptHandler) enterPhase(ctx context.Context, target string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	wf := h.workflow()
	if wf == nil {
		return ErrNoWorkflow
	}
	next, ok := ParsePhase(target)
	if !ok {
		return fmt.Errorf("%w: %q", ErrInvalidMode, target)
	}
	from := wf.Phase()
	if from == next {
		return nil
	}
	if err := wf.Transition(next); err != nil {
		return err
	}
	h.emit(events.NewPhaseChanged(from.String(), next.String()))
	return nil
}

// ── SwitchModeHandler ────────────────────────────────────────────────────────

// SwitchModeHandler drives a workflow phase transition requested by the user.
type SwitchModeHandler struct {
	handlerBase
}

// Command returns the command type this handler serves.
func (h *SwitchModeHandler) Command() runtime.CommandType { return runtime.CommandSwitchMode }

// Handle implements runtime.CommandHandler.
func (h *SwitchModeHandler) Handle(ctx context.Context, cmd runtime.RuntimeCommand) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c, ok := cmd.(runtime.SwitchModeCmd)
	if !ok {
		return fmt.Errorf("handlers: unexpected command %T", cmd)
	}
	next, ok := ParsePhase(c.Mode)
	if !ok {
		return fmt.Errorf("%w: %q", ErrInvalidMode, c.Mode)
	}
	wf := h.workflow()
	if wf == nil {
		return ErrNoWorkflow
	}
	start := h.now()
	from := wf.Phase()
	if from != next {
		if err := wf.Transition(next); err != nil {
			return err
		}
	}
	h.emit(events.NewPhaseChanged(from.String(), next.String()))
	h.emit(events.NewStageCompleted("switch_mode", time.Since(start),
		fmt.Sprintf("switched to %s", next)))
	return nil
}

// ── ApprovePatchHandler ──────────────────────────────────────────────────────

// ApprovePatchHandler approves a pending patch. When a RuntimeExecutor is
// wired, the approval is REAL: it applies the held patch (owning the
// filesystem write + verification gate), commits the MutationSet and emits the
// mutation lifecycle events. Without an executor, approval degrades to the
// injected Approver (tests) or a deterministic error — never a fabricated
// mutation record.
type ApprovePatchHandler struct {
	handlerBase
}

// Command returns the command type this handler serves.
func (h *ApprovePatchHandler) Command() runtime.CommandType { return runtime.CommandApprovePatch }

// Handle implements runtime.CommandHandler.
func (h *ApprovePatchHandler) Handle(ctx context.Context, cmd runtime.RuntimeCommand) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c, ok := cmd.(runtime.ApprovePatchCmd)
	if !ok {
		return fmt.Errorf("handlers: unexpected command %T", cmd)
	}
	start := h.now()

	if x := h.deps.Executor; x != nil {
		res, err := x.Approve(ctx, c.PatchID)
		if err != nil {
			h.emit(events.NewExecutionFailed(events.FailureRecoverable, err, "approve_patch"))
			return err
		}
		if res == nil {
			return fmt.Errorf("handlers: executor returned no result for patch %s", c.PatchID)
		}
		// Emit the mutation outcome only from the real result — never a
		// fabricated +1/-0 record.
		for _, m := range res.Mutations {
			if m.Outcome.MutationSucceeded() {
				h.emit(events.NewPatchApplied(m.File, m.DiffAdds, m.DiffRemoves, time.Since(start)))
			}
		}
		if !res.Proof.Outcome.MutationSucceeded() {
			return fmt.Errorf("handlers: patch %s resolved as %s", c.PatchID, res.Proof.Outcome)
		}
		h.emit(events.NewStageCompleted("approve_patch", time.Since(start),
			fmt.Sprintf("approved patch %s", c.PatchID)))
		return nil
	}

	res, err := h.resolve(ctx, c.PatchID, true, "")
	if err != nil {
		return err
	}
	h.emit(events.NewPatchApplied(res.File, res.LinesAdd, res.LinesDel, time.Since(start)))
	h.emit(events.NewStageCompleted("approve_patch", time.Since(start),
		fmt.Sprintf("approved patch %s", c.PatchID)))
	return nil
}

// ── RejectPatchHandler ───────────────────────────────────────────────────────

// RejectPatchHandler rejects a pending patch. When a RuntimeExecutor is wired,
// the rejection is REAL: the held mutation is rolled back and its boundary
// terminated as cancelled. Without an executor, rejection degrades to the
// injected Approver (tests) or a deterministic error — never a fabricated
// record.
type RejectPatchHandler struct {
	handlerBase
}

// Command returns the command type this handler serves.
func (h *RejectPatchHandler) Command() runtime.CommandType { return runtime.CommandRejectPatch }

// Handle implements runtime.CommandHandler.
func (h *RejectPatchHandler) Handle(ctx context.Context, cmd runtime.RuntimeCommand) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c, ok := cmd.(runtime.RejectPatchCmd)
	if !ok {
		return fmt.Errorf("handlers: unexpected command %T", cmd)
	}
	start := h.now()

	if x := h.deps.Executor; x != nil {
		res, err := x.Reject(ctx, c.PatchID, c.Reason)
		if err != nil {
			h.emit(events.NewExecutionFailed(events.FailurePermanent, err, "reject_patch"))
			return err
		}
		if res == nil {
			return fmt.Errorf("handlers: executor returned no result for patch %s", c.PatchID)
		}
		h.emit(events.NewPatchRejected(c.PatchID, c.Reason, 4))
		h.emit(events.NewStageCompleted("reject_patch", time.Since(start),
			fmt.Sprintf("rejected patch %s", c.PatchID)))
		return nil
	}

	res, err := h.resolve(ctx, c.PatchID, false, c.Reason)
	if err != nil {
		return err
	}
	h.emit(events.NewPatchRejected(res.File, c.Reason, 4))
	h.emit(events.NewStageCompleted("reject_patch", time.Since(start),
		fmt.Sprintf("rejected patch %s", c.PatchID)))
	return nil
}

// resolve maps an approval decision to a concrete mutation record. It is the
// test/fallback seam: production approval is routed through the executor (see
// Approve/Reject handlers), which never fabricates a record.
func (h *ApprovePatchHandler) resolve(ctx context.Context, patchID string, approve bool, reason string) (ApprovalResult, error) {
	return resolveApproval(ctx, h.deps.Approver, patchID, approve, reason)
}

func (h *RejectPatchHandler) resolve(ctx context.Context, patchID string, approve bool, reason string) (ApprovalResult, error) {
	return resolveApproval(ctx, h.deps.Approver, patchID, approve, reason)
}

// resolveApproval delegates to the injected Approver when present. Without an
// approver it returns a deterministic error — a nil approver NEVER fabricates
// a mutation record (Rule 3: no fake states).
func resolveApproval(ctx context.Context, a PatchApprover, patchID string, approve bool, reason string) (ApprovalResult, error) {
	if a != nil {
		return a.Resolve(ctx, patchID, approve, reason)
	}
	return ApprovalResult{}, fmt.Errorf("handlers: no approval authority wired for patch %q", patchID)
}

// ── CancelHandler ────────────────────────────────────────────────────────────

// CancelHandler records a user-initiated cancellation as a completed stage.
// In-flight operation teardown is owned by the caller; the handler surfaces
// the cancellation on the event stream.
type CancelHandler struct {
	handlerBase
}

// Command returns the command type this handler serves.
func (h *CancelHandler) Command() runtime.CommandType { return runtime.CommandCancel }

// Handle implements runtime.CommandHandler.
func (h *CancelHandler) Handle(ctx context.Context, cmd runtime.RuntimeCommand) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c, ok := cmd.(runtime.CancelCmd)
	if !ok {
		return fmt.Errorf("handlers: unexpected command %T", cmd)
	}
	reason := strings.TrimSpace(c.Reason)
	if reason == "" {
		reason = "user interrupt"
	}
	h.emit(events.NewStageCompleted("cancel", 0, reason))
	return nil
}

// ── Intent classification ────────────────────────────────────────────────────

// ClassifyIntent maps a raw prompt (and optional explicit mode) onto a
// canonical execution phase. An explicit mode wins; otherwise a deterministic
// keyword pass selects the phase, defaulting to ask.
func ClassifyIntent(prompt, mode string) (string, float64) {
	if ph, ok := ParsePhase(mode); ok {
		return ph.String(), 0.95
	}
	lower := strings.ToLower(prompt)
	switch {
	case containsAny(lower, investigateKeywords):
		return "investigate", 0.9
	case containsAny(lower, buildKeywords):
		return "build", 0.9
	case containsAny(lower, planKeywords):
		return "plan", 0.9
	case containsAny(lower, reviewKeywords):
		return "review", 0.9
	default:
		return "ask", 0.5
	}
}

var (
	investigateKeywords = []string{"bug", "fail", "error", "crash", "investigate", "debug", "stack trace", "panic", "regression", "why does"}
	buildKeywords       = []string{"implement", "build", "fix", "write", "create", "add", "refactor", "migrate", "update"}
	planKeywords        = []string{"plan", "architecture", "design", "blueprint", "structure", "approach", "migration"}
	reviewKeywords      = []string{"review", "audit", "risk", "verify", "check", "inspect"}
)

// containsAny reports whether s contains any of the keywords.
func containsAny(s string, keywords []string) bool {
	for _, k := range keywords {
		if strings.Contains(s, k) {
			return true
		}
	}
	return false
}

// ── Handlers bundle + registration ───────────────────────────────────────────

// Handlers bundles every canonical command handler bound to a shared
// HandlerDeps set.
type Handlers struct {
	deps HandlerDeps
}

// New returns a bundle of handlers bound to deps.
func New(deps HandlerDeps) *Handlers {
	return &Handlers{deps: deps}
}

// Submit returns the SubmitPrompt handler.
func (hs *Handlers) Submit() runtime.CommandHandler {
	return &SubmitPromptHandler{handlerBase{deps: hs.deps}}
}

// Switch returns the SwitchMode handler.
func (hs *Handlers) Switch() runtime.CommandHandler {
	return &SwitchModeHandler{handlerBase{deps: hs.deps}}
}

// Approve returns the ApprovePatch handler.
func (hs *Handlers) Approve() runtime.CommandHandler {
	return &ApprovePatchHandler{handlerBase{deps: hs.deps}}
}

// Reject returns the RejectPatch handler.
func (hs *Handlers) Reject() runtime.CommandHandler {
	return &RejectPatchHandler{handlerBase{deps: hs.deps}}
}

// Cancel returns the Cancel handler.
func (hs *Handlers) Cancel() runtime.CommandHandler {
	return &CancelHandler{handlerBase{deps: hs.deps}}
}

// Register wires every canonical handler onto the dispatcher. It returns the
// first registration error encountered, leaving the dispatcher in a partial
// state.
func (hs *Handlers) Register(d *runtime.CommandDispatcher) error {
	if d == nil {
		return errors.New("handlers: nil dispatcher")
	}
	regs := []struct {
		typ runtime.CommandType
		h   runtime.CommandHandler
	}{
		{runtime.CommandSubmitPrompt, hs.Submit()},
		{runtime.CommandSwitchMode, hs.Switch()},
		{runtime.CommandApprovePatch, hs.Approve()},
		{runtime.CommandRejectPatch, hs.Reject()},
		{runtime.CommandCancel, hs.Cancel()},
	}
	for _, r := range regs {
		if err := d.Register(r.typ, r.h); err != nil {
			return err
		}
	}
	return nil
}

// RegisterDefaults wires every handler onto the dispatcher with default
// dependencies (no bus, no workflow runtime). It is retained for harnesses
// that only need the routing table populated.
func RegisterDefaults(d *runtime.CommandDispatcher) error {
	return New(HandlerDeps{}).Register(d)
}

// InMemoryApprover is a deterministic, thread-safe PatchApprover that maps
// patch IDs to pre-registered mutation records. It is the harness/tests seam
// only — production approval is routed through the RuntimeExecutor, which
// applies real mutations. It NEVER fabricates a record for an unknown ID.
type InMemoryApprover struct {
	mu      sync.Mutex
	pending map[string]ApprovalResult
}

// NewInMemoryApprover returns an empty approver.
func NewInMemoryApprover() *InMemoryApprover {
	return &InMemoryApprover{pending: make(map[string]ApprovalResult)}
}

// Register records a mutation outcome for a patch ID.
func (a *InMemoryApprover) Register(patchID string, res ApprovalResult) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.pending == nil {
		a.pending = make(map[string]ApprovalResult)
	}
	a.pending[patchID] = res
}

// Resolve implements PatchApprover.
func (a *InMemoryApprover) Resolve(ctx context.Context, patchID string, approve bool, reason string) (ApprovalResult, error) {
	if err := ctx.Err(); err != nil {
		return ApprovalResult{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if res, ok := a.pending[patchID]; ok {
		return res, nil
	}
	return ApprovalResult{}, fmt.Errorf("handlers: no registered approval record for patch %q", patchID)
}
