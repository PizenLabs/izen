package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/gateway"
)

// ── AUTONOMY TARGET RESOLUTION (§8) ──────────────────────────────────
//
// Target ambiguity is a DECISION, never a dead end. The autonomy build path
// resolves the mutation target deterministically against the workspace BEFORE
// any model reasoning:
//
//   - the named target exists → single safe candidate → continue automatically
//   - one workspace file matches the target name → continue automatically
//   - several files match → small candidate selector (↑/↓ + Enter, Esc cancel)
//   - no file matches and the objective is not a creation request → a clear
//     target-not-found diagnosis with what was attempted / evidence / next
//
// The model is never invoked merely to compensate for a deterministic target
// resolution failure.

// resolveAutonomyBuildTarget resolves the raw @target token against the
// workspace. It returns the resolved path (relative to the workspace) and the
// full candidate list:
//
//   - an explicit path (contains a directory) that exists → single safe candidate
//   - exactly one file in the workspace matches the target name → single
//   - several files match → several candidates (the caller presents a selector)
//   - no file matches → no candidates (the caller decides create-vs-not-found)
func (m *model) resolveAutonomyBuildTarget(raw string) (resolved string, candidates []string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	target := gateway.CanonicalizeFileName(raw)

	root := "."
	if m.workspaceRoot != "" {
		root = m.workspaceRoot
	}

	// An explicit path (contains a directory component) that exists on disk is
	// authoritative: the user named a concrete location, never ambiguous.
	if strings.Contains(filepath.ToSlash(target), "/") {
		if _, err := os.Stat(filepath.Join(root, target)); err == nil {
			return filepath.ToSlash(target), []string{filepath.ToSlash(target)}
		}
	}

	// Basename matching against the whole workspace tree.
	base := filepath.Base(target)
	seen := make(map[string]bool)
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return filepath.SkipDir
		}
		if info.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return filepath.SkipDir
		}
		relSlash := filepath.ToSlash(rel)
		if strings.HasPrefix(relSlash, ".izen/") || strings.HasPrefix(relSlash, ".git/") ||
			strings.HasPrefix(relSlash, ".") {
			return nil
		}
		if strings.EqualFold(filepath.Base(relSlash), base) && !seen[relSlash] {
			seen[relSlash] = true
			candidates = append(candidates, relSlash)
		}
		return nil
	})
	if len(candidates) > 0 {
		return candidates[0], candidates
	}
	return target, nil
}

// creationRequestKeywords mark a NEW-file objective. A mutation request that
// does not carry them and names a non-existent target is a target-not-found.
var creationRequestKeywords = []string{
	"create ", "generate ", "write ", "new file", "new project", "make a ",
	"add a ", "add an ", "build a ", "start a ",
}

