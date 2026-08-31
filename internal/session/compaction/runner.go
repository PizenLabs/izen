package compaction

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/PizenLabs/izen/internal/session"
)

// Sink receives a produced generation. It is wired by the composition root to
// the SessionManager's SetCompactContext seam. Sink errors are surfaced via
// Runner.LastError — they never propagate into the main execution loop.
type Sink func(ctx context.Context, job Job, cc *session.CompactContext) error

// Job is one asynchronous compaction request: the slot to persist into, the
// durable session fields to preserve, the current full history and the last
// valid generation (nil when none exists).
type Job struct {
	Slot       session.SlotID
	SessionID  string
	Objective  string
	Mode       string
	Checkpoint string
	RunNumber  int
	CreatedAt  time.Time
	History    []session.Message
	Base       *session.CompactContext
}

// Runner executes compaction jobs on a background goroutine, strictly
// decoupled from the main execution loop: Submit NEVER blocks the caller. When
// the queue is full the job is dropped and counted (best-effort semantics —
// the next mutation triggers another submission, and compaction is always
// derivable from raw history, so a dropped run is never a data loss).
type Runner struct {
	engine *Engine
	sink   Sink
	queue  chan Job
	done   chan struct{}
	wg     sync.WaitGroup
	logFn  func(string, ...interface{})

	mu        sync.Mutex
	processed int
	dropped   int
	lastErr   error
}

// RunnerOption configures a Runner.
type RunnerOption func(*Runner)

// WithRunnerLogFn wires an activity sink for async lifecycle lines.
func WithRunnerLogFn(fn func(string, ...interface{})) RunnerOption {
	return func(r *Runner) {
		if fn != nil {
			r.logFn = fn
		}
	}
}

// WithRunnerQueueSize overrides the bounded async queue depth.
func WithRunnerQueueSize(n int) RunnerOption {
	return func(r *Runner) {
		if n > 0 {
			r.queue = make(chan Job, n)
		}
	}
}

// NewRunner builds a decoupled async compaction runner. Start must be called
// before Submit is useful.
func NewRunner(policy Policy, sink Sink, opts ...RunnerOption) *Runner {
	r := &Runner{
		engine: New(policy),
		sink:   sink,
		queue:  make(chan Job, 256),
		done:   make(chan struct{}),
		logFn:  func(string, ...interface{}) {},
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// Start launches the background worker. Idempotent.
func (r *Runner) Start() {
	if r == nil || r.done == nil {
		return
	}
	r.wg.Add(1)
	go r.worker()
}

// Submit enqueues a compaction job non-blockingly. A nil Runner or a closed
// Runner is a no-op; a full queue drops the job and counts it.
func (r *Runner) Submit(j Job) {
	if r == nil {
		return
	}
	select {
	case <-r.done:
		r.countDrop()
	case r.queue <- j:
	default:
		r.countDrop()
		r.logf("compaction: queue full — dropped job for slot %s", j.Slot)
	}
}

// Close stops the worker after draining the queued jobs. It is idempotent and
// safe to call concurrently with Submit.
func (r *Runner) Close() {
	if r == nil {
		return
	}
	select {
	case <-r.done:
		return
	default:
		close(r.done)
	}
	r.wg.Wait()
}

// Processed returns the number of jobs completed since Start.
func (r *Runner) Processed() int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.processed
}

// Dropped returns the number of jobs dropped because the queue was full or the
// runner closed.
func (r *Runner) Dropped() int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.dropped
}

// LastError returns the most recent sink error (observability only).
func (r *Runner) LastError() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastErr
}

func (r *Runner) worker() {
	defer r.wg.Done()
	for {
		select {
		case <-r.done:
			// Drain whatever remains so a Close never loses a queued run.
			for {
				select {
				case j := <-r.queue:
					r.run(context.Background(), j)
				default:
					return
				}
			}
		case j := <-r.queue:
			r.run(context.Background(), j)
		}
	}
}

func (r *Runner) run(ctx context.Context, j Job) {
	meta := GenerationMeta{
		SessionID:  j.SessionID,
		Objective:  j.Objective,
		Mode:       j.Mode,
		Checkpoint: j.Checkpoint,
		RunNumber:  j.RunNumber,
		CreatedAt:  j.CreatedAt,
	}
	next, _ := r.engine.RebuildFromLog(j.Base, j.History, meta)
	if r.sink != nil {
		if err := r.sink(ctx, j, next); err != nil {
			r.mu.Lock()
			r.lastErr = err
			r.mu.Unlock()
			r.logf("compaction: sink error for slot %s: %v", j.Slot, err)
		}
	}
	r.mu.Lock()
	r.processed++
	r.mu.Unlock()
}

// Compact synchronously runs the Generational Compactor over one job and
// returns the produced generation WITHOUT sinking it. It is the manual
// `/session compact <id>` seam: the caller (the presentation layer) persists
// the generation through the SessionManager's SetCompactContext and reports the
// result. It never touches the async worker's queue, so a manual run and the
// background runner cannot interleave on the same slot's state.
func (r *Runner) Compact(ctx context.Context, j Job) (*session.CompactContext, error) {
	if r == nil || r.engine == nil {
		return nil, errors.New("compaction: runner not wired")
	}
	meta := GenerationMeta{
		SessionID:  j.SessionID,
		Objective:  j.Objective,
		Mode:       j.Mode,
		Checkpoint: j.Checkpoint,
		RunNumber:  j.RunNumber,
		CreatedAt:  j.CreatedAt,
	}
	return r.engine.RebuildFromLog(j.Base, j.History, meta)
}

func (r *Runner) countDrop() {
	r.mu.Lock()
	r.dropped++
	r.mu.Unlock()
}

func (r *Runner) logf(format string, args ...interface{}) {
	if r != nil && r.logFn != nil {
		r.logFn(format, args...)
	}
}
