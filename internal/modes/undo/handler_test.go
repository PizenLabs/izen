package undo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/checkpoint"
)

func setupTestRepo(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "izen-undo-test-*")
	if err != nil {
		t.Fatalf("mkdir temp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	run(t, dir, "init")
	run(t, dir, "config", "user.email", "test@izen.dev")
	run(t, dir, "config", "user.name", "Izen Test")
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}
	run(t, dir, "add", "main.go")
	run(t, dir, "commit", "-m", "initial commit")

	return dir
}

func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := checkpoint.CommandContext(dir, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v\n%s", strings.Join(args, " "), err, string(out))
	}
}

func TestParseUndo(t *testing.T) {
	tests := []struct {
		input string
		want  Mode
	}{
		{"/undo", ModeSingleStep},
		{"/undo session", ModeSession},
		{"/undo --all", ModeSession},
		{"/undo --session", ModeSession},
		{"undo", ModeSingleStep},
		{"undo session", ModeSession},
		{"/undo nonsense", ModeSingleStep},
		{"", ModeSingleStep},
	}
	for _, tt := range tests {
		got := Parse(tt.input)
		if got != tt.want {
			t.Errorf("Parse(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestHandlerUndo(t *testing.T) {
	dir := setupTestRepo(t)

	cpEngine := checkpoint.NewEngine(dir)
	handler := NewHandler(cpEngine)

	// Create a session-start checkpoint.
	if _, err := cpEngine.CreateSessionStartSnapshot(); err != nil {
		t.Fatalf("session start snapshot: %v", err)
	}

	// Write initial content and create a pre-exec checkpoint.
	mainPath := filepath.Join(dir, "main.go")
	initialContent := []byte("package main\n\nfunc main() {}\n")
	if err := os.WriteFile(mainPath, initialContent, 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	cp, err := cpEngine.CreatePreExecSnapshot("$hot change year")
	if err != nil {
		t.Fatalf("pre-exec snapshot: %v", err)
	}
	if cp == nil {
		t.Fatal("expected non-nil checkpoint")
	}

	// Mutate the file.
	mutatedContent := []byte("package main\n\nfunc main() { println(\"2026\"); }\n")
	if err := os.WriteFile(mainPath, mutatedContent, 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Verify mutation took effect.
	current, _ := os.ReadFile(mainPath)
	if string(current) != string(mutatedContent) {
		t.Fatal("mutation did not take effect")
	}

	// Execute undo.
	result, err := handler.Undo()
	if err != nil {
		t.Fatalf("Undo: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got message: %s", result.Message)
	}
	if !strings.Contains(result.Message, "$hot change year") {
		t.Fatalf("message should reference checkpoint label, got: %s", result.Message)
	}

	// Verify file content restored.
	restored, _ := os.ReadFile(mainPath)
	if string(restored) != string(initialContent) {
		t.Fatalf("restored content mismatch:\nexpected: %q\ngot: %q",
			string(initialContent), string(restored))
	}
}

func TestHandlerUndoSession(t *testing.T) {
	dir := setupTestRepo(t)

	cpEngine := checkpoint.NewEngine(dir)
	handler := NewHandler(cpEngine)

	// Create session-start checkpoint.
	sessionCP, err := cpEngine.CreateSessionStartSnapshot()
	if err != nil {
		t.Fatalf("session start: %v", err)
	}
	if sessionCP == nil {
		t.Fatal("expected non-nil session checkpoint")
	}

	// Read initial session state.
	mainPath := filepath.Join(dir, "main.go")
	sessionContent, _ := os.ReadFile(mainPath)

	// Mutate the file multiple times.
	if err := os.WriteFile(mainPath, []byte("package main\n\nfunc main() { println(\"a\"); }\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := cpEngine.CreatePreExecSnapshot("mutation-a"); err != nil {
		t.Fatalf("mutation a: %v", err)
	}

	if err := os.WriteFile(mainPath, []byte("package main\n\nfunc main() { println(\"b\"); }\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := cpEngine.CreatePreExecSnapshot("mutation-b"); err != nil {
		t.Fatalf("mutation b: %v", err)
	}

	// Verify file is in mutated state.
	current, _ := os.ReadFile(mainPath)
	if strings.Contains(string(current), "println(\"b\")") == false {
		t.Fatal("expected mutated state before session undo")
	}

	// Session undo.
	result, err := handler.UndoSession()
	if err != nil {
		t.Fatalf("UndoSession: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got: %s", result.Message)
	}

	// Verify restored to session state.
	restored, _ := os.ReadFile(mainPath)
	if string(restored) != string(sessionContent) {
		t.Fatalf("session restore: expected %q, got %q",
			string(sessionContent), string(restored))
	}
}

func TestHandlerUndoByMode_Session(t *testing.T) {
	dir := setupTestRepo(t)

	cpEngine := checkpoint.NewEngine(dir)
	handler := NewHandler(cpEngine)

	if _, err := cpEngine.CreateSessionStartSnapshot(); err != nil {
		t.Fatalf("session start: %v", err)
	}

	mainPath := filepath.Join(dir, "main.go")
	sessionContent, _ := os.ReadFile(mainPath)

	// Mutate.
	if err := os.WriteFile(mainPath, []byte("package main\n\nfunc main() { println(\"x\"); }\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := cpEngine.CreatePreExecSnapshot("mutation-x"); err != nil {
		t.Fatalf("mutation-x: %v", err)
	}

	// Undo by mode (session).
	result, err := handler.UndoByMode(ModeSession)
	if err != nil {
		t.Fatalf("UndoByMode: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got: %s", result.Message)
	}

	restored, _ := os.ReadFile(mainPath)
	if string(restored) != string(sessionContent) {
		t.Fatalf("restored content mismatch:\nexpected: %q\ngot: %q",
			string(sessionContent), string(restored))
	}
}

func TestHandlerUndo_NoCheckpoints(t *testing.T) {
	dir := setupTestRepo(t)

	cpEngine := checkpoint.NewEngine(dir)
	handler := NewHandler(cpEngine)

	result, err := handler.Undo()
	if err != nil {
		t.Fatalf("Undo: %v", err)
	}
	if result.Success {
		t.Fatal("expected failure when no checkpoints exist")
	}
	if !strings.Contains(result.Message, "No checkpoints found") {
		t.Fatalf("unexpected message: %s", result.Message)
	}
}
