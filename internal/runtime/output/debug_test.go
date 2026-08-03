package output

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestMetricsCompressionRatio(t *testing.T) {
	m := Metrics{OriginalChars: 1000, CompressedChars: 250}
	if got := m.CompressionRatio(); got != 75 {
		t.Errorf("CompressionRatio() = %v, want 75", got)
	}
	if got := m.TokenBytesSaved(); got != 187 {
		t.Errorf("TokenBytesSaved() = %d, want 187", got)
	}

	empty := Metrics{}
	if got := empty.CompressionRatio(); got != 0 {
		t.Errorf("empty CompressionRatio() = %v, want 0", got)
	}
	if got := empty.TokenBytesSaved(); got != 0 {
		t.Errorf("empty TokenBytesSaved() = %d, want 0", got)
	}

	// Expanded output (should never go negative).
	grown := Metrics{OriginalChars: 100, CompressedChars: 200}
	if got := grown.TokenBytesSaved(); got != 0 {
		t.Errorf("grown TokenBytesSaved() = %d, want 0", got)
	}
}

func TestResultDebugWithTee(t *testing.T) {
	dir := t.TempDir()
	tee := NewTeeDir(dir)
	p := New().WithTee(tee)

	raw := "=== RUN   TestPass\n--- PASS: TestPass (0.00s)\n" +
		"=== RUN   TestFail\n    a_test.go:12: got 1, want 2\n--- FAIL: TestFail (0.00s)\n"
	res := p.Process("go test ./...", []byte(raw))

	di := res.Debug(tee)
	if di.LogPath == "" {
		t.Fatal("Debug log path empty; tee write expected")
	}
	if di.LogDir != dir {
		t.Errorf("Debug log dir = %q, want %q", di.LogDir, dir)
	}
	if di.OriginalChars == 0 || di.CompressedChars == 0 {
		t.Errorf("Debug chars = %d/%d, want > 0", di.OriginalChars, di.CompressedChars)
	}
	if di.CompressionRatioPct <= 0 {
		t.Errorf("Debug compression ratio = %v, want > 0 (passing tests dropped)", di.CompressionRatioPct)
	}
	if di.CharsSaved < 0 {
		t.Errorf("Debug chars saved = %d, want >= 0", di.CharsSaved)
	}
	if di.TokenBytesSaved < 0 {
		t.Errorf("Debug token bytes saved = %d, want >= 0", di.TokenBytesSaved)
	}
	if di.Tool != ToolGoTest {
		t.Errorf("Debug tool = %q, want %q", di.Tool, ToolGoTest)
	}
	if di.Err != nil {
		t.Errorf("Debug err = %v, want nil", di.Err)
	}
}

func TestResultDebugNoTee(t *testing.T) {
	res := New().Process("echo hi", []byte("hello world\n"))
	di := res.Debug(nil)
	if di.LogPath != "" {
		t.Errorf("Debug log path = %q, want empty (no tee)", di.LogPath)
	}
	if di.LogDir != "" {
		t.Errorf("Debug log dir = %q, want empty (no tee)", di.LogDir)
	}
	if di.OriginalChars == 0 {
		t.Error("Debug original chars = 0")
	}
}

func TestInspectWorkspace(t *testing.T) {
	root := t.TempDir()

	ws := InspectWorkspace(root)
	if ws.LogDir != filepath.Join(root, LogDirName) {
		t.Errorf("InspectWorkspace log dir = %q, want %q", ws.LogDir, filepath.Join(root, LogDirName))
	}
	if ws.LogCount != 0 {
		t.Errorf("InspectWorkspace log count = %d, want 0", ws.LogCount)
	}
	if ws.LastLog != "" {
		t.Errorf("InspectWorkspace last log = %q, want empty", ws.LastLog)
	}

	tee := NewTee(root)
	if _, err := tee.Write(ToolGeneric, []byte("first")); err != nil {
		t.Fatalf("tee write: %v", err)
	}
	if _, err := tee.Write(ToolGoTest, []byte("second")); err != nil {
		t.Fatalf("tee write: %v", err)
	}

	ws = InspectWorkspace(root)
	if ws.LogCount != 2 {
		t.Errorf("InspectWorkspace log count = %d, want 2", ws.LogCount)
	}
	if len(ws.LogFiles) != 2 {
		t.Errorf("InspectWorkspace log files = %d, want 2", len(ws.LogFiles))
	}
	if ws.LastLog == "" {
		t.Error("InspectWorkspace last log empty after writes")
	}
	if !strings.Contains(ws.LastLog, LogDirName) {
		t.Errorf("InspectWorkspace last log %q should live under .logs/", ws.LastLog)
	}
}
