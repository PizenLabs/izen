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
// modelPickerPreferredWidth/Height are kept for backward compat but the
// strict viewport spec now governs dimensions deterministically.
const modelPickerPreferredWidth = 84
const modelPickerPreferredHeight = 26

// modelPickerEdgeMargin is the minimum gap kept between the modal's own
// border and the raw edge of the terminal/pane.
const modelPickerEdgeMargin = 2

// modelPickerDialogSize computes the size to hand to ModelPickerModal.SetSize
// deterministically per spec: ModalHeight = min(0.75*termHeight,26),
// ModalWidth = min(0.80*termWidth,84). Outer height is fixed and never
// jitters when hovering between reasoning/non-reasoning models; only
// AvailableListRows inside shifts by EffortBlockArea (4 rows).
func (m *model) modelPickerDialogSize() (int, int) {
	termH := m.height
	termW := m.width
	// Fixed outer dimensions per spec. Use terminal size when known, fallback to preferred.
	var h, w int
	if termH > 0 {
		h = int(float64(termH) * 0.75)
		if h > 26 {
			h = 26
		}
		// Ensure modal fits inside terminal with edge margin and respects floor.
		if maxH := termH - modelPickerEdgeMargin; maxH < h {
			h = maxH
		}
		if h < modelPickerMinHeight {
			h = modelPickerMinHeight
		}
		// Also ensure at least header+footer+borders fits (without effort =10 + minRows)
		if h < modelPickerHeaderAndSearchArea+modelPickerFooterArea+modelPickerBordersAndPadding+modelListMinRows {
			h = modelPickerHeaderAndSearchArea + modelPickerFooterArea + modelPickerBordersAndPadding + modelListMinRows
		}
	} else {
		h = modelPickerPreferredHeight
	}
	if termW > 0 {
		w = int(float64(termW) * 0.80)
		if w > 84 {
			w = 84
		}
		if maxW := termW - modelPickerEdgeMargin; maxW < w {
			w = maxW
		}
		if w < modelPickerMinWidth {
			w = modelPickerMinWidth
		}
	} else {
		w = modelPickerPreferredWidth
	}
	return w, h
}

func (m *model) sessionPickerDialogSize() (int, int) {
	w := sessionPickerPreferredWidth
	h := sessionPickerPreferredHeight

	if m.width > 0 {
		if maxW := m.width - modelPickerEdgeMargin; maxW < w {
			w = maxW
		}
	}
	if m.height > 0 {
		if maxH := m.height - modelPickerEdgeMargin; maxH < h {
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

// renderSessionPickerModal renders the session picker as a centered modal.
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

	// Size the model picker to fit the *actual* terminal/pane, shrinking
	// below the preferred 68x18 when there isn't room for it (see
	// modelPickerDialogSize for why this must not be a hardcoded call).
	dialogW, dialogH := m.modelPickerDialogSize()
	m.modelPicker.SetSize(dialogW, dialogH)
	mpView := m.modelPicker.View()

	// Outer modal box. No hardcoded Height/MaxHeight here on purpose: mpView
	// (renderList in model_picker.go) is a fixed height for any given
	// dialogH — ModelPickerModal.listRowBudget always pads its row list out
	// to the same number of lines for that size — so this box naturally
	// renders at a constant size for the current terminal without needing
	// cross-file height arithmetic kept in sync by hand. Hardcoding a
	// Height here previously caused the bottom border to get silently
	// clipped whenever the true content height drifted even by a line.
	//
	// Width follows dialogW (+2 to match the picker's own inner
	// border/padding math) instead of a fixed 70, so the outer box shrinks
	// in step with the picker itself rather than overflowing the pane.
	modalBox := lipgloss.NewStyle().
		Width(dialogW+2).
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
	if m.showModelPicker && m.modelPicker != nil {
		return Workspace{Overlay: m.renderModelPickerModal()}
	}
	if m.showSessionPicker && m.sessionPicker != nil {
		return Workspace{Overlay: m.renderSessionPickerModal()}
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
