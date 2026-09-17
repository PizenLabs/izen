package ui

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDebugEnabled(t *testing.T) {
	cases := map[string]bool{
		"1":     true,
		"true":  true,
		"TRUE":  false,
		"0":     false,
		"false": false,
		"":      false,
		"yes":   false,
	}
	for val, want := range cases {
		t.Setenv("IZEN_DEBUG", val)
		if got := debugEnabled(); got != want {
			t.Errorf("debugEnabled() with IZEN_DEBUG=%q = %v, want %v", val, got, want)
		}
	}
}

func TestDebugLogCompletionDisabled(t *testing.T) {
	dir := t.TempDir()
	orig := getDebugLogDir()
	SetDebugLogDir(dir)
	defer SetDebugLogDir(orig)

	t.Setenv("IZEN_DEBUG", "")
	drainTelemetryBacklog()
	debugLogCompletion("hello", 1, 2, "stop", "test")
	telemetryFlush()

	if _, err := os.Stat(filepath.Join(dir, "completions.log")); !os.IsNotExist(err) {
		t.Errorf("expected completions.log to be absent when IZEN_DEBUG is unset, stat err = %v", err)
	}
}

func TestDebugLogCompletionEnabled(t *testing.T) {
	dir := t.TempDir()
	orig := getDebugLogDir()
	SetDebugLogDir(dir)
	defer SetDebugLogDir(orig)

	t.Setenv("IZEN_DEBUG", "1")
	drainTelemetryBacklog()
	debugLogCompletion("hello", 1, 2, "stop", "test")
	debugLogCompletion("world", 3, 4, "length", "test")

	telemetryFlush()

	data, err := os.ReadFile(filepath.Join(dir, "completions.log"))
	if err != nil {
		t.Fatalf("expected completions.log to exist when IZEN_DEBUG=1: %v", err)
	}
	lines := 0
	for _, b := range data {
		if b == '\n' {
			lines++
		}
	}
	if lines != 2 {
		t.Errorf("expected 2 log lines, got %d", lines)
	}
}

func TestUI_IsolatedDebugLogDirectory(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	orig := getDebugLogDir()
	SetDebugLogDir(dir)
	defer SetDebugLogDir(orig)

	// Use manual os.Setenv so this test can run in parallel.
	_ = os.Setenv("IZEN_DEBUG", "1")
	defer func() { _ = os.Unsetenv("IZEN_DEBUG") }()

	// Drain any backlog from earlier tests' async telemetry before enqueuing
	// our own records, so drop-on-saturation never masks our assertions.
	drainTelemetryBacklog()
	debugLogCompletion("alpha", 10, 20, "stop", "test")
	debugLogCompletion("beta", 30, 40, "length", "test")
	debugLogCompletion("gamma", 50, 60, "stop", "test")

	telemetryFlush()

	// Verify the completions file exists in the isolated directory.
	completionsPath := filepath.Join(dir, "completions.log")
	data, err := os.ReadFile(completionsPath)
	if err != nil {
		t.Fatalf("expected completions.log in isolated dir: %v", err)
	}
	lines := 0
	for _, b := range data {
		if b == '\n' {
			lines++
		}
	}
	if lines != 3 {
		t.Errorf("expected 3 log lines in isolated dir, got %d", lines)
	}

	// Verify the original workspace .izen/debug path was never touched.
	if dir == filepath.Join(".izen", "debug") {
		t.Fatal("debug log dir must not point at real workspace path")
	}
}

func TestDebugLogDirReset(t *testing.T) {
	// NOTE: deliberately non-parallel. It mutates the shared debugLogDir
	// global; running it in the parallel phase would race SetDebugLogDir with
	// TestUI_IsolatedDebugLogDirectory and route its records to the wrong dir.
	original := getDebugLogDir()
	defer SetDebugLogDir(original)

	custom := t.TempDir()
	SetDebugLogDir(custom)
	if got := getDebugLogDir(); got != custom {
		t.Fatalf("SetDebugLogDir(%q) did not take effect, got %q", custom, got)
	}

	SetDebugLogDir("")
	if got := getDebugLogDir(); got != defaultDebugLogDir() {
		t.Fatalf("SetDebugLogDir(\"\") did not reset, got %q want %q", got, defaultDebugLogDir())
	}
}
