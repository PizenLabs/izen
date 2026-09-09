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
//   - Header:       fixed top region — WorkflowState + CapabilitySet + toast
//     overlay. Derived from RuntimeContext + WorkflowStateMachine; never
//     stored/cached.
//   - Viewport:     main scrollable content (height-sized by the assembler).
//   - ProposalDock: optional mutation/processing dock ("" = none).
//   - Input:        autocomplete + separators + prompt region (precomposed),
//     anchored directly above the single-line lifecycle Footer.
//   - Footer:       fixed bottom region — single-line lifecycle bar
//     (IDLE "? help · <model>" / EXECUTING streaming metrics). Derived from
//     the interaction lifecycle; never stored/cached.
//   - Actions:      capabilities exposed by the current workflow.
//   - Sections:     mode-owned content sections.
type Workspace struct {
	Overlay      string
	Header       string
	Viewport     string
	ProposalDock string
	Input        string
	Footer       string
	Actions      []Action
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
// sessionPickerDialogSize clamps the session picker dialog to the terminal.
func (m *model) sessionPickerDialogSize() (int, int) {
	w := sessionPickerPreferredWidth
	h := sessionPickerPreferredHeight

	const edgeMargin = 2
	if m.width > 0 {
		if maxW := m.width - edgeMargin; maxW < w {
			w = maxW
		}
	}
	if m.height > 0 {
		if maxH := m.height - edgeMargin; maxH < h {
			h = maxH
		}
	}
	if w < sessionPickerMinWidth {
		w = sessionPickerMinWidth
	}
	if h < sessionPickerMinHeight {
		h = sessionPickerMinHeight
	}
	return w, h
}

func (m *model) renderSessionPickerModal() string {
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

	dialogW, dialogH := m.sessionPickerDialogSize()
	m.sessionPicker.SetSize(dialogW, dialogH)
	spView := m.sessionPicker.View()

	modalBox := lipgloss.NewStyle().
		Width(dialogW+2).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(colorMauve)).
		Padding(0, 1).
		Render(spView)

	centered := lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, modalBox)
	return overlayOn(normalContent, centered, m.width, m.height)
}

// ModelPickerModalSize computes the adaptive centred-dialog constraints
// from the parent terminal viewport (Wterm, Hterm):
//
//	ModalWidth  = max(64, min(110, Wterm - 6))
//	ModalHeight = max(16, min(30, Hterm - 4))
//
// Unknown dimensions (<= 0) fall back to the preferred 110x30 so headless
// callers still get a usable card. Split-pane resizes flow through here on
// every render, guaranteeing zero clipping: the picker list budget derives
// from the inner bounds via SetSize.
func ModelPickerModalSize(w, h int) (int, int) {
	mw, mh := 110, 30
	if w > 0 {
		mw = max(64, min(110, w-6))
	}
	if h > 0 {
		mh = max(16, min(30, h-4))
	}
	return mw, mh
}

// renderModelPickerModal wraps the Phase 3 contextual view (IZEN MODEL
// REGISTRY + table + reasoning + bindings) in a centred, Lipgloss-bordered
// floating modal with a terminal-native transparent interior: no solid
// background fill is applied, so the terminal's own background/transparency
// shows through inside the mauve rounded border. The dialog scales
// adaptively: inner content bounds are set via SetSize on every render so
// terminal resizes and tmux split-panes recalculate list scrolling budgets
// with zero UI clipping.
func (m *model) renderModelPickerModal() string {
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

	modalW, modalH := ModelPickerModalSize(m.width, m.height)
	// Inner content bounds: border + padding consume 4 columns / 2 rows.
	innerW, innerH := modalW-4, modalH-2
	m.modelPicker = m.modelPicker.SetSize(innerW, innerH)
	rawContent := m.modelPicker.View()

	// Content hard clip: enforce MaxWidth/MaxHeight on the picker blob so
	// an oversized list can never stretch the modal border or jitter the
	// viewport. The picker itself already budgets rows (modalH-7) and
	// single-line rows; this is belt-and-suspenders.
	boxContent := lipgloss.NewStyle().
		MaxWidth(innerW).
		MaxHeight(innerH).
		Render(rawContent)

	modalBox := lipgloss.NewStyle().
		Width(modalW).
		MaxWidth(modalW).
		Height(modalH).
		MaxHeight(modalH).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(colorMauve)).
		Padding(0, 1).
		Render(boxContent)

	centered := lipgloss.Place(
		m.width, m.height,
		lipgloss.Center, lipgloss.Center,
		modalBox,
		// Fill surrounding workspace overlay with neutral dimmed whitespace.
		lipgloss.WithWhitespaceChars(" "),
	)
	return overlayOn(normalContent, centered, m.width, m.height)
}

func (m *model) renderTraceOverlayModal() string {
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

	if m.telemetryDemuxer == nil {
		return normalContent
	}
	overlayContent := m.telemetryDemuxer.RenderOverlay(m.width, m.height)
	centered := lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, overlayContent)
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
	// FIRST-RUN DISK GATE: authoritative .izen/ existence check supersedes
	// any in-memory initStage value. This prevents stale/incorrect state
	// (e.g., initNone zero value, initComplete from auto-create bypass)
	// from rendering the workspace before the user completes onboarding.
	if !m.isProjectInitialized() {
		return Workspace{Overlay: m.renderInitView()}
	}
	if m.initStage != initNone && m.initStage != initComplete {
		return Workspace{Overlay: m.renderInitView()}
	}
	if m.showHelpOverlay {
		return Workspace{Overlay: m.renderHelpOverlay()}
	}
	if m.showModelPicker {
		return Workspace{Overlay: m.renderModelPickerModal()}
	}
	if m.showSessionPicker && m.sessionPicker != nil {
		return Workspace{Overlay: m.renderSessionPickerModal()}
	}
	if m.showTraceOverlay && m.telemetryDemuxer != nil {
		return Workspace{Overlay: m.renderTraceOverlayModal()}
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
	return m.assembleScreen(m.currentResultActions())
}

// ── /plan ──────────────────────────────────────────────────────────────────
type planView struct{}

func (planView) BuildWorkspace(m *model) Workspace {
	var actions []Action
	if len(m.handoffCtx.PendingTodos) > 0 {
		if m.planApproved {
			actions = append(actions, Action{
				ID:       "execute-build",
				Label:    Icon.Execute + " Execute Build",
				Shortcut: "alt+b",
				Command:  "/build",
				Enabled:  true,
				Priority: 100,
			})
			actions = append(actions, Action{
				ID:       "reject-plan",
				Label:    Icon.Error + " Reset & Clear",
				Shortcut: "alt+r",
				Command:  "/ask",
				Enabled:  true,
				Priority: 90,
			})
		} else {
			actions = append(actions, Action{
				ID:       "approve-plan",
				Label:    Icon.Success + " Approve & Run /build",
				Shortcut: "alt+p",
				Command:  "/build",
				Enabled:  true,
				Priority: 100,
			})
			actions = append(actions, Action{
				ID:       "reject-plan",
				Label:    Icon.Error + " Reject & Back",
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
	return m.assembleScreen(actions)
}

// ── /build ─────────────────────────────────────────────────────────────────
type buildView struct{}

func (buildView) BuildWorkspace(m *model) Workspace {
	return m.assembleScreen(m.currentResultActions())
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
	return m.assembleScreen(actions)
}

// ── /review ────────────────────────────────────────────────────────────────
type reviewView struct{}

func (reviewView) BuildWorkspace(m *model) Workspace {
	return m.assembleScreen(m.currentResultActions())
}
