package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/llm"
)

type modelPickerState int

const (
	mpLoading modelPickerState = iota
	mpReady
	mpErr
)

type ModelPickerModal struct {
	ti       textinput.Model
	state    modelPickerState
	models   []llm.ModelInfo
	filtered []llm.ModelInfo
	cursor   int
	loading  bool
	errMsg   string
	width    int
	height   int
	registry *llm.ModelRegistry
}

type modelPickerLoadedMsg struct {
	models []llm.ModelInfo
	err    error
}

type modelPickerRefreshMsg struct {
	models []llm.ModelInfo
	err    error
}

func NewModelPickerModal() *ModelPickerModal {
	ti := textinput.New()
	ti.Prompt = "▸ "
	ti.Placeholder = "type to filter models..."
	ti.CharLimit = 64
	ti.Width = 40
	ti.Focus()

	return &ModelPickerModal{
		ti:       ti,
		state:    mpLoading,
		registry: llm.NewModelRegistry(),
	}
}

func (mp *ModelPickerModal) LoadModels(providers map[string]string) tea.Cmd {
	mp.loading = true
	mp.state = mpLoading
	mp.models = nil
	mp.filtered = nil

	return func() tea.Msg {
		models, err := mp.registry.GetModels(providers)
		if err != nil && models == nil {
			return modelPickerLoadedMsg{err: err}
		}
		return modelPickerLoadedMsg{models: models}
	}
}

func (mp *ModelPickerModal) RefreshModels(providers map[string]string) tea.Cmd {
	return func() tea.Msg {
		mp.registry.InvalidateCache()
		models, err := mp.registry.Refresh(providers)
		if err != nil && models == nil {
			return modelPickerRefreshMsg{err: err}
		}
		return modelPickerRefreshMsg{models: models}
	}
}

type modelSelectedMsg struct {
	model llm.ModelInfo
}

func (mp *ModelPickerModal) Update(msg tea.Msg) (*ModelPickerModal, tea.Cmd) {
	switch msg := msg.(type) {
	case modelPickerLoadedMsg:
		mp.loading = false
		if msg.err != nil {
			mp.state = mpErr
			mp.errMsg = msg.err.Error()
			return mp, nil
		}
		mp.state = mpReady
		mp.models = msg.models
		mp.applyFilter()
		return mp, nil

	case modelPickerRefreshMsg:
		if msg.err != nil {
			mp.errMsg = msg.err.Error()
			mp.state = mpErr
			return mp, nil
		}
		mp.state = mpReady
		mp.errMsg = ""
		mp.models = msg.models
		mp.applyFilter()
		return mp, nil

	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlR:
			return mp, mp.RefreshModels(providerConfigsFromModel(mp.models))

		case tea.KeyUp:
			if mp.cursor > 0 {
				mp.cursor--
			}
			return mp, nil

		case tea.KeyDown:
			if mp.cursor < len(mp.filtered)-1 {
				mp.cursor++
			}
			return mp, nil

		case tea.KeyEnter:
			if mp.cursor >= 0 && mp.cursor < len(mp.filtered) {
				selected := mp.filtered[mp.cursor]
				return mp, func() tea.Msg {
					return modelSelectedMsg{model: selected}
				}
			}
			return mp, nil

		case tea.KeyEscape:
			return mp, nil

		default:
			if msg.Type == tea.KeyRunes || msg.Type == tea.KeySpace || msg.Type == tea.KeyBackspace || msg.Type == tea.KeyDelete {
				var cmd tea.Cmd
				mp.ti, cmd = mp.ti.Update(msg)
				mp.cursor = 0
				mp.applyFilter()
				return mp, cmd
			}
		}
	}

	return mp, nil
}

func (mp *ModelPickerModal) SetSize(w, h int) {
	mp.width = w
	mp.height = h
	mp.ti.Width = w - 12
}

func (mp *ModelPickerModal) applyFilter() {
	query := mp.ti.Value()
	if query == "" {
		mp.filtered = mp.models
		return
	}

	lower := strings.ToLower(query)
	var results []llm.ModelInfo
	for _, m := range mp.models {
		if strings.Contains(strings.ToLower(m.ID), lower) ||
			strings.Contains(strings.ToLower(m.Name), lower) ||
			strings.Contains(strings.ToLower(m.Provider), lower) {
			results = append(results, m)
		}
	}

	if len(results) > 100 {
		results = results[:100]
	}
	mp.filtered = results
}