// isAutonomyCreationRequest reports whether the objective is to create a NEW
// file rather than mutate an existing one.
func isAutonomyCreationRequest(description string) bool {
	lower := strings.ToLower(description)
	for _, kw := range creationRequestKeywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

// reportAutonomyTargetNotFound is the terminal target-not-found diagnosis. It
// states what the runtime attempted, what evidence it found, why the strategy
// failed, and what can be tried next — never a raw parser error.
func (m *model) reportAutonomyTargetNotFound(trace autonomy.Trace, raw string) tea.Cmd {
	m.push(roleError, fmt.Sprintf("[autonomy] target not found: %q", raw))
	m.push(roleSystem, infoStyle.Render("  what Izen attempted: resolve the mutation target deterministically"))
	m.push(roleSystem, infoStyle.Render(fmt.Sprintf("  what evidence it found: no file matching %q exists in the workspace", raw)))
	m.push(roleSystem, infoStyle.Render("  why the current strategy failed: the objective mutates an existing file, but no such file exists"))
	m.push(roleSystem, mutedStyle.Render("  next: name an existing file (@<path>) or create one (e.g. \"create @index.html\")"))
	m.refreshViewportContent()
	m.gotoBottomIfAllowed()
	return nil
}

// ── TARGET SELECTOR SURFACE ──────────────────────────────────────────

// stageAutonomyTargetSelector presents the ambiguous-target candidate list.
// Selection is an explicit human act; no candidate is ever auto-picked.
func (m *model) stageAutonomyTargetSelector(trace autonomy.Trace, candidates []string) {
	m.pendingAutonomyTargets = candidates
	m.autonomyTargetSelect = 0
	m.pendingAutonomyTargetTrace = trace
	m.enterApprovalState()
	m.push(roleStatus, "[autonomy] target is ambiguous — select the file to modify (↑/↓ + Enter, Esc cancels)")
	m.refreshViewportContent()
	m.gotoBottomIfAllowed()
}

// navigateAutonomyTarget moves the selector highlight. delta is -1 (up) or +1
// (down); the selection wraps.
func (m *model) navigateAutonomyTarget(delta int) {
	n := len(m.pendingAutonomyTargets)
	if n == 0 {
		return
	}
	m.autonomyTargetSelect = (m.autonomyTargetSelect + delta + n) % n
	m.refreshViewportContent()
}

// activateAutonomyTarget commits the highlighted candidate and resumes the
// build execution on the selected target. The grant already covers the
// boundary, so no re-authorization happens. The selected path is staged
// directly — it is never re-resolved (the human already decided).
func (m *model) activateAutonomyTarget() tea.Cmd {
	n := len(m.pendingAutonomyTargets)
	if n == 0 {
		return nil
	}
	if m.autonomyTargetSelect < 0 || m.autonomyTargetSelect >= n {
		m.autonomyTargetSelect = 0
	}
	selected := m.pendingAutonomyTargets[m.autonomyTargetSelect]
	trace := m.pendingAutonomyTargetTrace
	m.clearAutonomyTargetSelector()
	m.resolveApprovalState()
	trace.Intent.Targets = []string{selected}
	m.push(roleStatus, fmt.Sprintf("[autonomy] target resolved: %s", selected))
	m.refreshViewportContent()
	m.gotoBottomIfAllowed()
	// The selected candidate resumes on the RuntimeExecutor path — never the
	// legacy build staging.
	return m.executeAutonomyViaRuntime(trace)
}

// cancelAutonomyTargetSelector abandons the ambiguous objective: no file is
// selected, no mutation starts.
func (m *model) cancelAutonomyTargetSelector() tea.Cmd {
	if len(m.pendingAutonomyTargets) == 0 {
		return nil
	}
	m.clearAutonomyTargetSelector()
	m.resolveApprovalState()
	m.push(roleSystem, infoStyle.Render("[autonomy] target selection cancelled — no file was modified."))
	m.refreshViewportContent()
	m.gotoBottomIfAllowed()
	return nil
}

// clearAutonomyTargetSelector drops the pending selector state. It is the
// cleanup seam shared with the proposal cleanup so a stale selector can never
// block a later interaction.
func (m *model) clearAutonomyTargetSelector() {
	m.pendingAutonomyTargets = nil
	m.autonomyTargetSelect = 0
	m.pendingAutonomyTargetTrace = autonomy.Trace{}
}

// renderAutonomyTargetSelectorBlock renders the ambiguous-target candidate
// selector. It is minimal: the question, the numbered candidates, the current
// highlight, and the key bindings.
func (m *model) renderAutonomyTargetSelectorBlock(width int) string {
	if len(m.pendingAutonomyTargets) == 0 {
		return ""
	}
	boxWidth := width - 4
	if boxWidth < 40 {
		boxWidth = 40
	}

	var b strings.Builder
	b.WriteString(permissionTitleStyle.Render(Icon.Warning + " AUTONOMY TARGET SELECTION"))
	b.WriteString("\n\n")
	b.WriteString(permissionDescStyle.Render("Which target should I modify?"))
	b.WriteString("\n")
	for i, cand := range m.pendingAutonomyTargets {
		if i == m.autonomyTargetSelect {
			b.WriteString("  " + permissionKeyStyle.Render("[▶]") + " " + boldTextStyle.Render(cand))
		} else {
			b.WriteString("    " + mutedStyle.Render(cand))
		}
		b.WriteString("\n")
	}

	sep := strings.Repeat("─", boxWidth-4)
	b.WriteString(" " + sep + "\n")
	b.WriteString(" " + mutedStyle.Render("↑/↓ navigate · Enter select · Esc cancel") + "\n")

	return permissionBoxStyle.Width(boxWidth).Render(b.String())
}
