package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/PizenLabs/izen/internal/prompt"
	"gopkg.in/yaml.v3"
)

// LoadDotEnv reads key=value pairs from the given .env file path (if it exists)
// and sets them via os.Setenv. Returns the number of variables loaded.
func LoadDotEnv(path string) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer func() { _ = f.Close() }()

	count := 0
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		if key == "" {
			continue
		}
		// Strip surrounding quotes if present
		if len(val) >= 2 {
			if (val[0] == '"' && val[len(val)-1] == '"') ||
				(val[0] == '\'' && val[len(val)-1] == '\'') {
				val = val[1 : len(val)-1]
			}
		}
		if os.Getenv(key) == "" {
			_ = os.Setenv(key, val)
			count++
		}
	}
	return count
}

var envVarPattern = regexp.MustCompile(`\$\{([^}]+)\}`)

// ValidProviderNames returns the set of known AI provider names.
var ValidProviderNames = map[string]bool{
	"ollama":     true,
	"anthropic":  true,
	"openai":     true,
	"openrouter": true,
	"gemini":     true,
	"groq":       true,
	"opencode":   true,
	"9router":    true,
}

// ValidateProviderName checks if name is a known provider
func ValidateProviderName(name string) bool {
	return ValidProviderNames[name]
}

type AIProviderConfig struct {
	BaseURL      string `yaml:"base_url"`
	APIKey       string `yaml:"api_key"`
	DefaultModel string `yaml:"default_model"`
}

type AIConfig struct {
	DefaultProvider  string                      `yaml:"default_provider"`
	FallbackProvider string                      `yaml:"fallback_provider"`
	MaxTokens        int                         `yaml:"max_tokens"`
	Providers        map[string]AIProviderConfig `yaml:"providers"`
}

// ActiveBindingConfig is the atomic provider/model/variant binding.
type ActiveBindingConfig struct {
	Provider string `yaml:"provider"`
	Model    string `yaml:"model"`
	Variant  string `yaml:"variant,omitempty"`
}

// BindingsConfig is the unified model binding. Active is the single source
// of truth for the runtime model. Policy maps semantic roles to overrides.
type BindingsConfig struct {
	Active ActiveBindingConfig            `yaml:"active"`
	Policy map[string]ActiveBindingConfig `yaml:"policy,omitempty"`
}

type Config struct {
	// Unified model binding (single source of truth)
	Bindings BindingsConfig `yaml:"bindings"`

	AI        AIConfig        `yaml:"ai"`
	Models    ModelConfig     `yaml:"models"`
	Execution ExecutionConfig `yaml:"execution"`
	Timeout   TimeoutConfig   `yaml:"timeout,omitempty"`
	Fallback  FallbackConfig  `yaml:"fallback"`
	Lynx      LynxConfig      `yaml:"lynx"`
	MCP       MCPConfig       `yaml:"mcp"`
	Style     string          `yaml:"style"`
	Username  string          `yaml:"username"`
}

// TimeoutConfig carries user-overridable connection timeouts. Durations
// are strings parsed with time.ParseDuration (e.g. "60s", "1m30s") so the
// YAML stays human-readable; empty or unparseable values fall back to the
// dynamic resolver defaults.
type TimeoutConfig struct {
	// TTFT overrides the dynamic Time-To-First-Token deadline for every
	// model (see llm.ResolveTTFTTimeout). Empty means profile-based
	// resolution.
	TTFT string `yaml:"ttft,omitempty" json:"ttft,omitempty"`
}

// TTFTTimeoutOverride parses Timeout.TTFT for the dynamic TTFT resolver.
// It returns 0 when unset or unparseable so the resolver falls back to the
// model/provider profile tiers.
func (c *Config) TTFTTimeoutOverride() time.Duration {
	if c == nil {
		return 0
	}
	s := strings.TrimSpace(c.Timeout.TTFT)
	if s == "" {
		return 0
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0
	}
	return d
}

type ModelConfig struct {
	Default      string                      `yaml:"default"`
	Fast         string                      `yaml:"fast"`
	Provider     string                      `yaml:"provider"`
	MaxTokens    int                         `yaml:"max_tokens"`
	SessionModel string                      `yaml:"-"` // runtime session override, never persisted
	ModeDefaults map[string]string           `yaml:"mode_defaults,omitempty"`
	Modes        map[string]ModeSpec         `yaml:"modes,omitempty"`
	Tiers        map[string]IntentTierConfig `yaml:"tiers,omitempty"`
}

type IntentTierConfig struct {
	Provider       string `yaml:"provider,omitempty"`
	Model          string `yaml:"model,omitempty"`
	ActiveOverride string `yaml:"active_override,omitempty"`
}

// ActiveStylePolicy resolves the configured output style to a prompt policy.
// Unknown or empty values fall back to the default (Balanced).
func (c *Config) ActiveStylePolicy() prompt.StylePolicy {
	p, err := prompt.ParseStylePolicy(c.Style)
	if err != nil {
		return prompt.DefaultStylePolicy()
	}
	return p
}