func (mp *ModelPickerModal) View() string {
	if mp.loading {
		return mp.renderLoading()
	}
	if mp.state == mpErr {
		return mp.renderError()
	}
	return mp.renderList()
}

func (mp *ModelPickerModal) renderLoading() string {
	return lipgloss.NewStyle().
		Width(mp.width-4).
		Height(5).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(colorMauve)).
		Align(lipgloss.Center, lipgloss.Center).
		Render("Fetching models...")
}

func (mp *ModelPickerModal) renderError() string {
	return lipgloss.NewStyle().
		Width(mp.width-4).
		Height(5).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(colorRed)).
		Align(lipgloss.Center, lipgloss.Center).
		Render(fmt.Sprintf("Error: %s", mp.errMsg))
}

func (mp *ModelPickerModal) renderList() string {
	// Reserve lines for: title(1), textinput(1), count(1), footer(2).
	const reservedLines = 5
	maxH := mp.height - reservedLines
	if maxH > 12 {
		maxH = 12
	}
	if maxH < 3 {
		maxH = 3
	}

	var b strings.Builder

	// ── Header ─────────────────────────────────────────────────────────
	title := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(colorMauve)).
		Render(" Model Picker ")
	b.WriteString(title)
	b.WriteString("\n")

	// ── Search bar ──────────────────────────────────────────────────────
	b.WriteString(mp.ti.View())
	b.WriteString("\n")

	// Count + refresh hint on one line
	if mp.ti.Value() != "" {
		b.WriteString(mutedStyle.Render(fmt.Sprintf(" %d matches", len(mp.filtered))))
	} else {
		b.WriteString(mutedStyle.Render(fmt.Sprintf(" %d models", len(mp.models))))
	}
	b.WriteString("  ")
	b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(colorMuted)).Faint(true).Render("Ctrl+R refresh"))
	b.WriteString("\n\n")

	// ── Scrollable model list ───────────────────────────────────────────
	var prevProvider string
	displayed := 0
	for i, m := range mp.filtered {
		if displayed >= maxH {
			b.WriteString(mutedStyle.Render(fmt.Sprintf(" … %d more", len(mp.filtered)-maxH)))
			break
		}

		if m.Provider != prevProvider {
			if prevProvider != "" {
				b.WriteString("\n")
			}
			providerStyle := lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color(colorSapphire))
			header := " " + strings.ToUpper(m.Provider)

			authLabel := providerAuthStatus(m.Provider)
			if authLabel != "" {
				if strings.Contains(authLabel, "✓") {
					header += "  " + greenStyle.Render(authLabel)
				} else {
					header += "  " + redStyle.Render(authLabel)
				}
			}
			b.WriteString(providerStyle.Render(header))
			b.WriteString("\n")
			prevProvider = m.Provider
		}

		cursor := "  "
		itemStyle := dimmedStyle
		if i == mp.cursor {
			cursor = "▸ "
			itemStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color(colorAccent)).
				Bold(true)
		}

		line := fmt.Sprintf("%s%s", cursor, itemStyle.Render(m.ID))
		b.WriteString(line)
		b.WriteString("\n")
		displayed++
	}

	// ── Footer ──────────────────────────────────────────────────────────
	footer := mutedStyle.Render("↑↓ navigate  ↵ select  Esc close")
	b.WriteString(footer)

	borderColor := lipgloss.Color(colorMauve)
	content := b.String()

	return lipgloss.NewStyle().
		Width(mp.width-4).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor).
		Padding(1, 2).
		Render(content)
}

func providerAuthStatus(provider string) string {
	if provider == "ollama" {
		return ""
	}
	if config.HasCredentials(provider) {
		return "[Logged In]"
	}
	return "[Needs Auth]"
}

func providerConfigsFromModel(models []llm.ModelInfo) map[string]string {
	seen := make(map[string]string)
	for _, m := range models {
		switch m.Provider {
		case "openrouter":
			seen["openrouter"] = ""
		case "ollama":
			seen["ollama"] = ""
		case "anthropic":
			seen["anthropic"] = ""
		case "openai":
			seen["openai"] = ""
		}
	}
	return seen
}
