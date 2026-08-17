package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/autonomy"
)

// runAutonomyDecideCmd drives the human-authorized autonomous runtime on a
// prompt: it classifies intent (independent of mode), selects the capability
// workspace, evaluates the autonomy decision model, and renders the full
// observable trace. It is the TUI front end of the decision runtime — it
// changes no routing and executes nothing; it only decides.
func (m *model) runAutonomyDecideCmd(content string) tea.Cmd {
	if m.autonomy == nil {
		m.push(roleError, "[autonomy] decision runtime not wired — use compose.Application")
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return nil
	}
	content = strings.TrimSpace(content)
	if content == "" {
		m.push(roleSystem, infoStyle.Render("[Usage] $decide <prompt> — run the intent → workspace → autonomy decision trace"))
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return nil
	}

	trace := m.autonomy.Decide(content)

	var b strings.Builder
	b.WriteString(boldSapphireStyle.Render(Icon.Blueprint+" AUTONOMY DECISION") + "\n")
	fmt.Fprintf(&b, "  intent      : %s (%.0f%%)\n", trace.Intent.Intent, trace.Intent.Confidence*100)
	if len(trace.Intent.Targets) > 0 {
		fmt.Fprintf(&b, "  targets     : %s\n", strings.Join(trace.Intent.Targets, ", "))
	}
	req := trace.Intent.Required.String()
	if req == "" {
		req = "none"
	}
	fmt.Fprintf(&b, "  required    : %s\n", req)
	fmt.Fprintf(&b, "  workspace   : %s  (%s)\n", trace.Route.Workspace, trace.Route.Reason)
	fmt.Fprintf(&b, "  risk        : %s\n", trace.Risk)
	if trace.Route.Covers {
		b.WriteString("  contract    : " + greenStyle.Render("covers required capabilities") + "\n")
	} else {
		b.WriteString("  contract    : " + redStyle.Render("does NOT cover required capabilities") + "\n")
	}

	marker, label := autonomyMarker(trace.Decision.Decision)
	fmt.Fprintf(&b, "  decision    : %s%s (%s)\n", marker, label, trace.Decision.Reason)
	if len(trace.Decision.Missing) > 0 {
		fmt.Fprintf(&b, "  needs grant : %s\n", strings.Join(capNames(trace.Decision.Missing), ", "))
		if trace.Grant.Scope != "" {
			fmt.Fprintf(&b, "  scope       : %s\n", trace.Grant.Scope)
		}
		b.WriteString("\n  " + infoStyle.Render("Execution is suspended — the autonomy proposal (↑/↓ + Enter) will request this authorization internally.") + "\n")
	}
	if trace.Decision.Decision == autonomy.DecisionDirectResponse {
		b.WriteString("\n  " + infoStyle.Render("Direct response — no execution workspace, no timeline.") + "\n")
	}

	// Autonomous loop preview: show what the runtime WOULD do inside the
	// selected capability domain.
	if trace.Decision.Decision == autonomy.DecisionAutoContinue {
		loop := NewAutonomyLoopPreview(trace.Intent.Intent)
		if len(loop) > 0 {
			fmt.Fprintf(&b, "  loop        : %s\n", strings.Join(loop, " → "))
		}
	}

	m.push(roleStatus, b.String())
	m.refreshViewportContent()
	m.Viewport.GotoBottom()
	return nil
}

// autonomyMarker renders the decision verdict with a distinct visual marker.
func autonomyMarker(d autonomy.Decision) (string, string) {
	switch d {
	case autonomy.DecisionAutoContinue:
		return "▶ ", "auto_continue"
	case autonomy.DecisionAskUser:
		return "◈ ", "ask_user"
	case autonomy.DecisionBlock:
		return "■ ", "block"
	case autonomy.DecisionDirectResponse:
		return "◇ ", "direct_response"
	default:
		return "· ", string(d)
	}
}

// capNames renders a capability vector as strings.
func capNames(caps autonomy.CapabilitySet) []string {
	out := make([]string, 0, len(caps))
	for _, c := range caps {
		out = append(out, string(c))
	}
	return out
}

// NewAutonomyLoopPreview projects the canonical autonomous loop for an intent:
// investigate → plan → build → verify, with diagnosis feedback on failure. It
// is a pure description of the loop contract — it executes nothing.
func NewAutonomyLoopPreview(i autonomy.Intent) []string {
	if i == autonomy.IntentConversation {
		return nil
	}
	if !i.RequiresWorkspace() {
		return nil
	}
	if i.RequiresMutation() {
		return []string{"investigate", "plan", "build", "verify", "diagnose ↺"}
	}
	switch i {
	case autonomy.IntentInvestigation, autonomy.IntentDebugging:
		return []string{"investigate", "evidence", "report"}
	case autonomy.IntentPlanning:
		return []string{"investigate", "plan", "propose"}
	case autonomy.IntentVerification:
		return []string{"review", "verify", "report"}
	default:
		return []string{"ask", "read", "answer"}
	}
}

// handleAutonomyGrant is the DEPRECATED /grant command handler. Grant is no
// longer a user-facing command: authorization happens through the autonomy
// proposal (ask_user → Execute). This handler remains ONLY as an internal
// compatibility seam — it executes the same internal grant + revalidate +
// continue flow the proposal's Execute action uses. It is not reachable from
// the parser (the /grant token is not a registry command).
func (m *model) handleAutonomyGrant(content string) tea.Cmd {
	if m.autonomy == nil {
		m.push(roleError, "[autonomy] decision runtime not wired")
		m.refreshViewportContent()
		m.Viewport.GotoBottom()
		return nil
	}

	if m.pendingAutonomyProposal != nil {
		return m.executeAutonomyProposal()
	}

	// Deprecated bare-grant path: grant the full BUILD capability vector and
	// report that authorization is internal from now on.
	g := m.autonomy.GrantDefault(
		autonomy.CapRead, autonomy.CapAnalyze, autonomy.CapPropose,
		autonomy.CapMutate, autonomy.CapVerify,
	)
	var perms []string
	for _, p := range g.Permissions() {
		perms = append(perms, "  • "+p)
	}
	m.push(roleStatus, fmt.Sprintf(
		"%s Capability granted: %s\n  scope: %s\n%s\n%s",
		greenStyle.Render("✓"), strings.Join(capNames(g.Capabilities), " + "), g.Scope,
		strings.Join(perms, "\n"),
		mutedStyle.Render("/grant is deprecated — authorization is now an internal operation of the autonomy proposal (ask_user → Execute)."),
	))
	m.refreshViewportContent()
	m.Viewport.GotoBottom()
	return nil
}
