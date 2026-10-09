package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/providers"
	"github.com/PizenLabs/izen/internal/runtime/executor"
)

// runRuntimeUsage describes the `izen run` subcommand.
const runRuntimeUsage = `Usage: izen run [flags] "<prompt>"

Execute one prompt through the AUTHORITATIVE Izen runtime — the same bounded
autonomy Driver that ` + "`izen prompt`" + ` / ` + "`izen orchestrate`" + ` drive over the
RuntimeExecutor execution plane. The runtime owns admission, the provider seam,
the proposal/approval boundary, mutation, verification, recovery, cancellation
and terminal truth; this command only plays the human at the approval boundary.

The proposal diff is rendered to stdout and an interactive [y/N] approval prompt
blocks until you decide. "y" applies the proposal atomically, "i" inspects
without applying, and anything else (including Enter) rejects it.

Every prompt enters the runtime through the same admission front-end as the TUI:
the resolved capability set, target scope and mutation authorization are the
runtime's, never the command's. Explicit ` + "`-target`" + ` files are bound through the
runtime's authoritative target seam and never bypass resolution.

Flags:
  -dir <path>      Workspace root (default ".")
  -target <path>   Target file to analyze/modify (repeatable)

Examples:
  izen run "redesign the portfolio website"
  izen run -target main.go "add a health check endpoint"
  izen run "explain the routing layer"
`

// runRuntimeCommand implements `izen run`: it parses its arguments and drives
// the prompt through the shared authoritative CLI lifecycle
// (runAuthoritativeCommand → compose.Wire → autonomy Driver → RuntimeExecutor).
// It builds no private pipeline, planner or execution loop.
func runRuntimeCommand(args []string) error {
	dir, targets, prompt, help, err := parseRunArgs(args)
	if err != nil {
		return err
	}
	if help {
		fmt.Print(runRuntimeUsage)
		return nil
	}
	return runAuthoritativeCommand(dir, prompt, "$run", "run", targets)
}

// parseRunArgs parses the `izen run` argument grammar: `-dir <path>`, repeatable
// `-target <path>`, exactly one prompt, and `-h/--help`. It performs no I/O and
// no execution, so it is directly testable.
func parseRunArgs(args []string) (dir string, targets []string, prompt string, help bool, err error) {
	dir = "."
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-h", "--help":
			return "", nil, "", true, nil
		case "-dir":
			if i+1 >= len(args) {
				return "", nil, "", false, errors.New("izen run: -dir requires a path")
			}
			i++
			dir = args[i]
		case "-target":
			if i+1 >= len(args) {
				return "", nil, "", false, errors.New("izen run: -target requires a path")
			}
			i++
			targets = append(targets, args[i])
		default:
			if strings.HasPrefix(args[i], "-") {
				return "", nil, "", false, fmt.Errorf("izen run: unknown flag %q", args[i])
			}
			if prompt != "" {
				return "", nil, "", false, errors.New("izen run: exactly one prompt argument is required")
			}
			prompt = args[i]
		}
	}
	if prompt == "" {
		return "", nil, "", false, errors.New("izen run: a prompt argument is required")
	}
	return dir, targets, prompt, false, nil
}

// validateProviderModelBinding verifies provider/model tuple compatibility
// synchronously (no network). It mirrors the fast-path gate's fail-fast
// boundary so misconfiguration aborts before any provider invocation.
func validateProviderModelBinding(provider, model string) error {
	provider = strings.TrimSpace(provider)
	model = strings.TrimSpace(model)
	if provider == "" || model == "" {
		return nil
	}
	hasSlash := false
	for i, c := range model {
		if c == '/' {
			if i > 0 && len(model) > i+1 {
				hasSlash = true
			}
			break
		}
	}
	switch provider {
	case "ollama":
		if hasSlash {
			return fmt.Errorf("%w: model %q does not belong to provider %q", executor.ErrInvalidProviderConfiguration, model, provider)
		}
	case "openrouter":
		if !hasSlash {
			return fmt.Errorf("%w: model %q does not belong to provider %q", executor.ErrInvalidProviderConfiguration, model, provider)
		}
	}
	return nil
}

// buildActiveProvider constructs the ai.Provider for the configured active
// provider and returns it together with the effective model name. The API key
// resolves with strict precedence: ~/.izen/config.yml wins over the shell
// environment variable; an empty resolution surfaces the missing-key prompt.
func buildActiveProvider(cfg *config.Config) (ai.Provider, string, error) {
	name := cfg.ActiveProviderName()
	provCfg, ok := cfg.AI.Providers[name]
	if !ok {
		provCfg = config.AIProviderConfig{BaseURL: config.WellKnownBaseURL(name)}
	}
	apiKey := cfg.ResolveAPIKey(name)
	if apiKey == "" && strings.TrimSpace(provCfg.BaseURL) == "" {
		return nil, "", fmt.Errorf(
			"izen run: no AI provider configured (provider %q). Set one via 'izen auth login' or environment variables",
			name,
		)
	}
	if apiKey == "" {
		return nil, "", fmt.Errorf(
			"izen run: no API key for provider %q. Save one via 'izen auth login' or set %s",
			name, config.EnvVarForProvider(name),
		)
	}
	model := cfg.ActiveModelName()
	// Fail-fast provider boundary: a mismatched provider/model tuple (e.g.
	// provider "ollama" with model "cohere/north-mini-code:free") aborts here
	// with ErrInvalidProviderConfiguration before any execution loop or
	// preflight timeout can engage. Zero provider calls are made on mismatch.
	if err := validateProviderModelBinding(name, model); err != nil {
		return nil, "", err
	}
	switch name {
	case "ollama":
		return providers.NewOllamaProvider(provCfg.BaseURL, apiKey, model), model, nil
	case "openrouter":
		return providers.NewOpenRouterProvider(apiKey, model, provCfg.BaseURL), model, nil
	case "openai":
		return providers.NewOpenAIProvider(apiKey, model), model, nil
	case "anthropic":
		return providers.NewClaudeProvider(apiKey, model), model, nil
	case "gemini":
		return providers.NewGeminiProvider(apiKey, model), model, nil
	case "groq":
		return providers.NewGroqProvider(apiKey, model, provCfg.BaseURL), model, nil
	case "opencode":
		return providers.NewOpenCodeProvider(apiKey, model, provCfg.BaseURL), model, nil
	case "9router":
		return providers.NewNineRouterProvider(apiKey, model, provCfg.BaseURL), model, nil
	default:
		return nil, "", fmt.Errorf("izen run: unsupported provider %q", name)
	}
}
