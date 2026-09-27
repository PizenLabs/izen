package model_picker

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// role_config.go — ROLE OPERATIONAL PARAMETERS, EDITED IN PLACE.
//
// # WHAT THESE PARAMETERS ARE, AND WHY THEY ARE NOT THE CHAIN
//
// The fallback chain (Alt+F, and the ROLES tree) answers "which model next".
// These parameters answer "when does the runtime give up on the current one,
// how many times, and what counts as giving up". They are PER ROLE because the
// right answer is per role: a plan turn can afford two 30-second retries on a
// provider that is merely rate limiting, and a commit turn cannot afford to wait
// at all.
//
// # WHY Alt+E AND NOT A BARE KEY
//
// Same argument as Alt+F, and the same reason it is a MODIFIER rather than a
// mnemonic: this widget's primary job is a fuzzy search field over a model
// catalog, so a bare rune is a query mutation. `e` is a letter a user types
// while looking for a model ("deepseek", "embed", …), and a bare `e` that opens
// a dialog instead of inserting a character is a search box that appears broken
// with no visible cause.
//
// # WHY THIS IS A MODAL AND NOT INLINE STEPPERS
//
// Inline steppers on the metadata line would be four more rows of chrome on a
// pane that already has a fixed height budget, and every one of them would be a
// key that can be pressed while a search query is being typed. A modal takes
// the whole surface, so a keypress inside it is unambiguously about the modal.
// The only keys it answers are ARROWS — navigation keys, which can never be
// confused for text — plus Enter and Esc.

// RoleParams is the picker-local read model of one role's operational
// parameters: value types, no pointers, no tri-state.
//
// The tri-state lives in internal/config (nil means "the documented default"),
// because that is where it has to be resolved against a file. By the time a
// parameter reaches this widget it has been resolved, and an editor that
// rendered a default as an empty cell would be asking the user to guess which
// of "off" and "unset" an absent field is.
type RoleParams struct {
	// MaxRetries is the fallback attempt budget on a triggering failure.
	MaxRetries int
	// TimeoutSeconds is the per-model call deadline in seconds; 0 means the
	// provider profile's own.
	TimeoutSeconds int
	// RateLimit advances the chain on HTTP 429.
	RateLimit bool
	// ServerError advances the chain on HTTP 5xx.
	ServerError bool
	// ContextLength advances the chain when the prompt exceeds the model's
	// context window.
	ContextLength bool
}

// roleParamsField selects one editable row of the overlay.
type roleParamsField int

const (
	roleFieldRetries roleParamsField = iota
	roleFieldTimeout
	roleFieldRateLimit
	roleFieldServerError
	roleFieldContextLength
	roleFieldCount
)

// roleFieldLabel is the row label, shared by the renderer and the increment
// messages so the two can never disagree about what is being edited.
var roleFieldLabel = [roleFieldCount]string{
	roleFieldRetries:       "Max Retries",
	roleFieldTimeout:       "Timeout (s)",
	roleFieldRateLimit:     "429 Rate Limit",
	roleFieldServerError:   "5xx Server Error",
	roleFieldContextLength: "Context Length",
}

// roleFieldIsToggle reports whether a field is a flag rather than a number.
// A toggle steps 0→1→0 and a number steps ±1, and conflating them is how a
// timeout ends up as "true".
func roleFieldIsToggle(f roleParamsField) bool {
	return f >= roleFieldRateLimit
}

// Bounds for the numeric fields. They are named rather than inlined at the step
// site because the METADATA LINE renders the same numbers and a bound that
// exists in only one of the two places is a bound the other one will exceed.
const (
	roleMaxRetriesMin     = 0
	roleMaxRetriesMax     = 10
	roleTimeoutSecondsMin = 0
	roleTimeoutSecondsMax = 600
)

// roleParamsOverlay is the open inline editor. It is nil while closed, which is
// the only state in which a browsing key means what it says.
//
// # WHY EVERY EDIT RETURNS A NEW OVERLAY
//
// The Model is a value type and the rest of this widget is careful never to
// mutate through a shared pointer — the snapshot, the chains and the role map
// are all copied on the way in and out, for the same reason. An overlay that
// edited its own fields in place would break that: two Model values derived from
// the same overlay would share a draft, so an "immutable" transition would
// quietly rewrite the value it was supposed to be compared against. So `moved`
// and `adjusted` take a value receiver and return a FRESH overlay.
type roleParamsOverlay struct {
	role  string
	field roleParamsField
	// draft is the STAGED value. It is a copy, so Esc is a real cancel: the
	// parameters the user was looking at before Alt+E are still the ones on
	// screen after Esc, and there is no "undo" step to explain.
	draft RoleParams
}

