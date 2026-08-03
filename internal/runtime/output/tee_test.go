package output

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"
)

func TestTeeWriteCreatesTimestampedLog(t *testing.T) {
	dir := t.TempDir()
	tee := NewTeeDir(dir)

	path, err := tee.Write(ToolGoTest, []byte("hello test output"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	name := filepath.Base(path)
	if !logFileRE.MatchString(name) {
		t.Fatalf("log name %q does not match YYYYMMDD-HHMMSS-GO_TEST.log", name)
	}
	if !regexp.MustCompile(`^\d{8}-\d{6}-GO_TEST\.log$`).MatchString(name) {
		t.Errorf("log name %q missing GO_TEST type", name)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(b) != "hello test output" {
		t.Errorf("log content = %q", string(b))
	}
}

func TestTeeLastLogSymlinkPointsAtNewest(t *testing.T) {
	dir := t.TempDir()
	tee := NewTeeDir(dir)

	first, err := tee.Write(ToolGoTest, []byte("first"))
	if err != nil {
		t.Fatalf("first Write: %v", err)
	}
	second, err := tee.Write(ToolGoTest, []byte("second"))
	if err != nil {
		t.Fatalf("second Write: %v", err)
	}

	last, err := tee.LastLog()
	if err != nil {
		t.Fatalf("LastLog: %v", err)
	}
	if last != second {
		t.Errorf("last.log = %q, want newest %q", last, second)
	}
	b, err := os.ReadFile(last)
	if err != nil {
		t.Fatalf("ReadFile(last): %v", err)
	}
	if string(b) != "second" {
		t.Errorf("last.log content = %q, want %q", string(b), "second")
	}

	// The symlink must actually be a symlink (relative target).
	fi, err := os.Lstat(filepath.Join(dir, LastLogLink))
	if err != nil {
		t.Fatalf("Lstat last.log: %v", err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Error("last.log is not a symlink")
	}
	target, _ := os.Readlink(filepath.Join(dir, LastLogLink))
	if filepath.IsAbs(target) {
		t.Errorf("last.log target %q is absolute, want relative", target)
	}
	if filepath.Base(first) == target {
		t.Error("last.log still points at the first (older) log")
	}
}

func TestTeePrunesExpiredLogs(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	tee := NewTeeDir(dir).WithMaxAge(DefaultLogRetention)
	// Pin the clock so freshness is deterministic.
	tee.now = func() time.Time { return now }

	if _, err := tee.Write(ToolGeneric, []byte("fresh")); err != nil {
		t.Fatalf("fresh Write: %v", err)
	}
	// Simulate an expired log file (10 days old).
	stale := filepath.Join(dir, "20200101-000000-GENERIC.log")
	if err := os.WriteFile(stale, []byte("stale"), 0o644); err != nil {
		t.Fatalf("write stale: %v", err)
	}
	old := now.Add(-10 * 24 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	removed := tee.Prune()
	if removed != 1 {
		t.Errorf("Prune removed %d logs, want 1", removed)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("expired log still exists: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, LastLogLink)); err != nil {
		t.Errorf("last.log lost after pruning fresh log retained: %v", err)
	}
}

func TestTeeWritePrunesAutomatically(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	tee := NewTeeDir(dir).WithMaxAge(DefaultLogRetention)
	tee.now = func() time.Time { return now }

	stale := filepath.Join(dir, "20200101-000000-GENERIC.log")
	if err := os.WriteFile(stale, []byte("stale"), 0o644); err != nil {
		t.Fatalf("write stale: %v", err)
	}
	old := now.Add(-10 * 24 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	if _, err := tee.Write(ToolGoTest, []byte("fresh")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("expired log not pruned by Write")
	}
}

func TestTeeKeepsRecentLogs(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	tee := NewTeeDir(dir).WithMaxAge(DefaultLogRetention)
	tee.now = func() time.Time { return now }

	if _, err := tee.Write(ToolGoTest, []byte("one")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	removed := tee.Prune()
	if removed != 0 {
		t.Errorf("Prune removed %d fresh logs, want 0", removed)
	}
	if logs := tee.Logs(); len(logs) != 1 {
		t.Errorf("Logs = %v, want 1 retained log", logs)
	}
}

func TestTeeLogsNewestFirst(t *testing.T) {
	dir := t.TempDir()
	tee := NewTeeDir(dir)

	if _, err := tee.Write(ToolGoTest, []byte("older")); err != nil {
		t.Fatalf("first Write: %v", err)
	}
	time.Sleep(2 * time.Millisecond)
	if _, err := tee.Write(ToolLinterGo, []byte("newer")); err != nil {
		t.Fatalf("second Write: %v", err)
	}

	logs := tee.Logs()
	if len(logs) != 2 {
		t.Fatalf("Logs = %d entries, want 2", len(logs))
	}
	if !regexp.MustCompile(`LINTER_GO`).MatchString(filepath.Base(logs[0])) {
		t.Errorf("Logs[0] = %q, want LINTER_GO newest first", logs[0])
	}
}

func TestNewTeeUsesDotLogsDir(t *testing.T) {
	ws := t.TempDir()
	tee := NewTee(ws)
	if filepath.Base(tee.Dir()) != LogDirName {
		t.Errorf("tee dir = %q, want <workspace>/%s", tee.Dir(), LogDirName)
	}
	if _, err := tee.Write(ToolGoTest, []byte("x")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if fi, err := os.Stat(filepath.Join(ws, LogDirName)); err != nil || !fi.IsDir() {
		t.Errorf(".logs dir missing: %v", err)
	}
}
