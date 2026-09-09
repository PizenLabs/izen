package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeCascadeGlobal(t *testing.T, home, payload string) {
	t.Helper()
	dir := filepath.Join(home, ".izen")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir global .izen: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yml"), []byte(payload), 0644); err != nil {
		t.Fatalf("write global config.yml: %v", err)
	}
}

func writeCascadeLocal(t *testing.T, workDir, payload string) {
	t.Helper()
	dir := filepath.Join(workDir, ".izen")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir local .izen: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(payload), 0644); err != nil {
		t.Fatalf("write local config.json: %v", err)
	}
}

func TestCascadeLocalOverridesGlobalRoles(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	writeCascadeGlobal(t, home, "roles:\n  build: global-model\n  review: global-review\ncheckpoint_retention: 7\n")
	writeCascadeLocal(t, work, `{"roles":{"build":"local-model"}}`)

	cfg, err := ResolveConfigWithHome(work, home)
	if err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	if cfg.Roles["build"] != "local-model" {
		t.Errorf("roles[build] = %q, want %q (local must override global)", cfg.Roles["build"], "local-model")
	}
	if cfg.Roles["review"] != "global-review" {
		t.Errorf("roles[review] = %q, want inherited global value", cfg.Roles["review"])
	}
	if cfg.CheckpointRetention != 7 {
		t.Errorf("CheckpointRetention = %d, want inherited global 7", cfg.CheckpointRetention)
	}
}

func TestCascadeExplicitFalseOverridesGlobalTrue(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	writeCascadeGlobal(t, home, "auto_commit: true\n")
	writeCascadeLocal(t, work, `{"auto_commit": false}`)

	cfg, err := ResolveConfigWithHome(work, home)
	if err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	if cfg.AutoCommit {
		t.Error("AutoCommit = true, want false (explicit local false must win)")
	}
}

func TestCascadeEnvBeatsAllLayers(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	writeCascadeGlobal(t, home, "checkpoint_retention: 7\nauto_commit: false\n")
	writeCascadeLocal(t, work, `{"checkpoint_retention": 5, "auto_commit": false}`)
	t.Setenv("IZEN_CHECKPOINT_RETENTION", "42")
	t.Setenv("IZEN_AUTO_COMMIT", "true")
	t.Setenv("IZEN_ROLES", `{"build":"env-model"}`)

	cfg, err := ResolveConfigWithHome(work, home)
	if err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	if cfg.CheckpointRetention != 42 {
		t.Errorf("CheckpointRetention = %d, want env 42", cfg.CheckpointRetention)
	}
	if !cfg.AutoCommit {
		t.Error("AutoCommit = false, want env true")
	}
	if cfg.Roles["build"] != "env-model" {
		t.Errorf("roles[build] = %q, want env-model", cfg.Roles["build"])
	}
}

func TestCascadeDefaultsWithNoFiles(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	t.Setenv("IZEN_CHECKPOINT_RETENTION", "")
	t.Setenv("IZEN_AUTO_COMMIT", "")
	t.Setenv("IZEN_ROLES", "")

	cfg, err := ResolveConfigWithHome(work, home)
	if err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	if cfg.CheckpointRetention != DefaultCheckpointRetention {
		t.Errorf("CheckpointRetention = %d, want default %d", cfg.CheckpointRetention, DefaultCheckpointRetention)
	}
	if cfg.AutoCommit {
		t.Error("AutoCommit = true, want default false")
	}
	if cfg.Roles == nil {
		t.Error("Roles is nil, want non-nil map")
	}
}

func TestGitignoreAutoCreation(t *testing.T) {
	work := t.TempDir()
	if err := EnsureGitignore(work); err != nil {
		t.Fatalf("EnsureGitignore: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(work, ".izen", ".gitignore"))
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	if string(data) != DefaultGitignore {
		t.Errorf("gitignore mismatch.\n got:\n%s\nwant:\n%s", data, DefaultGitignore)
	}
	for _, want := range []string{"checkpoints/", "artifacts/", "sessions/", "audit/", "graph.bin.zst", "!config.json", "plans/"} {
		if !containsSubstr(string(data), want) {
			t.Errorf(".gitignore missing rule %q", want)
		}
	}
}

func TestGitignoreNeverOverwrites(t *testing.T) {
	work := t.TempDir()
	dir := filepath.Join(work, ".izen")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	custom := "# user custom\n*.log\n"
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(custom), 0644); err != nil {
		t.Fatal(err)
	}
	if err := EnsureGitignore(work); err != nil {
		t.Fatalf("EnsureGitignore: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if string(data) != custom {
		t.Errorf("existing .gitignore overwritten: %q", data)
	}
}

func TestResolveConfigEnsuresGitignore(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	t.Setenv("IZEN_CHECKPOINT_RETENTION", "")
	t.Setenv("IZEN_AUTO_COMMIT", "")
	t.Setenv("IZEN_ROLES", "")
	if _, err := ResolveConfigWithHome(work, home); err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	if _, err := os.Stat(filepath.Join(work, ".izen", ".gitignore")); err != nil {
		t.Errorf("expected .gitignore side effect: %v", err)
	}
}

func containsSubstr(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}
