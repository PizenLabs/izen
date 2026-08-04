package build

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/PizenLabs/izen/internal/controlplane/guard"
	"github.com/PizenLabs/izen/internal/engine"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/runtime/output"
	wschk "github.com/PizenLabs/izen/internal/workspace/checkpoint"
	wsfail "github.com/PizenLabs/izen/internal/workspace/failure"
)

type FileMutation struct {
	File    string
	Content string
	Mode    MutationMode

	// TaskID links this patch to a /plan ledger task. A value > 0 causes the
	// build engine to mark the task Completed in the shared ledger on commit.
	TaskID int
	// Strategy is the plan strategy label (e.g. ATOMIC_REPLACE) recorded in the
	// execution summary. Falls back to ATOMIC_REPLACE when empty.
	Strategy string
}

type MutationMode int

const (
	ModeDiff MutationMode = iota
	ModeFullRewrite
)

// ErrScopeFailure indicates a mutation target falls outside the authorized
// scope or matches a reserved system keyword. Triggers SCOPE_FAILURE recovery.
var ErrScopeFailure = errors.New("scope failure: mutation target is invalid or reserved")

// DefaultMaxSelfHealingRetries bounds the automatic patch re-generation
// attempts after a failed build verification in the self-healing loop.
const DefaultMaxSelfHealingRetries = 3

// Config holds tunable build-executor settings.
type Config struct {
	// MaxSelfHealingRetries bounds the automatic patch re-generation attempts
	// after a failed build verification. Negative values disable retries. The
	// zero value is replaced by DefaultMaxSelfHealingRetries.
	MaxSelfHealingRetries int
}

// DefaultConfig returns the build-executor configuration with the default
// self-healing retry bound (DefaultMaxSelfHealingRetries).
func DefaultConfig() Config {
	return Config{MaxSelfHealingRetries: DefaultMaxSelfHealingRetries}
}

// BuildResult reports the outcome of ExecuteBuildLoop.
type BuildResult struct {
	// Success reports whether the loop ended with a passing verification.
	Success bool
	// Attempts is the number of attempts executed (initial + retries).
	Attempts int
	// Failures collects the structured failure record of every failed attempt,
	// so retries-exhausted callers can surface all attempted failure reasons.
	Failures []AttemptFailure
	// FinalOutput is the verification output of the final failed attempt.
	FinalOutput string
	// Err is set when the loop aborted early (rollback failure, re-generation
	// error) or when retries were exhausted.
	Err error
}

// AttemptFailure is the structured record of a single failed loop attempt.
type AttemptFailure struct {
	// Attempt is the 1-based attempt counter.
	Attempt int
	// Output is the raw verification or apply error output of this attempt.
	Output string
	// Classification is the deterministic failure analysis of Output.
	Classification wsfail.FailureClassification
	// Feedback is the LLM feedback context built from this attempt's failure.
	Feedback string
	// Err is the underlying error, when the attempt failed outside verification
	// (e.g. a mutation apply error).
	Err error
}

// Report renders the structured failure report. On success it states the final
// attempt count; on failure it lists every attempted failure reason (category,
// attempt number, and the first output line).
func (r *BuildResult) Report() string {
	var b strings.Builder
	if r.Success {
		fmt.Fprintf(&b, "build succeeded after %d attempt(s)\n", r.Attempts)
		return b.String()
	}
	fmt.Fprintf(&b, "build failed after %d attempt(s)\n", r.Attempts)
	for _, af := range r.Failures {
		msg := firstOutputLine(af.Output)
		if msg == "" && af.Err != nil {
			msg = firstOutputLine(af.Err.Error())
		}
		fmt.Fprintf(&b, "- attempt %d [%s]: %s\n", af.Attempt, af.Classification.Category, msg)
	}
	return b.String()
}

// PatchGenerator re-generates a patch set from a self-healing feedback context
// after a failed build verification. It returns the corrected mutations for the
// next attempt; returning an error aborts the loop immediately.
type PatchGenerator func(feedback string, attempt int) ([]FileMutation, error)

