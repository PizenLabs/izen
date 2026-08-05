package execution

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/PizenLabs/izen/internal/core/authorization"
	"github.com/PizenLabs/izen/internal/core/budget"
	"github.com/PizenLabs/izen/internal/runtime/output"
)

type RunResult struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
	Command  string `json:"command"`
	Dir      string `json:"dir"`
	// Compressed holds the Phase 1 semantically compressed output (test
	// failures kept, passing blocks dropped, linter diagnostics flattened).
	// Empty when no output pipeline is wired.
	Compressed string `json:"compressed,omitempty"`
	// ToolType is the classification tag assigned by the output pipeline
	// (GO_TEST, GIT_STATUS, GENERIC, ...). Empty when no pipeline is wired.
	ToolType string `json:"tool_type,omitempty"`
	// LogPath is the persistent `.logs/` tee log written with the normalized
	// uncompressed output. Empty when tee logging is disabled.
	LogPath string `json:"log_path,omitempty"`
}

type processEntry struct {
	cmd       *exec.Cmd
	contextID string
}

var (
	procMu      sync.Mutex
	procEntries []processEntry
)

func registerProcess(cmd *exec.Cmd, ctxID string) {
	procMu.Lock()
	defer procMu.Unlock()
	procEntries = append(procEntries, processEntry{cmd: cmd, contextID: ctxID})
}

func unregisterProcess(cmd *exec.Cmd) {
	procMu.Lock()
	defer procMu.Unlock()
	for i, e := range procEntries {
		if e.cmd == cmd {
			procEntries = append(procEntries[:i], procEntries[i+1:]...)
			return
		}
	}
}

// TrackProcess registers an exec.Cmd in the global orphan-kill list so that
// KillAllOrphans can terminate it. Callers should defer UntrackProcess.
func TrackProcess(cmd *exec.Cmd) {
	registerProcess(cmd, "")
}

// UntrackProcess removes a previously registered exec.Cmd from the global list.
func UntrackProcess(cmd *exec.Cmd) {
	unregisterProcess(cmd)
}

func KillOrphanedByContext(ctxID string) {
	procMu.Lock()
	defer procMu.Unlock()
	var alive []processEntry
	for _, e := range procEntries {
		if e.contextID == ctxID {
			if e.cmd != nil && e.cmd.Process != nil {
				_ = e.cmd.Process.Signal(syscall.SIGKILL)
			}
			continue
		}
		alive = append(alive, e)
	}
	procEntries = alive
}

type SandboxMode int

const (
	SandboxDisabled SandboxMode = iota
	SandboxAll
	SandboxPolicy
	SandboxHighRisk
)

type Runner struct {
	sandbox        bool
	confirm        bool
	root           string
	activeCtxID    string
	sandboxMode    SandboxMode
	riskClassifier *RiskClassifier
	auth           *authorization.MutationAuthorization
	budget         *budget.MutationBudget
	// pipeline is the Phase 1 Tool Output Intelligence pipeline. Every
	// command output is normalized, classified, semantically compressed and
	// (when a workspace tee is attached) logged to `.logs/`. Nil keeps the
	// runner a pure transformation-free executor (headless/tests).
	pipeline *output.Pipeline
}

func NewRunner(root string, sandbox, confirm bool) *Runner {
	return &Runner{
		root:           root,
		sandbox:        sandbox,
		confirm:        confirm,
		sandboxMode:    SandboxPolicy,
		riskClassifier: NewRiskClassifier(),
	}
}

// WithPipeline attaches the Tool Output Intelligence pipeline. A workspace-
// rooted pipeline (output.New().WithWorkspace(root)) enables persistent
// `.logs/` recording of every executed command.
func (r *Runner) WithPipeline(p *output.Pipeline) *Runner {
	r.pipeline = p
	return r
}

func (r *Runner) SetAuthorization(auth *authorization.MutationAuthorization) {
	r.auth = auth
}

func (r *Runner) Authorization() *authorization.MutationAuthorization {
	return r.auth
}

func (r *Runner) SetBudget(b *budget.MutationBudget) {
	r.budget = b
}

func (r *Runner) Budget() *budget.MutationBudget {
	return r.budget
}

func (r *Runner) SetSandboxMode(mode SandboxMode) {
	r.sandboxMode = mode
}

func (r *Runner) SetRiskClassifier(rc *RiskClassifier) {
	r.riskClassifier = rc
}

func (r *Runner) SetContextID(id string) {
	r.activeCtxID = id
}

func (r *Runner) ActiveContextID() string {
	return r.activeCtxID
}

func (r *Runner) RequiresConfirm(command string) bool {
	if !r.confirm {
		return false
	}
	return isDangerous(command)
}

func (r *Runner) SandboxCheck(command string) error {
	if !r.sandbox {
		return nil
	}

	switch r.sandboxMode {
	case SandboxAll:
		return fmt.Errorf("sandbox: all command execution blocked by policy")
	case SandboxDisabled:
		return nil
	case SandboxHighRisk:
		if isDangerous(command) {
			return fmt.Errorf("sandbox: dangerous command blocked: %s", command)
		}
		return nil
	case SandboxPolicy:
		fallthrough
	default:
		if isDangerous(command) {
			return fmt.Errorf("sandbox: dangerous command blocked by policy: %s", command)
		}

		if r.riskClassifier != nil {
			risk := r.riskClassifier.ClassifyCommand(command)
			if risk.Level >= RiskHigh {
				return fmt.Errorf("sandbox: high-risk command blocked (%s): %s", risk.Label, command)
			}
		}

		return nil
	}
}