// RoleConfigActive reports whether the role-parameter editor is open. The
// parent uses it to keep Esc local to the editor rather than closing the modal.
func (m Model) RoleConfigActive() bool { return m.roleConfig != nil }

// RoleParamsFor returns the effective parameters for a role, seeded value or
// the documented default. It returns a copy.
func (m Model) RoleParamsFor(role string) RoleParams {
	if p, ok := m.roleParams[role]; ok {
		return p
	}
	return DefaultRoleParams()
}

// RoleParamsMap returns a defensive copy of every seeded role's parameters.
func (m Model) RoleParamsMap() map[string]RoleParams {
	out := make(map[string]RoleParams, len(m.roleParams))
	for k, v := range m.roleParams {
		out[k] = v
	}
	return out
}

// SetRoleParams seeds the picker-local role parameters from the parent's
// persisted config. It resets the unsaved marker for the same reason
// SetFallbackChains does: a re-seed is the parent saying "this is what is on
// disk", and any staged edit has just been overruled by an authoritative value.
func (m Model) SetRoleParams(params map[string]RoleParams) Model {
	m.roleParams = make(map[string]RoleParams, len(params))
	for role, p := range params {
		if role == "" {
			continue
		}
		m.roleParams[role] = p
	}
	m.roleParamsDirty = false
	return m
}

// RoleParamsDirty reports whether a parameter edit is staged but not persisted.
func (m Model) RoleParamsDirty() bool { return m.roleParamsDirty }

// DefaultRoleParams is the documented default parameter block. It lives here as
// well as in internal/config because the widget is a PURE VIEW with no config
// dependency, and a default the widget has to ask the parent for is a default
// that renders as a blank line on a cold picker that has never been seeded.
func DefaultRoleParams() RoleParams {
	return RoleParams{
		MaxRetries:     2,
		TimeoutSeconds: 0,
		RateLimit:      true,
		ServerError:    true,
		ContextLength:  false,
	}
}

// openRoleConfig opens the inline editor for the role the user is on: the
// highlighted ROLES-tree role when the roles pane is up, else the turn role
// implied by the active workspace. It returns the picker unchanged and a
// no-command when there is no role to edit, rather than opening an editor bound
// to a role nothing on screen names.
func (m Model) openRoleConfig() (Model, tea.Cmd) {
	roleKey := m.effectiveFallbackRole()
	if roleKey == "" {
		m.status = "No role to configure"
		return m, nil
	}
	// A fresh overlay, not a shared one: see the type comment.
	m.roleConfig = &roleParamsOverlay{role: roleKey, draft: m.RoleParamsFor(roleKey)}
	return m, nil
}

// handleRoleConfigKeys routes keys while the parameter editor is open.
//
// It answers ARROWS ONLY (plus Enter and Esc). Deliberately not k/j: they would
// work, and the overlay is modal so they cannot reach the search box — but a
// binding set that grows by exception is how a pane ends up with a bare key on
// it, and this one has a permanent reason not to have any.
func (m Model) handleRoleConfigKeys(msg tea.KeyMsg) (Model, tea.Cmd) {
	k := msg.String()
	switch k {
	case "esc":
		roleKey := m.roleConfig.role
		m.roleConfig = nil
		m.status = "Role parameters unchanged — " + roleKey
		return m, nil
	case "enter":
		return m.commitRoleConfig()
	case "up":
		m.roleConfig = m.roleConfig.moved(-1)
		return m, nil
	case "down":
		m.roleConfig = m.roleConfig.moved(1)
		return m, nil
	case "left", "right":
		delta := 1
		if k == "left" {
			delta = -1
		}
		m.roleConfig = m.roleConfig.adjusted(delta)
		return m, nil
	}
	return m, nil
}

// moved shifts the highlighted field, clamped to the field list. The clamp is
// silent rather than a wrap: a list of five fields that wraps is a list where
// ↑ from the top lands somewhere the user did not press toward.
func (o roleParamsOverlay) moved(delta int) *roleParamsOverlay {
	next := int(o.field) + delta
	if next < 0 {
		next = 0
	}
	if next >= int(roleFieldCount) {
		next = int(roleFieldCount) - 1
	}
	o.field = roleParamsField(next)
	return &o
}