type Executor struct {
	root       string
	engine     *Engine
	tx         *engine.Transaction
	scopeGuard *guard.ScopeGuard

	// checkpointMgr, when wired, protects every ApplyMutation with an atomic
	// rollback checkpoint: the target's original state is captured before the
	// write and restored if the patch or subsequent compilation fails. Nil
	// disables checkpointing entirely (headless/CLI fallbacks unchanged).
	checkpointMgr *wschk.Manager

	// maxSelfHealingRetries bounds the automatic patch re-generation attempts
	// in ExecuteBuildLoop. Defaults to DefaultMaxSelfHealingRetries.
	maxSelfHealingRetries int

	// verifyCompilation, when set, replaces the real `go build` invocation used
	// by VerifyCompilation, CheckAndRecover, and ExecuteBuildLoop. It is the
	// deterministic hook tests use to drive the self-healing loop without a Go
	// toolchain. Nil preserves the real compiler path.
	verifyCompilation func(ctx context.Context, packages ...string) (bool, string, error)

	// pipeline is the Phase 1 Tool Output Intelligence pipeline. Compilation
	// output is normalized, classified, semantically compressed and (with a
	// workspace tee) logged to `.logs/`. Nil keeps the executor a pure
	// transformation-free verifier (headless/tests).
	pipeline *output.Pipeline
}

func NewExecutor(root string, engine *Engine) *Executor {
	return &Executor{
		root:                  root,
		engine:                engine,
		maxSelfHealingRetries: DefaultMaxSelfHealingRetries,
		pipeline:              output.New().WithWorkspace(root),
	}
}

// WithPipeline overrides the output pipeline used for compilation output. Nil
// disables normalization/compression/tee-logging.
func (ex *Executor) WithPipeline(p *output.Pipeline) *Executor {
	ex.pipeline = p
	return ex
}

// WithConfig overrides the executor configuration (self-healing retry bound).
func (ex *Executor) WithConfig(cfg Config) *Executor {
	ex.maxSelfHealingRetries = cfg.MaxSelfHealingRetries
	return ex
}

// WithMaxSelfHealingRetries sets the self-healing retry bound. Values <= 0
// disable automatic retries (a single guarded attempt).
func (ex *Executor) WithMaxSelfHealingRetries(n int) *Executor {
	ex.maxSelfHealingRetries = n
	return ex
}

// WithVerifyCompilation wires a deterministic verification hook used in place
// of the real `go build` invocation. Nil restores the real compiler path.
func (ex *Executor) WithVerifyCompilation(fn func(ctx context.Context, packages ...string) (bool, string, error)) *Executor {
	ex.verifyCompilation = fn
	return ex
}

// WithCheckpointManager wires a checkpoint manager so every mutation applied
// through this executor is protected by an atomic rollback checkpoint. Nil
// disables checkpointing.
func (ex *Executor) WithCheckpointManager(mgr *wschk.Manager) *Executor {
	ex.checkpointMgr = mgr
	return ex
}

// CommitOpenCheckpoints finalizes every open checkpoint after successful
// build/test verification, discarding the buffered original blobs. It is a
// no-op when no checkpoint manager is wired.
func (ex *Executor) CommitOpenCheckpoints() error {
	if ex.checkpointMgr == nil {
		return nil
	}
	return ex.checkpointMgr.CommitAll()
}

// RollbackOpenCheckpoints atomically restores the workspace to the state
// captured by every open checkpoint: modified files are restored byte-exactly
// and newly created files are deleted. It is a no-op when no checkpoint
// manager is wired.
func (ex *Executor) RollbackOpenCheckpoints() error {
	if ex.checkpointMgr == nil {
		return nil
	}
	return ex.checkpointMgr.RollbackAll()
}

// SetScopeGuard attaches a scope guard for pre- and post-mutation validation.
// When set, every ApplyMutation call first validates the target path via
// ValidateMutationTarget and, on success, verifies the mutated file is within
// the authorized scope declaration.
func (ex *Executor) SetScopeGuard(sg *guard.ScopeGuard) {
	ex.scopeGuard = sg
}

func (ex *Executor) SetTransaction(tx *engine.Transaction) {
	ex.tx = tx
}

