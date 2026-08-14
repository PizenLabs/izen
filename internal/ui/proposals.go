package ui

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/gateway"
	"github.com/PizenLabs/izen/internal/patch"
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

// extractLangPathBlocks parses path-tagged markdown code blocks (```lang:path,
// ```lang path, ```file=path) and === FILE: blocks into proposals. Parsing is
// owned by internal/patch.ParseCodeFences, the single owner of patch
// extraction ("One Question, One Owner"). This is the fast-track multi-file
// extractor: a North Mini / SLM model that emits every file as a path-tagged
// block in one pass yields one proposal per file, so a single-pass build never
// falls back to per-task execution.
func extractLangPathBlocks(response string) []SemanticProposal {
	var proposals []SemanticProposal
	for _, f := range patch.ParseCodeFences(response) {
		if f.Path == "" || strings.TrimSpace(f.Content) == "" {
			continue
		}
		clean := filepath.Clean(f.Path)
		if clean == "" || clean == "." {
			continue
		}
		clean = gateway.CanonicalizeFileName(clean)
		proposals = append(proposals, SemanticProposal{
			ID:       fmt.Sprintf("build-%d", time.Now().UnixNano()),
			Target:   SemanticTarget{QualifiedName: clean},
			Diff:     strings.TrimSuffix(f.Content, "\n"),
			Expanded: true,
		})
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
		m.resolveApprovalState()
		m.recalcViewportHeight()
		m.awaitingConfirmation = false
		return nil
	}
	p := m.pendingProposals[0]
	m.state = StateProcessing
	m.recalcViewportHeight()
	// OPERATION LIFECYCLE: register the apply as the single authoritative
	// foreground operation so Ctrl+C during the disk mutation cancels the
	// patch-apply context (applyPatchWithDeadline derives from
	// operationContext) and the terminal mutationResultMsg handler finalizes it
	// via finalizeBuildOperation.
	m.beginOperation(OpBuild)
	return m.applyProposalCmd(p)
}

