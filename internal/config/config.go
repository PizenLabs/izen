package config

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Models    ModelConfig    `yaml:"models"`
	Execution ExecutionConfig `yaml:"execution"`
	Fallback  FallbackConfig  `yaml:"fallback"`
	Lynx      LynxConfig      `yaml:"lynx"`
	MCP       MCPConfig       `yaml:"mcp"`
}

type ModelConfig struct {
	Default  string `yaml:"default"`
	Fast     string `yaml:"fast"`
	Provider string `yaml:"provider"`
}

type ExecutionConfig struct {
	Sandbox bool `yaml:"sandbox"`
	Confirm bool `yaml:"confirm"`
}

type FallbackConfig struct {
	Enabled bool `yaml:"enabled"`
}

type MCPConfig struct {
	Enabled bool     `yaml:"enabled"`
	Servers []string `yaml:"servers"`
}

type LynxConfig struct {
	Enabled           bool    `yaml:"enabled"`
	LazyStart         bool    `yaml:"lazy_start"`
	SemanticThreshold float64 `yaml:"semantic_threshold"`
	IndexOnStart      bool    `yaml:"index_on_start"`
	MaxResults        int     `yaml:"max_results"`
}

func Load() (*Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	path := filepath.Join(home, ".izen", "izen.conf.yml")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Default(), nil
		}
		return nil, err
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}

func Default() *Config {
	return &Config{
		Models: ModelConfig{
			Default:  "claude-sonnet-4-20250514",
			Provider: "anthropic",
		},
		Execution: ExecutionConfig{
			Sandbox: true,
			Confirm: true,
		},
		Fallback: FallbackConfig{
			Enabled: true,
		},
		Lynx: LynxConfig{
			Enabled:           true,
			LazyStart:         true,
			SemanticThreshold: 0.6,
			IndexOnStart:      false,
			MaxResults:        20,
		},
		MCP: MCPConfig{
			Enabled: false,
		},
	}
}

func Save(cfg *Config) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	dir := filepath.Join(home, ".izen")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	path := filepath.Join(dir, "izen.conf.yml")
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0644)
}
