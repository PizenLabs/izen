package capabilities

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/domain/ports"
)

func TestExecShellExecute(t *testing.T) {
	sh := NewExecShell(5 * time.Second)
	var _ ports.ShellPort = sh

	res, err := sh.Execute(context.Background(), "echo hello")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", res.ExitCode)
	}
	if strings.TrimSpace(res.Stdout) != "hello" {
		t.Errorf("Stdout = %q, want %q", res.Stdout, "hello")
	}
}

func TestExecShellExecuteIn(t *testing.T) {
	dir := t.TempDir()
	sh := NewExecShell(5 * time.Second)

	res, err := sh.ExecuteIn(context.Background(), dir, "pwd")
	if err != nil {
		t.Fatalf("ExecuteIn: %v", err)
	}
	if res.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", res.ExitCode)
	}
	if strings.TrimSpace(res.Stdout) != dir {
		t.Errorf("Stdout = %q, want %q", res.Stdout, dir)
	}
}

func TestExecShellNonZeroExit(t *testing.T) {
	sh := NewExecShell(5 * time.Second)
	res, err := sh.Execute(context.Background(), "exit 7")
	if err == nil {
		t.Fatal("Execute with exit 7 returned nil error")
	}
	if res.ExitCode != 7 {
		t.Errorf("ExitCode = %d, want 7", res.ExitCode)
	}
}

func TestExecShellCapturesStderr(t *testing.T) {
	sh := NewExecShell(5 * time.Second)
	res, err := sh.Execute(context.Background(), "echo oops >&2")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.TrimSpace(res.Stderr) != "oops" {
		t.Errorf("Stderr = %q, want %q", res.Stderr, "oops")
	}
}

func TestExecShellTimeout(t *testing.T) {
	sh := NewExecShell(50 * time.Millisecond)
	start := time.Now()
	_, err := sh.Execute(context.Background(), "sleep 5")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("Execute of sleeping command with short timeout returned nil error")
	}
	if elapsed > 2*time.Second {
		t.Errorf("timeout did not bound execution: took %s", elapsed)
	}
}

func TestExecShellContextCancel(t *testing.T) {
	sh := NewExecShell(5 * time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := sh.Execute(ctx, "echo never")
	if err == nil {
		t.Fatal("Execute with cancelled context returned nil error")
	}
	if res.ExitCode == 0 {
		t.Errorf("ExitCode = 0 for cancelled execution")
	}
}
