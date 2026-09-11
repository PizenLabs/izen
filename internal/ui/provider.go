package ui

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/llm"
	"github.com/PizenLabs/izen/internal/providers"
	"github.com/PizenLabs/izen/internal/runtime/authority"
)

var validProviders = map[string]string{
	"ollama":     "OLLAMA_BASE_URL",
	"anthropic":  "ANTHROPIC_API_KEY",
	"openai":     "OPENAI_API_KEY",
	"gemini":     "GEMINI_API_KEY",
	"openrouter": "OPENROUTER_API_KEY",
	"groq":       "GROQ_API_KEY",
}

func (m *model) runUsageCmd() tea.Cmd {
	// ── Current Context ─────────────────────────────────────────────
	m.push(roleSystem, labelBoldStyle.Render(" usage inspector"))
	m.push(roleSystem, "")

	providerName := m.cfg.ActiveProviderName()
	modelName := m.cfg.ActiveModelName()
	maxTokens := m.cfg.AI.MaxTokens
	if maxTokens <= 0 {
		maxTokens = m.cfg.Models.MaxTokens
	}
	m.push(roleSystem, infoStyle.Render(fmt.Sprintf("  Provider     %s", providerName)))
	m.push(roleSystem, infoStyle.Render(fmt.Sprintf("  Model        %s", modelName)))
	m.push(roleSystem, infoStyle.Render(fmt.Sprintf("  Max Tokens   %d", maxTokens)))
	m.push(roleSystem, "")

	// ── Last Request Breakdown ──────────────────────────────────────
	m.push(roleSystem, labelBoldStyle.Render(" last request"))
	inputTok := m.InputTokens
	outputTok := m.OutputTokens
	totalTok := m.TotalTokens
	if totalTok == 0 {
		totalTok = inputTok + outputTok
	}
	isCloud := providerName != "ollama"
	turnCost := 0.0
	if isCloud && totalTok > 0 {
		inPerM, outPerM := m.lookupStreamPricing(modelName)
		turnCost = float64(inputTok)*(inPerM/1_000_000) + float64(outputTok)*(outPerM/1_000_000)
	}
	turnCost = llm.EnforceFreeModelOverride(modelName, turnCost)
	m.push(roleSystem, infoStyle.Render(fmt.Sprintf("  Input Tokens      %d", inputTok)))
	m.push(roleSystem, infoStyle.Render(fmt.Sprintf("  Output Tokens     %d", outputTok)))
	m.push(roleSystem, infoStyle.Render("  Cache Read        — (not tracked)"))
	m.push(roleSystem, infoStyle.Render("  Cache Write       — (not tracked)"))
	m.push(roleSystem, infoStyle.Render(fmt.Sprintf("  Total Tokens      %d", totalTok)))
	m.push(roleSystem, infoStyle.Render(fmt.Sprintf("  Total Cost        %s", llm.FormatCost(turnCost))))
	sessionCost := llm.EnforceFreeModelOverride(modelName, m.AccumulatedCost)
	m.push(roleSystem, infoStyle.Render(fmt.Sprintf("  Session Cost      %s", llm.FormatCost(sessionCost))))
	m.push(roleSystem, "")

	// ── Configured Providers Status ─────────────────────────────────
	m.push(roleSystem, labelBoldStyle.Render(" provider status"))
	for name, envVar := range validProviders {
		available := m.isProviderAvailable(name, envVar)
		status := "[×]"
		detail := fmt.Sprintf("missing %s (or config providers.%s.api_key)", envVar, name)
		if available {
			status = "[✓]"
			detail = "configured"
		}
		marker := ""
		if name == providerName {
			marker = " (active)"
		}
		m.push(roleSystem, infoStyle.Render(fmt.Sprintf("  %s %s — %s%s", status, name, detail, marker)))
	}
	m.push(roleSystem, "")
	m.push(roleSystem, mutedStyle.Render("  Provider switching is automatic via /models."))

	m.refreshViewportContent()
	m.Viewport.GotoBottom()
	return nil
}

