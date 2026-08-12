package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestEnsureLocalWorkspaceCreatesIzenDir asserts the defensive workspace
// loader recreates the .izen/ directory structure when it is missing, without
// fabricating a config.json (a fresh workspace must still flow through the
// onboarding wizard).
func TestEnsureLocalWorkspaceCreatesIzenDir(t *testing.T) {
	root := t.TempDir()

	cfg, err := EnsureLocalWorkspace(root)
	if err != nil {
		t.Fatalf("EnsureLocalWorkspace = %v, want nil", err)
	}
	if cfg == nil {
		t.Fatal("EnsureLocalWorkspace returned nil config")
	}

	if _, err := os.Stat(filepath.Join(root, ".izen")); err != nil {
		t.Errorf(".izen/ was not recreated: %v", err)
	}
	for _, sub := range []string{"graph", "history", "audit", "checkpoints", "patches"} {
		if _, err := os.Stat(filepath.Join(root, ".izen", sub)); err != nil {
			t.Errorf(".izen/%s was not recreated: %v", sub, err)
		}
	}

	// A fresh workspace must never be silently marked onboarded: config.json
	// is the sole authority and is left absent for the wizard to create.
	if _, err := os.Stat(filepath.Join(root, ".izen", "config.json")); !os.IsNotExist(err) {
		t.Error("EnsureLocalWorkspace fabricated config.json for a fresh workspace")
	}
}

// TestEnsureLocalWorkspaceIsIdempotent asserts repeated calls do not error
// and never lose existing config.
func TestEnsureLocalWorkspaceIsIdempotent(t *testing.T) {
	root := t.TempDir()
	if _, err := EnsureLocalWorkspace(root); err != nil {
		t.Fatalf("first EnsureLocalWorkspace = %v", err)
	}
	if _, err := EnsureLocalWorkspace(root); err != nil {
		t.Fatalf("second EnsureLocalWorkspace = %v", err)
	}
}

// TestEnsureLocalWorkspaceLoadsExistingConfig asserts an existing
// .izen/config.json is reloaded verbatim.
func TestEnsureLocalWorkspaceLoadsExistingConfig(t *testing.T) {
	root := t.TempDir()
	if err := SaveLocalConfig(root, &LocalConfig{Username: "Jaky", ProjectName: "myproj"}); err != nil {
		t.Fatalf("SaveLocalConfig = %v", err)
	}

	cfg, err := EnsureLocalWorkspace(root)
	if err != nil {
		t.Fatalf("EnsureLocalWorkspace = %v", err)
	}
	if cfg.Username != "Jaky" {
		t.Errorf("Username = %q, want %q", cfg.Username, "Jaky")
	}
	if cfg.ProjectName != "myproj" {
		t.Errorf("ProjectName = %q, want %q", cfg.ProjectName, "myproj")
	}
}

// TestEnsureLocalWorkspaceDegradesSafely asserts the loader never panics and
// never returns nil on degenerate inputs (empty root, unwritable path).
func TestEnsureLocalWorkspaceDegradesSafely(t *testing.T) {
	cfg, err := EnsureLocalWorkspace("")
	if err != nil {
		t.Fatalf("EnsureLocalWorkspace(\"\") = %v, want nil", err)
	}
	if cfg == nil {
		t.Fatal("EnsureLocalWorkspace(\"\") returned nil config")
	}

	// Unwritable root: a plain file used as the workspace root forces the
	// .izen/ MkdirAll to fail; the loader must degrade to an empty config
	// rather than panic.
	parent := t.TempDir()
	blocker := filepath.Join(parent, "blocker")
	if err := os.WriteFile(blocker, []byte("not a dir"), 0644); err != nil {
		t.Fatalf("WriteFile = %v", err)
	}
	cfg, err = EnsureLocalWorkspace(blocker)
	if err == nil {
		t.Error("EnsureLocalWorkspace(unwritable root) = nil error, want error")
	}
	if cfg == nil {
		t.Fatal("EnsureLocalWorkspace(unwritable root) returned nil config")
	}
}
