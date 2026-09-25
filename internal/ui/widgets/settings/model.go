// Package settings implements the standalone, reduced-schema settings modal.
//
// The widget is deliberately a leaf UI component: it owns only the three
// presentation preferences shown by the modal and emits value-change messages
// to its host. It does not know about the model registry, provider state,
// credentials, authorization, sessions, or shell execution. Persistence and
// runtime application belong to the caller.
package settings

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/prompt"
)

// ResponseStyle is the response verbosity policy exposed by this modal. It
// aliases the prompt package's canonical policy type so a committed value can
// be applied without a lossy conversion.
type ResponseStyle = prompt.StylePolicy

const (
	settingsModalMaxWidth         = 72
	settingsModalMaxHeight        = 18
	settingsWindowMargin          = 4
	settingsModalHorizontalChrome = 4
	settingsModalVerticalChrome   = 2
)

const (
	ResponseStyleBalanced ResponseStyle = prompt.StyleBalanced
	ResponseStyleVerbose  ResponseStyle = prompt.StyleVerbose
	ResponseStyleTerse    ResponseStyle = prompt.StyleTerse
	ResponseStyleUltra    ResponseStyle = prompt.StyleUltra

	// Short aliases are useful to callers rendering the compact schema.
	Balanced = ResponseStyleBalanced
	Verbose  = ResponseStyleVerbose
	Terse    = ResponseStyleTerse
	Ultra    = ResponseStyleUltra
)

// AutoScrollMode aliases the persisted viewport policy. Runtime ownership is
// the workspace viewport manager; this package only edits the value.
type AutoScrollMode = config.AutoScrollMode

const (
	AutoScrollSmart  = config.AutoScrollSmart
	AutoScrollAlways = config.AutoScrollAlways
	AutoScrollOff    = config.AutoScrollOff

	Smart  = AutoScrollSmart
	Always = AutoScrollAlways
	Off    = AutoScrollOff
)

// ResponseStyles is the display/cycle order specified by the settings schema.
var ResponseStyles = []ResponseStyle{
	ResponseStyleBalanced,
	ResponseStyleVerbose,
	ResponseStyleTerse,
	ResponseStyleUltra,
}

// AutoScrollModes is the display/cycle order specified by the settings schema.
var AutoScrollModes = []AutoScrollMode{
	AutoScrollSmart,
	AutoScrollAlways,
	AutoScrollOff,
}

// settingRowsByTab is the single source of truth for tab ownership. A tab
// renders only the indices in its slice; adding a future option cannot make it
// leak into another preference pane.
var settingRowsByTab = [...][]int{
	{0}, // Response
	{1}, // Visibility
	{2}, // Viewport
}

var settingDescriptions = [...]string{
	"Controls assistant output verbosity and token budget limit.",
	"Suppresses Chain-of-Thought (CoT) reasoning blocks from the main stream view.",
	"Smart: Pauses auto-scroll on manual scroll up. Always: Forces auto-scroll to bottom. Off: Disables auto-scroll.",
}

// Values is the complete, intentionally reduced settings schema. There is no
// provider/model/variant/role/credential field here by design.
type Values struct {
	ResponseStyle ResponseStyle
	HideThinking  bool
	AutoScroll    AutoScrollMode
}

// Field identifies the value changed by a CommitMsg.
type Field string

const (
	FieldResponseStyle Field = "response_style"
	FieldHideThinking  Field = "hide_thinking"
	FieldAutoScroll    Field = "auto_scroll"
)

// CommitMsg is emitted after a value is changed. The host owns persistence and
// runtime application; the widget never performs I/O.
type CommitMsg struct {
	Field  Field
	Values Values
}

// SettingsChangedMsg is a descriptive alias retained for hosts that prefer the
// event-oriented name.
type SettingsChangedMsg = CommitMsg

// CloseMsg is emitted when the user presses Esc.
type CloseMsg struct{}

// CloseSettingsMsg is a descriptive alias for CloseMsg.
type CloseSettingsMsg = CloseMsg

// Model is the value-type Bubble Tea model for the settings modal.
type Model struct {
	values Values

	activeTab int
	activeRow int

	// width/height retain the outer modal bounds used by resize observers.
	// innerWidth/innerHeight are the strict content canvas consumed by View.
	width       int
	height      int
	innerWidth  int
	innerHeight int
	closed      bool
	status      string
}

// SettingsModel is a descriptive alias for Model.
type SettingsModel = Model

// New creates a settings model. With no argument it starts at the canonical
// defaults; Values may be supplied to seed the modal from live configuration.
func New(initial ...Values) Model {
	values := Values{
		ResponseStyle: ResponseStyleBalanced,
		AutoScroll:    AutoScrollSmart,
	}
	if len(initial) > 0 {
		values = normalizeValues(initial[0])
	}
	return Model{
		values:    values,
		activeTab: 0,
		activeRow: 0,
	}.SetSize(settingsModalMaxWidth, settingsModalMaxHeight)
}

