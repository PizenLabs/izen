package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/PizenLabs/izen/internal/modes"
)

// Section is a mode-owned content block in the workspace. Modes compose their
// own sections; the renderer projects them without needing to know what they
// mean.
type Section struct {
	Title string
	Body  string
}

// Workspace is the complete, immutable description of everything the renderer
// displays. It is the single source of truth for the visible UI: the renderer
// receives exactly one Workspace and projects it, without knowing about modes,
// banners, prompts, footers, or action logic.
//
// Every field is owned by the layer that produced it (mode / workflow), never
// by the renderer:
//   - Overlay:      full-screen replacement (init / help / loading). Non-empty
//     => the renderer shows only this.
//   - Viewport:     main scrollable content (height-sized by the assembler).
//   - ProposalDock: optional mutation/processing dock ("" = none).
//   - Input:        autocomplete + separators + prompt region (precomposed).
//   - Footer:       status bar (telemetry with capabilities inlined).
//   - Actions:      capabilities exposed by the current workflow.
//   - Header:       mode-owned header line.
//   - Sections:     mode-owned content sections.
type Workspace struct {
	Overlay      string
	Viewport     string
	ProposalDock string
	Input        string
	Footer       string
	Actions      []Action
	Header       string
	Sections     []Section
}

// ViewMode builds the Workspace for a single workflow mode. Each mode owns its
// own view construction; there is no central switch over modes. Modes are
// registered explicitly into a Registry at bootstrap (see Registry), so adding
// a mode never requires editing a dispatcher or any existing infrastructure.
type ViewMode interface {
	BuildWorkspace(m *model) Workspace
}

// Registry maps each domain mode to its ViewMode builder. It is constructed
// explicitly during application bootstrap and injected into the UI, replacing
// implicit init()-based registration. This keeps initialization deterministic,
// makes the wiring testable, and lets plugin- or MCP-provided modes register
// themselves without touching package-level state.
type Registry struct {
	views map[modes.Mode]ViewMode
}

// NewRegistry returns an empty, deterministic Registry.
func NewRegistry() *Registry {
	return &Registry{views: make(map[modes.Mode]ViewMode)}
}

// Register associates a domain mode with its ViewMode builder.
func (r *Registry) Register(mode modes.Mode, v ViewMode) {
	r.views[mode] = v
}

// For resolves the ViewMode for a mode.
func (r *Registry) For(mode modes.Mode) (ViewMode, bool) {
	v, ok := r.views[mode]
	return v, ok
}

// BuildWorkspace is the single entry the renderer (and the rest of the app)
// uses to obtain the current screen. It is infrastructure — not a mode switch:
// it resolves UI lifecycle overlays (init / help / loading) and otherwise
// delegates to the registered ViewMode for the current mode. The renderer
// never sees mode, banner, prompt, footer, or action logic.
// renderModelPickerModal renders the model picker as a compact, centered
// floating dialog over the normal workspace background.
func (m *model) renderModelPickerModal() string {
	// Build the normal workspace content first (background).
	var normalWS Workspace
	if m.Ready && m.viewRegistry != nil {
		if v, ok := m.viewRegistry.For(m.resolver.Current()); ok {
			normalWS = v.BuildWorkspace(m)
		}
	}
	var parts []string
	if normalWS.Viewport != "" {
		parts = append(parts, normalWS.Viewport)
	}
	if normalWS.ProposalDock != "" {
		parts = append(parts, normalWS.ProposalDock)
	}
	if normalWS.Input != "" {
		parts = append(parts, normalWS.Input)
	}
	if normalWS.Footer != "" {
		parts = append(parts, normalWS.Footer)
	}
	normalContent := lipgloss.JoinVertical(lipgloss.Left, parts...)

	// Set compact dimensions on the model picker so it renders at modal size.
	m.modelPicker.SetSize(68, 18)
	mpView := m.modelPicker.View()

	// Outer modal box. No hardcoded Height/MaxHeight here on purpose: mpView
	// (renderList in model_picker.go) is a fixed height by construction —
	// modelListLineBudget always pads its row list out to the same number
	// of lines — so this box naturally renders at a constant size on every
	// frame without needing cross-file height arithmetic kept in sync by
	// hand. Hardcoding a Height here previously caused the bottom border
	// to get silently clipped whenever the true content height drifted
	// even by a line.
	modalBox := lipgloss.NewStyle().
		Width(70).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(colorBlue)).
		Padding(0, 1).
		Render(mpView)

	// Use lipgloss.Place for mathematically exact centering on a full-screen
	// canvas, then blend with the workspace background via overlayOn.
	centered := lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, modalBox)
	return overlayOn(normalContent, centered, m.width, m.height)
}

