package ui

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/PizenLabs/izen/internal/modes/plan"
	"github.com/PizenLabs/izen/pkg/capability"
)

// rewriteBuildDirective is the directive injected per target file when a build
// runs under a full-rewrite context: the current workspace file is obsolete
// and must never be referenced or reused. It is the ONLY per-file context a
// rewrite task carries, alongside the explicit user intent and target name.
const rewriteBuildDirective = "DO NOT USE OR REFERENCE ANY EXISTING CODE IN THE WORKSPACE. CREATE FROM SCRATCH."

// stripModePrefix removes a leading mode command (e.g. "/plan", "/build",
// "/investigate") from an input so the raw user intent is extracted for
// context decisions. Inputs without a mode prefix are returned trimmed.
func stripModePrefix(s string) string {
	s = strings.TrimSpace(s)
	for _, cmd := range []string{"/plan", "/build", "/investigate", "/objective"} {
		if strings.HasPrefix(strings.ToLower(s), cmd) {
			s = strings.TrimSpace(s[len(cmd):])
		}
	}
	return strings.TrimSpace(s)
}

// isFullRewriteIntent reports whether a build context demands a total
// replacement of the workspace (CLEAR ALL EXISTING CODE, redesign, rewrite,
// replace, brand-new-from-scratch). Under this context the current workspace
// file contents are OBSOLETE and must never be injected into the build prompt
// — doing so anchors small models on the old implementation (e.g. regenerating
// the To-Do App instead of the requested Portfolio).
func isFullRewriteIntent(intent string) bool {
	lower := strings.ToLower(strings.TrimSpace(intent))
	if lower == "" {
		return false
	}
	for _, marker := range []string{
		"clear all existing code", "clear all", "clear existing", "delete all",
		"wipe out", "from scratch", "brand new", "build a new", "create a new",
		"redesign", "rewrite", "re-write", "recreate", "replace existing",
		"rebuild from", "build from scratch",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// buildIntentContext returns the authoritative raw user intent for a build:
// the intent captured when /plan staged its tasks (survives mode switches),
// else the persisted session objective, else the current prompt with its mode
// prefix stripped, else the handoff ledger content.
func (m *model) buildIntentContext() string {
	if m == nil {
		return ""
	}
	if m.lastPlanIntent != "" {
		return m.lastPlanIntent
	}
	if m.sess != nil {
		if intent := m.sess.ObjectiveIntent(); strings.TrimSpace(intent) != "" {
			return intent
		}
	}
	if intent := stripModePrefix(m.currentPrompt); intent != "" {
		return intent
	}
	return strings.TrimSpace(m.handoffLedgerContent)
}

// fastTrackGoals renders the task goal list for the fast-track build prompt.
// Under a full-rewrite intent the explicit user intent is prepended as the
// absolute source of truth so the model synthesizes every target from the
// request, never from workspace state.
func fastTrackGoals(intent string, tasks []plan.Task) string {
	var goals []string
	for i, t := range tasks {
		goal := fmt.Sprintf("Task %d [%s]\nTarget file: %s\nDescription: %s", i+1, t.Type, t.Target, t.Description)
		if t.Evidence != "" {
			goal += "\n\n" + t.Evidence
		}
		goals = append(goals, goal)
	}
	if isFullRewriteIntent(intent) && strings.TrimSpace(intent) != "" {
		goals = append([]string{"USER INTENT (ABSOLUTE SOURCE OF TRUTH — CREATE FROM SCRATCH):", intent}, goals...)
	}
	return strings.Join(goals, "\n\n---\n\n")
}

// fastTrackFileContext renders the per-target file context block for the
// fast-track build prompt. Under a full-rewrite intent (obsolete workspace) it
// emits ONLY the target filename and the create-from-scratch directive — never
// the current file contents, which would anchor a small model on the old
// implementation. readTarget supplies live content for non-rewrite contexts
// (os.ReadFile); a nil reader treats every target as new.
func fastTrackFileContext(intent string, targets []string, readTarget func(string) ([]byte, error)) string {
	rewrite := isFullRewriteIntent(intent)
	var b strings.Builder
	for _, target := range targets {
		if rewrite {
			fmt.Fprintf(&b, "## Target File: %s (obsolete — rewrite required)\n", target)
			fmt.Fprintf(&b, "%s\n\n", rewriteBuildDirective)
			continue
		}
		if readTarget == nil {
			fmt.Fprintf(&b, "## Target File: %s (does not yet exist)\n\n", target)
			continue
		}
		if data, err := readTarget(target); err == nil {
			ext := filepath.Ext(target)
			lang := strings.TrimPrefix(ext, ".")
			if lang == "" {
				lang = "text"
			}
			fmt.Fprintf(&b, "## Current Content of: %s\n```%s\n%s\n```\n\n", target, lang, string(data))
		} else {
			fmt.Fprintf(&b, "## Target File: %s (does not yet exist)\n\n", target)
		}
	}
	return b.String()
}

// detectBuildTargetType classifies the requested target type from a build
// intent prompt. It is deliberately minimal and keyword-scoped: only target
// types that carry a hard alignment rule in pkg/capability are recognized
// (today: portfolio). Unknown targets return "" so the alignment gate is a
// strict no-op for every other request.
func detectBuildTargetType(intent string) string {
	lower := strings.ToLower(intent)
	if strings.Contains(lower, "portfolio") {
		return "portfolio"
	}
	return ""
}

// proposalNewContent returns the proposed NEW content of a build proposal for
// alignment checking. It prefers the exact Patch.Modified payload (fast-track
// native tool calls), then a full-content Diff (FILE:/lang:path/fallback
// blocks). Unified-diff hunks carry no full content and are skipped — the
// anchored-regeneration scenarios always emit full content.
func proposalNewContent(p SemanticProposal) string {
	if p.Patch != nil && p.Patch.Modified != "" {
		return p.Patch.Modified
	}
	if p.Diff != "" && !isDiffContent(p.Diff) {
		return p.Diff
	}
	return ""
}

// buildAlignmentRejection describes proposals rejected by the Semantic
// Alignment Gate before they can be rendered on the TUI.
type buildAlignmentRejection struct {
	// TargetType is the canonical target type the intent requested.
	TargetType string
	// Files lists the proposal target files whose content contradicted
	// TargetType.
	Files []string
}

// Error renders the explicit UI directive so the operator understands exactly
// why the patch was never displayed and how to recover.
func (r *buildAlignmentRejection) Error() string {
	if r == nil {
		return ""
	}
	return fmt.Sprintf("CRITICAL: Output describes %s, but user requested %s. Re-generate completely.",
		describeAlignmentMismatch(r.TargetType), capability.DisplayTargetType(r.TargetType))
}

// describeAlignmentMismatch names the contradictory target detected for a
// request. It is the counterpart of capability.DisplayTargetType.
func describeAlignmentMismatch(targetType string) string {
	switch strings.ToLower(targetType) {
	case "portfolio":
		return "To-Do App"
	default:
		return "a mismatched target"
	}
}

// gateBuildProposals applies the Semantic Alignment Gate to a set of build
// proposals BEFORE they are displayed: when the user's intent targets a known
// type (e.g. portfolio), every proposal whose content describes a
// contradictory target (e.g. a To-Do App) is removed so it can NEVER render on
// the TUI. It returns the surviving proposals and, when at least one was
// rejected, a rejection record. A nil rejection means every proposal aligned.
func gateBuildProposals(intent string, proposals []SemanticProposal) ([]SemanticProposal, *buildAlignmentRejection) {
	target := detectBuildTargetType(intent)
	if target == "" || len(proposals) == 0 {
		return proposals, nil
	}
	files := make([]capability.AlignmentFile, 0, len(proposals))
	for _, p := range proposals {
		if content := proposalNewContent(p); content != "" {
			files = append(files, capability.AlignmentFile{Path: p.Target.QualifiedName, Content: []byte(content)})
		}
	}
	check, err := capability.CheckAlignment(target, files)
	if err != nil && !errors.Is(err, capability.ErrSemanticMismatch) {
		return proposals, nil
	}
	if len(check.Mismatches) == 0 {
		return proposals, nil
	}
	rejected := make(map[string]bool, len(check.Mismatches))
	var rejectedFiles []string
	for _, m := range check.Mismatches {
		if m.Path == "" || rejected[m.Path] {
			continue
		}
		rejected[m.Path] = true
		rejectedFiles = append(rejectedFiles, m.Path)
	}
	var accepted []SemanticProposal
	for _, p := range proposals {
		if !rejected[p.Target.QualifiedName] {
			accepted = append(accepted, p)
		}
	}
	return accepted, &buildAlignmentRejection{TargetType: target, Files: rejectedFiles}
}

// hardKillAlignmentFailure is the HARD STOP the Semantic Alignment Gate
// performs when every generated proposal contradicts the requested target
// type. It raises the explicit critical alignment banner, purges the session
// task cache so the stale (poisoned) plan cannot persist into the next /build
// invocation, and clears every pending proposal. The mismatched patch is
// NEVER rendered and nothing is written.
func (m *model) hardKillAlignmentFailure(rejection *buildAlignmentRejection) {
	if rejection == nil {
		return
	}
	banner := fmt.Sprintf("[CRITICAL ALIGNMENT FAIL] Generated patch contained %s code instead of %s. Execution blocked.",
		describeAlignmentMismatch(rejection.TargetType), capability.DisplayTargetType(rejection.TargetType))
	m.push(roleError, banner)

	// Purge the session task cache immediately: a plan that produced a
	// mismatched generation must not persist stale task context into the next
	// build.
	if m.sess != nil {
		m.sess.ClearTasks()
		m.sess.ClearHistory()
		_ = m.sess.Save()
	}
	m.pendingProposals = nil
	m.fastTrackTargets = nil
	m.pendingHotfixTask = nil
	m.pendingHotfixPatch = nil
	if m.toolCallBuffer != nil {
		m.toolCallBuffer.Reset()
	}
	m.refreshViewportContent()
	m.Viewport.GotoBottom()
}
