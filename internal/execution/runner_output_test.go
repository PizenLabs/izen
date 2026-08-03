package execution

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/runtime/output"
)

// TestRunnerPipelineWritesLogs verifies the Phase 1 Tool Output Pipeline is
// wired into the core shell runner: every command's output is classified,
// normalized, compressed, and recorded as a persistent tee log under `.logs/`
// (activating the planner's TeeLogAdapter).
func TestRunnerPipelineWritesLogs(t *testing.T) {
	dir := t.TempDir()
	r := NewRunner(dir, false, false)
	r.SetAuthorization(testAuth())
	r.WithPipeline(output.New().WithWorkspace(dir))

	result, err := r.Run("echo 'hello pipeline world'")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Stdout != "hello pipeline world" {
		t.Errorf("Stdout = %q, want %q", result.Stdout, "hello pipeline world")
	}

	// The command was classified by the pipeline.
	if result.ToolType == "" {
		t.Error("RunResult.ToolType empty — pipeline classification missing")
	}
	// The persistent tee log was written under <root>/.logs/.
	if result.LogPath == "" {
		t.Error("RunResult.LogPath empty — tee log not recorded")
	}
	logsDir := filepath.Join(dir, output.LogDirName)
	entries, err := os.ReadDir(logsDir)
	if err != nil {
		t.Fatalf("read .logs/: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no tee logs written to .logs/")
	}

	// last.log symlink resolves to the newest log containing the output.
	last := filepath.Join(logsDir, output.LastLogLink)
	data, err := os.ReadFile(last)
	if err != nil {
		t.Fatalf("read last.log: %v", err)
	}
	if !strings.Contains(string(data), "hello pipeline world") {
		t.Errorf("last.log missing command output: %q", string(data))
	}
}

// TestRunnerPipelineCompressesGoTest verifies the semantic compressor runs over
// shell output: passing test blocks are dropped, failures are kept.
func TestRunnerPipelineCompressesGoTest(t *testing.T) {
	dir := t.TempDir()
	r := NewRunner(dir, false, false)
	r.SetAuthorization(testAuth())
	r.WithPipeline(output.New().WithWorkspace(dir))

	// A synthetic `go test -v` style run. The command string carries the
	// "go test" token so the classifier tags it GO_TEST and the semantic
	// compressor drops passing blocks while keeping failures.
	cmd := "go test 2>/dev/null; printf '=== RUN   TestOK\\n--- PASS: TestOK (0.00s)\\n=== RUN   TestBad\\n--- FAIL: TestBad (0.01s)\\n\\tbad.go:12: got 1, want 2\\nFAIL\\n'"
	result, err := r.Run(cmd)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.ToolType != string(output.ToolGoTest) {
		t.Errorf("ToolType = %q, want GO_TEST", result.ToolType)
	}
	if !strings.Contains(result.Compressed, "TestBad") {
		t.Errorf("compressed output lost failing test:\n%s", result.Compressed)
	}
	if strings.Contains(result.Compressed, "TestOK") {
		t.Errorf("compressed output kept passing test:\n%s", result.Compressed)
	}
}
