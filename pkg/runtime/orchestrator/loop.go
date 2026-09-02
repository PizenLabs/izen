package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/PizenLabs/izen/pkg/runtime/executor"
	"github.com/PizenLabs/izen/pkg/runtime/gate"
	"github.com/PizenLabs/izen/pkg/runtime/harness"
)

// LoopState enumerates the states of the closed execution loop.
type LoopState int

const (
	// StateIdle is the initial state before any cycle begins.
	StateIdle LoopState = iota
	// StateExecuting means model output is being streamed into the RMAH
	// extractor (Model Output -> RMAH).
	StateExecuting
	// StateVerifying means the candidate is being validated through the gate
	// pipeline (RMAH -> Gate).
	StateVerifying
	// StateAwaitingHuman means the loop halted on format/schema/ambiguity and
	// diverted to the DecisionSurface with diagnostic evidence attached.
	StateAwaitingHuman
	// StateCommitted means the candidate was committed atomically via the sole
	// authority (Gate -> RuntimeExecutor).
	StateCommitted
	// StateFailed means an unrecoverable internal error occurred.
	StateFailed
)

// String returns a stable lowercase label for the loop state.
func (s LoopState) String() string {
	switch s {
	case StateIdle:
		return "idle"
	case StateExecuting:
		return "executing"
	case StateVerifying:
		return "verifying"
	case StateAwaitingHuman:
		return "awaiting_human"
	case StateCommitted:
		return "committed"
	case StateFailed:
		return "failed"
	default:
		return "unknown"
	}
}

// maxFormatFailures is the consecutive format/schema rejection budget. After
// this many consecutive RMAH/Gate rejections (or any Ambiguous flag) the loop
// halts immediately and diverts to the DecisionSurface — it never burns tokens
// on endless retries.
const maxFormatFailures = 2

// MemorySnapshot is the Observation-phase capture of a target file's bytes. It
// is loaded ONCE per cycle and is the single byte source consumed by RMAH, the
// Gate Pipeline, and the RuntimeExecutor — never a disk read.
type MemorySnapshot struct {
	// TargetFile is the resolved canonical target path.
	TargetFile string
	// Content is the target file's raw bytes.
	Content []byte
}

// ModelOutputExtractor is the RMAH translation boundary of the loop. It
// streams raw model output into a harness.CandidateArtifact anchored against
// the Observation-phase memory buffer (never the disk).
type ModelOutputExtractor interface {
	Extract(ctx context.Context, memoryBuffer []byte, rawModelOutput []byte) (harness.CandidateArtifact, error)
}

// GatePipeline is the validation boundary of the loop. It evaluates a
// candidate against the Observation-phase memory buffer and returns the
// composed gate verdict.
type GatePipeline interface {
	Evaluate(ctx context.Context, memoryBuffer []byte, candidate harness.CandidateArtifact) *gate.GateResult
}

// SnapshotReader loads a target file's bytes into the memory snapshot at the
// Observation phase. It is the ONLY disk-read authority in the loop.
type SnapshotReader interface {
	ReadSnapshot(ctx context.Context, path string) ([]byte, error)
}

// FSSnapshotReader reads target bytes through os.ReadFile. It is the default
// production reader; tests substitute a counting reader to prove zero
// disk-read redundancy during the extraction and verification sub-cycles.
type FSSnapshotReader struct{}

// ReadSnapshot reads path via os.ReadFile.
func (FSSnapshotReader) ReadSnapshot(_ context.Context, path string) ([]byte, error) {
	return os.ReadFile(path)
}

// MemoryBackedExtractor adapts the RMAH ExtractorPipeline to the loop boundary.
// It anchors Tier 2/3 extraction against the snapshot's target identity.
type MemoryBackedExtractor struct {
	// Pipeline is the underlying RMAH extraction pipeline.
	Pipeline *harness.ExtractorPipeline
	// TargetFile is the snapshot target passed to Tier 2/3 anchoring.
	TargetFile string
}

// NewMemoryBackedExtractor wraps a fresh RMAH pipeline anchored to targetFile.
func NewMemoryBackedExtractor(targetFile string) *MemoryBackedExtractor {
	return &MemoryBackedExtractor{Pipeline: harness.NewExtractorPipeline(), TargetFile: targetFile}
}

// Extract streams rawModelOutput through the RMAH pipeline against the memory
// buffer.
func (m *MemoryBackedExtractor) Extract(_ context.Context, memoryBuffer []byte, rawModelOutput []byte) (harness.CandidateArtifact, error) {
	return m.Pipeline.Extract(rawModelOutput, memoryBuffer, m.TargetFile)
}

