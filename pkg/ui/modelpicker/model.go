// Package modelpicker provides the interactive TUI model picker component. It
// binds its effort selector dynamically to the highlighted model's
// ModelCapabilities.SupportedEfforts list — never to a hardcoded set — so the
// same component renders zero effort options for a plain chat model and the
// full auto/low/medium/high/xhigh set for a reasoning model.
//
// The model is a value type designed to be embedded in a larger Bubble Tea
// model; its state transitions are pure methods so the dynamic-effort binding
// contract is fully unit-testable without a terminal.
package modelpicker

import (
	"strings"

	"github.com/PizenLabs/izen/pkg/provider/capability"
)

// Selection is the resolved model + effort pair returned when the user
// confirms a choice.
type Selection struct {
	// Model is the highlighted model's capabilities.
	Model capability.ModelCapabilities
	// Effort is the currently selected effort level for that model.
	Effort capability.EffortLevel
}

// Model is the interactive model picker state.
type Model struct {
	// models is the full, unfiltered capability list.
	models []capability.ModelCapabilities
	// filtered is the list after the search filter is applied.
	filtered []capability.ModelCapabilities
	// filter is the current search query.
	filter string
	// cursor is the index of the highlighted model within filtered.
	cursor int
	// effortIdx is the index into the highlighted model's effort options.
	effortIdx int
	// Width and Height are the terminal bounds the View lays out within.
	Width  int
	Height int

	selected *Selection
	done     bool
}

// New returns an empty picker.
func New() Model {
	return Model{}
}

// SetModels replaces the picker's model list and resets navigation state. The
// cursor is anchored at the first model and the effort index is re-clamped to
// that model's supported efforts. Every record is normalized on ingest so the
// effort binding always reflects the derived capability set.
func (m Model) SetModels(models []capability.ModelCapabilities) Model {
	m.models = make([]capability.ModelCapabilities, 0, len(models))
	for _, c := range models {
		m.models = append(m.models, c.Normalize())
	}
	m.filtered = m.models
	m.filter = ""
	m.cursor = 0
	m.resetEffortForHighlight()
	return m
}

// Models returns a defensive copy of the full model list.
func (m Model) Models() []capability.ModelCapabilities {
	return append([]capability.ModelCapabilities(nil), m.models...)
}

// SetFilter narrows the visible models to those matching the query. The cursor
// is re-anchored at the first match.
func (m Model) SetFilter(query string) Model {
	m.filter = query
	m.filtered = filterModels(m.models, query)
	m.cursor = 0
	m.resetEffortForHighlight()
	return m
}

// Highlighted returns the capability record of the currently highlighted
// model, or nil when the list is empty.
func (m Model) Highlighted() *capability.ModelCapabilities {
	if len(m.filtered) == 0 || m.cursor < 0 || m.cursor >= len(m.filtered) {
		return nil
	}
	return &m.filtered[m.cursor]
}

// MoveCursor shifts the highlight by delta within the filtered list (negative
// moves up). Every navigation re-binds the effort selector to the newly
// highlighted model.
func (m Model) MoveCursor(delta int) Model {
	if len(m.filtered) == 0 {
		return m
	}
	next := m.cursor + delta
	if next < 0 {
		next = 0
	}
	if next >= len(m.filtered) {
		next = len(m.filtered) - 1
	}
	m.cursor = next
	m.resetEffortForHighlight()
	return m
}

// SetCursor moves the highlight to the given filtered-list index.
func (m Model) SetCursor(i int) Model {
	if len(m.filtered) == 0 {
		m.cursor = 0
		return m
	}
	if i < 0 {
		i = 0
	}
	if i >= len(m.filtered) {
		i = len(m.filtered) - 1
	}
	m.cursor = i
	m.resetEffortForHighlight()
	return m
}

// EffortOptions returns the dynamically bound effort list for the highlighted
// model: exactly ModelCapabilities.SupportedEfforts, or nil (zero options)
// when the model is a non-reasoning chat model.
func (m Model) EffortOptions() []capability.EffortLevel {
	hl := m.Highlighted()
	if hl == nil {
		return nil
	}
	return hl.EffortOptions()
}

// HasEffortOptions reports whether the highlighted model exposes a reasoning
// effort selector.
func (m Model) HasEffortOptions() bool {
	return len(m.EffortOptions()) > 0
}

// EffortCount returns the number of effort options for the highlighted model.
func (m Model) EffortCount() int {
	return len(m.EffortOptions())
}