// ResolveTier functions removed: model resolution is now the exclusive
// authority of the stateless Policy Resolver (internal/runtime/authority).
// Legacy tier mappings are purged per Phase 1 authority migration.

// SetTierOverride sets the active_override for the given intent tier,
// persisting the model selection as the session-level override.
func (c *Config) SetTierOverride(tier, modelName string) {
	if c.Models.Tiers == nil {
		c.Models.Tiers = make(map[string]IntentTierConfig)
	}
	tc := c.Models.Tiers[tier]
	tc.ActiveOverride = modelName
	c.Models.Tiers[tier] = tc
	c.Models.SessionModel = modelName
}

// ActiveTierForFile returns the intent tier for a given file path based
// on its extension and purpose. License files, Dockerfiles, and dotfiles
// are classified as high_intent since they carry legal/operational weight.
func (c *Config) ActiveTierForFile(file string) string {
	base := strings.ToLower(strings.TrimSpace(file))
	switch {
	case base == "license" || base == "license.md" || base == "license.txt" ||
		strings.HasPrefix(base, ".env") || base == "dockerfile" ||
		strings.HasPrefix(base, "dockerfile."):
		return "high_intent"
	case strings.HasSuffix(base, ".md") || strings.HasSuffix(base, ".txt"):
		return "medium_intent"
	default:
		return "low_intent"
	}
}

type ModeSpec struct {
	Provider string `yaml:"provider" json:"provider"`
	Model    string `yaml:"model" json:"model"`
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
	Enabled   bool `yaml:"enabled"`
	LazyStart bool `yaml:"lazy_start"`
}