// overlayOn renders bg as a full-screen string with fg centered on top.
// ANSI codes from both strings are preserved via line-level composition.
// ANSI reset codes are inserted at segment boundaries to prevent background
// styling from bleeding into the foreground overlay area.
func overlayOn(bg, fg string, w, h int) string {
	bgLines := strings.Split(bg, "\n")
	fgLines := strings.Split(fg, "\n")

	fgH := len(fgLines)
	fgW := 0
	for _, l := range fgLines {
		if lw := lipgloss.Width(l); lw > fgW {
			fgW = lw
		}
	}
	if fgW > w {
		fgW = w
	}
	if fgH > h {
		fgH = h
	}

	sy := max(0, (h-fgH)/2)
	sx := max(0, (w-fgW)/2)

	totalH := max(h, len(bgLines))

	const ansiReset = "\033[0m"

	result := make([]string, totalH)
	for i := 0; i < totalH; i++ {
		var bgLine string
		if i < len(bgLines) {
			bgLine = bgLines[i]
		}
		if bw := lipgloss.Width(bgLine); bw < w {
			bgLine += strings.Repeat(" ", w-bw)
		}

		fi := i - sy
		if fi >= 0 && fi < fgH {
			fl := fgLines[fi]
			if fw := lipgloss.Width(fl); fw < fgW {
				fl += strings.Repeat(" ", fgW-fw)
			}

			left, midRight := splitVis(bgLine, sx)
			_, right := splitVis(midRight, fgW)

			result[i] = left + ansiReset + fl + ansiReset + right
		} else {
			result[i] = bgLine
		}
	}
	return strings.Join(result, "\n")
}

// splitVis splits s at the specified visible-character position,
// preserving ANSI codes in both halves.
func splitVis(s string, visLen int) (string, string) {
	if visLen <= 0 {
		return "", s
	}
	var left, right strings.Builder
	visW := 0
	for _, r := range s {
		rw := lipgloss.Width(string(r))
		if visW < visLen {
			left.WriteRune(r)
		} else {
			right.WriteRune(r)
		}
		visW += rw
	}
	if visW < visLen {
		left.WriteString(strings.Repeat(" ", visLen-visW))
	}
	return left.String(), right.String()
}

func (m *model) BuildWorkspace() Workspace {
	if m.initStage != initNone && m.initStage != initComplete {
		return Workspace{Overlay: m.renderInitView()}
	}
	if m.showHelpOverlay {
		return Workspace{Overlay: m.renderHelpOverlay()}
	}
	if m.showModelPicker && m.modelPicker != nil {
		return Workspace{Overlay: m.renderModelPickerModal()}
	}
	if !m.Ready {
		return Workspace{Overlay: "Loading IZEN..."}
	}
	if m.viewRegistry == nil {
		return Workspace{}
	}
	v, ok := m.viewRegistry.For(m.resolver.Current())
	if !ok {
		return Workspace{}
	}
	return v.BuildWorkspace(m)
}

// ── /ask ───────────────────────────────────────────────────────────────────
// Read-only mode: no handoff capabilities are exposed.
type askView struct{}

func (askView) BuildWorkspace(m *model) Workspace {
	ws := m.assembleScreen(m.currentResultActions())
	ws.Header = "ask · explain, inspect, understand"
	return ws
}

// ── /plan ──────────────────────────────────────────────────────────────────
type planView struct{}

func (planView) BuildWorkspace(m *model) Workspace {
	var actions []Action
	if len(m.handoffCtx.PendingTodos) > 0 {
		if m.planApproved {
			actions = append(actions, Action{
				ID:       "execute-build",
				Label:    "▶ Execute Build",
				Shortcut: "alt+b",
				Command:  "/build",
				Enabled:  true,
				Priority: 100,
			})
			actions = append(actions, Action{
				ID:       "reject-plan",
				Label:    "✗ Reset & Clear",
				Shortcut: "alt+r",
				Command:  "/ask",
				Enabled:  true,
				Priority: 90,
			})
		} else {
			actions = append(actions, Action{
				ID:       "approve-plan",
				Label:    "✓ Approve & Run /build",
				Shortcut: "alt+p",
				Command:  "/build",
				Enabled:  true,
				Priority: 100,
			})
			actions = append(actions, Action{
				ID:       "reject-plan",
				Label:    "✗ Reject & Back",
				Shortcut: "alt+r",
				Command:  "/ask",
				Enabled:  true,
				Priority: 90,
			})
			actions = append(actions, Action{
				ID:       "execute-patch",
				Label:    "> Execute & Verify Patch",
				Shortcut: "alt+c",
				Command:  "/build",
				Enabled:  true,
				Priority: 80,
			})
		}
	} else if len(m.currentResultActions()) > 0 {
		actions = append(actions, m.currentResultActions()...)
	}
	ws := m.assembleScreen(actions)
	ws.Header = "plan · architecture, migrations, refactors — strategic blueprint"
	return ws
}

// ── /build ─────────────────────────────────────────────────────────────────
type buildView struct{}

func (buildView) BuildWorkspace(m *model) Workspace {
	ws := m.assembleScreen(m.currentResultActions())
	ws.Header = "build · implement, refactor, elevate"
	return ws
}

// ── /investigate ───────────────────────────────────────────────────────────
type investigateView struct{}

func (investigateView) BuildWorkspace(m *model) Workspace {
	var actions []Action
	if m.handoffCtx.ProposedFix != "" {
		actions = append(actions, Action{
			ID:       "formulate-plan",
			Label:    "Formulate Execution Plan",
			Shortcut: "alt+b",
			Command:  "/plan",
			Query:    "Formulate an execution plan for the proposed fix:\n\n" + m.handoffCtx.ProposedFix,
			Enabled:  true,
			Priority: 100,
		})
	}
	ws := m.assembleScreen(actions)
	ws.Header = "investigate · debug, trace, root-cause"
	return ws
}

// ── /review ────────────────────────────────────────────────────────────────
type reviewView struct{}

func (reviewView) BuildWorkspace(m *model) Workspace {
	ws := m.assembleScreen(m.currentResultActions())
	ws.Header = "review · analyze, critique, improve"
	return ws
}
