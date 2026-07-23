package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

func localProjectConfigPath(root string) string {
	return filepath.Join(root, ".izen", "config.yaml")
}

func DetectOrInitProjectConfig(root string) (*LocalConfig, error) {
	cfgPath := localProjectConfigPath(root)
	if _, err := os.Stat(cfgPath); err == nil {
		return LoadLocalConfig(root)
	}

	fmt.Println("\n━━━ IZEN Project Setup ━━━")
	fmt.Println("No .izen/config.yaml found. Let's set up your project.")

	reader := bufio.NewReader(os.Stdin)

	providers := selectProviders(reader)
	apiKeys := collectAPIKeys(reader, providers)

	projectCfg := buildProjectConfig(providers, apiKeys)

	dir := filepath.Dir(cfgPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", dir, err)
	}

	data, err := yaml.Marshal(projectCfg)
	if err != nil {
		return nil, fmt.Errorf("marshal project config: %w", err)
	}

	if err := os.WriteFile(cfgPath, data, 0644); err != nil {
		return nil, fmt.Errorf("write %s: %w", cfgPath, err)
	}

	fmt.Printf("\n✓ Project config written to %s\n\n", cfgPath)

	return &LocalConfig{}, nil
}

type localProjectYAML struct {
	AI struct {
		DefaultProvider string                      `yaml:"default_provider"`
		Providers       map[string]AIProviderConfig `yaml:"providers"`
	} `yaml:"ai"`
	Models struct {
		Default string `yaml:"default"`
	} `yaml:"models"`
}

func buildProjectConfig(providers []string, apiKeys map[string]string) *localProjectYAML {
	providerDefaults := map[string]AIProviderConfig{
		"ollama": {
			BaseURL:      "http://localhost:11434/v1",
			APIKey:       "ollama",
			DefaultModel: "qwen2.5-coder:7b",
		},
		"openrouter": {
			BaseURL:      "https://openrouter.ai/api/v1",
			APIKey:       "${OPENROUTER_API_KEY}",
			DefaultModel: "anthropic/claude-3.5-sonnet",
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
		"groq": {
			BaseURL:      "https://api.groq.com/openai/v1",
			APIKey:       "${GROQ_API_KEY}",
			DefaultModel: "llama-3.3-70b-versatile",
		},
	}

	var cfg localProjectYAML
	if len(providers) > 0 {
		cfg.AI.DefaultProvider = providers[0]
	}
	cfg.AI.Providers = make(map[string]AIProviderConfig)

	for _, name := range providers {
		p := providerDefaults[name]
		if key, ok := apiKeys[name]; ok && key != "" {
			p.APIKey = key
		}
		cfg.AI.Providers[name] = p
	}

	if p, ok := cfg.AI.Providers[cfg.AI.DefaultProvider]; ok {
		cfg.Models.Default = p.DefaultModel
	}

	return &cfg
}

func selectProviders(reader *bufio.Reader) []string {
	fmt.Println("\nSelect AI provider(s):")
	fmt.Println("  1) Ollama (local, default)")
	fmt.Println("  2) OpenRouter")
	fmt.Println("  3) Anthropic")
	fmt.Println("  4) OpenAI")
	fmt.Println("  5) Groq")
	fmt.Print("Enter numbers (comma-separated, e.g. 1,3): ")

	input, _ := reader.ReadString('\n')
	input = strings.TrimSpace(input)

	available := map[string]string{
		"1": "ollama",
		"2": "openrouter",
		"3": "anthropic",
		"4": "openai",
		"5": "groq",
	}

	seen := make(map[string]bool)
	var selected []string
	for _, part := range strings.Split(input, ",") {
		part = strings.TrimSpace(part)
		if name, ok := available[part]; ok && !seen[name] {
			seen[name] = true
			selected = append(selected, name)
		}
	}

	if len(selected) == 0 {
		selected = []string{"ollama"}
	}

	return selected
}

func collectAPIKeys(reader *bufio.Reader, providers []string) map[string]string {
	keys := make(map[string]string)

	for _, name := range providers {
		if name == "ollama" {
			continue
		}

		envVar := apiKeyEnvVar(name)
		existing := os.Getenv(envVar)
		if existing != "" {
			fmt.Printf("  %s: using %s from environment\n", name, envVar)
			keys[name] = "${" + envVar + "}"
			continue
		}

		fmt.Printf("  %s API key (or press Enter to use ${%s}): ", name, envVar)
		input, _ := reader.ReadString('\n')
		input = strings.TrimSpace(input)

		if input != "" {
			keys[name] = input
		} else {
			keys[name] = "${" + envVar + "}"
		}
	}

	return keys
}

func apiKeyEnvVar(provider string) string {
	switch provider {
	case "ollama":
		return ""
	case "openrouter":
		return "OPENROUTER_API_KEY"
	case "anthropic":
		return "ANTHROPIC_API_KEY"
	case "openai":
		return "OPENAI_API_KEY"
	case "groq":
		return "GROQ_API_KEY"
	default:
		return strings.ToUpper(provider) + "_API_KEY"
	}
}
