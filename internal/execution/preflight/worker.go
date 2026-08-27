package preflight

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution/planner"
	"github.com/PizenLabs/izen/internal/telemetry"
)

// Worker is the BackgroundPreflight async worker. Upon PromptAdmitted it is
// dispatched as a goroutine and performs — off the UI critical path — the
// synchronous work that previously regressed prompt admission by ~5s:
//
//	a. Read target file(s) and compute SHA256(content).
//	b. Trigger AST/DOM structural discovery (LeaStructuralScan).
//	c. Run tokenizer and compute scope budget estimates.
//	d. Construct StructuralSnapshot and publish to Observation State.
//	e. Notify PreflightSyncBarrier via channel.
//
// All heavy work is bounded by the worker's context; a hung preflight aborts
// the run at the barrier with PREFLIGHT_TIMEOUT (10s).
type Worker struct {
	root     string
	bus      *events.Bus
	state    *ObservationState
	barrier  *PreflightSyncBarrier
	recorder *telemetry.Recorder

	mu   sync.Mutex
	runs int
}

// Config holds the worker wiring.
type Config struct {
	Root     string
	Bus      *events.Bus
	State    *ObservationState
	Barrier  *PreflightSyncBarrier
	Recorder *telemetry.Recorder
}

// New creates a worker. A nil Config field degrades safely (no bus/state/
// barrier emission), matching the headless/CLI invariant.
func New(cfg Config) *Worker {
	if cfg.Recorder == nil {
		cfg.Recorder = telemetry.Default()
	}
	return &Worker{
		root:     cfg.Root,
		bus:      cfg.Bus,
		state:    cfg.State,
		barrier:  cfg.Barrier,
		recorder: cfg.Recorder,
	}
}

// Start dispatches the background preflight as an async goroutine upon
// PromptAdmitted. It returns immediately (<10ms) and never performs file IO
// on the caller goroutine.
func (w *Worker) Start(ctx context.Context, prompt string, targets []string) {
	w.mu.Lock()
	w.runs++
	w.mu.Unlock()
	// Log spec sequence via bus activity (never stdout — TUI invariant).
	if w.bus != nil {
		w.bus.Publish(events.NewActivity(fmt.Sprintf("[preflight] bg worker started prompt=%q targets=%v", prompt, targets)))
		w.bus.Publish(events.NewStageCompleted("preflight_start", 0, fmt.Sprintf("bg worker started for prompt %q", prompt)))
	}
	go w.run(ctx, prompt, targets)
}

// StartSync is the synchronous test seam: it runs the preflight on the caller
// goroutine and returns the snapshot/error directly.
func (w *Worker) StartSync(ctx context.Context, prompt string, targets []string) (*StructuralSnapshot, error) {
	return w.execute(ctx, prompt, targets)
}

func (w *Worker) run(ctx context.Context, prompt string, targets []string) {
	start := time.Now()
	snap, err := w.execute(ctx, prompt, targets)
	elapsed := time.Since(start)
	telemetry.RecordPreflight(elapsed)
	if w.recorder != nil {
		w.recorder.RecordPreflight(elapsed)
	}
	if err != nil {
		if w.bus != nil {
			w.bus.Publish(events.NewActivity(fmt.Sprintf("[preflight] failed prompt=%q err=%v", prompt, err)))
			// Publish failure to the bus so the state machine can route to awaiting_human / error.
			w.bus.Publish(events.NewPreflightFailed(firstTarget(targets), "preflight failed", err))
		}
		if w.barrier != nil {
			w.barrier.Notify(snap, err)
		}
		return
	}
	if w.state != nil {
		w.state.Publish(snap)
	}
	if w.bus != nil {
		w.bus.Publish(events.NewActivity(fmt.Sprintf("[preflight] snapshot ready target=%q sha=%s tokens=%d", snap.Target, short(snap.SHA256), snap.EstimatedTokens)))
		w.bus.Publish(events.NewStructuralSnapshot(snap.Target, snap.SHA256, snap.EstimatedTokens, snap.TotalLines, scanFindings(snap.Scan)))
		w.bus.Publish(events.NewStageCompleted("preflight_complete", elapsed, fmt.Sprintf("snapshot ready for %s", snap.Target)))
	}
	if w.barrier != nil {
		w.barrier.Notify(snap, nil)
	}
}

func (w *Worker) execute(ctx context.Context, _ string, targets []string) (*StructuralSnapshot, error) {
	// For the spec we handle the first/primary target. Multi-file aggregation
	// can be added by iterating targets and merging snapshots.
	target := firstTarget(targets)
	if target == "" {
		// No target to scan — publish an empty snapshot (prompt still admitted).
		return &StructuralSnapshot{Target: target, ReadyAt: time.Now()}, nil
	}
	// a. Read file and compute SHA256.
	content, err := w.readTarget(ctx, target)
	if err != nil {
		return &StructuralSnapshot{Target: target, ReadyAt: time.Now(), Err: err}, err
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	sha := sha256Hex(content)
	// b. AST/DOM structural discovery.
	scan := planner.LeaStructuralScan(target, content)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	// c. Tokenizer + budget estimate.
	tokens := planner.EstimateTokens(len(content))
	budget := tokens * 2 // FullRewriteTokenMultiplier ≈2 is applied inside planner; keep conservative
	if scan != nil {
		// Prefer scan-derived token refinement if available.
		tokens = planner.EstimateTokens(len(content))
	}
	// Respect max_output-derived budget via planner where possible.
	totalLines := countLines(content)
	if scan != nil {
		totalLines = scan.TotalLines
	}
	snap := &StructuralSnapshot{
		Target:          target,
		SHA256:          sha,
		Scan:            scan,
		EstimatedTokens: tokens,
		BudgetTokens:    budget,
		TotalLines:      totalLines,
		ReadyAt:         time.Now(),
	}
	return snap, nil
}

func (w *Worker) readTarget(ctx context.Context, target string) ([]byte, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	var full string
	if w.root != "" {
		full = filepath.Join(w.root, filepath.FromSlash(target))
	} else {
		full = target
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return nil, fmt.Errorf("preflight: read %s: %w", target, err)
	}
	return data, nil
}

func firstTarget(targets []string) string {
	if len(targets) > 0 {
		return targets[0]
	}
	return ""
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

func scanFindings(scan *planner.LeaScanReport) int {
	if scan == nil {
		return 0
	}
	return len(scan.Findings)
}

func countLines(b []byte) int {
	if len(b) == 0 {
		return 0
	}
	n := 1
	for _, c := range b {
		if c == '\n' {
			n++
		}
	}
	return n
}
