package substrate

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
)

// ExecResult captures stdout, stderr and exit code of a command.
type ExecResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
	Err      error
}

// ExecCommand runs args[0] with args[1:] in dir with env augmentation.
// It is the sole exec site; semantic layers delegate via this helper.
func ExecCommand(ctx context.Context, dir string, env []string, args []string) ExecResult {
	if len(args) == 0 {
		return ExecResult{ExitCode: -1, Err: exec.ErrNotFound}
	}
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	if dir != "" {
		cmd.Dir = dir
	}
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	code := 0
	if err != nil {
		if ctx.Err() != nil {
			return ExecResult{Stdout: outBuf.String(), Stderr: errBuf.String(), ExitCode: -1, Err: ctx.Err()}
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else {
			return ExecResult{Stdout: outBuf.String(), Stderr: errBuf.String(), ExitCode: -1, Err: err}
		}
	}
	return ExecResult{Stdout: outBuf.String(), Stderr: errBuf.String(), ExitCode: code, Err: nil}
}
