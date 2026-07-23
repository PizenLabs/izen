package ui

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/ai"
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
	costStr := "$0.0000"
	var turnCost float64
	if isCloud && totalTok > 0 {
		turnCost = float64(inputTok)*(3.0/1_000_000) + float64(outputTok)*(15.0/1_000_000)
		costStr = fmt.Sprintf("$%.4f", turnCost)
	}
	m.push(roleSystem, infoStyle.Render(fmt.Sprintf("  Input Tokens      %d", inputTok)))
	m.push(roleSystem, infoStyle.Render(fmt.Sprintf("  Output Tokens     %d", outputTok)))
	m.push(roleSystem, infoStyle.Render("  Cache Read        — (not tracked)"))
	m.push(roleSystem, infoStyle.Render("  Cache Write       — (not tracked)"))
	m.push(roleSystem, infoStyle.Render(fmt.Sprintf("  Total Tokens      %d", totalTok)))
	m.push(roleSystem, infoStyle.Render(fmt.Sprintf("  Total Cost        %s", costStr)))
	if m.AccumulatedCost > 0 {
		m.push(roleSystem, infoStyle.Render(fmt.Sprintf("  Session Cost      $%.4f", m.AccumulatedCost)))
	}
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
	m.push(roleSystem, mutedStyle.Render("  Provider switching is automatic via /model."))

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

func init() {
	_ = ai.Provider(nil)
}
