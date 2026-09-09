package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPhase3CreateAndLoadActiveSession(t *testing.T) {
	workDir := t.TempDir()
	sess, err := CreateSession(workDir)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if sess.SessionID == "" {
		t.Fatal("SessionID empty")
	}
	for _, name := range []string{"session.json", "context.json", "checkpoint.json"} {
		if _, err := os.Stat(filepath.Join(workDir, ".izen", "sessions", sess.SessionID, name)); err != nil {
			t.Fatalf("expected %s: %v", name, err)
		}
	}
	active, err := os.ReadFile(filepath.Join(workDir, ".izen", "sessions", "active"))
	if err != nil {
		t.Fatalf("read active: %v", err)
	}
	if strings.TrimSpace(string(active)) != sess.SessionID {
		t.Fatalf("active = %q, want %q", strings.TrimSpace(string(active)), sess.SessionID)
	}

	loaded, err := LoadActiveSession(workDir)
	if err != nil {
		t.Fatalf("LoadActiveSession: %v", err)
	}
	if loaded.SessionID != sess.SessionID {
		t.Fatalf("loaded id = %q, want %q", loaded.SessionID, sess.SessionID)
	}
}

func TestPhase3LoadActiveSymlink(t *testing.T) {
	workDir := t.TempDir()
	sess, err := CreateSession(workDir)
	if err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(workDir, ".izen", "sessions")
	activePath := filepath.Join(base, "active")
	idBytes, err := os.ReadFile(activePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(activePath); err != nil {
		t.Fatal(err)
	}
	// Symlink active -> <session_id> directory.
	if err := os.Symlink(strings.TrimSpace(string(idBytes)), activePath); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	loaded, err := LoadActiveSession(workDir)
	if err != nil {
		t.Fatalf("LoadActiveSession via symlink: %v", err)
	}
	if loaded.SessionID != sess.SessionID {
		t.Fatalf("loaded id = %q, want %q", loaded.SessionID, sess.SessionID)
	}
}

func TestPhase3LoadActiveMissing(t *testing.T) {
	if _, err := LoadActiveSession(t.TempDir()); err == nil {
		t.Fatal("expected error for missing active marker")
	}
	if _, err := CreateSession(""); err == nil {
		t.Fatal("expected error for empty workDir")
	}
}
