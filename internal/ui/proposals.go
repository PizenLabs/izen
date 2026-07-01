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

func extractBuildProposals(response string) []SemanticProposal {
	var proposals []SemanticProposal
	lines := strings.Split(response, "\n")
	var current *SemanticProposal
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
					current = &SemanticProposal{
						ID: fmt.Sprintf("build-%d", time.Now().UnixNano()),
						Target: SemanticTarget{
							QualifiedName: strings.TrimSpace(parts[1]),
						},
					}
				} else {
					current = nil
				}
				continue
			}
		} else {
			if strings.HasPrefix(trimmed, "```") {
				inBlock = false
				if current != nil && content.Len() > 0 {
					current.Diff = content.String()
					clean := filepath.Clean(current.Target.QualifiedName)
					if clean != "" && clean != "." {
						current.Target.QualifiedName = clean
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

func extractDiffPatches(response string) []SemanticProposal {
	var proposals []SemanticProposal
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
				proposals = append(proposals, SemanticProposal{
					ID:     fmt.Sprintf("build-%d", time.Now().UnixNano()),
					Target: SemanticTarget{QualifiedName: clean},
					Diff:   body,
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
		ID:       p.ID,
		File:     p.Target.QualifiedName,
		Modified: p.Diff,
	}
	orig, err := os.ReadFile(p.Target.QualifiedName)
	if err == nil {
		patch.Original = string(orig)
	}
	if err := m.execEng.Patches.Apply(patch); err != nil {
		m.push(roleError, "apply failed: "+err.Error())
	} else {
		m.push(roleSystem, infoStyle.Render("applied: "+p.Target.QualifiedName))
	}
	m.pendingProposals = m.pendingProposals[1:]
	if len(m.pendingProposals) == 0 {
		m.state = StateChat
		m.awaitingConfirmation = false
		m.createBuildCheckpoint(1)
	} else {
		// Show numbered diff of next proposal
		rendered := RenderNumberedDiff(p.Diff, m.width)
		m.push(roleSystem, fmt.Sprintf("next: %s", m.pendingProposals[0].Target.QualifiedName))
		for _, l := range strings.Split(rendered, "\n") {
			m.push(roleSystem, l)
		}
		m.push(roleSystem, infoStyle.Render("  [1] Accept  [2] Allow All  [3] Reject"))
	}
	return nil
}

func (m *model) applyAllProposals() tea.Cmd {
	applied := 0
	for _, p := range m.pendingProposals {
		patch := &execution.Patch{
			ID:       p.ID,
			File:     p.Target.QualifiedName,
			Modified: p.Diff,
		}
		orig, err := os.ReadFile(p.Target.QualifiedName)
		if err == nil {
			patch.Original = string(orig)
		}
		if err := m.execEng.Patches.Apply(patch); err != nil {
			m.push(roleError, "apply failed: "+err.Error())
		} else {
			applied++
			m.push(roleSystem, infoStyle.Render("applied: "+p.Target.QualifiedName))
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