func (m *model) isProviderAvailable(name, envVar string) bool {
	if name == "ollama" {
		_, ok := m.cfg.AI.Providers["ollama"]
		return ok
	}
	// Strict precedence: an explicit key saved in ~/.izen/config.yml counts
	// as configured even when the shell environment variable is unset, and
	// it wins when both are set.
	if m.cfg != nil {
		if p, ok := m.cfg.AI.Providers[name]; ok {
			if strings.TrimSpace(p.APIKey) != "" {
				return true
			}
		}
	}
	if envVar == "" {
		envVar = config.EnvVarForProvider(name)
	}
	return strings.TrimSpace(os.Getenv(envVar)) != ""
}

func (m *model) switchProvider(name string) tea.Cmd {
	if name == "" {
		m.push(roleSystem, infoStyle.Render("usage: /provider <name>"))
		return nil
	}

	envVar, ok := validProviders[name]
	if !ok {
		m.push(roleError, fmt.Sprintf("unknown provider: %s", name))
		m.push(roleSystem, infoStyle.Render("valid providers: ollama, anthropic, openai, gemini, openrouter, groq"))
		return nil
	}

	if !m.isProviderAvailable(name, envVar) {
		m.push(roleError, fmt.Sprintf("[✗] ROUTING ERROR: Cannot switch to %s. %s is unset.", name, envVar))
		return nil
	}

	provider, ok := m.mgr.Get(name)
	if !ok {
		m.push(roleError, fmt.Sprintf("[✗] ROUTING ERROR: Provider %s is not registered.", name))
		return nil
	}

	oldName := "none"
	if m.provider != nil {
		oldName = m.provider.Name()
	}

	m.provider = provider
	m.cfg.AI.DefaultProvider = name

	if m.planEngine != nil {
		m.planEngine.SetProvider(m.provider.Execute)
		m.planEngine.SetStreamProvider(m.provider.ExecuteStream)
	}

	// Re-bind the RuntimeExecutor to the switched provider. The executor
	// resolves the model that travels with ITS bound provider at invocation
	// time; without this re-bind it would keep the construction-time provider
	// (e.g. Ollama) while reading the new provider's model (e.g. an OpenRouter
	// model) — the exact provider/model mismatch that must never reach the
	// network. Provider identity and model identity travel together only when
	// the runtime's provider tracks the switch.
	if m.executor != nil {
		m.executor.SetProvider(provider)
	}

	// Re-pin the layered pipeline router's intent tiers to the new active
	// provider so mode commands never route a stale local model into a cloud
	// request (OpenRouter rejects e.g. local-id:7b with HTTP 400).
	m.syncPipelineTiers()

	// Provider switch state invalidation: clear the active binding if it does
	// not belong to the newly active provider. Without this, an Ollama model
	// leaks into an OpenRouter context and is rejected by the validator.
	if auth := m.modelAuthority; auth != nil {
		binding := auth.ActiveBinding()
		if binding.ModelID != "" && !modelBelongsToProvider(name, string(binding.ModelID)) {
			// Clear stale binding and re-seed with new provider's default.
			newDefault := ""
			if provCfg, ok := m.cfg.AI.Providers[name]; ok {
				newDefault = provCfg.DefaultModel
			}
			if newDefault != "" && modelBelongsToProvider(name, newDefault) {
				auth.Activate(authority.ModelBinding{
					ProviderID: authority.ProviderID(name),
					ModelID:    authority.ModelID(newDefault),
				})
				_ = config.PersistActiveBinding(name, newDefault, "")
			} else {
				auth.Activate(authority.ModelBinding{})
			}
		}
	}

	m.push(roleSystem, fmt.Sprintf("[✓] Provider switched: %s → %s", oldName, name))

	return func() tea.Msg {
		return providerSwitchMsg{name: name}
	}
}

func ValidateProviderEnvVars() []string {
	var missing []string
	for name, envVar := range validProviders {
		if name == "ollama" {
			continue
		}
		if os.Getenv(envVar) == "" {
			missing = append(missing, envVar)
		}
	}
	return missing
}