func (ex *Executor) ApplyMutation(ctx context.Context, mut FileMutation) (retErr error) {
	start := time.Now()
	strategy := mutationStrategy(mut)
	if ex.engine != nil {
		ex.engine.emit(events.NewPatchAttempted(mut.File, strategy, 1))
	}

	// Pre-mutation scope guard: reject reserved keywords and out-of-scope paths.
	if ex.scopeGuard != nil {
		if err := ex.scopeGuard.ValidateMutationTarget(mut.File, nil); err != nil {
			ex.emitFailure(events.FailurePermanent, fmt.Errorf("%w: %s", ErrScopeFailure, err.Error()), "build.scope")
			return fmt.Errorf("%w: %s", ErrScopeFailure, err.Error())
		}
	}

	absPath := filepath.Join(ex.root, mut.File)
	dir := filepath.Dir(absPath)

	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create directory %s: %w", dir, err)
	}

	if ex.tx != nil {
		if err := ex.tx.Record(absPath); err != nil {
			return fmt.Errorf("transaction record %s: %w", mut.File, err)
		}
	}

	// Check if this is a file mutation to README.md - prohibit and require explicit SHELL_EXEC
	if strings.Contains(mut.File, "README.md") {
		return fmt.Errorf("README.md modifications prohibited; use SHELL_EXEC for dependency fixes instead")
	}

	// Guardrail: every build mutation MUST be explicitly validated by a human
	// operator via the Proposal system. Reject any mutation that has not been
	// queued and approved — this enforces the fail-closed Human-in-the-Loop
	// contract. The UI routes approved proposals to this executor; unvetted
	// mutations are never applied to disk.
	if !ex.engine.IsApprovedByFile(mut.File, mut.TaskID) {
		err := fmt.Errorf("human validation required for %s: mutation must be approved via Proposal UI before execution", mut.File)
		ex.emitFailure(events.FailureRecoverable, err, "build.guardrail")
		return err
	}

	// Stateful checkpoint: capture the target's byte-exact original state before
	// writing any patch to disk. If the write, a post-mutation scope check, or
	// the subsequent compilation verification fails, the mutation is rolled
	// back atomically. The checkpoint stays open on success until compilation
	// and tests pass (CommitOpenCheckpoints) or fail (RollbackOpenCheckpoints).
	if ex.checkpointMgr != nil {
		chk, err := ex.checkpointMgr.CreateCheckpoint("build.executor", []string{mut.File})
		if err != nil {
			ex.emitFailure(events.FailurePermanent, err, "build.checkpoint")
			return fmt.Errorf("checkpoint %s: %w", mut.File, err)
		}
		defer func() {
			if retErr != nil {
				if rbErr := ex.checkpointMgr.Rollback(chk.ID); rbErr != nil {
					retErr = fmt.Errorf("%w; rollback %s failed: %w", retErr, mut.File, rbErr)
				}
			}
		}()
	}

	if err := os.WriteFile(absPath, []byte(mut.Content), 0644); err != nil {
		ex.emitFailure(events.FailureRecoverable, err, "build.patch")
		return err
	}
	ex.engine.RecordPatch(mut.TaskID, mut.File, strategy)
	if ex.engine != nil {
		ex.engine.emit(events.NewPatchApplied(mut.File, countLines(mut.Content), 0, time.Since(start)))
	}

	// Post-mutation scope verification: confirm the file was written within
	// the authorized scope. If the file drifted outside scope, trigger rollback.
	if ex.scopeGuard != nil {
		if err := ex.scopeGuard.ValidateMutationTarget(mut.File, nil); err != nil {
			return fmt.Errorf("%w: post-mutation scope drift: %s", ErrScopeFailure, err.Error())
		}
	}

	return nil
}

// emitFailure routes a build failure to the event bus when an engine is wired.
func (ex *Executor) emitFailure(class events.FailureClassification, err error, stage string) {
	if ex != nil && ex.engine != nil {
		ex.engine.emit(events.NewExecutionFailed(class, err, stage))
	}
}

// countLines returns the number of lines in content. Empty content is 0.
func countLines(content string) int {
	n := strings.Count(content, "\n")
	if content != "" && !strings.HasSuffix(content, "\n") {
		n++
	}
	return n
}

// mutationStrategy resolves the strategy label recorded in the execution
// summary, defaulting to ATOMIC_REPLACE when unset.
func mutationStrategy(mut FileMutation) string {
	if mut.Strategy != "" {
		return mut.Strategy
	}
	return "ATOMIC_REPLACE"
}

func (ex *Executor) VerifyCompilation(ctx context.Context, packages ...string) (bool, string, error) {
	args := []string{"build"}
	if len(packages) > 0 {
		args = append(args, packages...)
	} else {
		args = append(args, "./...")
	}

	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = ex.root
	output, err := cmd.CombinedOutput()

	// ── TOOL OUTPUT PIPELINE (PHASE 1) ──────────────────────────────────
	// Compilation output is normalized, classified (GO_TEST/LINTER_GO/GENERIC),
	// semantically compressed and tee-logged to `.logs/` so the planner's log
	// source and failure analysis always see the canonical form. The raw
	// output still drives the success/failure return below.
	if ex.pipeline != nil {
		ex.pipeline.Process("go "+strings.Join(args, " "), output)
	}

	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return false, string(output), nil
		}
		return false, string(output), err
	}
	return true, "", nil
}

