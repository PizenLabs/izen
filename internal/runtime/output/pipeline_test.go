package output

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProcessFullPipeline(t *testing.T) {
	raw := "\x1b[31m=== RUN   TestPassing\r\n--- PASS: TestPassing (0.00s)\r\n" +
		"=== RUN   TestFailing\r\n    x_test.go:5: boom\r\n--- FAIL: TestFailing (0.00s)\r\n" +
		"FAIL\r\nFAIL\texample.com/demo\t0.05s\x1b[0m\r\n"

	p := New()
	res := p.Process("go test ./...", []byte(raw))

	if res.Tool != ToolGoTest {
		t.Errorf("tool = %s, want GO_TEST", res.Tool)
	}
	if strings.Contains(res.Normalized, "\r") || strings.Contains(res.Normalized, "\x1b") {
		t.Errorf("normalized output retains control bytes: %q", res.Normalized)
	}
	if strings.Contains(res.Compressed, "TestPassing") || strings.Contains(res.Compressed, "--- PASS") {
		t.Errorf("passing test leaked into compressed output:\n%s", res.Compressed)
	}
	if !strings.Contains(res.Compressed, "--- FAIL: TestFailing") {
		t.Errorf("failing test missing from compressed output:\n%s", res.Compressed)
	}
	if res.LogPath != "" {
		t.Errorf("no tee attached but LogPath = %q", res.LogPath)
	}
}

func TestProcessWithTeeWritesUncompressedLog(t *testing.T) {
	ws := t.TempDir()
	p := New().WithWorkspace(ws)

	raw := "=== RUN   TestPassing\n--- PASS: TestPassing (0.00s)\n" +
		"=== RUN   TestFailing\n    x_test.go:5: boom\n--- FAIL: TestFailing (0.00s)\n" +
		"FAIL\nFAIL\texample.com/demo\t0.05s\n"

	res := p.Process("go test ./...", []byte(raw))

	if res.LogPath == "" {
		t.Fatal("expected tee log path")
	}
	// The tee log is the uncompressed normalized output.
	b, err := os.ReadFile(res.LogPath)
	if err != nil {
		t.Fatalf("ReadFile(log): %v", err)
	}
	logContent := string(b)
	if !strings.Contains(logContent, "TestPassing") {
		t.Errorf("tee log lost uncompressed passing lines:\n%s", logContent)
	}
	if !strings.Contains(logContent, "TestFailing") {
		t.Errorf("tee log lost failing lines:\n%s", logContent)
	}
	// Symlink lands on the written log.
	last, err := NewTee(ws).LastLog()
	if err != nil {
		t.Fatalf("LastLog: %v", err)
	}
	if last != res.LogPath {
		t.Errorf("last.log = %q, want %q", last, res.LogPath)
	}
	if _, err := os.Stat(filepath.Join(ws, LogDirName)); err != nil {
		t.Errorf(".logs dir missing: %v", err)
	}
}

func TestPipelineNilReceiverIsSafe(t *testing.T) {
	var p *Pipeline
	res := p.Process("go test ./...", []byte("=== RUN   A\n--- PASS: A (0.00s)\nFAIL\n"))
	if res.Tool != ToolGoTest {
		t.Errorf("nil pipeline tool = %s", res.Tool)
	}
	// Passing block dropped; the FAIL summary survives as the execution signal.
	if res.Compressed != "FAIL" {
		t.Errorf("nil pipeline compressed = %q, want %q", res.Compressed, "FAIL")
	}
}

func TestPipelineExecuteRunsCommand(t *testing.T) {
	ws := t.TempDir()
	p := New().WithWorkspace(ws)

	res := p.Execute(t.Context(), "printf 'hello\\nworld\\n'", ExecuteOptions{Dir: ws})
	if res.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0", res.ExitCode)
	}
	if !strings.Contains(res.Normalized, "hello") || !strings.Contains(res.Normalized, "world") {
		t.Errorf("normalized = %q", res.Normalized)
	}
	if res.Err != nil {
		t.Errorf("err = %v, want nil", res.Err)
	}
}

func TestPipelineExecuteNonZeroExitIsSignalNotError(t *testing.T) {
	p := New()
	res := p.Execute(t.Context(), "exit 7", ExecuteOptions{})
	if res.ExitCode != 7 {
		t.Errorf("exit code = %d, want 7", res.ExitCode)
	}
	if res.Err != nil {
		t.Errorf("non-exit error surfaced: %v", res.Err)
	}
}
