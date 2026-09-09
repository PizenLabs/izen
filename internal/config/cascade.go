// Package config (cascade.go) implements the Phase 1 cascading configuration
// pipeline.
//
// NOTE ON NAMING: the Phase 1 spec calls the resolved type "Config".
// That name is already taken in this package by the legacy YAML application
// config (config.go). To avoid breaking the existing codebase the Phase 1
// type is named CascadeConfig. Its shape matches the spec exactly:
//
//	Precedence: Env Vars >> .izen/config.json >> ~/.izen/config.yml >> defaults
//
// Fields: Roles, CheckpointRetention, AutoCommit, Providers (never persisted).
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/PizenLabs/izen/internal/provider/detector"
	"gopkg.in/yaml.v3"
)

// DefaultCheckpointRetention is the system default when no layer sets one.
const DefaultCheckpointRetention = 10

// DefaultGitignore is the canonical hygiene file written to
// <workDir>/.izen/.gitignore when missing. It tracks ONLY config.json and
// plans/ and ignores all runtime state.
const DefaultGitignore = `# Managed by izen — tracks only config.json and plans/.
# All runtime state is ignored and must never be committed.
*
!config.json
!plans/
!plans/**
!.gitignore
checkpoints/
artifacts/
sessions/
audit/
graph.bin.zst
`

// CascadeConfig is the Phase 1 resolved configuration (spec: "Config").
type CascadeConfig struct {
	Roles               map[string]string         `json:"roles" yaml:"roles"`
	CheckpointRetention int                       `json:"checkpoint_retention" yaml:"checkpoint_retention"`
	AutoCommit          bool                      `json:"auto_commit" yaml:"auto_commit"`
	Providers           []detector.ProviderConfig `json:"-" yaml:"-"`
}

// cascadeFile mirrors the persisted JSON/YAML shape. Pointers distinguish
// "key absent" (inherit) from "key explicitly set" (override, including zero
// values such as checkpoint_retention: 0 or auto_commit: false).
type cascadeFile struct {
	Roles               map[string]string `json:"roles" yaml:"roles"`
	CheckpointRetention *int              `json:"checkpoint_retention" yaml:"checkpoint_retention"`
	AutoCommit          *bool             `json:"auto_commit" yaml:"auto_commit"`
}

// DefaultCascadeConfig returns system defaults.
func DefaultCascadeConfig() *CascadeConfig {
	return &CascadeConfig{
		Roles:               make(map[string]string),
		CheckpointRetention: DefaultCheckpointRetention,
		AutoCommit:          false,
	}
}

// ResolveConfig merges Env Vars >> .izen/config.json >> ~/.izen/config.yml
// >> system defaults for workDir, attaches detected providers, and ensures
// .izen/.gitignore hygiene. It never leaks ephemeral runtime data to Git:
// only config.json and plans/ are tracked (see DefaultGitignore).
func ResolveConfig(workDir string) (*CascadeConfig, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	return ResolveConfigWithHome(workDir, home)
}

// ResolveConfigWithHome is ResolveConfig with an injectable home directory
// for tests. An empty home disables the global ~/.izen/config.yml layer and
// file-credential loading.
func ResolveConfigWithHome(workDir, home string) (*CascadeConfig, error) {
	cfg := DefaultCascadeConfig()

	// Layer 1 (lowest): system defaults already applied.

	// Layer 2: global ~/.izen/config.yml.
	if home != "" {
		if global, err := loadGlobalConfig(filepath.Join(home, ".izen", "config.yml")); err != nil {
			return nil, err
		} else if global != nil {
			applyFileLayer(cfg, global)
		}
	}

	// Layer 3: local .izen/config.json.
	if workDir != "" {
		if local, err := loadLocalConfig(filepath.Join(workDir, ".izen", "config.json")); err != nil {
			return nil, err
		} else if local != nil {
			applyFileLayer(cfg, local)
		}
	}

	// Layer 4 (highest): environment variables.
	applyEnvLayer(cfg)

	// Providers: env-detected credentials override file credentials.
	cfg.Providers = detector.DetectProvidersWithHome(home)

	// Git hygiene side effect (best-effort but explicit: surface mkdir/write
	// failures when a workDir was given).
	if workDir != "" {
		if err := EnsureGitignore(workDir); err != nil {
			return nil, err
		}
	}

	return cfg, nil
}