// verify runs the compilation check through the deterministic override when one
// is wired, falling back to the real `go build` invocation otherwise. It is the
// single verification path shared by CheckAndRecover and ExecuteBuildLoop.
func (ex *Executor) verify(ctx context.Context, packages ...string) (bool, string, error) {
	if ex.verifyCompilation != nil {
		return ex.verifyCompilation(ctx, packages...)
	}
	return ex.VerifyCompilation(ctx, packages...)
}

func (ex *Executor) CheckAndRecover(ctx context.Context, file string, content string, packages ...string) (bool, string, error) {
	ok, output, err := ex.verify(ctx, packages...)
	if err != nil {
		return false, output, err
	}
	if ok {
		ex.engine.RecordCompilationSuccess()
		// Compilation and tests passed — finalize every open checkpoint and
		// discard the buffered original blobs.
		if err := ex.CommitOpenCheckpoints(); err != nil {
			return false, output, fmt.Errorf("commit checkpoints: %w", err)
		}
		return true, "", nil
	}

	ex.engine.RecordCompilationFailure(file)

	// Compilation failed — automatically roll back every open checkpoint so the
	// workspace returns to its exact pre-mutation state before any retry.
	if err := ex.RollbackOpenCheckpoints(); err != nil {
		return false, output, fmt.Errorf("rollback checkpoints: %w", err)
	}

	if ex.engine.MustRewriteEntireFile(file) {
		mut := FileMutation{
			File:    file,
			Content: content,
			Mode:    ModeFullRewrite,
		}
		if err := ex.ApplyMutation(ctx, mut); err != nil {
			return false, output, fmt.Errorf("force rewrite failed: %w", err)
		}
	}

	return false, output, nil
}

// ExecuteBuildLoop orchestrates the self-healing build loop. For each attempt:
//
//  1. Capture a baseline checkpoint (chk_0) covering every target file before
//     any mutation is written.
//  2. Apply the patch set and run verification (the loop-level equivalent of
//     CheckAndRecover, orchestrated across the whole batch so one baseline
//     checkpoint protects every mutation).
//  3. On verification failure: roll the workspace back to its clean pre-mutation
//     state, classify the output via the Failure Analysis Subsystem, emit a
//     SelfHealingAttempt event (with the retry count) on the event bus, build a
//     feedback context from the classification + attempted patch, and hand it to
//     the patch generator to re-generate a corrected patch.
//  4. On verification success: commit the baseline checkpoint and return.
//  5. When retries are exhausted: ensure the workspace is cleanly rolled back
//     and return a structured failure report listing every attempted failure.
//
// generator re-generates patches after a failure. When it is nil the loop runs
// a single guarded attempt (apply, verify, rollback on failure) — the backward
// compatible behavior for callers that do not wire self-healing. Packages are
// passed to the verification step (default ./...).
func (ex *Executor) ExecuteBuildLoop(ctx context.Context, mutations []FileMutation, generator PatchGenerator, packages ...string) (*BuildResult, error) {
	total := 1 + ex.maxSelfHealingRetries
	if generator == nil {
		total = 1
	}
	if total < 1 {
		total = 1
	}

	result := &BuildResult{}

	for attempt := 1; attempt <= total; attempt++ {
		result.Attempts = attempt
		// Reset the engine's legacy recovery state so CheckAndRecover-style
		// escalation (force full rewrite) never pollutes a self-healing retry:
		// re-generation is owned by the patch generator, not by failure counts.
		if ex.engine != nil {
			ex.engine.Reset()
		}

		master, err := ex.captureBaseline(mutations)
		if err != nil {
			result.Err = err
			return result, err
		}

		var applyErr error
		failedFile := ""
		for _, mut := range mutations {
			if err := ex.ApplyMutation(ctx, mut); err != nil {
				applyErr = err
				failedFile = mut.File
				break
			}
		}

		if applyErr == nil {
			ok, output, verr := ex.verify(ctx, packages...)
			if verr != nil {
				ex.rollbackAttempt(master)
				af := ex.recordFailure(attempt, firstTargetFile(mutations), mutations, output, verr)
				result.Failures = append(result.Failures, af)
				result.Err = verr
				return result, verr
			}
			if ok {
				if err := ex.finalizeSuccess(master); err != nil {
					result.Err = err
					return result, err
				}
				result.Success = true
				return result, nil
			}
			ex.rollbackAttempt(master)
			af := ex.recordFailure(attempt, firstTargetFile(mutations), mutations, output, nil)
			result.Failures = append(result.Failures, af)
			result.FinalOutput = output
		} else {
			ex.rollbackAttempt(master)
			af := ex.recordFailure(attempt, failedFile, mutations, "", applyErr)
			result.Failures = append(result.Failures, af)
			result.FinalOutput = applyErr.Error()
		}

		if attempt >= total {
			result.Err = ex.exhausted(mutations, result)
			return result, result.Err
		}

		next, err := ex.regenerate(generator, result.Failures[len(result.Failures)-1])
		if err != nil {
			result.Err = err
			return result, err
		}
		mutations = next
	}

	return result, result.Err
}

