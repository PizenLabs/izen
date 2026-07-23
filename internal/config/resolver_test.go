package config

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestModeModelResolverSessionPriority(t *testing.T) {
	tmpDir := t.TempDir()

	writeProjectConfig(t, tmpDir, map[string]string{"build": "project-build-model"})
	writeGlobalConfig(t, map[string]string{"build": "global-build-model"})

	resolver := NewModeModelResolver(tmpDir)
	resolver.SetSessionModel("session-model")

	result := resolver.ResolveModelForMode("build")
	if result != "session-model" {
		t.Errorf("ResolveModelForMode = %q, want %q", result, "session-model")
	}

	resolver.ClearSessionModel()
}

func TestModeModelResolverProjectPriority(t *testing.T) {
	tmpDir := t.TempDir()

	writeProjectConfig(t, tmpDir, map[string]string{"build": "project-build-model"})
	writeGlobalConfig(t, map[string]string{"build": "global-build-model"})

	resolver := NewModeModelResolver(tmpDir)

	result := resolver.ResolveModelForMode("build")
	if result != "project-build-model" {
		t.Errorf("ResolveModelForMode = %q, want %q", result, "project-build-model")
	}
}

func TestModeModelResolverGlobalFallback(t *testing.T) {
	tmpDir := t.TempDir()

	writeGlobalConfig(t, map[string]string{"review": "global-review-model"})

	resolver := NewModeModelResolver(tmpDir)

	result := resolver.ResolveModelForMode("review")
	if result != "global-review-model" {
		t.Errorf("ResolveModelForMode = %q, want %q", result, "global-review-model")
	}
}

func TestModeModelResolverNoMatch(t *testing.T) {
	tmpDir := t.TempDir()

	writeProjectConfig(t, tmpDir, map[string]string{"build": "build-model"})
	writeGlobalConfig(t, map[string]string{"review": "review-model"})

	resolver := NewModeModelResolver(tmpDir)

	result := resolver.ResolveModelForMode("ask")
	if result != "" {
		t.Errorf("ResolveModelForMode = %q, want empty string", result)
	}
}

func TestModeModelResolverProjectTakesPriorityOverGlobal(t *testing.T) {
	tmpDir := t.TempDir()

	writeProjectConfig(t, tmpDir, map[string]string{
		"build":       "project-build",
		"investigate": "project-investigate",
	})
	writeGlobalConfig(t, map[string]string{
		"build":       "global-build",
		"investigate": "global-investigate",
	})

	resolver := NewModeModelResolver(tmpDir)

	buildModel := resolver.ResolveModelForMode("build")
	if buildModel != "project-build" {
		t.Errorf("build model = %q, want %q", buildModel, "project-build")
	}

	invModel := resolver.ResolveModelForMode("investigate")
	if invModel != "project-investigate" {
		t.Errorf("investigate model = %q, want %q", invModel, "project-investigate")
	}
}

func TestModeModelResolverGlobalOnly(t *testing.T) {
	tmpDir := t.TempDir()

	writeGlobalConfig(t, map[string]string{"plan": "global-plan-model"})

	resolver := NewModeModelResolver(tmpDir)

	result := resolver.ResolveModelForMode("plan")
	if result != "global-plan-model" {
		t.Errorf("ResolveModelForMode = %q, want %q", result, "global-plan-model")
	}
}

func TestModeModelResolverNoConfigs(t *testing.T) {
	tmpDir := t.TempDir()

	resolver := NewModeModelResolver(tmpDir)

	result := resolver.ResolveModelForMode("build")
	if result != "" {
		t.Errorf("ResolveModelForMode = %q, want empty", result)
	}
}

func TestModeModelResolverEmptyModeDefaults(t *testing.T) {
	tmpDir := t.TempDir()

	writeProjectConfig(t, tmpDir, nil)
	writeGlobalConfig(t, nil)

	resolver := NewModeModelResolver(tmpDir)

	result := resolver.ResolveModelForMode("build")
	if result != "" {
		t.Errorf("ResolveModelForMode = %q, want empty", result)
	}
}

func TestModeModelResolverSessionCleared(t *testing.T) {
	tmpDir := t.TempDir()

	writeProjectConfig(t, tmpDir, map[string]string{"build": "project-model"})

	resolver := NewModeModelResolver(tmpDir)
	resolver.SetSessionModel("session-model")
	resolver.ClearSessionModel()

	result := resolver.ResolveModelForMode("build")
	if result != "project-model" {
		t.Errorf("after ClearSessionModel, ResolveModelForMode = %q, want %q", result, "project-model")
	}
}

func TestActiveModelNameWithSessionModel(t *testing.T) {
	cfg := Default()
	cfg.Models.SessionModel = "session-override"

	result := cfg.ActiveModelName()
	if result != "session-override" {
		t.Errorf("ActiveModelName = %q, want %q", result, "session-override")
	}
}

func TestActiveModelNameWithoutSessionModel(t *testing.T) {
	cfg := Default()
	cfg.Models.SessionModel = ""

	result := cfg.ActiveModelName()
	if result != "qwen2.5-coder:7b" {
		t.Errorf("ActiveModelName = %q, want %q", result, "qwen2.5-coder:7b")
	}
}

func writeProjectConfig(t *testing.T, root string, modeDefaults map[string]string) {
	t.Helper()

	dir := filepath.Join(root, ".izen")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}

	cfg := struct {
		Models struct {
			ModeDefaults map[string]string `yaml:"mode_defaults"`
		} `yaml:"models"`
	}{}
	cfg.Models.ModeDefaults = modeDefaults

	data, err := yaml.Marshal(&cfg)
	if err != nil {
		t.Fatalf("marshal project config: %v", err)
	}

	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func writeGlobalConfig(t *testing.T, modeDefaults map[string]string) {
	t.Helper()

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}

	dir := filepath.Join(home, ".izen")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}

	cfg := struct {
		Models struct {
			ModeDefaults map[string]string `yaml:"mode_defaults"`
		} `yaml:"models"`
	}{}
	cfg.Models.ModeDefaults = modeDefaults

	data, err := yaml.Marshal(&cfg)
	if err != nil {
		t.Fatalf("marshal global config: %v", err)
	}

	path := filepath.Join(home, ".izen", "config.yml")
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}

	t.Cleanup(func() {
		_ = os.Remove(path)
	})
}
