package ui

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/gateway"
)

// searchReplaceBlockRe matches SEARCH/REPLACE blocks that the LLM may emit
// directly (without ``` fences). Each block has the form:
//
//	<<<<<<< SEARCH
//	<original lines>
//	=======
//	<replacement lines>
//	>>>>>>>
var searchReplaceBlockRe = regexp.MustCompile(`(?s)<<<<<<< SEARCH\n(.*?)=======\n(.*?)>>>>>>>`)

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

	// PHASE 3b: Extract SEARCH/REPLACE blocks and convert to unified diff
	// for proper red/green rendering. Must run after diff patches so explicit
	// ```diff blocks take priority.
	proposals = append(proposals, extractSearchReplaceProposals(response)...)

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
		clean = gateway.CanonicalizeFileName(clean)

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
						current.Target.QualifiedName = gateway.CanonicalizeFileName(clean)
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
					Target:   SemanticTarget{QualifiedName: gateway.CanonicalizeFileName(clean)},
					Diff:     body,
					Expanded: true,
				})
			}
		}
	}
	return proposals
}

// extractSearchReplaceProposals scans for <<<<<<< SEARCH / ======= / >>>>>>>
// blocks that the LLM may emit directly without ``` fences. For each block, it
// infers the target file path from preceding context, reads the original file
// from disk, applies the SEARCH/REPLACE to compute the modified content, and
// builds a unified diff for proper red/green rendering.
func extractSearchReplaceProposals(response string) []SemanticProposal {
	if !strings.Contains(response, "<<<<<<< SEARCH") {
		return nil
	}
	var proposals []SemanticProposal
	matches := searchReplaceBlockRe.FindAllStringSubmatch(response, -1)
	if len(matches) == 0 {
		return nil
	}
	for _, m := range matches {
		searchText := strings.TrimSpace(m[1])
		replaceText := strings.TrimSpace(m[2])
		if searchText == "" && replaceText == "" {
			continue
		}
		filePath := findNearestFilePath(response)
		if filePath == "" {
			continue
		}
		clean := filepath.Clean(filePath)
		if clean == "" || clean == "." {
			continue
		}
		clean = gateway.CanonicalizeFileName(clean)
		origBytes, err := os.ReadFile(clean)
		if err != nil {
			continue
		}
		orig := string(origBytes)
		blocks := execution.ParseSearchReplaceBlocks(response)
		modified, ok := execution.ApplySearchReplaceBlocks(orig, blocks)
		if !ok || modified == orig {
			continue
		}
		diff := buildSyntheticDiff(clean, orig, modified)
		proposals = append(proposals, SemanticProposal{
			ID:       fmt.Sprintf("build-sr-%d", time.Now().UnixNano()),
			Target:   SemanticTarget{QualifiedName: clean},
			Diff:     diff,
			Expanded: true,
		})
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
		clean = gateway.CanonicalizeFileName(clean)

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
		m.recalcViewportHeight()
		m.awaitingConfirmation = false
		return nil
	}
	p := m.pendingProposals[0]
	m.state = StateProcessing
	m.recalcViewportHeight()
	return m.applyProposalCmd(p)
}

func (m *model) applyProposalCmd(p SemanticProposal) tea.Cmd {
	eng := m.execEng
	return func() (msg tea.Msg) {
		// Never let a panic in patch application crash the TUI. Recover, log
		// the trace internally, and surface a user-friendly status-bar error.
		defer func() {
			if r := recover(); r != nil {
				log.Printf("Recovered from patch panic in applyProposalCmd (file=%s): %v", p.Target.QualifiedName, r)
				msg = mutationResultMsg{
					err:  fmt.Errorf("failed to apply patch safely to %s: proposal expired or context changed", p.Target.QualifiedName),
					file: p.Target.QualifiedName,
				}
			}
		}()

		// Use the stored Patch.Modified when available (preserves full file
		// content or SEARCH/REPLACE blocks for exact application), falling
		// back to the display Diff for backward compatibility.
		modified := p.Diff
		if p.Patch != nil && p.Patch.Modified != "" {
			modified = p.Patch.Modified
		}
		patch := &execution.Patch{
			ID:       p.ID,
			File:     p.Target.QualifiedName,
			Modified: modified,
			TaskID:   m.currentBuildTaskID,
		}
		orig, err := os.ReadFile(p.Target.QualifiedName)
		if err == nil {
			patch.Original = string(orig)
		}
		if err := eng.Patches.Apply(patch); err != nil {
			return mutationResultMsg{err: err, file: p.Target.QualifiedName}
		}
		status := "modified"
		if isNewFileCreation(p.Diff) {
			status = "created"
		}
		return mutationResultMsg{
			file:   p.Target.QualifiedName,
			status: status,
		}
	}
}