// CycleOutcome reports the result of one ExecuteCycle invocation.
type CycleOutcome struct {
	// State is the loop state after the cycle.
	State LoopState
	// Candidate is the extracted candidate (nil when extraction failed).
	Candidate *harness.CandidateArtifact
	// Evidence is the diagnostic evidence attached to a halt (ArtifactEvidence
	// is carried onto the DecisionSurface diversion).
	Evidence harness.ArtifactEvidence
	// GateResult is the composed gate verdict, when the candidate reached the
	// gate pipeline.
	GateResult *gate.GateResult
	// Reason is a bounded human-readable justification.
	Reason string
}

// Loop is the Orchestrator Execution Loop. It owns the closed execution path
//
//	Model Output -> RMAH Extractor -> Gate Pipeline -> RuntimeExecutor
//
// with a hard fast-fail recovery budget: RMAH/Gate rejections for
// format/schema/syntax reasons halt after maxFormatFailures consecutive
// attempts, and any Ambiguous flag halts immediately — both divert to
// awaiting_human with diagnostic evidence instead of burning tokens on endless
// retries.
type Loop struct {
	harnessExtractor ModelOutputExtractor
	gatePipeline     GatePipeline
	executor         *executor.RuntimeExecutor
	reader           SnapshotReader

	snapshot *MemorySnapshot

	state          LoopState
	formatFailures int
	lastEvidence   harness.ArtifactEvidence
	lastReason     string
}

// NewLoop wires the closed execution path. A nil reader defaults to the
// filesystem reader.
func NewLoop(extractor ModelOutputExtractor, gp GatePipeline, exec *executor.RuntimeExecutor, reader SnapshotReader) *Loop {
	if reader == nil {
		reader = FSSnapshotReader{}
	}
	return &Loop{
		harnessExtractor: extractor,
		gatePipeline:     gp,
		executor:         exec,
		reader:           reader,
		state:            StateIdle,
	}
}

// Observe captures the target file's bytes into the memory snapshot ONCE per
// cycle (the Observation phase) and resets the recovery counters. It is the
// only disk read of the cycle.
func (l *Loop) Observe(ctx context.Context, path string) error {
	if l == nil {
		return errors.New("orchestrator loop: nil Loop")
	}
	if path == "" {
		return errors.New("orchestrator loop: observation requires a target path")
	}
	data, err := l.reader.ReadSnapshot(ctx, path)
	if err != nil {
		return fmt.Errorf("orchestrator loop: observe %q: %w", path, err)
	}
	l.snapshot = &MemorySnapshot{TargetFile: path, Content: data}
	l.formatFailures = 0
	l.state = StateExecuting
	return nil
}

// Snapshot returns the current memory snapshot, or nil before Observe.
func (l *Loop) Snapshot() *MemorySnapshot {
	if l == nil {
		return nil
	}
	return l.snapshot
}

// State returns the current loop state.
func (l *Loop) State() LoopState {
	if l == nil {
		return StateIdle
	}
	return l.state
}

// Evidence returns the diagnostic ArtifactEvidence attached to the most recent
// halt (awaiting_human diversion).
func (l *Loop) Evidence() harness.ArtifactEvidence {
	if l == nil {
		return harness.ArtifactEvidence{}
	}
	return l.lastEvidence
}

// Reason returns the bounded reason of the most recent halt.
func (l *Loop) Reason() string {
	if l == nil {
		return ""
	}
	return l.lastReason
}

