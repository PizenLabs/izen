package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type LocalConfig struct {
	Username string `json:"username"`
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
