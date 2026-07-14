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
)

type RunResult struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
	Command  string `json:"command"`
	Dir      string `json:"dir"`
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

type Runner struct {
	sandbox     bool
	confirm     bool
	root        string
	activeCtxID string
}

func NewRunner(root string, sandbox, confirm bool) *Runner {
	return &Runner{
		root:    root,
		sandbox: sandbox,
		confirm: confirm,
	}
}

func (r *Runner) SetContextID(id string) {
	r.activeCtxID = id
}

func (r *Runner) ActiveContextID() string {
	return r.activeCtxID
}

func (r *Runner) Run(command string) (*RunResult, error) {
	return r.run(command, r.root)
}

func (r *Runner) RunInDir(command, dir string) (*RunResult, error) {
	fullDir := filepath.Join(r.root, dir)
	return r.run(command, fullDir)
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
	if isDangerous(command) {
		return fmt.Errorf("dangerous command blocked by sandbox: %s", command)
	}
	return nil
}

func (r *Runner) run(command, dir string) (*RunResult, error) {
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
	return result, nil
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
