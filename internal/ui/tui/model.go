// Package tui is the interactive proposal gateway renderer. It renders a
// DecisionSurface as a terminal selection menu and collapses a keypress into a
// single ProposalIntent.
//
// INVARIANT: the modal is PURE PRESENTATION. It never reads the workspace,
// never writes a file, and never invokes a provider. Selecting an option only
// returns the ProposalIntent value; the engine routes it across the
// RuntimeExecutor boundary via ResumeWithProposal.
//
// ARCHITECTURE: this package defines its OWN pure-data mirror of the autonomy
// proposal vocabulary (ProposalIntent / ProposalOption / DecisionSurface). It
// deliberately does NOT import internal/runtime/autonomy — the UI layer is a
// projection and must stay decoupled from the driver. The composition root
// (which may import both) or a test adapter converts between the two shapes.
package tui

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"
)

// ModalMode identifies which interactive modal the event dispatcher is
// currently routing keyboard input to. Only one modal is ever active; the
// dispatcher routes every other message to the workspace as usual.
type ModalMode int

const (
	// ModalNone: no modal is active — the workspace renders normally.
	ModalNone ModalMode = iota
	// ModalProposal: the interactive DecisionSurface proposal menu is active
	// and owns the keyboard (1-5 quick select, ↑/↓ navigate, Enter select,
	// Esc cancel).
	ModalProposal
)

// String returns the stable modal label.
func (m ModalMode) String() string {
	switch m {
	case ModalProposal:
		return "proposal"
	default:
		return "none"
	}
}

// HumanBoundaryProposalMsg is the TUI event a Zero-Token DecisionSurface
// barrier parks on. It carries the pure-data DecisionSurface the interactive
// ProposalModel renders. A non-nil surface MUST activate the interactive
// proposal modal — the update loop never degrades it to a static pause card.
type HumanBoundaryProposalMsg struct {
	DecisionSurface *DecisionSurface
}

var _ tea.Msg = HumanBoundaryProposalMsg{}

// ProposalResumedMsg is delivered after a human-selected ProposalIntent has
// been routed to the engine resumer (Driver.ResumeWithProposal). Err is nil
// when the intent was accepted for execution.
type ProposalResumedMsg struct {
	Intent ProposalIntent
	Err    error
}

var _ tea.Msg = ProposalResumedMsg{}

// Model is the TUI event dispatcher. It owns the active-modal routing: a
// HumanBoundaryProposalMsg carrying a DecisionSurface activates the
// interactive ProposalModel (never a static PauseState), and while the modal
// is active the keyboard is consumed by it — a selection routes the pure
// ProposalIntent to the bound engine resumer.
//
// The resumer is the composition-root binding of Driver.ResumeWithProposal.
// A nil resumer leaves selection disabled (the modal still renders).
type Model struct {
	activeModal   ModalMode
	proposalModel *ProposalModel
	resume        ResumeProposalFunc
	width         int
}

// NewModel returns a dispatcher with no active modal. resume is the
// composition-root binding of Driver.ResumeWithProposal (may be nil to disable
// routing).
func NewModel(resume ResumeProposalFunc) *Model {
	return &Model{resume: resume}
}

// ActiveModal reports the currently active modal mode.
func (m *Model) ActiveModal() ModalMode {
	if m == nil {
		return ModalNone
	}
	return m.activeModal
}

// ProposalActive reports whether the interactive proposal modal is the active
// view. It is the dispatch decision surface tests assert.
func (m *Model) ProposalActive() bool {
	return m != nil && m.activeModal == ModalProposal && m.proposalModel != nil
}

// ProposalSurface returns the DecisionSurface the active proposal modal
// renders, or nil when the proposal modal is not active.
func (m *Model) ProposalSurface() *DecisionSurface {
	if m == nil || m.proposalModel == nil {
		return nil
	}
	return &m.proposalModel.Surface
}

// Init implements the Bubble Tea model lifecycle. The dispatcher schedules no
// background command of its own; an active proposal modal initializes itself.
func (m *Model) Init() tea.Cmd {
	if m == nil || m.proposalModel == nil {
		return nil
	}
	return m.proposalModel.Init()
}

// Update implements the Bubble Tea event loop. It routes the HumanBoundary
// proposal event onto the interactive ProposalModel modal and, while that
// modal is active, collapses every keypress into a ProposalIntent routed to
// the engine resumer (Driver.ResumeWithProposal).
func (m *Model) Update(msg tea.Msg) (*Model, tea.Cmd) {
	if m == nil {
		return m, nil
	}
	switch msg := msg.(type) {
	case HumanBoundaryProposalMsg:
		// A DecisionSurface payload is a LIVE human decision gate: switch the
		// active TUI view component to the interactive ProposalModel and focus
		// keyboard input on it. It must NEVER degrade to a static pause.
		if msg.DecisionSurface != nil {
			m.activeModal = ModalProposal
			m.proposalModel = NewProposalModel(*msg.DecisionSurface)
			return m, m.proposalModel.Init()
		}
		// No surface: nothing interactive to render — leave the current modal
		// untouched and never manufacture a fake surface.
		return m, nil

	case tea.WindowSizeMsg:
		if msg.Width > 0 {
			m.width = msg.Width
		}
		return m, nil

	case tea.KeyMsg:
		if m.activeModal == ModalProposal && m.proposalModel != nil {
			intent, ok := m.proposalModel.HandleKey(msg.String())
			if !ok {
				// Navigation / unbound key: the modal stays active and owns
				// the keyboard until a selection is made.
				return m, nil
			}
			// A selection occurred: release the modal and route the pure
			// ProposalIntent to the engine resumer (Driver.ResumeWithProposal).
			// ProposalCancel routes too, so the engine transitions to ABORTED
			// with zero spend — the UI never hard-cancels on its own.
			m.activeModal = ModalNone
			resume := m.resume
			if resume == nil {
				return m, nil
			}
			return m, func() tea.Msg {
				return ProposalResumedMsg{
					Intent: intent,
					Err:    Route(context.Background(), resume, intent),
				}
			}
		}
		return m, nil
	}
	return m, nil
}

// View renders the active modal frame. It returns "" when no modal is active
// so the dispatcher can be composed over a workspace renderer without
// disturbing it.
func (m *Model) View() string {
	if m == nil || m.activeModal != ModalProposal || m.proposalModel == nil {
		return ""
	}
	return m.proposalModel.Render(m.width)
}
