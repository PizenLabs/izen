package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/execution"
)

func extractBuildProposals(response string) []patchProposal {
	var proposals []patchProposal
	lines := strings.Split(response, "\n")
	var current *patchProposal
	inBlock := false
	content := &strings.Builder{}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !inBlock {
			if strings.HasPrefix(trimmed, "```") {
				lang := strings.TrimPrefix(trimmed, "```")
				inBlock = true
				content.Reset()
				if strings.Contains(lang, ":") {
					parts := strings.SplitN(lang, ":", 2)
					current = &patchProposal{File: strings.TrimSpace(parts[1])}
				} else {
					current = nil
				}
				continue
			}
		} else {
			if strings.HasPrefix(trimmed, "```") {
				inBlock = false
				if current != nil && content.Len() > 0 {
					current.Content = content.String()
					clean := filepath.Clean(current.File)
					if clean != "" && clean != "." {
						current.File = clean
						proposals = append(proposals, *current)
					}
				}
				current = nil
				continue
			}
			if current != nil {
				content.WriteString(line)
				content.WriteString("\n")
			}
		}
	}
	return proposals
}

func (m *model) applySingleProposal() tea.Cmd {
	if len(m.pendingProposals) == 0 {
		m.awaitingConfirmation = false
		return nil
	}
	p := m.pendingProposals[0]
	patch := &execution.Patch{
		ID:       fmt.Sprintf("build-%d", time.Now().UnixNano()),
		File:     p.File,
		Modified: p.Content,
	}
	orig, err := os.ReadFile(p.File)
	if err == nil {
		patch.Original = string(orig)
	}
	if err := m.execEng.Patches.Apply(patch); err != nil {
		m.push(roleError, "apply failed: "+err.Error())
	} else {
		m.push(roleSystem, infoStyle.Render("applied: "+p.File))
	}
	m.pendingProposals = m.pendingProposals[1:]
	if len(m.pendingProposals) == 0 {
		m.awaitingConfirmation = false
		m.createBuildCheckpoint(1)
	}
	return nil
}

func (m *model) applyAllProposals() tea.Cmd {
	applied := 0
	for _, p := range m.pendingProposals {
		patch := &execution.Patch{
			ID:       fmt.Sprintf("build-%d", time.Now().UnixNano()),
			File:     p.File,
			Modified: p.Content,
		}
		orig, err := os.ReadFile(p.File)
		if err == nil {
			patch.Original = string(orig)
		}
		if err := m.execEng.Patches.Apply(patch); err != nil {
			m.push(roleError, "apply failed: "+err.Error())
		} else {
			applied++
			m.push(roleSystem, infoStyle.Render("applied: "+p.File))
		}
	}
	m.pendingProposals = nil
	m.awaitingConfirmation = false
	if applied > 0 {
		m.createBuildCheckpoint(applied)
	}
	return nil
}

func (m *model) createBuildCheckpoint(fileCount int) {
	cp, err := m.execEng.Checkpoints.Create(fmt.Sprintf("izen build: %d file(s)", fileCount))
	if err != nil {
		m.push(roleSystem, infoStyle.Render("checkpoint: "+err.Error()))
	} else {
		m.push(roleSystem, infoStyle.Render(
			fmt.Sprintf("checkpoint: %s (%d files)", cp.Hash[:8], fileCount)))
	}
}

func (m *model) renderConfirmation(width int) string {
	var inner strings.Builder
	inner.WriteString("\n")
	inner.WriteString(confirmDimStyle.Render("  proposed file changes:"))
	for _, p := range m.pendingProposals {
		inner.WriteString("\n  " + confirmFileStyle.Render("📝 "+p.File))
	}
	inner.WriteString("\n")
	inner.WriteString(confirmKeyStyle.Render("  [1] Accept"))
	inner.WriteString(confirmDescStyle.Render("  (apply this batch)"))
	inner.WriteString("\n")
	inner.WriteString(confirmKeyStyle.Render("  [2] Allow All"))
	inner.WriteString(confirmDescStyle.Render("  (trust agent for session)"))
	inner.WriteString("\n")
	inner.WriteString(confirmKeyStyle.Render("  [3] Reject"))
	inner.WriteString(confirmDescStyle.Render("  (cancel all changes)"))
	inner.WriteString("\n")

	boxWidth := 48
	if width < boxWidth+4 {
		boxWidth = width - 4
	}
	return confirmBoxStyle.Width(boxWidth).Render(inner.String())
}
