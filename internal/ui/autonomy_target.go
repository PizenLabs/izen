package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/gateway"
	"github.com/PizenLabs/izen/internal/kernelbridge"
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
//
// # Every existence claim here is kernel evidence
//
// This resolution used to answer "does this path exist" with a bare os.Stat and
// return a boolean. That is the defect the Runtime Kernel exists to remove: a
// boolean carries no event, no state, no evidence and no verification, so the
// mutation target the runtime goes on to modify rests on an unverifiable claim.
//
// Every presence verdict below is now adjudicated by the kernel from a
// file.exists capability's evidence, under an OBSERVE contract that requires an
// independent re-check. When the runtime cannot prove presence it reports
// nothing rather than guessing, and a target it could not prove is never offered
// as a candidate — an unproven target is not a candidate, it is an unknown.

// targetResolution is the result of resolving one @target token.
//
// It carries the kernel executions that produced it, not as diagnostics but
// because the caller needs them to be truthful: a target-not-found diagnosis
// that claims "no such file exists" must be able to point at the evidence that
// established it, and must be able to say when it could not be established.
type targetResolution struct {
	// resolved is the chosen workspace-relative path, or the canonicalised raw
	// token when nothing was proven to exist.
	resolved string
	// candidates are the proven-existing files the caller may choose between.
	// Empty means nothing in the workspace was proven to match.
	candidates []string
	// observations holds every kernel execution this resolution performed, in
	// order. It is normally one entry, and two when an explicit path had to fall
	// back to basename discovery.
	observations []kernelbridge.Observation
}

// provenAbsent reports whether the runtime proved the named target does not
// exist. It is deliberately distinct from "no candidate was found": only the
// first may be rendered to a human as a statement about the filesystem.
func (r targetResolution) provenAbsent(target string) bool {
	for _, obs := range r.observations {
		if obs.Absent(target) {
			return true
		}
	}
	return false
}

// unproven reports whether any execution in this resolution failed to reach
// PROVEN. When it is true the resolution knows less than it appears to, and the
// caller must not present its candidates as an exhaustive answer.
func (r targetResolution) unproven() bool {
	if len(r.observations) == 0 {
		return true
	}
	for _, obs := range r.observations {
		if !obs.Proven() {
			return true
		}
	}
	return false
}

// resolveAutonomyBuildTarget resolves the raw @target token against the
// workspace through the Runtime Kernel.
//
//   - an explicit path (contains a directory) that is proven present → single
//     safe candidate, decided without walking the workspace
//   - exactly one workspace file whose name matches and is proven present →
//     single candidate
//   - several proven-present matches → several candidates (the caller presents a
//     selector, which is a human decision)
//   - nothing proven present → no candidates (the caller decides
//     create-vs-not-found)
//
// The workspace walk below is ENUMERATION, not observation: it produces a list
// of names to ask about. It never decides that one of them exists. Every name it
// yields leaves this function only after kernel evidence has proven it present,
// and a name it could not prove is dropped rather than offered.
func (m *model) resolveAutonomyBuildTarget(raw string) targetResolution {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return targetResolution{}
	}
	target := filepath.ToSlash(gateway.CanonicalizeFileName(raw))

	root := "."
	if m.workspaceRoot != "" {
		root = m.workspaceRoot
	}
	ctx := m.operationContext()

	var res targetResolution
	// observed records which targets the kernel has already been asked about, so
	// a fallback path cannot ask the same question twice and present the same
	// execution twice as if it were two independent findings.
	observed := map[string]bool{}
	observe := func(targets ...string) kernelbridge.Observation {
		obs := kernelbridge.Observe(ctx, root, targets)
		res.observations = append(res.observations, obs)
		for _, t := range targets {
			observed[t] = true
		}
		return obs
	}

	// ── Explicit path: the user named a concrete location ─────────────────
	//
	// An explicit path that contains a directory component is authoritative and
	// never ambiguous, so it is observed directly. This is the one existence
	// decision on this path, and it is the whole reason it now costs a kernel
	// execution instead of a syscall: the answer decides which file a mutation
	// lands on, so it may not rest on an unrecorded boolean.
	if strings.Contains(target, "/") {
		obs := observe(target)
		if obs.Exists(target) {
			res.resolved = target
			res.candidates = []string{target}
			return res
		}
		// Not proven present. Either it is proven absent — in which case basename
		// discovery below may still find the file the user meant — or the runtime
		// could not answer at all. Both fall through; the two stay distinguishable
		// through the recorded observations rather than through a guess.
	}

	// ── Basename discovery ────────────────────────────────────────────────
	names := workspaceNamesMatching(root, filepath.Base(target))
	if len(names) == 0 {
		// Ask the kernel about the token itself, so the caller receives a real
		// absence observation instead of an empty candidate list that says nothing
		// about the workspace. Without this, "no candidates" would be
		// indistinguishable from "nobody looked". Skipped when the explicit branch
		// above already asked exactly this question.
		if !observed[target] {
			observe(target)
		}
		res.resolved = target
		return res
	}

	obs := observe(names...)
	// Only names the kernel proved present may leave this function. A name the
	// runtime could not account for is dropped rather than offered, because
	// offering it would hand the mutation authority to an unverified claim.
	proven := obs.ProvenTargets()
	if len(proven) == 0 {
		res.resolved = target
		return res
	}
	res.resolved = proven[0]
	res.candidates = proven
	return res
}