// applyFileLayer overlays one file layer onto cfg. Roles merge by key
// (layer wins per key); scalar pointers override only when non-nil so an
// absent key inherits while an explicit zero value still applies.
func applyFileLayer(cfg *CascadeConfig, layer *cascadeFile) {
	if layer.Roles != nil {
		if cfg.Roles == nil {
			cfg.Roles = make(map[string]string, len(layer.Roles))
		}
		for k, v := range layer.Roles {
			cfg.Roles[k] = v
		}
	}
	if layer.CheckpointRetention != nil {
		cfg.CheckpointRetention = *layer.CheckpointRetention
	}
	if layer.AutoCommit != nil {
		cfg.AutoCommit = *layer.AutoCommit
	}
}

// applyEnvLayer overlays environment variables (highest precedence):
//   - IZEN_CHECKPOINT_RETENTION=int
//   - IZEN_AUTO_COMMIT=true|false|1|0|yes|no (ParseBool semantics + yes/no)
//   - IZEN_ROLES={"role":"model",...} (JSON object; merged by key)
func applyEnvLayer(cfg *CascadeConfig) {
	if v := strings.TrimSpace(os.Getenv("IZEN_CHECKPOINT_RETENTION")); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.CheckpointRetention = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("IZEN_AUTO_COMMIT")); v != "" {
		if b, ok := parseBoolEnv(v); ok {
			cfg.AutoCommit = b
		}
	}
	if v := strings.TrimSpace(os.Getenv("IZEN_ROLES")); v != "" {
		var roles map[string]string
		if err := json.Unmarshal([]byte(v), &roles); err == nil && roles != nil {
			if cfg.Roles == nil {
				cfg.Roles = make(map[string]string, len(roles))
			}
			for k, val := range roles {
				cfg.Roles[k] = val
			}
		}
	}
}

// parseBoolEnv extends strconv.ParseBool with yes/no/on/off.
func parseBoolEnv(s string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "y", "yes", "on":
		return true, true
	case "n", "no", "off":
		return false, true
	default:
		b, err := strconv.ParseBool(strings.TrimSpace(s))
		if err != nil {
			return false, false
		}
		return b, true
	}
}

// loadLocalConfig reads .izen/config.json. A missing file yields (nil, nil);
// malformed JSON yields an explicit error.
func loadLocalConfig(path string) (*cascadeFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read local config %s: %w", path, err)
	}
	var cf cascadeFile
	if err := json.Unmarshal(data, &cf); err != nil {
		return nil, fmt.Errorf("parse local config %s: %w", path, err)
	}
	return &cf, nil
}

// loadGlobalConfig reads ~/.izen/config.yml. A missing file yields (nil, nil);
// malformed YAML yields an explicit error. Unknown keys are ignored so the
// legacy full application config.yml stays compatible.
func loadGlobalConfig(path string) (*cascadeFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read global config %s: %w", path, err)
	}
	var cf cascadeFile
	if err := yaml.Unmarshal(data, &cf); err != nil {
		return nil, fmt.Errorf("parse global config %s: %w", path, err)
	}
	return &cf, nil
}

// EnsureGitignore creates <workDir>/.izen/.gitignore with DefaultGitignore
// when missing. Existing files are left untouched. It also ensures the
// .izen directory exists.
func EnsureGitignore(workDir string) error {
	if workDir == "" {
		return fmt.Errorf("EnsureGitignore: empty workDir")
	}
	dir := filepath.Join(workDir, ".izen")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	path := filepath.Join(dir, ".gitignore")
	if _, err := os.Stat(path); err == nil {
		return nil // present: never overwrite user edits
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	if err := os.WriteFile(path, []byte(DefaultGitignore), 0644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