// adjusted steps the highlighted field. Out-of-range steps are dropped rather
// than clamped, so holding → on Max Retries stops at 10 instead of pinning there
// and appearing to do nothing.
func (o roleParamsOverlay) adjusted(delta int) *roleParamsOverlay {
	if roleFieldIsToggle(o.field) {
		// A toggle ignores the direction: a boolean has no "up", and making
		// ←/→ mean different things for the two kinds of field is a row the
		// user has to read the label to use.
		switch o.field {
		case roleFieldRateLimit:
			o.draft.RateLimit = !o.draft.RateLimit
		case roleFieldServerError:
			o.draft.ServerError = !o.draft.ServerError
		case roleFieldContextLength:
			o.draft.ContextLength = !o.draft.ContextLength
		}
		return &o
	}
	switch o.field {
	case roleFieldRetries:
		next := o.draft.MaxRetries + delta
		if next < roleMaxRetriesMin || next > roleMaxRetriesMax {
			return &o
		}
		o.draft.MaxRetries = next
	case roleFieldTimeout:
		next := o.draft.TimeoutSeconds + delta
		if next < roleTimeoutSecondsMin || next > roleTimeoutSecondsMax {
			return &o
		}
		o.draft.TimeoutSeconds = next
	}
	return &o
}

// commitRoleConfig stages the edited parameters and closes the editor.
//
// It stages the same way the chain does — the widget holds the value, the
// parent writes the file — because config.Save rewrites the whole config and a
// parameter set that writes on every arrow press makes a user assembling four
// edits perform four full rewrites with no way back. The metadata line's
// unsaved marker is what tells the user the second Enter is the one that saves.
func (m Model) commitRoleConfig() (Model, tea.Cmd) {
	overlay := m.roleConfig
	if overlay == nil {
		return m, nil
	}
	roleKey := overlay.role
	m.roleConfig = nil
	if m.roleParams == nil {
		m.roleParams = make(map[string]RoleParams)
	}
	m.roleParams[roleKey] = overlay.draft
	m.roleParamsDirty = true
	m.roleParamsRole = roleKey
	m.status = fmt.Sprintf("Role parameters staged for %s — Enter saves, Esc discards", roleKey)
	return m, nil
}

// ApplyRoleParamsConfirm marks a confirmed parameter edit as persisted, so the
// unsaved marker clears. The parent calls it only after a SUCCESSFUL write.
func (m Model) ApplyRoleParamsConfirm() Model {
	m.roleParamsDirty = false
	m.roleParamsRole = ""
	return m
}

// EmitRoleParamsConfirm returns the persist request for a staged parameter
// edit, or nil when nothing is staged — so Enter on an untouched picker cannot
// rewrite the user's config file.
func (m Model) EmitRoleParamsConfirm() tea.Cmd {
	if !m.roleParamsDirty || m.roleParamsRole == "" {
		return nil
	}
	roleKey := m.roleParamsRole
	params := m.RoleParamsFor(roleKey)
	// Close over scalars, not the Model: the command runs on the Bubble Tea
	// goroutine after this transition returns, by which time the widget value
	// it was derived from may already have been replaced.
	payload := RoleParamsChangedMsg{Role: roleKey, Params: params}
	return func() tea.Msg { return payload }
}

// roleConfigTarget is the role a parameter edit applies to. It is the role the
// editor is open on, else the role whose parameters were last staged, else the
// surface's effective role — so the metadata line names the same role the
// confirm message will carry even between the stage and the confirm.
func (m Model) roleConfigTarget() string {
	if m.roleConfig != nil && m.roleConfig.role != "" {
		return m.roleConfig.role
	}
	if m.roleParamsRole != "" {
		return m.roleParamsRole
	}
	return m.effectiveFallbackRole()
}

// FormatRoleParams renders a parameter block's values: the budget, the deadline
// and the active triggers, in one line.
//
// It is EXPORTED and it is the ONLY renderer of that phrase, because the picker
// renders it on the metadata row and the parent renders it in the transcript
// line that confirms the write. Two formatters means two places for the timeout's
// "provider default" wording to drift, and a user who reads the confirmation and
// then the metadata row would see two different descriptions of the same edit.
//
// The timeout renders its MEANING, not just its number: 0 is a legitimate and
// common value that means "the provider profile's own deadline", and printing
// "0s" would read as "no timeout at all", which is a different and alarming
// claim.
func FormatRoleParams(p RoleParams) string {
	timeout := "provider default"
	if p.TimeoutSeconds > 0 {
		timeout = fmt.Sprintf("%ds", p.TimeoutSeconds)
	}
	triggers := make([]string, 0, 3)
	if p.RateLimit {
		triggers = append(triggers, "429")
	}
	if p.ServerError {
		triggers = append(triggers, "5xx")
	}
	if p.ContextLength {
		triggers = append(triggers, "ctx")
	}
	triggerText := "none"
	if len(triggers) > 0 {
		triggerText = strings.Join(triggers, ",")
	}
	return fmt.Sprintf("retries %d · timeout %s · triggers %s", p.MaxRetries, timeout, triggerText)
}