// ExecuteCycle runs one model-output cycle over the Observation-phase memory
// snapshot:
//
//  1. Stream the response into the RMAH extractor.
//  2. Validate the candidate through the gate pipeline.
//  3. Commit only via the Sole Authority (RuntimeExecutor).
//
// On a format/schema rejection with fewer than maxFormatFailures consecutive
// failures the loop stays in StateExecuting (one bounded retry remains). On the
// second consecutive rejection, on any Ambiguous evidence, or on a gate
// escalation the loop halts to StateAwaitingHuman with the ArtifactEvidence
// attached — it never re-issues recovering model calls blindly.
func (l *Loop) ExecuteCycle(ctx context.Context, rawModelOutput []byte) (*CycleOutcome, error) {
	if l == nil {
		return nil, errors.New("orchestrator loop: nil Loop")
	}
	if l.harnessExtractor == nil {
		return nil, errors.New("orchestrator loop: no RMAH extractor wired")
	}
	if l.gatePipeline == nil {
		return nil, errors.New("orchestrator loop: no gate pipeline wired")
	}
	if l.executor == nil {
		return nil, errors.New("orchestrator loop: no runtime executor wired")
	}
	if l.snapshot == nil {
		return nil, errors.New("orchestrator loop: no memory snapshot — call Observe first")
	}

	// ── 1. Stream the response into the RMAH extractor. ────────────────
	l.state = StateExecuting
	candidate, err := l.harnessExtractor.Extract(ctx, l.snapshot.Content, rawModelOutput)
	if err != nil {
		return l.handleExtractionFailure(err)
	}

	// The candidate must address the observed target (basename identity): a
	// model naming a different file is a schema violation of the artifact
	// contract.
	if !targetConsistent(candidate.TargetFile, l.snapshot.TargetFile) {
		reason := fmt.Sprintf("gate: candidate targets %q, expected %q", candidate.TargetFile, l.snapshot.TargetFile)
		return l.handleFormatFailure(reason, candidate.Evidence)
	}
	// Re-anchor the candidate to the Observation-phase canonical identity: the
	// Sole Authority commits to the observed path, never a model-supplied
	// relative spelling.
	candidate.TargetFile = l.snapshot.TargetFile

	// ── 2. Validate through the gate pipeline. ─────────────────────────
	l.state = StateVerifying
	gateResult := l.gatePipeline.Evaluate(ctx, l.snapshot.Content, candidate)
	if !gateResult.Authorized {
		return l.handleGateRejection(candidate, gateResult)
	}

	// ── 3. Commit only via the Sole Authority (RuntimeExecutor). ───────
	if err := l.executor.CommitMutation(ctx, candidate, l.snapshot.Content); err != nil {
		l.state = StateFailed
		return &CycleOutcome{
			State:      StateFailed,
			Candidate:  &candidate,
			Evidence:   candidate.Evidence,
			GateResult: gateResult,
			Reason:     err.Error(),
		}, err
	}
	l.state = StateCommitted
	l.formatFailures = 0
	return &CycleOutcome{
		State:      StateCommitted,
		Candidate:  &candidate,
		Evidence:   candidate.Evidence,
		GateResult: gateResult,
	}, nil
}

// handleExtractionFailure routes an RMAH translation failure. Ambiguity halts
// immediately (fail-closed); every other translation failure is a format
// error subject to the bounded retry budget.
func (l *Loop) handleExtractionFailure(err error) (*CycleOutcome, error) {
	if errors.Is(err, harness.ErrAmbiguousMatch) {
		ev := harness.ArtifactEvidence{Ambiguous: true, Inferred: true}
		return l.haltAwaitingHuman(ev, "ambiguous model output: RMAH refused to guess")
	}
	return l.handleFormatFailure(err.Error(), harness.ArtifactEvidence{})
}

// handleFormatFailure applies the fast-fail budget to a format/schema/syntax
// rejection. After maxFormatFailures consecutive rejections the loop halts to
// awaiting_human with diagnostic evidence; otherwise it stays in StateExecuting
// for one bounded retry.
func (l *Loop) handleFormatFailure(reason string, evidence harness.ArtifactEvidence) (*CycleOutcome, error) {
	l.formatFailures++
	if l.formatFailures >= maxFormatFailures {
		ev := evidence
		ev.Inferred = true
		return l.haltAwaitingHuman(ev, fmt.Sprintf("RMAH/Gate rejected model output format on %d consecutive attempts: %s", l.formatFailures, reason))
	}
	l.state = StateExecuting
	return &CycleOutcome{
		State:    StateExecuting,
		Evidence: evidence,
		Reason:   reason,
	}, nil
}

// handleGateRejection routes a non-authorized gate verdict. Ambiguity and
// format/schema failures follow the fast-fail budget; escalations (valid
// candidates needing a human decision) park at awaiting_human immediately.
func (l *Loop) handleGateRejection(candidate harness.CandidateArtifact, res *gate.GateResult) (*CycleOutcome, error) {
	if candidate.Evidence.Ambiguous {
		ev := candidate.Evidence
		ev.Ambiguous = true
		return l.haltAwaitingHuman(ev, res.Reason)
	}
	if res.FormatError {
		return l.handleFormatFailure(res.Reason, candidate.Evidence)
	}
	return l.haltAwaitingHuman(candidate.Evidence, res.Reason)
}

// haltAwaitingHuman parks the loop at the DecisionSurface boundary with the
// diagnostic ArtifactEvidence attached.
func (l *Loop) haltAwaitingHuman(evidence harness.ArtifactEvidence, reason string) (*CycleOutcome, error) {
	l.state = StateAwaitingHuman
	l.lastEvidence = evidence
	l.lastReason = reason
	return &CycleOutcome{
		State:    StateAwaitingHuman,
		Evidence: evidence,
		Reason:   reason,
	}, nil
}

// targetConsistent reports whether candidatePath addresses the observed target
// path by basename identity. It is a deterministic schema guard: the executor
// is the sole authority on WHERE a mutation is applied, and it applies to the
// snapshot target only.
func targetConsistent(candidatePath, snapshotPath string) bool {
	if candidatePath == "" || snapshotPath == "" {
		return false
	}
	return filepath.Base(candidatePath) == filepath.Base(snapshotPath)
}