// captureBaseline snapshots the pre-mutation state of every target file so the
// whole attempt can be rolled back atomically. Returns the empty ID when no
// checkpoint manager is wired.
func (ex *Executor) captureBaseline(mutations []FileMutation) (wschk.CheckpointID, error) {
	if ex.checkpointMgr == nil {
		return "", nil
	}
	chk, err := ex.checkpointMgr.CreateCheckpoint("build.selfheal", targetFiles(mutations))
	if err != nil {
		return "", fmt.Errorf("self-heal baseline checkpoint: %w", err)
	}
	return chk.ID, nil
}

// rollbackAttempt restores the workspace to its clean pre-attempt state: every
// open per-mutation checkpoint is rolled back first, then the baseline
// checkpoint if it is still open (RollbackOpenCheckpoints may have consumed it).
func (ex *Executor) rollbackAttempt(master wschk.CheckpointID) {
	if ex.checkpointMgr == nil {
		return
	}
	if err := ex.RollbackOpenCheckpoints(); err != nil {
		ex.noteError(fmt.Errorf("self-heal rollback open checkpoints: %w", err))
	}
	if master != "" && ex.checkpointMgr.Get(master) != nil {
		if err := ex.checkpointMgr.Rollback(master); err != nil {
			ex.noteError(fmt.Errorf("self-heal rollback baseline %s: %w", master, err))
		}
	}
}

// finalizeSuccess commits every open per-mutation checkpoint and the baseline
// checkpoint, discarding the buffered original blobs. CommitOpenCheckpoints
// commits every open checkpoint (including the baseline), so the explicit
// baseline commit only runs if the baseline is still open.
func (ex *Executor) finalizeSuccess(master wschk.CheckpointID) error {
	if err := ex.CommitOpenCheckpoints(); err != nil {
		return fmt.Errorf("self-heal commit checkpoints: %w", err)
	}
	if ex.checkpointMgr != nil && master != "" && ex.checkpointMgr.Get(master) != nil {
		if err := ex.checkpointMgr.Commit(master); err != nil {
			return fmt.Errorf("self-heal commit baseline %s: %w", master, err)
		}
	}
	if ex.engine != nil {
		ex.engine.emit(events.NewStageCompleted("build.selfheal", 0, "self-healing loop completed successfully"))
	}
	return nil
}

// recordFailure classifies a failed attempt, builds its feedback context, and
// emits the self-healing attempt event. It is a pure function over the attempt.
func (ex *Executor) recordFailure(attempt int, file string, mutations []FileMutation, output string, err error) AttemptFailure {
	raw := output
	if raw == "" && err != nil {
		raw = err.Error()
	}
	class := wsfail.ClassifyError(raw)
	af := AttemptFailure{
		Attempt:        attempt,
		Output:         output,
		Classification: class,
		Feedback:       wsfail.BuildFeedbackContext(class, attemptDiff(mutations)),
		Err:            err,
	}
	if ex.engine != nil {
		ex.engine.emit(events.NewSelfHealingAttempt(attempt, file, class.Category.String()))
	}
	return af
}

// exhausted finalizes a retries-exhausted run: the workspace was already rolled
// back by the failing attempt, and the structured report carries every attempted
// failure reason. It emits the exhausted event and returns a descriptive error.
func (ex *Executor) exhausted(mutations []FileMutation, result *BuildResult) error {
	summary := make([]string, 0, len(result.Failures))
	for _, af := range result.Failures {
		msg := firstOutputLine(af.Output)
		if af.Err != nil {
			msg = firstOutputLine(af.Err.Error())
		}
		summary = append(summary, fmt.Sprintf("attempt %d [%s]: %s", af.Attempt, af.Classification.Category, msg))
	}
	err := fmt.Errorf("self-healing exhausted after %d attempt(s): %s", result.Attempts, strings.Join(summary, "; "))
	if ex.engine != nil {
		ex.engine.emit(events.NewSelfHealingExhausted(result.Attempts, result.FinalOutput))
		ex.engine.emit(events.NewExecutionFailed(events.FailureRecoverable, err, "build.selfheal"))
	}
	return err
}