// renderRoleParamsLine is the metadata row: the active role's operational
// parameters, the key that edits them, and the unsaved marker.
//
// It sits directly under the chain line because the two answer one question
// together — "what happens when this model fails" — and a user reading them
// together sees the whole retry story, while a user reading them in different
// places sees two unrelated numbers.
func (m Model) renderRoleParamsLine() string {
	roleKey := m.roleConfigTarget()
	label := "Role Params:   " + roleKey + ":  "
	body := accentStyle.Render(FormatRoleParams(m.RoleParamsFor(roleKey)))
	marker := ""
	switch {
	case m.roleConfig != nil:
		marker = mutedStyle.Render(" *editing — Alt+E is open")
	case m.roleParamsDirty:
		marker = mutedStyle.Render(" *unsaved — Enter saves, Esc discards")
	default:
		marker = mutedStyle.Render(" · Alt+E edits")
	}
	return mutedStyle.Render(label) + body + marker
}

// renderRoleConfigOverlay draws the inline role-parameter editor: the role it
// edits, one row per field, the highlighted row marked, and the key hints.
//
// Every row shows its CURRENT value even when it is not the highlighted one.
// A five-row editor that only renders the selected field is a form the user has
// to remember, and a parameter the user cannot see is a parameter they will not
// change.
func (m Model) renderRoleConfigOverlay() string {
	innerW := m.innerWidth
	if innerW <= 0 {
		innerW = m.width
	}
	if innerW <= 0 {
		innerW = 64
	}
	innerH := m.innerHeight
	if innerH <= 0 {
		innerH = m.height
	}

	overlay := m.roleConfig
	title := accentStyle.Render("ROLE PARAMETERS — " + strings.ToUpper(overlay.role))
	lines := []string{title, ""}

	for f := roleParamsField(0); f < roleFieldCount; f++ {
		label := roleFieldLabel[f]
		if f == overlay.field {
			label = "▸ " + label
		}
		row := padRightExact(label, 22) + " " + roleFieldValue(*overlay, f)
		if f == overlay.field {
			lines = append(lines, roleChildFocusStyle.Render(row))
			continue
		}
		lines = append(lines, mutedStyle.Render(row))
	}

	lines = append(lines, "")
	lines = append(lines, mutedStyle.Render("↑/↓ field · ←/→ adjust · Enter stage · Esc cancel"))

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#cba6f7")).
		Width(max(16, innerW-4)).
		Padding(1, 2).
		Render(lipgloss.JoinVertical(lipgloss.Left, lines...))

	if innerH > 0 {
		boxLines := strings.Split(strings.ReplaceAll(box, "\r", ""), "\n")
		if len(boxLines) > innerH {
			boxLines = boxLines[:innerH]
		}
		box = strings.Join(boxLines, "\n")
	}
	return box
}

// roleFieldValue renders one field's current value: a number, or an explicit
// on/off.
//
// The toggles render their FULL name rather than a glyph because the rows are
// about which provider refusal advances the chain, and "on" next to "429 Rate
// Limit" says the same thing as a green dot and does not depend on the terminal
// having a colour palette this process knows about.
func roleFieldValue(o roleParamsOverlay, f roleParamsField) string {
	switch f {
	case roleFieldRetries:
		return fmt.Sprintf("%d", o.draft.MaxRetries)
	case roleFieldTimeout:
		if o.draft.TimeoutSeconds <= 0 {
			return "provider default (0)"
		}
		return fmt.Sprintf("%ds", o.draft.TimeoutSeconds)
	case roleFieldRateLimit:
		return onOff(o.draft.RateLimit)
	case roleFieldServerError:
		return onOff(o.draft.ServerError)
	case roleFieldContextLength:
		return onOff(o.draft.ContextLength)
	default:
		return "—"
	}
}

func onOff(v bool) string {
	if v {
		return "on"
	}
	return "off"
}
