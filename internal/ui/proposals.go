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

// fileTagBlockRegex matches the structured FILE: tag format followed by a code block.
// Format: FILE: <path>\n```<lang>\n<content>\n```
var fileTagBlockRegex = regexp.MustCompile("(?mi)^FILE:\\s*(\\S+)\\s*\\n```[a-zA-Z]*\\n(.*?)```")

// fallbackCodeBlockRegex catches any code block that might contain file content
// when the model ignores the structured format. Used as last-resort fallback.
var fallbackCodeBlockRegex = regexp.MustCompile("(?s)```([a-zA-Z0-9_+-]+)\\n(.*?)```")

func extractBuildProposals(response string) []SemanticProposal {
	var proposals []SemanticProposal

	// PHASE 1: Extract FILE: tag blocks (structured format from strengthened prompt).
	proposals = append(proposals, extractFileTagBlocks(response)...)

	// PHASE 2: Original line-by-line parser for lang:path blocks.
	proposals = append(proposals, extractLangPathBlocks(response)...)

	// PHASE 3: Extract diff blocks.
	proposals = append(proposals, extractDiffPatches(response)...)

	// PHASE 4: Fallback — if no proposals found, scan for bare code blocks
	// and try to infer file paths from the response context.
	if len(proposals) == 0 {
		proposals = append(proposals, extractFallbackBlocks(response)...)
	}

	return proposals
}

// extractFileTagBlocks parses the FILE: <path> ... ``` ... ``` structured format.
// When the target file already exists on disk, the raw content is converted into
// a synthetic unified diff so the diff-rendering pipeline (green/red coloring,
// line numbers) is always used for existing files.
func extractFileTagBlocks(response string) []SemanticProposal {
	var proposals []SemanticProposal
	matches := fileTagBlockRegex.FindAllStringSubmatch(response, -1)
	for _, match := range matches {
		if len(match) < 3 {
			continue
		}
		rawPath := strings.TrimSpace(match[1])
		body := strings.TrimSpace(match[2])
		if rawPath == "" || body == "" {
			continue
		}
		clean := filepath.Clean(rawPath)
		if clean == "" || clean == "." {
			continue
		}

		diff := body
		// Safety net: if the file already exists on disk, the model should have
		// used diff format. Convert the full-content overwrite into a synthetic
		// unified diff so the renderer shows proper green/red coloring.
		if origBytes, err := os.ReadFile(clean); err == nil {
			origContent := string(origBytes)
			if origContent != body {
				// File exists and content differs — build a synthetic diff
				diff = buildSyntheticDiff(clean, origContent, body)
			}
			// If origContent == body, the file is unchanged — still emit the
			// proposal (the user may expect to see it) but keep it as-is.
		}
		// If os.ReadFile fails, the file doesn't exist — this is genuinely a
		// new file creation, so leave `diff` as the raw content.

		proposals = append(proposals, SemanticProposal{
			ID:       fmt.Sprintf("build-%d", time.Now().UnixNano()),
			Target:   SemanticTarget{QualifiedName: clean},
			Diff:     diff,
			Expanded: true,
		})
	}
	return proposals
}

// buildSyntheticDiff constructs a unified diff that replaces every line of the
// old content with every line of the new content. This is a coarse-grained
// "full replacement" diff — it won't show fine-grained line changes, but it
// ensures the diff renderer produces colored output (red deletions, green
// additions) rather than plain uncolored text.
func buildSyntheticDiff(path, oldContent, newContent string) string {
	oldLines := strings.Split(oldContent, "\n")
	newLines := strings.Split(newContent, "\n")

	var b strings.Builder
	b.WriteString("--- a/")
	b.WriteString(path)
	b.WriteString("\n+++ b/")
	b.WriteString(path)
	b.WriteString("\n")
	fmt.Fprintf(&b, "@@ -1,%d +1,%d @@\n", len(oldLines), len(newLines))

	for _, line := range oldLines {
		b.WriteString("-")
		b.WriteString(line)
		b.WriteString("\n")
	}
	for _, line := range newLines {
		b.WriteString("+")
		b.WriteString(line)
		b.WriteString("\n")
	}

	return strings.TrimRight(b.String(), "\n")
}

// extractLangPathBlocks parses ```lang:path blocks (original format).
func extractLangPathBlocks(response string) []SemanticProposal {
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
					current.Expanded = true
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
					ID:       fmt.Sprintf("build-%d", time.Now().UnixNano()),
					Target:   SemanticTarget{QualifiedName: clean},
					Diff:     body,
					Expanded: true,
				})
			}
		}
	}
	return proposals
}