// regenerate hands the failure feedback context to the patch generator for a
// corrected patch set on the next attempt.
func (ex *Executor) regenerate(generator PatchGenerator, af AttemptFailure) ([]FileMutation, error) {
	next, err := generator(af.Feedback, af.Attempt+1)
	if err != nil {
		return nil, fmt.Errorf("self-healing re-generation after attempt %d: %w", af.Attempt, err)
	}
	return next, nil
}

// noteError folds a best-effort cleanup error into the loop result. It is
// surfaced via the event bus so projections observe rollback failures.
func (ex *Executor) noteError(err error) {
	if ex.engine != nil {
		ex.engine.emit(events.NewExecutionFailed(events.FailurePermanent, err, "build.selfheal.rollback"))
	}
}

// attemptDiff renders the attempted patch set as a compact diff-like payload
// for the feedback context ("WHAT WAS ATTEMPTED" section).
func attemptDiff(mutations []FileMutation) string {
	if len(mutations) == 0 {
		return ""
	}
	var b strings.Builder
	for _, mut := range mutations {
		fmt.Fprintf(&b, "FILE: %s\n```\n%s\n```\n", mut.File, mut.Content)
	}
	return b.String()
}

// targetFiles returns the deduplicated, ordered set of files a patch set
// targets, used to scope the baseline checkpoint.
func targetFiles(mutations []FileMutation) []string {
	seen := make(map[string]bool, len(mutations))
	files := make([]string, 0, len(mutations))
	for _, mut := range mutations {
		if mut.File == "" || seen[mut.File] {
			continue
		}
		seen[mut.File] = true
		files = append(files, mut.File)
	}
	return files
}

// firstTargetFile returns the first non-empty target file of a patch set, used
// to attribute a verification failure that is not tied to a single mutation.
func firstTargetFile(mutations []FileMutation) string {
	for _, mut := range mutations {
		if mut.File != "" {
			return mut.File
		}
	}
	return ""
}

// firstOutputLine returns the first non-empty line of a multi-line string.
func firstOutputLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	if len(s) > 160 {
		return s[:157] + "..."
	}
	return s
}

func ParseBuildOutput(output string) []FileMutation {
	var mutations []FileMutation
	lines := strings.Split(output, "\n")

	var currentFile string
	var currentContent strings.Builder
	var inBlock bool
	var inDiff bool

	flush := func() {
		if currentFile != "" && currentContent.Len() > 0 {
			mode := ModeDiff
			if inDiff {
				mode = ModeDiff
			}
			mutations = append(mutations, FileMutation{
				File:    currentFile,
				Content: strings.TrimSpace(currentContent.String()),
				Mode:    mode,
			})
			currentFile = ""
			currentContent.Reset()
			inBlock = false
			inDiff = false
		}
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "FILE:") {
			flush()
			currentFile = strings.TrimSpace(strings.TrimPrefix(trimmed, "FILE:"))
			continue
		}

		if strings.HasPrefix(trimmed, "--- a/") {
			flush()
			filePart := strings.TrimPrefix(trimmed, "--- a/")
			filePart = strings.TrimSpace(filePart)
			if idx := strings.IndexAny(filePart, " \t"); idx != -1 {
				filePart = filePart[:idx]
			}
			currentFile = filePart
			inDiff = true
			currentContent.WriteString(line)
			currentContent.WriteString("\n")
			continue
		}

		if strings.HasPrefix(trimmed, "+++ b/") {
			currentContent.WriteString(line)
			currentContent.WriteString("\n")
			continue
		}

		if strings.HasPrefix(trimmed, "@@") && strings.Contains(trimmed, "@@") {
			currentContent.WriteString(line)
			currentContent.WriteString("\n")
			continue
		}

		if strings.HasPrefix(line, "```") {
			if inBlock {
				flush()
			} else {
				flush()
				inBlock = true
			}
			continue
		}

		if currentFile != "" {
			currentContent.WriteString(line)
			currentContent.WriteString("\n")
		}
	}

	flush()
	return mutations
}