func GetActiveProviderFromEnv() string {
	if os.Getenv("ANTHROPIC_API_KEY") != "" {
		return "anthropic"
	}
	if os.Getenv("OPENAI_API_KEY") != "" {
		return "openai"
	}
	if os.Getenv("GEMINI_API_KEY") != "" {
		return "gemini"
	}
	if os.Getenv("OPENROUTER_API_KEY") != "" {
		return "openrouter"
	}
	if os.Getenv("GROQ_API_KEY") != "" {
		return "groq"
	}
	return "ollama"
}

var openRouterModelIDRe = regexp.MustCompile(`^[^/\s]+/[^/\s]+$`)

func modelBelongsToProvider(provider, model string) bool {
	model = strings.TrimSpace(model)
	if model == "" {
		return false
	}
	isOpenRouterStyle := openRouterModelIDRe.MatchString(model)
	switch provider {
	case "openrouter":
		return isOpenRouterStyle
	case "ollama":
		return !isOpenRouterStyle
	default:
		return true
	}
}

// buildProviderInstance constructs a live ai.Provider for name bound to the
// given key/baseURL/model. It mirrors the composition-root registration
// (runtime/compose registerProviders) so a hot-reloaded key produces the
// identical client the next prompt submission will use. Unknown providers
// yield nil (never a fabricated client).
func buildProviderInstance(name, apiKey, baseURL, model string) ai.Provider {
	switch name {
	case "ollama":
		return providers.NewOllamaProvider(baseURL, apiKey, model)
	case "openrouter":
		return providers.NewOpenRouterProvider(apiKey, model, baseURL)
	case "openai":
		return providers.NewOpenAIProvider(apiKey, model)
	case "anthropic":
		return providers.NewClaudeProvider(apiKey, model)
	case "gemini":
		return providers.NewGeminiProvider(apiKey, model)
	case "groq":
		return providers.NewGroqProvider(apiKey, model, baseURL)
	case "opencode":
		return providers.NewOpenCodeProvider(apiKey, model, baseURL)
	case "9router":
		return providers.NewNineRouterProvider(apiKey, model, baseURL)
	default:
		return nil
	}
}

// hotReloadProviderKey re-initializes the live runtime client for provider
// immediately after its API key is saved: the rebuilt instance (carrying the
// new bearer token) is re-registered on the provider manager, and when the
// provider is the session-active one, m.provider plus the plan/stream
// engines and the runtime executor are re-bound to it. Subsequent prompt
// submissions in the same session therefore use the newly saved key with no
// restart. All nil-guard branches are no-ops for test harnesses.
func (m *model) hotReloadProviderKey(provider, apiKey string) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	apiKey = strings.TrimSpace(apiKey)
	if provider == "" || apiKey == "" {
		return
	}
	baseURL := ""
	defModel := ""
	if m.cfg != nil {
		if p, ok := m.cfg.AI.Providers[provider]; ok {
			baseURL = p.BaseURL
			defModel = p.DefaultModel
		}
	}
	if strings.TrimSpace(baseURL) == "" {
		baseURL = config.WellKnownBaseURL(provider)
	}
	inst := buildProviderInstance(provider, apiKey, baseURL, defModel)
	if inst == nil {
		return
	}
	if m.mgr != nil {
		m.mgr.Register(provider, inst)
		if got, ok := m.mgr.Get(provider); ok {
			inst = got
		}
	}
	active := ""
	if m.provider != nil {
		active = m.provider.Name()
	} else if m.cfg != nil {
		active = m.cfg.ActiveProviderName()
	}
	if active != "" && active != provider {
		return
	}
	m.provider = inst
	if m.planEngine != nil {
		m.planEngine.SetProvider(m.provider.Execute)
		m.planEngine.SetStreamProvider(m.provider.ExecuteStream)
	}
	if m.executor != nil {
		m.executor.SetProvider(inst)
	}
}

func init() {
	_ = ai.Provider(nil)
}
