package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/execution"
)

var diffBlockRegex = regexp.MustCompile("(?s)```diff\\n(.*?)```")

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

func extractDiffPatches(response string) []patchProposal {
	var proposals []patchProposal
	matches := diffBlockRegex.FindAllStringSubmatch(response, -1)
	for _, m := range matches {
		diffContent := strings.TrimSpace(m[1])
		file, body := parseUnifiedDiff(diffContent)
		if file == "" || body == "" {
			file, body = parseUnifiedDiffHunks(diffContent)
		}
		if file != "" && body != "" {
			clean := filepath.Clean(file)
			if clean != "" && clean != "." {
				proposals = append(proposals, patchProposal{
					File:    clean,
					Content: body,
				})
			}
		}
	}
	return proposals
}

func parseUnifiedDiff(content string) (string, string) {
	lines := strings.Split(content, "\n")
	var filePath string
	var body strings.Builder
	inHunk := false

	for _, line := range lines {
		if strings.HasPrefix(line, "+++ b/") {
			filePath = strings.TrimPrefix(line, "+++ b/")
			continue
		}
		if strings.HasPrefix(line, "---") {
			continue
		}
		if strings.HasPrefix(line, "@@") {
			inHunk = true
			body.WriteString(line)
			body.WriteString("\n")
			continue
		}
		if inHunk {
			body.WriteString(line)
			body.WriteString("\n")
		}
	}

	return filePath, strings.TrimRight(body.String(), "\n")
}

func parseUnifiedDiffHunks(content string) (string, string) {
	lines := strings.Split(content, "\n")
	var filePath string
	var body strings.Builder
	inHunk := false

	for _, line := range lines {
		if strings.HasPrefix(line, "+++ ") {
			raw := strings.TrimPrefix(line, "+++ ")
			if strings.HasPrefix(raw, "b/") {
				raw = raw[2:]
			}
			filePath = raw
			continue
		}
		if strings.HasPrefix(line, "--- ") {
			continue
		}
		if strings.HasPrefix(line, "@@") {
			inHunk = true
			body.WriteString(line)
			body.WriteString("\n")
			continue
		}
		if inHunk {
			body.WriteString(line)
			body.WriteString("\n")
		}
	}
	if body.Len() > 0 {
		return filePath, strings.TrimRight(body.String(), "\n")
	}
	return filePath, body.String()
}

func (m *model) applySingleProposal() tea.Cmd {
	if len(m.pendingProposals) == 0 {
		m.state = StateChat
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
		m.state = StateChat
		m.awaitingConfirmation = false
		m.createBuildCheckpoint(1)
	} else {
		diff := RenderInlineDiff(p.Content)
		m.push(roleSystem, fmt.Sprintf("next diff for %s:\n%s", m.pendingProposals[0].File, diff))
		m.push(roleSystem, infoStyle.Render("\n  [1] Accept  [2] Allow All  [3] Reject"))
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
	m.state = StateChat
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
