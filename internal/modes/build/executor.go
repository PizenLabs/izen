package build

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/PizenLabs/izen/internal/controlplane/guard"
	"github.com/PizenLabs/izen/internal/engine"
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

type Executor struct {
	root       string
	engine     *Engine
	tx         *engine.Transaction
	scopeGuard *guard.ScopeGuard
}

func NewExecutor(root string, engine *Engine) *Executor {
	return &Executor{
		root:   root,
		engine: engine,
	}
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

func (ex *Executor) ApplyMutation(ctx context.Context, mut FileMutation) error {
	// Pre-mutation scope guard: reject reserved keywords and out-of-scope paths.
	if ex.scopeGuard != nil {
		if err := ex.scopeGuard.ValidateMutationTarget(mut.File, nil); err != nil {
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
		return fmt.Errorf("human validation required for %s: mutation must be approved via Proposal UI before execution", mut.File)
	}

	if err := os.WriteFile(absPath, []byte(mut.Content), 0644); err != nil {
		return err
	}
	ex.engine.RecordPatch(mut.TaskID, mut.File, mutationStrategy(mut))

	// Post-mutation scope verification: confirm the file was written within
	// the authorized scope. If the file drifted outside scope, trigger rollback.
	if ex.scopeGuard != nil {
		if err := ex.scopeGuard.ValidateMutationTarget(mut.File, nil); err != nil {
			return fmt.Errorf("%w: post-mutation scope drift: %s", ErrScopeFailure, err.Error())
		}
	}

	return nil
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

	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return false, string(output), nil
		}
		return false, string(output), err
	}
	return true, "", nil
}

func (ex *Executor) CheckAndRecover(ctx context.Context, file string, content string, packages ...string) (bool, string, error) {
	ok, output, err := ex.VerifyCompilation(ctx, packages...)
	if err != nil {
		return false, output, err
	}
	if ok {
		ex.engine.RecordCompilationSuccess()
		return true, "", nil
	}

	ex.engine.RecordCompilationFailure(file)

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