// NewDefault is an explicit spelling of New() for callers that prefer a named
// constructor.
func NewDefault() Model { return New() }

// NewSettings is a descriptive constructor alias for New.
func NewSettings(initial ...Values) Model { return New(initial...) }

// NewWithValues is an explicit constructor for a persisted/live value set.
func NewWithValues(values Values) Model { return New(values) }

// NewFromConfig seeds the modal from the canonical global config without
// giving the widget any persistence responsibility of its own.
func NewFromConfig(cfg *config.Config) Model {
	if cfg == nil {
		return New()
	}
	return New(Values{
		ResponseStyle: cfg.ActiveStylePolicy(),
		HideThinking:  cfg.UI.HideThinking,
		AutoScroll:    cfg.ActiveAutoScrollMode(),
	})
}

func normalizeValues(values Values) Values {
	style, err := prompt.ParseStylePolicy(string(values.ResponseStyle))
	if err != nil || style == "" {
		style = ResponseStyleBalanced
	}
	return Values{
		ResponseStyle: style,
		HideThinking:  values.HideThinking,
		AutoScroll:    config.NormalizeAutoScrollMode(string(values.AutoScroll)),
	}
}

// Init implements tea.Model. Settings has no timers, network calls, or other
// startup work.
func (m Model) Init() tea.Cmd { return nil }

// Values returns a copy of the current modal values.
func (m Model) Values() Values { return m.values }

// ResponseStyle returns the current response style.
func (m Model) ResponseStyle() ResponseStyle { return m.values.ResponseStyle }

// HideThinking returns the current CoT visibility preference.
func (m Model) HideThinking() bool { return m.values.HideThinking }

// HideThinkingBlocks is an explicit alias for HideThinking.
func (m Model) HideThinkingBlocks() bool { return m.values.HideThinking }

// AutoScroll returns the current viewport auto-scroll policy.
func (m Model) AutoScroll() AutoScrollMode { return m.values.AutoScroll }

// AutoScrollBehavior is an explicit alias for AutoScroll.
func (m Model) AutoScrollBehavior() AutoScrollMode { return m.values.AutoScroll }

// ActiveTab returns the highlighted horizontal tab.
func (m Model) ActiveTab() int { return m.activeTab }

// ActiveRow returns the highlighted setting in the active panel. The reduced
// schema currently has one row per panel, so this stays synchronized with the
// active tab while Up/Down navigation moves between panels.
func (m Model) ActiveRow() int { return m.activeRow }

// SetValues replaces the visible values without emitting a commit message.
func (m Model) SetValues(values Values) Model {
	m.values = normalizeValues(values)
	return m
}

// SetStatus sets a transient host-provided status line (for example, a
// persistence error). It does not change the schema.
func (m Model) SetStatus(status string) Model {
	m.status = status
	return m
}

// Status returns the transient status line.
func (m Model) Status() string { return m.status }

// Done reports whether the widget has been closed.
func (m Model) Done() bool { return m.closed }

// SetSize updates the outer modal bounds and derives the strict inner content
// bounds used for rendering. Non-positive dimensions are floored to one so a
// very small terminal cannot leave the stale default-sized modal overflowing
// the workspace.
func (m Model) SetSize(width, height int) Model {
	m.width = max(1, width)
	m.height = max(1, height)
	m.innerWidth = max(1, m.width-settingsModalHorizontalChrome)
	m.innerHeight = max(1, m.height-settingsModalVerticalChrome)
	return m
}

// Size returns the current outer modal bounds.
func (m Model) Size() (int, int) { return m.width, m.height }

// InnerSize returns the strict inner content bounds after border and padding
// are removed from the outer modal dimensions.
func (m Model) InnerSize() (int, int) { return m.innerWidth, m.innerHeight }

func (m *Model) normalizeActive() {
	if m.activeTab < 0 {
		m.activeTab = 0
	}
	if m.activeTab >= len(settingRowsByTab) {
		m.activeTab = len(settingRowsByTab) - 1
	}
	// Each reduced-schema panel owns exactly one row. Keep the public row
	// focus synchronized with the tab so rendering and value changes cannot
	// drift into another panel's field.
	m.activeRow = m.activeTab
}

// closeCmd returns the host-facing close event.
func closeCmd() tea.Cmd {
	return func() tea.Msg { return CloseMsg{} }
}

func commitCmd(field Field, values Values) tea.Cmd {
	return func() tea.Msg {
		return CommitMsg{Field: field, Values: values}
	}
}

func displayResponseStyle(style ResponseStyle) string {
	if style == "" {
		return "Balanced"
	}
	return strings.ToUpper(style.String()[:1]) + style.String()[1:]
}

func displayBool(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func displayAutoScroll(mode AutoScrollMode) string {
	switch config.NormalizeAutoScrollMode(string(mode)) {
	case AutoScrollAlways:
		return "Always"
	case AutoScrollOff:
		return "Off"
	default:
		return "Smart"
	}
}