func (m *model) applyProposalCmd(p SemanticProposal) tea.Cmd {
	return func() (msg tea.Msg) {
		// ── AUTHORITATIVE STAGE: mutation apply ─────────────────────
		// The disk mutation is a real local stage; its target is the file the
		// proposal will write. The terminal mutationResultMsg handler finalizes
		// the stage together with the apply operation.
		m.setStage("apply", p.Target.QualifiedName, stageRunning)
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
		origContent := ""
		if data, err := os.ReadFile(p.Target.QualifiedName); err == nil {
			origContent = string(data)
		}
		patch := &execution.Patch{
			ID:            p.ID,
			File:          p.Target.QualifiedName,
			Modified:      modified,
			Original:      origContent,
			TaskID:        m.currentBuildTaskID,
			IsFullRewrite: p.Patch != nil && p.Patch.IsFullRewrite,
		}
		if err := m.transitionToBuilding(); err != nil {
			return mutationResultMsg{err: fmt.Errorf("workflow transition: %w", err), file: p.Target.QualifiedName}
		}
		if m.execEng != nil && len(m.execEng.Checkpoints.List()) == 0 {
			_, _ = m.execEng.Checkpoints.Create("izen build: on-the-fly checkpoint fallback")
		}
		if err := m.authorizeBuildExecution([]string{p.Target.QualifiedName}, true); err != nil {
			return mutationResultMsg{err: err, file: p.Target.QualifiedName}
		}
		if err := m.applyPatchWithDeadline(patch); err != nil {
			// Graceful no-op skip: the destruction guardrail refused a >80%
			// file wipe without an explicit delete/clear instruction. Treat as
			// skipped, not failed — and DO NOT retry as a full rewrite, which
			// would bypass the guardrail and wipe the file.
			if errors.Is(err, execution.ErrDestructivePatchSkipped) {
				return mutationResultMsg{
					file:   p.Target.QualifiedName,
					status: "skipped",
				}
			}
			// Per-file full-rewrite fallback: when a patch fails due to
			// hunk/context mismatch (e.g. whitespace variations on blank
			// lines), retry with IsFullRewrite for this specific file
			// only, preserving already-successful patches on other files.
			if origContent != "" && !patch.IsFullRewrite {
				resolved := execution.ResolveModifiedContent(origContent, modified)
				if resolved != origContent && !isDiffContent(resolved) {
					retryPatch := &execution.Patch{
						ID:            patch.ID + "-full",
						File:          patch.File,
						Modified:      resolved,
						Original:      origContent,
						TaskID:        patch.TaskID,
						IsFullRewrite: true,
					}
					if retryErr := m.applyPatchWithDeadline(retryPatch); retryErr == nil {
						return mutationResultMsg{
							file:   p.Target.QualifiedName,
							status: "modified",
						}
					}
				}
			}
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
	// OPERATION LIFECYCLE: register the apply-all batch as the single
	// authoritative foreground operation (see applySingleProposal). The
	// terminal applyAllResultMsg handler finalizes it.
	m.beginOperation(OpBuild)
	return m.applyAllProposalsCmd()
}

func (m *model) applyAllProposalsCmd() tea.Cmd {
	proposals := make([]SemanticProposal, len(m.pendingProposals))
	copy(proposals, m.pendingProposals)
	return func() (msg tea.Msg) {
		// ── AUTHORITATIVE STAGE: batch mutation apply ────────────────
		if len(proposals) > 0 {
			m.setStage("apply", proposals[0].Target.QualifiedName, stageRunning)
		}
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
			origContent := ""
			if data, err := os.ReadFile(p.Target.QualifiedName); err == nil {
				origContent = string(data)
			}
			patch := &execution.Patch{
				ID:            p.ID,
				File:          p.Target.QualifiedName,
				Modified:      modified,
				Original:      origContent,
				TaskID:        m.currentBuildTaskID,
				IsFullRewrite: p.Patch != nil && p.Patch.IsFullRewrite,
			}
			if err := m.transitionToBuilding(); err != nil {
				results = append(results, mutationResultMsg{err: fmt.Errorf("workflow transition: %w", err), file: p.Target.QualifiedName})
				continue
			}
			if m.execEng != nil && len(m.execEng.Checkpoints.List()) == 0 {
				_, _ = m.execEng.Checkpoints.Create("izen build: on-the-fly checkpoint fallback")
			}
			if err := m.authorizeBuildExecution([]string{p.Target.QualifiedName}, true); err != nil {
				results = append(results, mutationResultMsg{err: err, file: p.Target.QualifiedName})
				continue
			}
			if err := m.applyPatchWithDeadline(patch); err != nil {
				// Graceful no-op skip: the destruction guardrail refused a
				// >80% file wipe without an explicit delete/clear instruction.
				// Treat as skipped, not failed — and DO NOT retry as a full
				// rewrite, which would bypass the guardrail and wipe the file.
				if errors.Is(err, execution.ErrDestructivePatchSkipped) {
					results = append(results, mutationResultMsg{file: p.Target.QualifiedName, status: "skipped"})
					continue
				}
				// Per-file full-rewrite fallback: when a patch fails due to
				// hunk/context mismatch (e.g. whitespace variations on blank
				// lines), retry with IsFullRewrite for this specific file
				// only, preserving already-successful patches on other files.
				if origContent != "" && !patch.IsFullRewrite {
					resolved := execution.ResolveModifiedContent(origContent, modified)
					if resolved != origContent && !isDiffContent(resolved) {
						retryPatch := &execution.Patch{
							ID:            patch.ID + "-full",
							File:          patch.File,
							Modified:      resolved,
							Original:      origContent,
							TaskID:        patch.TaskID,
							IsFullRewrite: true,
						}
						if retryErr := m.applyPatchWithDeadline(retryPatch); retryErr == nil {
							results = append(results, mutationResultMsg{file: p.Target.QualifiedName, status: "modified"})
							continue
						}
					}
				}
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

// isDiffContent reports whether content appears to be a unified diff
// (contains @@ hunk headers or ---/+++ file markers). Used by the
// per-file full-rewrite fallback to avoid writing diff headers into files.
func isDiffContent(s string) bool {
	lines := strings.SplitN(s, "\n", 6)
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "@@") ||
			strings.HasPrefix(trimmed, "--- ") ||
			strings.HasPrefix(trimmed, "+++ ") {
			return true
		}
	}
	return false
}

// streamShellCmd launches a bash process and streams its stdout/stderr to the
// event loop as live shellChunkMsg values, followed by a terminal shellExitMsg.
// It is the real-time counterpart of execShellCmd: the running command shows an
// animated snowflake spinner, and its output is inspectable via Ctrl+O while it
// is still producing. The caller must also dispatch shimmerTickCmd and
// smoothStreamTickCmd so the spinner animates for the whole duration.
func (m *model) streamShellCmd(cmd string) tea.Cmd {
	ctx, cancel := context.WithCancel(context.Background())
	// The cancel is stored on the model (main goroutine — safe) so Ctrl+C can
	// abort the running process; it is cleared by the shellExitMsg handler.
	m.shellCancel = cancel

	// Seed the running exec entry NOW (main goroutine) so the tree shows the
	// animated snowflake for the whole duration — even for a command that
	// produces no output and exits before the first streamed chunk arrives.
	if m.activityTree != nil {
		m.activityTree.AppendOrUpdateExec(cmd, -1, 0, "")
	}

	shellCh := make(chan tea.Msg, 512)
	m.shellCh = shellCh
	m.shellRunning = true

	go func() {
		// ── WORKER LIFETIME (Phase 3) ────────────────────────────────
		// The streaming shell pump is a real worker; register it against the
		// active operation so terminal-lifecycle tests can prove it releases
		// before operation finalization. A no-op when no operation is attached.
		m.spawnOpWorker("shell")
		defer m.releaseOpWorker("shell")
		defer cancel()
		defer close(shellCh)

		start := time.Now()
		c := exec.CommandContext(ctx, "bash", "-c", cmd)
		stdout, err := c.StdoutPipe()
		if err != nil {
			shellCh <- shellExitMsg{cmd: cmd, exitCode: -1, elapsed: 0, err: err}
			return
		}
		stderr, err := c.StderrPipe()
		if err != nil {
			shellCh <- shellExitMsg{cmd: cmd, exitCode: -1, elapsed: 0, err: err}
			return
		}
		if err := c.Start(); err != nil {
			shellCh <- shellExitMsg{cmd: cmd, exitCode: -1, elapsed: 0, err: err}
			return
		}

		// ── CANCELLATION-SAFE PIPE DRAIN ─────────────────────────────
		// Killing the direct child does not necessarily release its pipes: a
		// grandchild that inherited the write ends (e.g. `bash -c "sleep 30"`
		// keeps `sleep` alive) holds them open, which would block the pump
		// goroutines below forever — leaking the worker even though the
		// operation was cancelled. Closing the read ends on ctx.Done makes the
		// pumps return immediately so the terminal shellExitMsg is ALWAYS
		// emitted after a cancellation.
		stopPipes := make(chan struct{})
		go func() {
			select {
			case <-ctx.Done():
				_ = stdout.Close()
				_ = stderr.Close()
			case <-stopPipes:
			}
		}()
		defer close(stopPipes)

		// Drain both pipes concurrently so a chatty stream never deadlocks
		// the process (pipes block writes once their kernel buffers fill).
		var wg sync.WaitGroup

		// emit delivers a streamed chunk to the event loop. Delivery is
		// RELIABLE — a previous non-blocking `select { default: }` silently
		// dropped output whenever the channel was momentarily contended, which
		// produced "shell exited 0 but no output streamed" failures under CI
		// load. The consumer (readShellCh, always dispatched by the event loop)
		// continuously drains the buffered channel, so a blocking send cannot
		// deadlock; the context-cancellation branch unblocks the pump if the
		// shell is aborted mid-stream.
		emit := func(text string) bool {
			select {
			case shellCh <- shellChunkMsg{text: text}:
				return true
			case <-ctx.Done():
				return false
			}
		}

		pump := func(r io.Reader) {
			defer wg.Done()
			br := bufio.NewReaderSize(r, 4096)
			var line strings.Builder
			for {
				chunk := make([]byte, 1024)
				n, readErr := br.Read(chunk)
				if n > 0 {
					line.Write(chunk[:n])
					// Emit whole lines as soon as a newline arrives so the
					// viewport updates incrementally, not in one burst.
					raw := line.String()
					for {
						idx := strings.IndexByte(raw, '\n')
						if idx < 0 {
							break
						}
						if !emit(raw[:idx+1]) {
							return
						}
						raw = raw[idx+1:]
					}
					line.Reset()
					line.WriteString(raw)
				}
				if readErr != nil {
					if line.Len() > 0 {
						emit(line.String())
					}
					return
				}
			}
		}
		wg.Add(2)
		go pump(stdout)
		go pump(stderr)

		runErr := c.Wait()
		wg.Wait()

		exitCode := 0
		if runErr != nil {
			var exitErr *exec.ExitError
			if errors.As(runErr, &exitErr) {
				exitCode = exitErr.ExitCode()
			} else {
				exitCode = -1
			}
		}
		shellCh <- shellExitMsg{cmd: cmd, exitCode: exitCode, elapsed: time.Since(start), err: runErr}
	}()

	return m.readShellCh()
}

// readShellCh reads one message from the streaming shell channel and returns
// it to the event loop. It returns nil (no-op) when the channel has been torn
// down, mirroring the readStream pattern so the loop never blocks forever.
func (m *model) readShellCh() tea.Cmd {
	return func() tea.Msg {
		if m.shellCh == nil {
			return nil
		}
		msg, ok := <-m.shellCh
		if !ok {
			return nil
		}
		return msg
	}
}
