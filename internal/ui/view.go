package ui

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/PizenLabs/izen/internal/modes"
)

func (m *model) View() string {
	width := m.width
	if width < 40 {
		width = 40
	}

	header := m.renderHeader(width)
	modeBar := m.renderModeBar(width)
	banner := m.renderStartupBanner(width)
	topDiv := hairlineStyle.Render(strings.Repeat("─", width))
	body := m.renderBody(width)
	botDiv := topDiv
	status := m.renderStatusBar(width)

	parts := []string{header, modeBar, banner, topDiv, body}
	if m.showSuggestions && len(m.suggestions) > 0 {
		parts = append(parts, "\n"+m.renderSuggestions(width))
	}
	parts = append(parts, botDiv, status)

	return strings.Join(parts, "\n")
}

func (m *model) renderBody(width int) string {
	var body strings.Builder

	for _, rec := range m.records {
		gutter := gutterFor(rec.role)
		content := rec.text
		switch rec.role {
		case roleUser:
			body.WriteString(gutter + lipgloss.NewStyle().Foreground(lipgloss.Color(colorText)).Render(content))
		case roleAI:
			body.WriteString(gutter + lipgloss.NewStyle().Foreground(lipgloss.Color(colorText)).Render(content))
		case roleError:
			body.WriteString(gutter + errorStyle.Render(content))
		case roleStatus:
			body.WriteString(gutter + lipgloss.NewStyle().Foreground(lipgloss.Color(colorGutterStatus)).Render(content))
		default:
			body.WriteString(gutter + outputStyle.Render(content))
		}
		body.WriteString("\n")
	}

	if m.agentRunning {
		sp := spinnerStyle.Render(spinnerFrames[m.spinnerFrame])
		aiGutter := gutterAIStyle.Render("▌") + " "
		body.WriteString(aiGutter + sp + "  " + lipgloss.NewStyle().Foreground(lipgloss.Color(colorYellow)).Render(m.agentLabel+"…"))
		body.WriteString("\n")
	} else if m.agentDone {
		doneGutter := gutterStatusStyle.Render("▌") + " "
		body.WriteString(doneGutter + labelStatusStyle.Render(m.agentLabel+" complete"))
		body.WriteString("\n")
	} else if m.streaming {
		sp := spinnerStyle.Render(spinnerFrames[m.spinnerFrame])
		aiGutter := gutterAIStyle.Render("▌") + " "
		if m.responseBuffer.Len() > 0 {
			streamStyle := lipgloss.NewStyle().
				Foreground(lipgloss.Color(colorText)).
				Width(width - 4)
			body.WriteString(aiGutter + streamStyle.Render(m.responseBuffer.String()))
		} else {
			body.WriteString(aiGutter + sp + "  " + infoStyle.Render("thinking…"))
		}
		body.WriteString("\n")
	}

	if len(m.attachedFiles) > 0 {
		var ctx strings.Builder
		ctx.WriteString("\n  ")
		ctx.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(colorMuted)).Render("context"))
		ctx.WriteString(" ")
		for i, f := range m.attachedFiles {
			if i > 0 {
				ctx.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(colorDimmed)).Render(" • "))
			}
			ctx.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(colorAccent)).Render(f))
		}
		body.WriteString(ctx.String())
		body.WriteString("\n")
	}

	if m.awaitingConfirmation && len(m.pendingProposals) > 0 {
		body.WriteString(m.renderConfirmation(width))
		body.WriteString("\n")
	}

	promptLine := m.renderPrompt(width)
	body.WriteString(promptLine)

	return body.String()
}

func (m *model) renderPrompt(width int) string {
	gutter := gutterUserStyle.Render("▌") + " "
	label := labelUserStyle.Render("you")
	chevron := promptStyle.Render(" ❯ ")
	inputText := outputStyle.Render(m.input.String())
	cursor := cursorStyle.Render("▋")
	return "\n" + gutter + label + chevron + inputText + cursor
}