// workspaceNamesMatching enumerates workspace-relative paths whose basename
// equals base, in walk order.
//
// It answers "what could this name refer to", never "does this exist". Every
// entry it returns is a QUESTION; the kernel answers it. Hidden and VCS internals
// are skipped because a mutation target inside .git is never what the user meant,
// and that is a naming policy rather than an existence claim.
//
// It takes no receiver on purpose. A method on the model would imply it reads
// model state, and the one fact that matters here — which directory is the
// workspace — is passed explicitly so the confinement is visible at the call site.
func workspaceNamesMatching(root, base string) []string {
	if base == "" || base == "." {
		return nil
	}
	var names []string
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
			names = append(names, relSlash)
		}
		return nil
	})
	return names
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
// states what the runtime attempted, what the kernel actually proved, why the
// strategy failed, and what can be tried next — never a raw parser error.
//
// The evidence line is read from the kernel's own observation. It used to assert
// "no file matching X exists in the workspace" on the strength of a bare stat;
// now it says what was proven, and when the runtime could not prove anything it
// says THAT instead of claiming an absence nobody established.
func (m *model) reportAutonomyTargetNotFound(trace autonomy.Trace, raw string, res targetResolution) tea.Cmd {
	m.push(roleError, fmt.Sprintf("[autonomy] target not found: %q", raw))
	m.push(roleSystem, infoStyle.Render("  what Izen attempted: resolve the mutation target deterministically"))
	m.push(roleSystem, infoStyle.Render("  what evidence it found: "+describeTargetEvidence(res)))
	m.push(roleSystem, infoStyle.Render("  why the current strategy failed: the objective mutates an existing file, but no such file was proven to exist"))
	m.push(roleSystem, mutedStyle.Render("  next: name an existing file (@<path>) or create one (e.g. \"create @index.html\")"))
	m.refreshViewportContent()
	m.gotoBottomIfAllowed()
	return nil
}

// describeTargetEvidence renders what the kernel established, in the closed
// vocabulary of its own outcome.
func describeTargetEvidence(res targetResolution) string {
	if len(res.observations) == 0 {
		return "nothing — no kernel observation was performed"
	}
	if res.unproven() {
		parts := make([]string, 0, len(res.observations))
		for _, obs := range res.observations {
			parts = append(parts, fmt.Sprintf("%s (%s)", obs.ExecutionID, obs.Outcome))
		}
		return "no verified answer — the workspace observation did not reach PROVEN: " + strings.Join(parts, ", ")
	}
	absent := make([]string, 0, len(res.observations))
	for _, obs := range res.observations {
		for target := range obs.Presence {
			if obs.Absent(target) {
				absent = append(absent, target)
			}
		}
	}
	if len(absent) == 0 {
		return fmt.Sprintf("PROVEN — no candidate matched (%d observation(s))", len(res.observations))
	}
	return "PROVEN — the workspace does not contain: " + strings.Join(absent, ", ")
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

	b.WriteString(" " + boundRule(width, permissionBoxStyle, 2) + "\n")
	b.WriteString(" " + mutedStyle.Render("↑/↓ navigate · Enter select · Esc cancel") + "\n")

	return boundBox(permissionBoxStyle, width).Render(b.String())
}
