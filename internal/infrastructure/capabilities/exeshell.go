package capabilities

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"time"

	"github.com/PizenLabs/izen/internal/domain/ports"
)

// defaultShellTimeout bounds every command when the adapter was constructed
// without an explicit timeout.
const defaultShellTimeout = 60 * time.Second

// ExecShell implements ports.ShellPort by executing commands through the OS
// shell (/bin/sh -c). Every invocation runs under a context with an optional
// timeout so runaway processes are always bounded.
type ExecShell struct {
	timeout time.Duration
}

// NewExecShell returns a shell adapter. A non-positive timeout falls back to
// defaultShellTimeout.
func NewExecShell(timeout time.Duration) *ExecShell {
	if timeout <= 0 {
		timeout = defaultShellTimeout
	}
	return &ExecShell{timeout: timeout}
}

// Execute runs a command in the default working directory.
func (s *ExecShell) Execute(ctx context.Context, command string) (ports.ShellResult, error) {
	return s.run(ctx, "", command)
}

// ExecuteIn runs a command in the given working directory.
func (s *ExecShell) ExecuteIn(ctx context.Context, dir, command string) (ports.ShellResult, error) {
	return s.run(ctx, dir, command)
}

// run executes command through the shell, bounding it by the caller's context
// and the adapter timeout.
func (s *ExecShell) run(ctx context.Context, dir, command string) (ports.ShellResult, error) {
	runCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, "sh", "-c", command)
	cmd.Dir = dir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	result := ports.ShellResult{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: exitCodeOf(err),
	}
	if err != nil && result.ExitCode == 0 {
		// Non-exit errors (missing binary, fork failure, context deadline
		// before exec) have no meaningful exit status; report -1.
		result.ExitCode = -1
	}
	return result, err
}

// exitCodeOf extracts the process exit code from a command error, or 0 when
// the command succeeded.
func exitCodeOf(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}
