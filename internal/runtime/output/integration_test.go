package output

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestIntegrationFailedGoTestRun verifies the end-to-end Phase 1 contract on a
// real failed `go test` run: symlink creation in .logs/ plus clean, compressed
// formatting that drops passing tests and keeps the failing assertions and
// final summary. It is skipped when no Go toolchain is available.
func TestIntegrationFailedGoTestRun(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}

	ws := t.TempDir()
	writeFailingModule(t, ws)

	p := New().WithWorkspace(ws)
	// timeout bounds the run so a hung test binary cannot stall the suite.
	res := p.Execute(context.Background(), "go test -v ./...", ExecuteOptions{
		Dir:     ws,
		Timeout: 2 * time.Minute,
	})

	if res.Tool != ToolGoTest {
		t.Errorf("classified tool = %s, want GO_TEST", res.Tool)
	}
	if res.ExitCode == 0 {
		t.Fatal("expected a non-zero exit from the failing test run")
	}

	// 1. Persistent tee log created under .logs/ with a timestamped name.
	if res.LogPath == "" {
		t.Fatal("expected a tee log path for the failed go test run")
	}
	if !strings.Contains(filepath.Base(res.LogPath), "GO_TEST") {
		t.Errorf("log name %q missing GO_TEST type", filepath.Base(res.LogPath))
	}
	if _, err := os.Stat(res.LogPath); err != nil {
		t.Errorf("tee log missing on disk: %v", err)
	}

	// 2. last.log symlink resolves to the newest (this) log.
	tee := NewTee(ws)
	last, err := tee.LastLog()
	if err != nil {
		t.Fatalf("last.log symlink missing: %v", err)
	}
	if last != res.LogPath {
		t.Errorf("last.log = %q, want newest log %q", last, res.LogPath)
	}

	// 3. The tee log is the uncompressed output — passing test intact.
	logBytes, err := os.ReadFile(res.LogPath)
	if err != nil {
		t.Fatalf("read tee log: %v", err)
	}
	if !strings.Contains(string(logBytes), "TestPassing") {
		t.Error("tee log lost the uncompressed passing test")
	}

	// 4. Clean compressed formatting: passing tests dropped, failing test and
	// the final execution summary kept.
	if strings.Contains(res.Compressed, "TestPassing") {
		t.Errorf("compressed output leaked passing test:\n%s", res.Compressed)
	}
	if strings.Contains(res.Compressed, "--- PASS") {
		t.Errorf("compressed output leaked --- PASS marker:\n%s", res.Compressed)
	}
	for _, want := range []string{
		"--- FAIL: TestFailing",
		"expected 42, got 1",
		"FAIL",
	} {
		if !strings.Contains(res.Compressed, want) {
			t.Errorf("compressed output lost %q in:\n%s", want, res.Compressed)
		}
	}
}

// writeFailingModule creates a minimal Go module with one passing and one
// failing test so `go test -v` emits deterministic PASS/FAIL output.
func writeFailingModule(t *testing.T, ws string) {
	t.Helper()
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(ws, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	write("go.mod", "module example.com/demo\n\ngo 1.26\n")
	write("demo_test.go", `package demo

import "testing"

func TestPassing(t *testing.T) {
	if 1+1 != 2 {
		t.Fatal("math is broken")
	}
}

func TestFailing(t *testing.T) {
	t.Errorf("expected 42, got %d", 1)
}
`)
}