// CurrentEffort returns the highlighted model's effort at the current effort
// index. When the model has no effort options it returns the zero effort
// (empty string), which the capability math treats as "auto".
func (m Model) CurrentEffort() capability.EffortLevel {
	opts := m.EffortOptions()
	if len(opts) == 0 {
		return ""
	}
	if m.effortIdx < 0 || m.effortIdx >= len(opts) {
		m.effortIdx = 0
		return opts[0]
	}
	return opts[m.effortIdx]
}

// SetEffort sets the effort selection for the highlighted model. An effort the
// model does not support is snapped to the nearest supported index.
func (m Model) SetEffort(effort capability.EffortLevel) Model {
	opts := m.EffortOptions()
	if len(opts) == 0 {
		m.effortIdx = 0
		return m
	}
	for i, o := range opts {
		if o == effort {
			m.effortIdx = i
			return m
		}
	}
	// Unsupported effort: keep the current index valid rather than inventing
	// a choice the model does not support.
	m.resetEffortForHighlight()
	return m
}

// SetEffortIndex sets the effort selection by index, clamped to the
// highlighted model's option count.
func (m Model) SetEffortIndex(i int) Model {
	n := m.EffortCount()
	if n == 0 {
		m.effortIdx = 0
		return m
	}
	if i < 0 {
		i = 0
	}
	if i >= n {
		i = n - 1
	}
	m.effortIdx = i
	return m
}

// EffortIndex returns the current effort index for the highlighted model.
func (m Model) EffortIndex() int { return m.effortIdx }

// MoveEffort shifts the effort selection by delta, clamped to the highlighted
// model's supported options.
func (m Model) MoveEffort(delta int) Model {
	n := m.EffortCount()
	if n == 0 {
		return m
	}
	return m.SetEffortIndex(m.effortIdx + delta)
}

// ThinkingBudget returns the thinking token budget for the highlighted model
// at its current effort. It is 0 for non-reasoning models and for auto effort.
func (m Model) ThinkingBudget() int {
	hl := m.Highlighted()
	if hl == nil {
		return 0
	}
	return hl.ThinkingBudget(m.CurrentEffort())
}

// TotalMaxTokens returns the total output token budget for the highlighted
// model at its current effort.
func (m Model) TotalMaxTokens() int {
	hl := m.Highlighted()
	if hl == nil {
		return 0
	}
	return hl.TotalMaxTokens(m.CurrentEffort())
}

// Select confirms the highlighted model at its current effort and resolves the
// picker. It returns the resolved picker model (carrying the recorded
// selection), the selection itself, and whether confirmation occurred.
func (m Model) Select() (Model, Selection, bool) {
	hl := m.Highlighted()
	if hl == nil {
		return m, Selection{}, false
	}
	m.selected = &Selection{Model: *hl, Effort: m.CurrentEffort()}
	m.done = true
	return m, *m.selected, true
}

// Done reports whether the picker resolved.
func (m Model) Done() bool { return m.done }

// Result returns the confirmed selection, or nil when the picker has not
// resolved.
func (m Model) Result() *Selection {
	if m.selected == nil {
		return nil
	}
	cp := *m.selected
	return &cp
}

// resetEffortForHighlight re-clamps the effort index to the highlighted
// model's option list. This is the synchronization invariant: switching models
// MUST rebind the effort selector to the new model's capabilities.
func (m *Model) resetEffortForHighlight() {
	n := m.EffortCount()
	if n == 0 {
		m.effortIdx = 0
		return
	}
	if m.effortIdx >= n {
		m.effortIdx = n - 1
	}
	if m.effortIdx < 0 {
		m.effortIdx = 0
	}
}

// filterModels returns the models whose ID, name, or provider contains every
// whitespace-separated query token (case-insensitive AND matching).
func filterModels(models []capability.ModelCapabilities, query string) []capability.ModelCapabilities {
	tokens := tokenize(query)
	if len(tokens) == 0 {
		return models
	}
	var out []capability.ModelCapabilities
	for _, m := range models {
		if matchAll(m, tokens) {
			out = append(out, m)
		}
	}
	return out
}

// tokenize splits a query into lowercased, deduplicated tokens.
func tokenize(query string) []string {
	if query == "" {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, f := range strings.Fields(query) {
		f = strings.ToLower(f)
		if f == "" || seen[f] {
			continue
		}
		seen[f] = true
		out = append(out, f)
	}
	return out
}

// matchAll reports whether every token appears within the model's searchable
// corpus.
func matchAll(m capability.ModelCapabilities, tokens []string) bool {
	corpus := strings.ToLower(m.Provider + " " + m.Name + " " + m.ModelID)
	for _, tok := range tokens {
		if !strings.Contains(corpus, tok) {
			return false
		}
	}
	return true
}
