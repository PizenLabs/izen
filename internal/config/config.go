package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

type AIProviderConfig struct {
	BaseURL      string `yaml:"base_url"`
	APIKey       string `yaml:"api_key"`
	DefaultModel string `yaml:"default_model"`
}

type AIConfig struct {
	DefaultProvider  string                      `yaml:"default_provider"`
	FallbackProvider string                      `yaml:"fallback_provider"`
	Providers        map[string]AIProviderConfig `yaml:"providers"`
}

type Config struct {
	AI        AIConfig        `yaml:"ai"`
	Models    ModelConfig     `yaml:"models"`
	Execution ExecutionConfig `yaml:"execution"`
	Fallback  FallbackConfig  `yaml:"fallback"`
	Lynx      LynxConfig      `yaml:"lynx"`
	MCP       MCPConfig       `yaml:"mcp"`
	Username  string          `yaml:"username"`
}

type ModelConfig struct {
	Default  string `yaml:"default"`
	Fast     string `yaml:"fast"`
	Provider string `yaml:"provider"`
}

type ExecutionConfig struct {
	Sandbox      bool               `yaml:"sandbox"`
	Confirm      bool               `yaml:"confirm"`
	Policy       PolicyConfig       `yaml:"policy"`
	Verification VerificationConfig `yaml:"verification"`
	SandboxMode  string             `yaml:"sandbox_mode"`
}

type PolicyConfig struct {
	StrictMode  bool     `yaml:"strict_mode"`
	DeniedCaps  []string `yaml:"denied_capabilities"`
	AllowedCaps []string `yaml:"allowed_capabilities,omitempty"`
}

type VerificationConfig struct {
	Enabled     bool     `yaml:"enabled"`
	Steps       []string `yaml:"steps,omitempty"`
	FailOnWarn  bool     `yaml:"fail_on_warning"`
	MaxDuration string   `yaml:"max_duration,omitempty"`
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

func (c *Config) ActiveProviderName() string {
	if c.AI.DefaultProvider != "" {
		if _, ok := c.AI.Providers[c.AI.DefaultProvider]; ok {
			return c.AI.DefaultProvider
		}
	}
	if c.AI.FallbackProvider != "" {
		if _, ok := c.AI.Providers[c.AI.FallbackProvider]; ok {
			return c.AI.FallbackProvider
		}
	}
	if c.Models.Provider != "" {
		return c.Models.Provider
	}
	return "unknown"
}

func (c *Config) ActiveModelName() string {
	provider := c.ActiveProviderName()
	if provCfg, ok := c.AI.Providers[provider]; ok && provCfg.DefaultModel != "" {
		return provCfg.DefaultModel
	}
	if c.Models.Default != "" {
		return c.Models.Default
	}
	return "qwen2.5-coder:7b"
}

func (c *Config) Validate() error {
	provider := c.ActiveProviderName()
	if provider == "unknown" {
		return fmt.Errorf("no AI provider configured")
	}
	model := c.ActiveModelName()
	if model == "" {
		return fmt.Errorf("no model configured for provider %q", provider)
	}
	provCfg, ok := c.AI.Providers[provider]
	if !ok || provCfg.BaseURL == "" {
		return fmt.Errorf("provider %q has no base_url configured", provider)
	}
	return nil
}

func configPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".izen", "config.yml")
}

func legacyConfigPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".izen", "izen.conf.yml")
}

func Load() (*Config, error) {
	path := configPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}

		legacy := legacyConfigPath()
		if data, err = os.ReadFile(legacy); err == nil {
			var cfg Config
			if err := yaml.Unmarshal(data, &cfg); err == nil {
				if saveErr := Save(&cfg); saveErr == nil {
					_ = os.Remove(legacy)
					fmt.Fprintf(os.Stderr, "izen: migrated config from %s to %s\n", legacy, path)
				}
			}
			return &cfg, nil
		}

		return Default(), nil
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}

func Default() *Config {
	return &Config{
		AI: AIConfig{
			DefaultProvider:  "ollama",
			FallbackProvider: "openai",
			Providers: map[string]AIProviderConfig{
				"ollama": {
					BaseURL:      "http://localhost:11434/v1",
					APIKey:       "ollama",
					DefaultModel: "qwen2.5-coder:7b",
				},
			},
		},
		Models: ModelConfig{
			Default:  "qwen2.5-coder:7b",
			Provider: "ollama",
		},
		Execution: ExecutionConfig{
			Sandbox:     true,
			Confirm:     true,
			SandboxMode: "policy",
			Policy: PolicyConfig{
				StrictMode: true,
			},
			Verification: VerificationConfig{
				Enabled: true,
				Steps:   []string{"go fmt", "go vet", "go test"},
			},
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

	path := filepath.Join(dir, "config.yml")
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0644)
}

type ConfigChangeMsg struct{}

func StartConfigWatcher(ch chan<- bool) {
	path := configPath()
	var lastMod time.Time
	go func() {
		for {
			time.Sleep(2 * time.Second)
			info, err := os.Stat(path)
			if err != nil {
				continue
			}
			mod := info.ModTime()
			if mod.After(lastMod) && !lastMod.IsZero() {
				select {
				case ch <- true:
				default:
				}
			}
			lastMod = mod
		}
	}()
}