// extractFallbackBlocks is the last-resort parser. When Qwen (or any model) ignores
// the structured format and wraps content in bare ```plaintext or ```go blocks,
// this function attempts to recover the file content by inferring the target path
// from the nearest FILE:/file:/edit file/ filename mention in the preceding text.
func extractFallbackBlocks(response string) []SemanticProposal {
	var proposals []SemanticProposal
	matches := fallbackCodeBlockRegex.FindAllStringSubmatchIndex(response, -1)
	for _, loc := range matches {
		if len(loc) < 4 {
			continue
		}
		lang := response[loc[2]:loc[3]]
		body := strings.TrimSpace(response[loc[4]:loc[5]])

		// Skip diff blocks — already handled.
		if lang == "diff" {
			continue
		}
		if body == "" {
			continue
		}

		// Search backward from the code block for a file path hint.
		preBlock := strings.TrimSpace(response[:loc[0]])
		filePath := findNearestFilePath(preBlock)
		if filePath == "" {
			continue
		}

		clean := filepath.Clean(filePath)
		if clean == "" || clean == "." {
			continue
		}

		diff := body
		// Safety net: if the file already exists on disk, convert to synthetic diff
		// so the renderer shows colored output instead of uncolored plaintext.
		if origBytes, err := os.ReadFile(clean); err == nil {
			origContent := string(origBytes)
			if origContent != body {
				diff = buildSyntheticDiff(clean, origContent, body)
			}
		}

		proposals = append(proposals, SemanticProposal{
			ID:       fmt.Sprintf("build-%d", time.Now().UnixNano()),
			Target:   SemanticTarget{QualifiedName: clean},
			Diff:     diff,
			Expanded: true,
		})
	}
	return proposals
}

// findNearestFilePath scans backward through preceding text to find a file path
// mentioned via common patterns: FILE: path, file: path, edit file path, or
// a bare filename on a line by itself.
func findNearestFilePath(text string) string {
	lines := strings.Split(text, "\n")
	// Scan last 10 lines for a file path hint.
	start := 0
	if len(lines) > 10 {
		start = len(lines) - 10
	}
	for i := len(lines) - 1; i >= start; i-- {
		trimmed := strings.TrimSpace(lines[i])
		lower := strings.ToLower(trimmed)

		// FILE: path or file: path
		if strings.HasPrefix(lower, "file:") {
			raw := strings.TrimSpace(trimmed[5:])
			if raw != "" {
				return raw
			}
		}
		// edit file path
		if strings.HasPrefix(lower, "edit file") {
			raw := strings.TrimSpace(trimmed[9:])
			if raw != "" {
				return raw
			}
		}
	}
	return ""
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
			filePath = strings.TrimPrefix(strings.TrimPrefix(line, "+++ "), "b/")
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
		m.setApplyError("apply failed: " + err.Error())
		return nil
	}

	// Track and emit collapsed single-line accepted summary
	status := "modified"
	if isNewFileCreation(p.Diff) {
		status = "created"
	}
	m.acceptedProposals = append(m.acceptedProposals, acceptedProposal{
		Target: p.Target.QualifiedName,
		Status: status,
	})
	acceptedLine := fmt.Sprintf("%s Accepted • %s • %s", acceptedDotStyle, p.Target.QualifiedName, status)
	m.push(roleSystem, acceptedLineStyle.Render(acceptedLine))

	m.pendingProposals = m.pendingProposals[1:]
	m.proposalDiffOffset = 0
	if len(m.pendingProposals) == 0 {
		m.state = StateChat
		m.awaitingConfirmation = false
		m.createBuildCheckpoint(1)
	} else {
		// Minimal next-proposal notification — no full diff bloat
		m.push(roleSystem, infoStyle.Render("next: "+m.pendingProposals[0].Target.QualifiedName))
		modeColor := m.modeStyle(m.resolver.Current())
		m.push(roleSystem, modeColor.Render("  [A] Accept  [L] Allow All  [R] Reject"))
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
			m.setApplyError("apply failed: " + err.Error())
			continue
		}
		applied++

		status := "modified"
		if isNewFileCreation(p.Diff) {
			status = "created"
		}
		m.acceptedProposals = append(m.acceptedProposals, acceptedProposal{
			Target: p.Target.QualifiedName,
			Status: status,
		})
		acceptedLine := fmt.Sprintf("%s Accepted • %s • %s", acceptedDotStyle, p.Target.QualifiedName, status)
		m.push(roleSystem, acceptedLineStyle.Render(acceptedLine))
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
		shortHash := cp.Hash
		if len(shortHash) > 8 {
			shortHash = shortHash[:8]
		}
		m.push(roleSystem, infoStyle.Render(
			fmt.Sprintf("checkpoint: %s (%d files)", shortHash, fileCount)))
	}
}

// shellExecRegex matches bash/sh code blocks in AI responses.
var shellExecRegex = regexp.MustCompile("(?s)```(?:bash|sh)\\n(.*?)```")

// extractShellCommands scans a response for bash/sh code blocks and returns
// them as pending shell execution proposals requiring explicit user approval.
func extractShellCommands(response string) []shellExecBlock {
	matches := shellExecRegex.FindAllStringSubmatch(response, -1)
	var blocks []shellExecBlock
	for _, m := range matches {
		cmd := strings.TrimSpace(m[1])
		if cmd == "" {
			continue
		}
		desc := cmd
		if idx := strings.Index(cmd, "\n"); idx >= 0 {
			desc = cmd[:idx]
		}
		if len(desc) > 60 {
			desc = desc[:60] + "..."
		}
		blocks = append(blocks, shellExecBlock{
			Command:     cmd,
			Description: desc,
		})
	}
	return blocks
}

// execShellCmd executes a shell command and pushes output as records.
func (m *model) execShellCmd(cmd string) tea.Cmd {
	return func() tea.Msg {
		m.push(roleSystem, fmt.Sprintf("$ %s", cmd))
		out, err := execShell(cmd)
		if err != nil {
			m.push(roleError, "shell exec: "+err.Error())
		}
		if out != "" {
			for _, line := range strings.Split(strings.TrimRight(out, "\r\n"), "\n") {
				m.push(roleSystem, line)
			}
		}
		return nil
	}
}
