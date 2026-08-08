package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/PizenLabs/izen/internal/state"
)

type LocalConfig struct {
	Username     string `json:"username"`
	DetectedLang string `json:"detected_lang,omitempty"`
	DetectedFw   string `json:"detected_framework,omitempty"`
	ProjectName  string `json:"project_name,omitempty"`
	LastDetected string `json:"last_detected,omitempty"`
}

func localConfigPath(root string) string {
	return filepath.Join(root, ".izen", "config.json")
}

func LoadLocalConfig(root string) (*LocalConfig, error) {
	path := localConfigPath(root)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &LocalConfig{}, nil
		}
		return nil, err
	}
	var cfg LocalConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &cfg, nil
}

func SaveLocalConfig(root string, cfg *LocalConfig) error {
	dir := filepath.Join(root, ".izen")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	path := localConfigPath(root)
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// EnsureLocalWorkspace is the defensive workspace loader. It guarantees a
// usable local workspace state for the given root directory:
//
//   - If .izen/ is missing (or was emptied/deleted), the directory structure
//     is recreated so downstream engines never panic, return unhandled errors,
//     or block on a missing workspace.
//   - If .izen/config.json exists it is reloaded; otherwise a zero-valued
//     LocalConfig is returned (a fresh workspace has no config yet — it is
//     never fabricated here, onboarding is responsible for writing it).
//
// It never panics, never blocks on execution channels, and never returns a nil
// config: every failure degrades to an empty LocalConfig so callers always
// have a safe default to render against.
func EnsureLocalWorkspace(root string) (*LocalConfig, error) {
	if root == "" {
		return &LocalConfig{}, nil
	}
	dir := filepath.Join(root, state.LocalDir)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		if mkErr := os.MkdirAll(dir, 0755); mkErr != nil {
			return &LocalConfig{}, fmt.Errorf("mkdir %s: %w", dir, mkErr)
		}
	}
	if err := state.InitLocalState(root); err != nil {
		return &LocalConfig{}, err
	}
	return LoadLocalConfig(root)
}
