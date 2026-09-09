package model

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// FileConfigRepository persists role bindings to <workDir>/.izen/config.json
// (local scope) or ~/.izen/config.yml-adjacent global config (global scope).
// It owns ALL file I/O for role bindings so the TUI presentation layer stays
// a pure view. Only this repository and the config package touch the disk.
type FileConfigRepository struct {
	workDir string
}

// NewFileConfigRepository builds a repository rooted at workDir. An empty
// workDir disables local persistence (SaveBinding returns an error for local
// scope); global scope resolves via the OS home directory.
func NewFileConfigRepository(workDir string) *FileConfigRepository {
	return &FileConfigRepository{workDir: workDir}
}

// SaveBinding implements ConfigRepository. It preserves all unrelated keys.
func (r *FileConfigRepository) SaveBinding(ctx context.Context, cmd BindModelToRoleCommand) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(string(cmd.Role)) == "" {
		return fmt.Errorf("model: empty role name")
	}
	if strings.TrimSpace(cmd.ModelID) == "" {
		return fmt.Errorf("model: empty model id for role %q", string(cmd.Role))
	}
	path, err := r.configPath(cmd.IsGlobal)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("model: mkdir %s: %w", dir, err)
	}
	var root map[string]json.RawMessage
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &root) // corrupt file: start over with roles
	}
	if root == nil {
		root = make(map[string]json.RawMessage)
	}
	var roles map[string]string
	if raw, ok := root["roles"]; ok {
		_ = json.Unmarshal(raw, &roles)
	}
	if roles == nil {
		roles = make(map[string]string)
	}
	roles[string(cmd.Role)] = cmd.ModelID
	encoded, err := json.Marshal(roles)
	if err != nil {
		return fmt.Errorf("model: marshal roles: %w", err)
	}
	root["roles"] = encoded
	data, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return fmt.Errorf("model: marshal config: %w", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("model: write %s: %w", path, err)
	}
	return nil
}

func (r *FileConfigRepository) configPath(isGlobal bool) (string, error) {
	if isGlobal {
		home, err := os.UserHomeDir()
		if err != nil || strings.TrimSpace(home) == "" {
			return "", fmt.Errorf("model: global scope needs a home directory")
		}
		return filepath.Join(home, ".izen", "config.json"), nil
	}
	if strings.TrimSpace(r.workDir) == "" {
		return "", fmt.Errorf("model: empty workDir, role binding not persisted")
	}
	return filepath.Join(r.workDir, ".izen", "config.json"), nil
}