func (c *Config) ActiveProviderName() string {
	// Unified binding is the primary authority.
	if c.Bindings.Active.Provider != "" {
		if _, ok := c.AI.Providers[c.Bindings.Active.Provider]; ok {
			return c.Bindings.Active.Provider
		}
	}
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
	// Unified binding is the primary authority.
	if c.Bindings.Active.Model != "" {
		return c.Bindings.Active.Model
	}
	if c.Models.SessionModel != "" {
		return c.Models.SessionModel
	}
	provider := c.ActiveProviderName()
	if provCfg, ok := c.AI.Providers[provider]; ok && provCfg.DefaultModel != "" {
		return provCfg.DefaultModel
	}
	if c.Models.Default != "" {
		return c.Models.Default
	}
	// Zero fallback allowed — no hardcoded default model.
	return ""
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

// SanitizeUsername removes any @ prefix and trims whitespace so the
// username is clean for LLM display and prompt injection.
func SanitizeUsername(rawName string) string {
	cleaned := strings.TrimSpace(rawName)
	cleaned = strings.TrimPrefix(cleaned, "@")
	if cleaned == "" {
		return "Developer"
	}
	return cleaned
}

// SanitizeForSession strips @username mentions from text to prevent legacy
// identity references (e.g. @Jaky) from leaking into active session state.
// It also removes any "Handoff context injected" marker contamination.
func SanitizeForSession(text string) string {
	result := text
	atMentionRe := regexp.MustCompile(`@\w[\w.-]*`)
	result = atMentionRe.ReplaceAllString(result, "[redacted]")
	result = strings.ReplaceAll(result, "Handoff context injected.", "")
	result = strings.TrimSpace(result)
	return result
}

// wellKnownBaseURLs maps known provider names to their canonical base URLs.
// When a user's config file defines a provider without a base_url, the
// loader backfills the default so the provider is immediately usable.
var wellKnownBaseURLs = map[string]string{
	"ollama":     "http://localhost:11434/v1",
	"anthropic":  "https://api.anthropic.com/v1",
	"openai":     "https://api.openai.com/v1",
	"openrouter": "https://openrouter.ai/api/v1",
	"groq":       "https://api.groq.com/openai/v1",
}

// SetDefaults fills in missing well-known fields (BaseURL) for providers
// that appear in the config but lack them. This prevents cold-boot crashes
// when a user adds a provider block without setting base_url.
func (c *Config) SetDefaults() {
	for name, prov := range c.AI.Providers {
		if prov.BaseURL == "" {
			if defURL, ok := wellKnownBaseURLs[name]; ok {
				prov.BaseURL = defURL
				c.AI.Providers[name] = prov
			}
		}
	}
}

func ExpandEnvVar(val string) string {
	return envVarPattern.ReplaceAllStringFunc(val, func(match string) string {
		name := match[2 : len(match)-1]
		defaultVal := ""
		if idx := strings.Index(name, ":-"); idx >= 0 {
			defaultVal = name[idx+2:]
			name = name[:idx]
		}
		if env := os.Getenv(name); env != "" {
			return env
		}
		return defaultVal
	})
}

// loadEnv attempts to load .env files from the working directory and
// ~/.config/izen/.env on startup. Existing environment variables take
// precedence (dotenv values are only set when the env var is empty).
func loadEnv() {
	cwd, _ := os.Getwd()
	if cwd != "" {
		LoadDotEnv(filepath.Join(cwd, ".env"))
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		LoadDotEnv(filepath.Join(home, ".config", "izen", ".env"))
	}
}

func (c *AIConfig) ExpandEnvVars() {
	for name, prov := range c.Providers {
		prov.APIKey = ExpandEnvVar(prov.APIKey)
		prov.BaseURL = ExpandEnvVar(prov.BaseURL)
		prov.DefaultModel = ExpandEnvVar(prov.DefaultModel)
		c.Providers[name] = prov
	}
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
	loadEnv()

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
				cfg.AI.ExpandEnvVars()
				cfg.SetDefaults()
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

	cfg.AI.ExpandEnvVars()
	cfg.SetDefaults()

	return &cfg, nil
}

func Default() *Config {
	return &Config{
		AI: AIConfig{
			DefaultProvider:  "ollama",
			FallbackProvider: "openai",
			MaxTokens:        4096,
			Providers: map[string]AIProviderConfig{
				"ollama": {
					BaseURL:      "http://localhost:11434/v1",
					APIKey:       "ollama",
					DefaultModel: "qwen2.5-coder:7b",
				},
				"anthropic": {
					BaseURL:      "https://api.anthropic.com/v1",
					APIKey:       "${ANTHROPIC_API_KEY}",
					DefaultModel: "claude-sonnet-4-20250514",
				},
				"openai": {
					BaseURL:      "https://api.openai.com/v1",
					APIKey:       "${OPENAI_API_KEY}",
					DefaultModel: "gpt-4o",
				},
				"openrouter": {
					BaseURL:      "https://openrouter.ai/api/v1",
					APIKey:       "${OPENROUTER_API_KEY}",
					DefaultModel: "anthropic/claude-3.5-sonnet",
				},
				"groq": {
					BaseURL:      "https://api.groq.com/openai/v1",
					APIKey:       "${GROQ_API_KEY}",
					DefaultModel: "llama-3.3-70b-versatile",
				},
			},
		},
		Models: ModelConfig{
			Default:  "qwen2.5-coder:7b",
			Provider: "ollama", MaxTokens: 4096,
			Modes: map[string]ModeSpec{
				"ask":         {Provider: "", Model: ""},
				"plan":        {Provider: "", Model: ""},
				"build":       {Provider: "", Model: ""},
				"review":      {Provider: "", Model: ""},
				"investigate": {Provider: "", Model: ""},
			},
			Tiers: map[string]IntentTierConfig{
				"low_intent":    {Provider: "", Model: ""},
				"medium_intent": {Provider: "", Model: ""},
				"high_intent":   {Provider: "", Model: ""},
				"reasoning":     {Provider: "", Model: ""},
				"execution":     {Provider: "", Model: ""},
				"informational": {Provider: "", Model: ""},
			},
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
			Enabled:   true,
			LazyStart: true,
		},
		MCP: MCPConfig{
			Enabled: false,
		},
		Style: "balanced",
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

func (c *Config) MigrateLegacyConfig() bool {
	// Migrate from Models.Default to Bindings.Active if the active binding is empty.
	if c.Bindings.Active.Model == "" && c.Models.Default != "" {
		c.Bindings.Active.Model = c.Models.Default
		c.Bindings.Active.Provider = c.ActiveProviderName()
		return true
	}
	return false
}

// PersistActiveBinding persists the unified active model binding.
func PersistActiveBinding(provider, model, variant string) error {
	cfg := GetGlobalConfig()
	cfg.Bindings.Active = ActiveBindingConfig{
		Provider: provider,
		Model:    model,
		Variant:  variant,
	}
	return Save(cfg)
}

var globalConfig *Config

func GetGlobalConfig() *Config {
	if globalConfig != nil {
		return globalConfig
	}
	cfg, err := Load()
	if err != nil {
		cfg = Default()
	}
	cfg.MigrateLegacyConfig()
	globalConfig = cfg
	return globalConfig
}

// WellKnownBaseURL returns the canonical base URL for a known provider name,
// or the empty string when the provider has no well-known endpoint.
func WellKnownBaseURL(provider string) string {
	lower := strings.ToLower(strings.TrimSpace(provider))
	return wellKnownBaseURLs[lower]
}

// SaveProviderAPIKey persists a provider API key into the unified config
// store (~/.izen/config.yml) and immediately refreshes the in-memory global
// config so the running session sees it without a restart. The provider block
// is created on demand with its well-known base URL when absent. This is the
// ONLY persistence path for the model picker's inline API-key overlay.
func SaveProviderAPIKey(provider, apiKey string) error {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		return fmt.Errorf("provider name required")
	}
	if strings.TrimSpace(apiKey) == "" {
		return fmt.Errorf("api key required for %q", provider)
	}
	cfg := GetGlobalConfig()
	if cfg.AI.Providers == nil {
		cfg.AI.Providers = make(map[string]AIProviderConfig)
	}
	prov, ok := cfg.AI.Providers[provider]
	if !ok || prov.BaseURL == "" {
		if defURL := WellKnownBaseURL(provider); defURL != "" {
			prov.BaseURL = defURL
		}
	}
	prov.APIKey = apiKey
	cfg.AI.Providers[provider] = prov
	return Save(cfg)
}

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
