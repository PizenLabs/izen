package ui

import (
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
func (m *model) BuildWorkspace() Workspace {
	if m.initStage != initNone && m.initStage != initComplete {
		return Workspace{Overlay: m.renderInitView()}
	}
	if m.showHelpOverlay {
		return Workspace{Overlay: m.renderHelpOverlay()}
	}
	if m.showModelPicker && m.modelPicker != nil {
		return Workspace{Overlay: m.modelPicker.View()}
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