func (r *Runner) run(command, dir string) (*RunResult, error) {
	if err := checkAuthorization(r.auth); err != nil {
		return &RunResult{
			Command:  command,
			Dir:      dir,
			ExitCode: -1,
			Stderr:   err.Error(),
		}, err
	}

	if r.sandbox {
		if err := r.SandboxCheck(command); err != nil {
			return &RunResult{
				Command:  command,
				Dir:      dir,
				ExitCode: -1,
				Stderr:   err.Error(),
			}, err
		}
	}

	ctx := context.Background()
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	result := &RunResult{
		Command: command,
		Dir:     dir,
	}

	// ── LIVE ACTIVITY TREE: RUNNING STATE ────────────────────────────
	// Emit a "running" exec event (exitCode -1) before the process starts so
	// the UI tree shows the command with the animated snowflake spinner for
	// the whole duration, not just the final [done] line.
	if globalEventLog != nil {
		globalEventLog(CommandExecEvent{Command: command, ExitCode: -1})
	}
	startTime := time.Now()

	registerProcess(cmd, r.activeCtxID)

	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			result.ExitCode = exitErr.ExitCode()
		} else {
			unregisterProcess(cmd)
			return result, err
		}
	}

	unregisterProcess(cmd)
	result.Stdout = strings.TrimSpace(stdout.String())
	result.Stderr = strings.TrimSpace(stderr.String())

	// ── LIVE ACTIVITY TREE: COMPLETED STATE ──────────────────────────
	// The terminal exec event carries the real exit code, elapsed time, and
	// the combined output so the tree entry flips from the spinner to a
	// scannable "(exit N · Xs)" line and the Ctrl+O expansion can show the
	// full stdout/stderr.
	if globalEventLog != nil {
		combined := result.Stdout
		if result.Stderr != "" {
			if combined != "" {
				combined += "\n"
			}
			combined += result.Stderr
		}
		globalEventLog(CommandExecEvent{
			Command:  command,
			ExitCode: result.ExitCode,
			Elapsed:  time.Since(startTime),
			Output:   combined,
		})
	}

	// ── TOOL OUTPUT PIPELINE (PHASE 1) ──────────────────────────────────
	// Every executed command's combined output is normalized (ANSI-stripped),
	// classified by tool type, semantically compressed for LLM consumption,
	// and — when the pipeline carries a workspace tee — recorded uncompressed
	// under `.logs/`. The raw stdout/stderr stay intact for downstream UI
	// rendering; the compressed form is what reaches LLM-facing context.
	if r.pipeline != nil {
		combined := result.Stdout
		if result.Stderr != "" {
			if combined != "" {
				combined += "\n"
			}
			combined += result.Stderr
		}
		res := r.pipeline.Process(result.Command, []byte(combined))
		result.Compressed = res.Compressed
		result.ToolType = string(res.Tool)
		result.LogPath = res.LogPath
	}

	if r.budget != nil {
		_ = r.budget.Consume(budget.BudgetDelta{ShellCmds: 1})
	}

	markAuthConsumed(r.auth)
	return result, nil
}

func (r *Runner) Run(command string) (*RunResult, error) {
	if err := checkAuthorization(r.auth); err != nil {
		return &RunResult{
			Command:  command,
			ExitCode: -1,
			Stderr:   err.Error(),
		}, err
	}
	return r.run(command, r.root)
}

func (r *Runner) RunInDir(command, dir string) (*RunResult, error) {
	if err := checkAuthorization(r.auth); err != nil {
		return &RunResult{
			Command:  command,
			Dir:      dir,
			ExitCode: -1,
			Stderr:   err.Error(),
		}, err
	}
	fullDir := filepath.Join(r.root, dir)
	return r.run(command, fullDir)
}

func (r *Runner) KillOrphans() {
	if r.activeCtxID != "" {
		KillOrphanedByContext(r.activeCtxID)
	}
}

func KillAllOrphans() {
	procMu.Lock()
	defer procMu.Unlock()
	for _, e := range procEntries {
		if e.cmd != nil && e.cmd.Process != nil {
			_ = e.cmd.Process.Signal(syscall.SIGKILL)
		}
	}
	procEntries = nil
}

var dangerousPatterns = []string{
	"rm -rf /",
	"rm -rf ~",
	"rm -rf .",
	"rm -rf *",
	"rm -rf --no-preserve-root",
	"mkfs.",
	"dd if=",
	":(){ :|:& };:",
	"> /dev/",
	"chmod 0",
	"chown -R",
	"git push --force",
	// ── ABSOLUTE HUMAN-IN-THE-LOOP GATE: every sudo invocation requires
	// explicit user confirmation and is banned from silent background execution.
	"sudo",
	// ── OS-FENCE: commands that are hallmarks of a non-macOS environment
	// (apt-get, apt, dpkg, yum, dnf) are blocked when running on Darwin so the
	// engine cannot generate incorrect platform commands.
	"apt-get",
	"apt ",
	"dpkg",
	"yum ",
	"dnf ",
}

func isDangerous(command string) bool {
	lower := strings.ToLower(strings.TrimSpace(command))
	for _, pattern := range dangerousPatterns {
		if strings.Contains(lower, pattern) {
			return true
		}
	}
	return false
}