func (m *model) renderHeader(width int) string {
	var h strings.Builder

	h.WriteString(logoStyle.Render("izen"))
	h.WriteString(" ")
	h.WriteString(versionStyle.Render("v" + version))
	h.WriteString(bulletStyle.String())

	provider := m.cfg.ActiveProviderName()
	modelName := m.cfg.ActiveModelName()
	h.WriteString(dimStyle.Render(provider + " " + modelName))
	h.WriteString(bulletStyle.String())

	wd, _ := os.Getwd()
	shortWd := shortenPath(wd)
	h.WriteString(dimStyle.Render(shortWd))

	if m.sess.Objective != "" {
		obj := m.sess.Objective
		avail := width - lipgloss.Width(h.String()) - 6
		if avail > 10 && len(obj) > avail {
			obj = obj[:avail-3] + "…"
		}
		h.WriteString(dotStyle.Render(" · "))
		h.WriteString(infoStyle.Render(obj))
	}

	return h.String()
}

func (m *model) renderModeBar(width int) string {
	var b strings.Builder

	current := "/" + m.resolver.Current().String()
	for i, mname := range coreModes {
		if i > 0 {
			b.WriteString(hairlineStyle.Render("  "))
		}
		if mname == current {
			mode, _ := modes.Parse(mname[1:])
			activeStyle := modeTabActiveStyle.Foreground(modeAccentColor(mode))
			b.WriteString(activeStyle.Render(mname))
		} else {
			b.WriteString(modeTabInactiveStyle.Render(mname))
		}
	}

	desc := m.resolver.Current().Description()
	descStyled := dimStyle.Render("— " + desc)
	barW := lipgloss.Width(b.String())
	gap := width - barW - lipgloss.Width(descStyled) - 2
	if gap < 2 {
		gap = 2
	}
	b.WriteString(strings.Repeat(" ", gap))
	b.WriteString(descStyled)

	return b.String()
}

func (m *model) renderStatusBar(width int) string {
	wd, _ := os.Getwd()
	shortWd := shortenPath(wd)
	branch, _ := m.gitEng.Branch()

	var left strings.Builder
	left.WriteString(statusLeftStyle.Render(shortWd))
	if branch != "" {
		left.WriteString(statusLeftStyle.Render(" (" + branch + ")"))
	}

	provider := m.cfg.ActiveProviderName()
	modelName := m.cfg.ActiveModelName()

	totalTokens := m.tokenInput + m.tokenOutput
	maxContext := 32768
	pct := float64(totalTokens) / float64(maxContext) * 100
	var tokStr string
	if totalTokens >= 1000 {
		tokStr = fmt.Sprintf("%.1fk/%dk", float64(totalTokens)/1000, maxContext/1000)
	} else {
		tokStr = fmt.Sprintf("%d/%dk", totalTokens, maxContext/1000)
	}

	var costStr string
	if provider == "ollama" {
		costStr = "$0.00"
	} else {
		cost := float64(m.tokenInput)*(3.0/1_000_000) + float64(m.tokenOutput)*(15.0/1_000_000)
		costStr = fmt.Sprintf("$%.4f", cost)
	}

	var right strings.Builder
	right.WriteString(statusRightStyle.Render(provider))
	right.WriteString(statusSepStyle.String())
	right.WriteString(statusRightStyle.Render(modelName))
	right.WriteString(statusSepStyle.String())
	right.WriteString(statusRightStyle.Render(tokStr + fmt.Sprintf(" (%.0f%%)", pct)))
	right.WriteString(statusSepStyle.String())
	right.WriteString(statusRightStyle.Render(costStr))

	leftW := lipgloss.Width(left.String())
	rightW := lipgloss.Width(right.String())
	gap := width - leftW - rightW
	if gap < 1 {
		gap = 1
	}

	return left.String() + strings.Repeat(" ", gap) + right.String()
}

func RenderInlineDiff(diff string) string {
	if diff == "" {
		return ""
	}
	diffGreen := lipgloss.NewStyle().Foreground(lipgloss.Color("#a6e3a1"))
	diffRed := lipgloss.NewStyle().Foreground(lipgloss.Color("#f38ba8"))
	diffMuted := lipgloss.NewStyle().Foreground(lipgloss.Color(colorMuted))
	var b strings.Builder
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			b.WriteString(diffGreen.Render(line))
		} else if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
			b.WriteString(diffRed.Render(line))
		} else if strings.HasPrefix(line, "@@") {
			b.WriteString(diffMuted.Render(line))
		} else {
			b.WriteString(line)
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
