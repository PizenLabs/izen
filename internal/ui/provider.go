package ui

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/llm"
	appruntime "github.com/PizenLabs/izen/internal/runtime"
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
		turnCost = float64(inputTok)*(3.0/1_000_000) + float64(outputTok)*(15.0/1_000_000)
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
		detail := fmt.Sprintf("missing %s", envVar)
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
	return os.Getenv(envVar) != ""
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

	// Provider switch state invalidation: clear stale target assignments that
	// do not belong to the newly active provider. Without this, an Ollama
	// assignment (local-id:7b) leaks into an OpenRouter worker context
	// and is correctly rejected by the OpenRouter regex validator, but the
	// root cause is stale authority state. Switching must re-resolve.
	if rt := m.ensureModelRuntime(); rt != nil {
		newDefault := ""
		if provCfg, ok := m.cfg.AI.Providers[name]; ok {
			newDefault = provCfg.DefaultModel
		}
		for _, tgtStr := range []string{"ask", "investigate", "plan", "build", "review"} {
			tgt := appruntime.WorkspaceTarget(tgtStr)
			ref := rt.EffectiveModel(tgt)
			if ref.ID != "" && !modelBelongsToProvider(name, ref.ID) {
				// Clear stale assignment
				rt.SeedAssignment(tgt, appruntime.ModelRef{})
				switch tgtStr {
				case "ask":
					m.cfg.Assignments.Ask = ""
				case "investigate":
					m.cfg.Assignments.Investigate = ""
				case "plan":
					m.cfg.Assignments.Plan = ""
				case "build":
					m.cfg.Assignments.Build = ""
				case "review":
					m.cfg.Assignments.Review = ""
				}
				// Re-resolve with new provider's default to keep harnesses functional
				if newDefault != "" && modelBelongsToProvider(name, newDefault) {
					rt.SeedAssignment(tgt, appruntime.ModelRef{ID: newDefault, Provider: name})
					switch tgtStr {
					case "ask":
						m.cfg.Assignments.Ask = newDefault
					case "investigate":
						m.cfg.Assignments.Investigate = newDefault
					case "plan":
						m.cfg.Assignments.Plan = newDefault
					case "build":
						m.cfg.Assignments.Build = newDefault
					case "review":
						m.cfg.Assignments.Review = newDefault
					}
				}
			}
		}
		if m.sessionModel != "" && !modelBelongsToProvider(name, m.sessionModel) {
			m.sessionModel = ""
			m.cfg.Models.SessionModel = ""
			if newDefault != "" && modelBelongsToProvider(name, newDefault) {
				m.sessionModel = newDefault
				m.cfg.Models.SessionModel = newDefault
			}
		}
		// Also clear the active target's session override if stale
		if m.cfg != nil && m.cfg.Models.SessionModel != "" && !modelBelongsToProvider(name, m.cfg.Models.SessionModel) {
			m.cfg.Models.SessionModel = ""
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

func init() {
	_ = ai.Provider(nil)
}
