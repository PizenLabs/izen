package execution

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/context"
)

func TestPatchManagerLedgerBridge(t *testing.T) {
	dir, _ := os.MkdirTemp("", "izen-patch-ledger-*")
	defer func() { _ = os.RemoveAll(dir) }()

	pm := NewPatchManager(dir)
	ledger := context.NewTaskLedger()
	pm.SetLedger(ledger)
	pm.SetContextID("#ctx-go-1-r1")

	var summaryLog string
	SetActivityLogger(func(format string, args ...interface{}) {
		summaryLog += fmt.Sprintf(format, args...)
	})
	defer SetActivityLogger(nil)

	testFile := filepath.Join("subdir", "test.txt")
	fullPath := filepath.Join(dir, testFile)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(fullPath, []byte("original"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := pm.Apply(&Patch{
		ID:       "p1",
		File:     testFile,
		Modified: "modified content",
		TaskID:   3,
	}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if !ledger.IsCompleted(3) {
		t.Fatal("expected task 3 to be Completed in the shared ledger")
	}
	if ledger.IsCompleted(99) {
		t.Fatal("unrelated task must remain pending")
	}
	if !strings.Contains(summaryLog, "BUILD MUTATION SUMMARY") {
		t.Fatalf("expected execution summary in activity log, got: %q", summaryLog)
	}
	if !strings.Contains(summaryLog, "**Files Mutated:** `subdir/test.txt` (strategy: ATOMIC_REPLACE)") {
		t.Fatalf("expected mutated file in summary, got: %q", summaryLog)
	}
}

func TestPatchManagerLedgerNoOpWithoutTaskID(t *testing.T) {
	dir, _ := os.MkdirTemp("", "izen-patch-ledger-noop-*")
	defer func() { _ = os.RemoveAll(dir) }()

	pm := NewPatchManager(dir)
	ledger := context.NewTaskLedger()
	pm.SetLedger(ledger)

	testFile := "f.txt"
	fullPath := filepath.Join(dir, testFile)
	if err := os.WriteFile(fullPath, []byte("original"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := pm.Apply(&Patch{
		ID:       "p2",
		File:     testFile,
		Modified: "<<<<<<< SEARCH\noriginal\n=======\nreplacement\n>>>>>>>",
	}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if ledger.IsCompleted(0) {
		t.Fatal("TaskID 0 must not mark the ledger")
	}
}

func TestRunnerBasic(t *testing.T) {
	r := NewRunner(".", false, false)
	result, err := r.Run("echo hello")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Stdout != "hello" {
		t.Fatalf("expected 'hello', got %q", result.Stdout)
	}
	if result.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", result.ExitCode)
	}
}

func TestRunnerExitCode(t *testing.T) {
	r := NewRunner(".", false, false)
	result, err := r.Run("exit 42")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.ExitCode != 42 {
		t.Fatalf("expected exit code 42, got %d", result.ExitCode)
	}
}

func TestRunnerSandboxBlocksDangerous(t *testing.T) {
	r := NewRunner(".", true, false)
	_, err := r.Run("rm -rf /")
	if err == nil {
		t.Fatal("expected sandbox to block dangerous command")
	}
}

func TestRunnerSandboxAllowsSafe(t *testing.T) {
	r := NewRunner(".", true, false)
	result, err := r.Run("echo safe")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Stdout != "safe" {
		t.Fatalf("expected 'safe', got %q", result.Stdout)
	}
}

func TestIsDangerous(t *testing.T) {
	cases := []struct {
		cmd       string
		dangerous bool
	}{
		{"echo hello", false},
		{"go test ./...", false},
		{"rm -rf /", true},
		{"rm -rf --no-preserve-root /var", true},
		{"dd if=/dev/zero of=/dev/sda", true},
		{"git push --force origin main", true},
		{"ls -la", false},
		{"git status", false},
	}
	for _, c := range cases {
		got := isDangerous(c.cmd)
		if got != c.dangerous {
			t.Errorf("isDangerous(%q) = %v, want %v", c.cmd, got, c.dangerous)
		}
	}
}

func TestRunnerStderr(t *testing.T) {
	r := NewRunner(".", false, false)
	result, err := r.Run("echo error >&2")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Stderr != "error" {
		t.Fatalf("expected stderr 'error', got %q", result.Stderr)
	}
}

func TestRunInDir(t *testing.T) {
	dir, _ := os.MkdirTemp("", "izen-exec-test-*")
	defer func() { _ = os.RemoveAll(dir) }()

	if err := os.WriteFile(filepath.Join(dir, "marker.txt"), []byte("here"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	r := NewRunner(dir, false, false)
	result, err := r.RunInDir("cat marker.txt", ".")
	if err != nil {
		t.Fatalf("RunInDir: %v", err)
	}
	if result.Stdout != "here" {
		t.Fatalf("expected 'here', got %q", result.Stdout)
	}
}

func TestRequiresConfirm(t *testing.T) {
	r := NewRunner(".", false, true)

	if r.RequiresConfirm("echo hi") {
		t.Fatal("echo should not require confirm")
	}
	if !r.RequiresConfirm("rm -rf /") {
		t.Fatal("rm -rf / should require confirm")
	}
}

func TestParseTestOutput(t *testing.T) {
	output := `--- PASS: TestFoo (0.00s)
--- FAIL: TestBar (0.01s)
    bar_test.go:10: assertion failed
--- SKIP: TestBaz (0.00s)
ok  	github.com/PizenLabs/izen/internal/foo	0.123s`

	result := parseTestOutput(output)
	if !result.Passed {
		t.Fatal("expected passed")
	}
	if result.PassedN != 1 {
		t.Fatalf("expected 1 passed, got %d", result.PassedN)
	}
	if result.FailedN != 1 {
		t.Fatalf("expected 1 failed, got %d", result.FailedN)
	}
	if result.Skipped != 1 {
		t.Fatalf("expected 1 skipped, got %d", result.Skipped)
	}
	if len(result.Failed) != 1 {
		t.Fatalf("expected 1 failed test detail, got %d", len(result.Failed))
	}
	if result.Failed[0].Name != "TestBar" {
		t.Fatalf("expected failed test 'TestBar', got %q", result.Failed[0].Name)
	}
}

func TestParseTestOutputFail(t *testing.T) {
	output := `--- FAIL: TestBad (0.01s)
    bad_test.go:5: boom
FAIL	github.com/PizenLabs/izen/internal/bad	0.456s`

	result := parseTestOutput(output)
	if result.Passed {
		t.Fatal("expected failed")
	}
	if result.FailedN != 1 {
		t.Fatalf("expected 1 failed, got %d", result.FailedN)
	}
}

func TestParseTestOutputWithCoverage(t *testing.T) {
	output := `ok  	github.com/PizenLabs/izen/internal/foo	0.123s	coverage: 72.3%`

	result := parseTestOutput(output)
	if result.Cover != "72.3%" {
		t.Fatalf("expected '72.3%%', got %q", result.Cover)
	}
}

func TestPatchManager(t *testing.T) {
	dir, _ := os.MkdirTemp("", "izen-patch-test-*")
	defer func() { _ = os.RemoveAll(tmpDir(t, dir)) }()

	pm := NewPatchManager(dir)

	testFile := filepath.Join("subdir", "test.txt")
	fullPath := filepath.Join(dir, testFile)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(fullPath, []byte("original content"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	patch, err := pm.Capture(testFile)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if patch.Original != "original content" {
		t.Fatalf("expected 'original content', got %q", patch.Original)
	}
	if !patch.Applied {
		t.Fatal("expected patch to be applied")
	}

	patch.Modified = "modified content"
	if err := pm.Apply(patch); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	data, _ := os.ReadFile(fullPath)
	if string(data) != "modified content" {
		t.Fatalf("expected 'modified content', got %q", string(data))
	}

	if err := pm.Rollback(patch.ID); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	data, _ = os.ReadFile(fullPath)
	if string(data) != "original content" {
		t.Fatalf("expected 'original content' after rollback, got %q", string(data))
	}

	patches, err := pm.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(patches) != 1 {
		t.Fatalf("expected 1 patch, got %d", len(patches))
	}
}

func tmpDir(t *testing.T, dir string) string {
	t.Helper()
	return dir
}

func init() {
	// Override tmpDir
}

func TestPatchListEmpty(t *testing.T) {
	dir, _ := os.MkdirTemp("", "izen-patch-empty-*")
	defer func() { _ = os.RemoveAll(dir) }()

	pm := NewPatchManager(dir)
	patches, err := pm.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(patches) != 0 {
		t.Fatalf("expected 0 patches, got %d", len(patches))
	}
}

func TestSanitizeDiffContentNewFile(t *testing.T) {
	input := `@@ -0,0 +6 @@
+MIT License
+
+Copyright (c) 2024 Pizen Labs
+
+Permission is hereby granted`

	expected := "MIT License\n\nCopyright (c) 2024 Pizen Labs\n\nPermission is hereby granted"
	got := SanitizeDiffContent(input)
	if got != expected {
		t.Fatalf("SanitizeDiffContent(new file):\n got: %q\nwant: %q", got, expected)
	}
}

func TestSanitizeDiffContentModification(t *testing.T) {
	input := `--- a/foo.go
+++ b/foo.go
@@ -1,5 +1,6 @@
 package foo
 
 func Hello() string {
-	return "goodbye"
+	return "hello"
 }
`

	expected := "package foo\n\nfunc Hello() string {\n\treturn \"hello\"\n}"
	got := SanitizeDiffContent(input)
	if got != expected {
		t.Fatalf("SanitizeDiffContent(modification):\n got: %q\nwant: %q", got, expected)
	}
}

func TestSanitizeDiffContentWithFence(t *testing.T) {
	input := "```diff\n--- a/LICENSE\n+++ b/LICENSE\n@@ -0,0 +3 @@\n+MIT License\n+Copyright (c) 2024\n```"

	expected := "MIT License\nCopyright (c) 2024"
	got := SanitizeDiffContent(input)
	if got != expected {
		t.Fatalf("SanitizeDiffContent(with fence):\n got: %q\nwant: %q", got, expected)
	}
}

func TestSanitizeDiffContentCleanCode(t *testing.T) {
	input := "package main\n\nfunc main() {}\n"
	got := SanitizeDiffContent(input)
	if got != input {
		t.Fatalf("SanitizeDiffContent(clean): expected passthrough, got %q", got)
	}
}

func TestSanitizeDiffContentBlankLines(t *testing.T) {
	input := `@@ -0,0 +3 @@
+line1
+
+line3`

	expected := "line1\n\nline3"
	got := SanitizeDiffContent(input)
	if got != expected {
		t.Fatalf("SanitizeDiffContent(blank lines):\n got: %q\nwant: %q", got, expected)
	}
}

func TestSanitizeDiffContentContextLines(t *testing.T) {
	input := `--- a/main.go
+++ b/main.go
@@ -1,3 +1,4 @@
 package main
 
+// new comment
 func main() {}`

	expected := "package main\n\n// new comment\nfunc main() {}"
	got := SanitizeDiffContent(input)
	if got != expected {
		t.Fatalf("SanitizeDiffContent(context lines):\n got: %q\nwant: %q", got, expected)
	}
}

func TestSanitizeDiffContentEmptyInput(t *testing.T) {
	got := SanitizeDiffContent("")
	if got != "" {
		t.Fatalf("SanitizeDiffContent(empty): expected empty, got %q", got)
	}
}

// TestSanitizeLLMResponseWrappedFence is the regression guard for the hotfix
// markdown-fence bug: when the local model returns the entire file wrapped in a
// single code block (e.g. "```mit ... ```"), the literal triple backticks must
// NOT be written into the file.
func TestSanitizeLLMResponseWrappedFence(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "mit license fence",
			input: "```mit\nMIT License\n\nCopyright (c) 2024\n```",
			want:  "MIT License\n\nCopyright (c) 2024",
		},
		{
			name:  "go fence",
			input: "```go\npackage main\n\nfunc main() {}\n```",
			want:  "package main\n\nfunc main() {}",
		},
		{
			name:  "bare fence",
			input: "```\nhello world\n```",
			want:  "hello world",
		},
		{
			name:  "no fence passthrough",
			input: "MIT License\n\nCopyright (c) 2024",
			want:  "MIT License\n\nCopyright (c) 2024",
		},
		{
			name:  "clean code preserves trailing newline",
			input: "package main\n\nfunc main() {}\n",
			want:  "package main\n\nfunc main() {}\n",
		},
	}
	for _, c := range cases {
		got := SanitizeLLMResponse(c.input)
		if got != c.want {
			t.Errorf("%s: SanitizeLLMResponse =\n %q\nwant\n %q", c.name, got, c.want)
		}
	}
}

func TestPatchLoadNotFound(t *testing.T) {
	dir, _ := os.MkdirTemp("", "izen-patch-load-*")
	defer func() { _ = os.RemoveAll(dir) }()

	pm := NewPatchManager(dir)
	_, err := pm.Load("nonexistent")
	if err == nil {
		t.Fatal("expected error loading nonexistent patch")
	}
}

func TestRunnerCommand(t *testing.T) {
	r := NewRunner(".", false, false)
	result, err := r.Run("printf 'line1\nline2'")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(result.Stdout, "line1") {
		t.Fatalf("expected stdout to contain 'line1', got %q", result.Stdout)
	}
}

func TestRunnerDir(t *testing.T) {
	result, err := (&Runner{}).run("pwd", "/tmp")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Stdout != "/tmp" {
		t.Fatalf("expected '/tmp', got %q", result.Stdout)
	}
}
