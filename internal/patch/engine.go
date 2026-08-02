package patch

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/PizenLabs/izen/internal/events"
)

// Request is a single patch application attempt.
type Request struct {
	// File is the workspace-relative target path.
	File string
	// Original is the caller's snapshot of the current file content (may be
	// empty for a new file). The engine re-reads the file before validating so
	// the disk state is authoritative.
	Original string
	// Raw is the raw patch payload (unified diff, SEARCH/REPLACE block, or
	// full content).
	Raw string
	// TaskObjective is the natural-language objective driving the change. Used
	// by the ContextualSafetyEvaluator to permit contextual redesign rewrites.
	TaskObjective string
	// FileType is the file extension (e.g. ".html") used for safety policy.
	FileType string
	// Approved is true once Tier 4 human-in-the-loop approval has been given
	// (e.g. after an ApprovalRequested event). It authorizes high-risk writes.
	Approved bool
}

// Engine orchestrates the multi-tier patch pipeline: Parse -> Validate ->
// Safety -> Apply, emitting PatchParsed, PatchValidated, PatchRejected and
// ApprovalRequested events on the bus. It is safe for concurrent use: every
// stage is deterministic and the engine holds no mutable state.
type Engine struct {
	parser     Parser
	validator  Validator
	safety     SafetyEvaluator
	applicator Applicator
	bus        *events.Bus
}

// NewEngine wires the default tiered parser, structural validator, contextual
// safety evaluator and file applicator. Override any stage with the fluent
// setters.
func NewEngine() *Engine {
	return &Engine{
		parser:     NewTieredParser(),
		validator:  NewStructuralValidator(),
		safety:     NewContextualSafetyEvaluator(),
		applicator: NewFileApplicator(),
	}
}

func (e *Engine) WithParser(p Parser) *Engine          { e.parser = p; return e }
func (e *Engine) WithValidator(v Validator) *Engine    { e.validator = v; return e }
func (e *Engine) WithSafety(s SafetyEvaluator) *Engine { e.safety = s; return e }
func (e *Engine) WithApplicator(a Applicator) *Engine  { e.applicator = a; return e }
func (e *Engine) WithEventBus(bus *events.Bus) *Engine { e.bus = bus; return e }

// Apply runs the pipeline. It returns ErrAlreadyApplied (a benign no-op) when
// the file already reflects the patch, ErrApprovalRequired when Tier 4 must be
// engaged, ErrAmbiguousPatch when no tier can resolve the payload, and
// ErrSafetyViolation when the safety evaluator forbids the change.
func (e *Engine) Apply(root string, req Request) (ApplyResult, error) {
	// Read the authoritative disk state.
	current := req.Original
	if data, err := os.ReadFile(filepath.Join(root, req.File)); err == nil {
		current = string(data)
	}

	// ── Parse ─────────────────────────────────────────────────────────────
	// Parse against the authoritative on-disk content so hunks and SEARCH
	// contexts match the true file state.
	patch, err := e.parser.Parse(current, req.Raw)
	if err != nil {
		// A malformed payload with no usable tier is a hard rejection, not an
		// approval request: the model must retry with a well-formed patch.
		if errors.Is(err, ErrAmbiguousPatch) {
			e.emitRejected(req.File, err.Error(), int(Tier3WholeFile))
			return ApplyResult{}, err
		}
		// Tier 1/2 hunk mismatch: fall through to Tier 4 human approval before
		// giving up — the caller may resolve the file state and retry.
		e.emitRejected(req.File, err.Error(), int(Tier2SearchReplace))
		return ApplyResult{}, ErrApprovalRequired
	}
	patch.File = req.File
	e.emitParsed(patch)

	// ── Validate ──────────────────────────────────────────────────────────
	report := e.validator.Validate(patch, current)
	if report.AlreadyApplied {
		e.emitRejected(patch.File, "patch already applied", int(patch.Tier))
		return ApplyResult{
			AlreadyApplied: true,
			File:           patch.File,
			Tier:           patch.Tier,
			Strategy:       patch.Strategy,
		}, ErrAlreadyApplied
	}
	if !report.Valid {
		reason := strings.Join(report.Reasons, "; ")
		e.emitRejected(patch.File, reason, int(patch.Tier))
		return ApplyResult{}, fmt.Errorf("%w: %s", ErrAmbiguousPatch, reason)
	}
	e.emitValidated(patch)

	// ── Safety ────────────────────────────────────────────────────────────
	decision := e.safety.Evaluate(patch, SafetyContext{
		TaskObjective: req.TaskObjective,
		FileType:      req.FileType,
		Approved:      req.Approved,
		Tier:          patch.Tier,
	})
	if !decision.Allowed {
		if decision.RequiresApproval && !req.Approved {
			e.emitApproval(patch.File, decision.Reason)
			return ApplyResult{}, ErrApprovalRequired
		}
		e.emitRejected(patch.File, decision.Reason, int(patch.Tier))
		return ApplyResult{}, fmt.Errorf("%w: %s", ErrSafetyViolation, decision.Reason)
	}

	// ── Apply ─────────────────────────────────────────────────────────────
	result, err := e.applicator.Apply(root, patch)
	if err != nil {
		e.emitRejected(patch.File, err.Error(), int(patch.Tier))
		return ApplyResult{}, err
	}
	return result, nil
}

func (e *Engine) emitParsed(p Patch) {
	if e.bus == nil {
		return
	}
	e.bus.Publish(events.NewPatchParsed(p.File, p.Strategy, int(p.Tier)))
}

func (e *Engine) emitValidated(p Patch) {
	if e.bus == nil {
		return
	}
	e.bus.Publish(events.NewPatchValidated(p.File, p.Strategy, int(p.Tier)))
}

func (e *Engine) emitRejected(file, reason string, tier int) {
	if e.bus == nil {
		return
	}
	e.bus.Publish(events.NewPatchRejected(file, reason, tier))
}

func (e *Engine) emitApproval(file, reason string) {
	if e.bus == nil {
		return
	}
	e.bus.Publish(events.NewApprovalRequested(file, reason, ""))
}
