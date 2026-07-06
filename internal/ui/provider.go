package ui

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/ai"
)

var validProviders = map[string]string{
	"ollama":    "OLLAMA_BASE_URL",
	"anthropic": "ANTHROPIC_API_KEY",
	"openai":    "OPENAI_API_KEY",
	"gemini":    "GEMINI_API_KEY",
}

func (m *model) listProviders() {
	m.push(roleSystem, labelBoldStyle.Render("available providers"))

	defaultName := m.cfg.ActiveProviderName()
	currentName := ""
	if m.provider != nil {
		currentName = m.provider.Name()
	}

	for name, envVar := range validProviders {
		available := m.isProviderAvailable(name, envVar)
		status := "[✗]"
		if available {
			status = "[✓]"
		}
		marker := ""
		if name == defaultName {
			marker = " (default)"
		}
		if name == currentName {
			marker = " (active)"
		}
		envStatus := "env: " + envVar
		if available {
			envStatus = "env: set"
		}
		m.push(roleSystem, infoStyle.Render(fmt.Sprintf("  %s %s — %s%s", status, name, envStatus, marker)))
	}
	m.push(roleSystem, infoStyle.Render("  usage: /provider <name>"))
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
		m.push(roleSystem, infoStyle.Render("usage: /provider <ollama|anthropic|openai|gemini>"))
		return nil
	}

	envVar, ok := validProviders[name]
	if !ok {
		m.push(roleError, fmt.Sprintf("unknown provider: %s", name))
		m.push(roleSystem, infoStyle.Render("valid providers: ollama, anthropic, openai, gemini"))
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
	return "ollama"
}

func init() {
	_ = ai.Provider(nil)
}
