package execution

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

type RunResult struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
	Command  string `json:"command"`
	Dir      string `json:"dir"`
}

type Runner struct {
	sandbox bool
	confirm bool
	root    string
}

func NewRunner(root string, sandbox, confirm bool) *Runner {
	return &Runner{
		root:    root,
		sandbox: sandbox,
		confirm: confirm,
	}
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

	cmd := exec.CommandContext(context.Background(), "sh", "-c", command)
	cmd.Dir = dir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	result := &RunResult{
		Command: command,
		Dir:     dir,
	}

	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			result.ExitCode = exitErr.ExitCode()
		} else {
			return result, err
		}
	}

	result.Stdout = strings.TrimSpace(stdout.String())
	result.Stderr = strings.TrimSpace(stderr.String())
	return result, nil
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
