package ui

import (
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/git"
	"github.com/PizenLabs/izen/internal/state"
)

func (m *model) handleInitKeyMsg(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.initStage {
	case initGitCheck:
		return m.handleInitGitCheck(msg)
	case initConfirm:
		return m.handleInitConfirm(msg)
	case initIdentity:
		return m.handleInitIdentity(msg)
	case initProviderSelect:
		return m.handleInitProviderSelect(msg)
	}
	return m, nil
}

func (m *model) handleInitGitCheck(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Defensive blur: prevent any focused textinput (m.ti) from swallowing
	// key events during the git check stage.
	m.ti.Blur()

	switch msg.String() {
	case "y", "Y", "enter":
		m.initGitInitDone = false
		m.initGitInitErr = ""
		return m, m.runGitInit()
	case "n", "N", "esc":
		m.initGitInitDone = true
		m.advancePastGitCheck()
		return m, nil
	}
	return m, nil
}

func (m *model) advancePastGitCheck() {
	m.ti.Blur()
	m.initStage = initIdentity
	m.initIdentityInput = textinput.New()
	m.initIdentityInput.Prompt = ""
	m.initIdentityInput.CharLimit = 64
	m.initIdentityInput.Placeholder = "username"
	if m.initPrefillUsername != "" {
		m.userName = m.initPrefillUsername
	}
	m.initIdentityInput.SetValue(m.userName)
	m.initIdentityInput.Focus()
}

func (m *model) runGitInit() tea.Cmd {
	return func() tea.Msg {
		err := git.InitRepo(m.workspaceRoot)
		return gitInitResultMsg{err: err}
	}
}

func (m *model) handleInitConfirm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.ti.Blur()

	switch msg.String() {
	case "y", "Y", "enter":
		m.initConfirmDone = true
		m.initStage = initIdentity
		m.initIdentityInput = textinput.New()
		m.initIdentityInput.Prompt = ""
		m.initIdentityInput.CharLimit = 64
		m.initIdentityInput.Placeholder = "username"
		// Pre-fill from the global profile when available so the user can
		// just press Enter to confirm.
		if m.initPrefillUsername != "" {
			m.userName = m.initPrefillUsername
		}
		m.initIdentityInput.SetValue(m.userName)
		m.initIdentityInput.Focus()
		return m, nil
	case "n", "N", "esc":
		m.initStage = initComplete
		return m, m.ti.Focus()
	}
	return m, nil
}

func (m *model) handleInitIdentity(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.ti.Blur()

	switch msg.Type {
	case tea.KeyEnter:
		val := config.SanitizeUsername(m.initIdentityInput.Value())
		if val != "" {
			m.userName = val
		}
		m.saveInitState()
		m.initStage = initProviderSelect
		m.initProviderIdx = 0
		m.initProviderItems = m.buildProviderList()
		// Pre-select the provider from the global profile when available.
		if m.initPrefillProvider != "" {
			for i, name := range m.initProviderItems {
				if name == m.initPrefillProvider {
					m.initProviderIdx = i
					break
				}
			}
		}
		m.initProviderFilter = ""
		return m, nil
	case tea.KeyEscape:
		m.initStage = initComplete
		return m, m.ti.Focus()
	default:
		var cmd tea.Cmd
		m.initIdentityInput, cmd = m.initIdentityInput.Update(msg)
		return m, cmd
	}
}

func (m *model) handleInitProviderSelect(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.ti.Blur()

	switch msg.Type {
	case tea.KeyEnter:
		if m.initProviderIdx >= 0 && m.initProviderIdx < len(m.filteredProviders()) {
			selected := m.filteredProviders()[m.initProviderIdx]
			m.initStage = initComplete
			m.initProviderItems = nil
			m.saveInitState()
			return m, tea.Batch(m.switchProvider(selected), m.ti.Focus(), buildGraphCmd(m.graphEng))
		}
		return m, nil
	case tea.KeyUp:
		items := m.filteredProviders()
		if m.initProviderIdx > 0 {
			m.initProviderIdx--
		} else {
			m.initProviderIdx = len(items) - 1
		}
		return m, nil
	case tea.KeyDown:
		items := m.filteredProviders()
		if m.initProviderIdx < len(items)-1 {
			m.initProviderIdx++
		} else {
			m.initProviderIdx = 0
		}
		return m, nil
	case tea.KeyEscape:
		m.initStage = initComplete
		return m, m.ti.Focus()
	case tea.KeyBackspace, tea.KeyDelete:
		if len(m.initProviderFilter) > 0 {
			_, size := utf8.DecodeLastRuneInString(m.initProviderFilter)
			m.initProviderFilter = m.initProviderFilter[:len(m.initProviderFilter)-size]
			m.initProviderIdx = 0
		}
		return m, nil
	default:
		if msg.Type == tea.KeyRunes || msg.Type == tea.KeySpace {
			m.initProviderFilter += msg.String()
			m.initProviderIdx = 0
		}
		return m, nil
	}
}

func (m *model) filteredProviders() []string {
	if m.initProviderFilter == "" {
		return m.initProviderItems
	}
	lower := strings.ToLower(m.initProviderFilter)
	var filtered []string
	for _, item := range m.initProviderItems {
		if strings.Contains(strings.ToLower(item), lower) {
			filtered = append(filtered, item)
		}
	}
	if len(filtered) == 0 {
		return m.initProviderItems
	}
	return filtered
}

func (m *model) buildProviderList() []string {
	var names []string
	for name := range m.cfg.AI.Providers {
		names = append(names, name)
	}
	// Ensure common providers are always listed even without config entries
	known := map[string]bool{"ollama": true, "anthropic": true, "openai": true, "gemini": true}
	for name := range known {
		if !mapContains(names, name) {
			names = append(names, name)
		}
	}
	// Deduplicate
	seen := make(map[string]bool)
	var unique []string
	for _, n := range names {
		if !seen[n] {
			seen[n] = true
			unique = append(unique, n)
		}
	}
	return unique
}

func mapContains(slice []string, target string) bool {
	for _, s := range slice {
		if s == target {
			return true
		}
	}
	return false
}

func (m *model) getActiveProviderName() string {
	return m.cfg.ActiveProviderName()
}

// saveInitState persists the identity and local workspace state when the
// TUI onboarding flow completes, preventing stale init loops on restart.
func (m *model) saveInitState() {
	root := m.workspaceRoot
	if root == "" {
		root, _ = os.Getwd()
	}
	_ = state.InitLocalState(root)
	_ = config.SaveLocalConfig(root, &config.LocalConfig{Username: m.userName})
	// Write a minimal session.json to anchor HasLocalState
	sessPath := filepath.Join(root, ".izen", "session.json")
	if _, err := os.Stat(sessPath); os.IsNotExist(err) {
		_ = os.WriteFile(sessPath, []byte("{}"), 0644)
	}
}
