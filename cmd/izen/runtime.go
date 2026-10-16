package main

import (
	"fmt"
	"strings"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/providers"
	"github.com/PizenLabs/izen/internal/runtime/executor"
)

// runRuntimeUsage describes the `izen run` subcommand.
const runRuntimeUsage = `Usage: izen run [flags] "<objective>"

Execute one objective through the AUTHORITATIVE Izen runtime — the same bounded
autonomy Driver that the TUI drives over the RuntimeExecutor execution plane.
The runtime owns admission, the provider seam, the proposal/approval boundary,
mutation, verification, recovery, cancellation and terminal truth; this command
only plays the human at the approval boundary (or auto-approves/denies when a
non-interactive -approve policy is given).

Flags:
  -dir <path>          Workspace root (default ".")
  -target <path>       Target file to analyze/modify (repeatable)
  -provider <name>     Provider override (ollama|openrouter|openai|anthropic|gemini|groq|opencode|9router)
  -model <id>          Model override (must belong to the selected provider)
  -base-url <url>      Provider endpoint override (self-hosted / automation)
  -mode <name>         Execution mode: ask|investigate|plan|build|review (default build)
  -approve <policy>    Approval policy: ask|always|never (default ask)
  -timeout <dur>       Wall-clock bound, e.g. 90s or 5m (default 10m)
  -json                Emit one machine-readable JSON report on stdout
  -quiet               Suppress operator diagnostics on stderr

Exit codes:
  0 success   1 failed   2 usage/config   3 provider
  4 human-required   5 unsubstantiated   6 timeout   7 denied

Examples:
  izen run "redesign the portfolio website"
  izen run -target main.go "add a health check endpoint"
  izen run -json -approve never "delete the migration files"
  izen run -provider opencode -model z-ai/glm-4 "explain the routing layer"
`

// runRuntimeCommand implements `izen run`: it parses the shared execution
// grammar and drives the objective through the one authoritative CLI lifecycle.
func runRuntimeCommand(args []string) int {
	return runExecRequest("run", args, runRuntimeUsage)
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

// buildProvider constructs the ai.Provider for an explicit (provider, model,
// baseURL) selection. The API key resolves with strict precedence: the
// configured key in ~/.izen/config.yml wins over the provider's environment
// variable. An explicit baseURL override wins over the configured endpoint so
// automation can target a self-hosted or benchmark endpoint, and it is never
// silently dropped.
func buildProvider(cfg *config.Config, providerName, model, baseURL string) (ai.Provider, string, error) {
	name := strings.TrimSpace(providerName)
	if name == "" {
		name = cfg.ActiveProviderName()
	}
	provCfg, ok := cfg.AI.Providers[name]
	if !ok {
		provCfg = config.AIProviderConfig{BaseURL: config.WellKnownBaseURL(name)}
	}
	effectiveBase := strings.TrimSpace(baseURL)
	if effectiveBase == "" {
		effectiveBase = strings.TrimSpace(provCfg.BaseURL)
	}
	if effectiveBase == "" {
		effectiveBase = config.WellKnownBaseURL(name)
	}
	apiKey := cfg.ResolveAPIKey(name)
	if apiKey == "" && effectiveBase == "" {
		return nil, "", fmt.Errorf(
			"no AI provider configured (provider %q). Set one via 'izen auth login' or environment variables",
			name,
		)
	}
	if apiKey == "" && name != "ollama" {
		return nil, "", fmt.Errorf(
			"no API key for provider %q. Save one via 'izen auth login' or set %s",
			name, config.EnvVarForProvider(name),
		)
	}
	if model == "" {
		model = cfg.ActiveModelName()
	}
	if model == "" {
		return nil, "", fmt.Errorf("no model selected for provider %q", name)
	}
	// Fail-fast provider boundary: a mismatched provider/model tuple aborts
	// here before any execution loop or preflight timeout can engage.
	if err := validateProviderModelBinding(name, model); err != nil {
		return nil, "", err
	}
	switch name {
	case "ollama":
		return providers.NewOllamaProvider(effectiveBase, apiKey, model), model, nil
	case "openrouter":
		return providers.NewOpenRouterProvider(apiKey, model, effectiveBase), model, nil
	case "openai":
		return providers.NewOpenAIProvider(apiKey, model), model, nil
	case "anthropic":
		return providers.NewClaudeProvider(apiKey, model), model, nil
	case "gemini":
		return providers.NewGeminiProvider(apiKey, model), model, nil
	case "groq":
		return providers.NewGroqProvider(apiKey, model, effectiveBase), model, nil
	case "opencode":
		return providers.NewOpenCodeProvider(apiKey, model, effectiveBase), model, nil
	case "9router":
		return providers.NewNineRouterProvider(apiKey, model, effectiveBase), model, nil
	default:
		return nil, "", fmt.Errorf("unsupported provider %q", name)
	}
}