func (m *model) applyAllProposals() tea.Cmd {
	m.state = StateProcessing
	m.recalcViewportHeight()
	m.acceptAll = true
	return m.applyAllProposalsCmd()
}

func (m *model) applyAllProposalsCmd() tea.Cmd {
	proposals := make([]SemanticProposal, len(m.pendingProposals))
	copy(proposals, m.pendingProposals)
	eng := m.execEng
	return func() (msg tea.Msg) {
		var results []mutationResultMsg
		// Never let a panic in patch application crash the TUI. Recover, log
		// the trace internally, and surface a user-friendly status-bar error
		// for any proposal that was in flight when the panic occurred.
		defer func() {
			if r := recover(); r != nil {
				log.Printf("Recovered from patch panic in applyAllProposalsCmd: %v", r)
				results = append(results, mutationResultMsg{
					err: fmt.Errorf("failed to apply patch safely — proposal expired or context changed"),
				})
				msg = applyAllResultMsg{results: results}
			}
		}()
		for _, p := range proposals {
			// Use the stored Patch.Modified when available (preserves full
			// file content or SEARCH/REPLACE blocks for exact application),
			// falling back to the display Diff for backward compatibility.
			modified := p.Diff
			if p.Patch != nil && p.Patch.Modified != "" {
				modified = p.Patch.Modified
			}
			patch := &execution.Patch{
				ID:       p.ID,
				File:     p.Target.QualifiedName,
				Modified: modified,
				TaskID:   m.currentBuildTaskID,
			}
			orig, err := os.ReadFile(p.Target.QualifiedName)
			if err == nil {
				patch.Original = string(orig)
			}
			if err := eng.Patches.Apply(patch); err != nil {
				results = append(results, mutationResultMsg{err: err, file: p.Target.QualifiedName})
				continue
			}
			status := "modified"
			if isNewFileCreation(p.Diff) {
				status = "created"
			}
			results = append(results, mutationResultMsg{file: p.Target.QualifiedName, status: status})
		}
		return applyAllResultMsg{results: results}
	}
}

func (m *model) createBuildCheckpoint(fileCount int) {
	cp, err := m.execEng.Checkpoints.Create(fmt.Sprintf("izen build: %d file(s)", fileCount))
	if err != nil {
		m.push(roleSystem, infoStyle.Render("checkpoint: "+err.Error()))
	} else if cp != nil {
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
// the command strings for explicit human-in-the-loop confirmation.
func extractShellCommands(response string) []string {
	matches := shellExecRegex.FindAllStringSubmatch(response, -1)
	var cmds []string
	for _, m := range matches {
		cmd := strings.TrimSpace(m[1])
		if cmd == "" {
			continue
		}
		cmds = append(cmds, cmd)
	}
	return cmds
}

// sanitizeShellCmd guards the TUI input bar against auto-loading commands
// that are dangerously long or contain diff formatting (e.g., unified diff
// paste). Returns (cleaned, rejected, reason).
var diffHeaderRegex = regexp.MustCompile(`(?m)^(?:---\s+\S+|\+\+\+\s+\S+|@@\s+-\d+(?:,\d+)?\s+\+\d+(?:,\d+)?\s*@@)`)

func sanitizeShellCmd(cmd string) (string, bool, string) {
	trimmed := strings.TrimSpace(cmd)
	if trimmed == "" {
		return cmd, true, "empty command"
	}

	const maxLen = 500
	if len(trimmed) > maxLen {
		return cmd, true, fmt.Sprintf(
			"command exceeds %d character limit (%d chars)", maxLen, len(trimmed))
	}

	if diffHeaderRegex.MatchString(trimmed) {
		return cmd, true, "command contains unified diff headers (---/+++/@@)"
	}

	return cmd, false, ""
}

// execShellCmd executes a shell command and pushes output as records.
func (m *model) execShellCmd(cmd string) tea.Cmd {
	return func() tea.Msg {
		out, err := execShell(cmd)
		var lines []string
		lines = append(lines, "$ "+cmd)
		if err != nil {
			lines = append(lines, "shell exec: "+err.Error())
		}
		if out != "" {
			lines = append(lines, strings.Split(strings.TrimRight(out, "\r\n"), "\n")...)
		}
		return shellOutputMsg{lines: lines}
	}
}
