// Package model_picker is the Phase 2 pure-view TUI model picker.
//
// Pure-view contract: zero I/O. The picker renders exclusively from the
// immutable *registry.ModelSnapshot (lock-free RAM read via Registry.Load at
// construction). It never touches the filesystem, network, or config store.
// Role bindings are emitted as tea.Cmd messages carrying
// modelapp.BindModelToRoleCommand for the top-level app layer to persist via
// ApplicationService.BindRole. State updates arrive via snapshot pointer
// replacement (SetSnapshot) or non-mutating view models.
package model_picker

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	modelapp "github.com/PizenLabs/izen/internal/app/model"
	"github.com/PizenLabs/izen/internal/domain/role"
	"github.com/PizenLabs/izen/internal/provider/registry"
)

// Role hotkeys.
const (
	RoleDefaultKey = "d"
	RolePlanKey    = "p"
	RoleSmolKey    = "s"
	RoleVisionKey  = "v"
	RoleAdviserKey = "a"
	// ScopeToggleKey flips local vs global binding scope.
	ScopeToggleKey = "g"
)

// ModelsLoadedMsg carries a fresh RAM snapshot into Update (sent by the
// parent after background sync; the picker itself never fetches).
type ModelsLoadedMsg struct {
	Models []registry.ModelDescriptor
}

// SnapshotMsg carries a snapshot pointer replacement into Update. It is the
// preferred update path: immutable pointer swap, no mutation, no I/O.
type SnapshotMsg struct {
	Snap *registry.ModelSnapshot
}

// ModelsErrMsg carries a background load failure into Update.
type ModelsErrMsg struct {
	Err error
}

// BindingSucceededMsg confirms a role binding persisted by the app layer.
// The picker applies it to its badge view model (snapshot untouched).
type BindingSucceededMsg struct {
	Role    string
	ModelID string
}

// BindingFailedMsg surfaces a persistence failure from the app layer.
type BindingFailedMsg struct {
	Role string
	Err  error
}

// Model is the picker state. It is a value type designed for embedding in a
// parent Bubble Tea model; use UpdateModel for the typed transition.
//
// snap is treated as read-only: never mutate its slices. filtered is a
// derived view slice rebuilt on every query/provider/cursor/snapshot change.
type Model struct {
	snap *registry.ModelSnapshot

	filtered []registry.ModelDescriptor
	query    string
	provider string
	cursor   int

	// roles maps role name -> bound model ID for badge rendering. It is a
	// non-mutating view model seeded by the parent (SetRoles) and updated
	// only on BindingSucceededMsg — never persisted here.
	roles map[string]string

	// searchFocused selects key routing: true routes printable runes into
	// the search query (zero-latency RAM filter); false routes d/p/s/v/a
	// into role-binding command emission. Tab toggles.
	searchFocused bool

	// reasoningIdx selects within the highlighted model's permitted options
	// (adapter.OptionsForMode). Fixed/None modes ignore it.
	reasoningIdx int
	// isGlobal selects the BindModelToRoleCommand target scope.
	isGlobal bool

	loading bool
	err     error
	status  string

	width  int
	height int
	done   bool
}

// New builds a pure-view picker from an immutable snapshot. A nil snapshot
// yields an empty picker. Models populate instantly (no blocking fetch);
// Init returns nil.
func New(snap *registry.ModelSnapshot) Model {
	if snap == nil {
		snap = &registry.ModelSnapshot{}
	}
	m := Model{
		snap:          snap,
		roles:         make(map[string]string),
		searchFocused: true,
	}
	m.refilter()
	m.resetReasoning()
	return m
}

// NewFromRegistry builds a picker by reading the registry's current snapshot
// via the lock-free Load() (O(1) RAM, zero I/O). It stores only the snapshot
// pointer, never the registry itself.
func NewFromRegistry(reg *registry.Registry) Model {
	if reg == nil {
		return New(nil)
	}
	return New(reg.Load())
}

// NewWithRoles additionally seeds role badges (e.g. from CascadeConfig.Roles).
func NewWithRoles(snap *registry.ModelSnapshot, roles map[string]string) Model {
	m := New(snap)
	m.roles = cloneRoles(roles)
	return m
}

// NewFromRegistryWithRoles seeds badges while reading from Registry.Load().
func NewFromRegistryWithRoles(reg *registry.Registry, roles map[string]string) Model {
	m := NewFromRegistry(reg)
	m.roles = cloneRoles(roles)
	return m
}

// Init implements tea.Model. Pure view: never performs I/O, never dispatches
// background loads. Background sync is owned by the app layer via
// ApplicationService.RefreshRegistry; fresh snapshots arrive as SnapshotMsg.
func (m Model) Init() tea.Cmd { return nil }

// SetSnapshot replaces the snapshot pointer (immutable swap) and rebuilds the
// derived filtered view. The old snapshot is never mutated.
func (m Model) SetSnapshot(snap *registry.ModelSnapshot) Model {
	if snap == nil {
		snap = &registry.ModelSnapshot{}
	}
	m.snap = snap
	m.refilter()
	m.resetReasoning()
	return m
}

// SetRoles replaces the badge map (defensive copy).
func (m Model) SetRoles(roles map[string]string) Model {
	m.roles = cloneRoles(roles)
	m.refreshBadges()
	return m
}

// Roles returns a copy of the current role bindings.
func (m Model) Roles() map[string]string {
	return cloneRoles(m.roles)
}

// SetQuery replaces the search query and re-filters against RAM.
func (m Model) SetQuery(q string) Model {
	m.query = q
	m.refilter()
	m.resetReasoning()
	return m
}

// Query returns the current search query.
func (m Model) Query() string { return m.query }

// SetProviderFilter narrows results to one provider ("" = all).
func (m Model) SetProviderFilter(p string) Model {
	m.provider = p
	m.refilter()
	m.resetReasoning()
	return m
}

// FocusSearch routes printable runes into the search box.
func (m Model) FocusSearch() Model { m.searchFocused = true; return m }

// FocusList routes d/p/s/v/a into role-binding command emission.
func (m Model) FocusList() Model { m.searchFocused = false; return m }

// SearchFocused reports the current focus.
func (m Model) SearchFocused() bool { return m.searchFocused }

// IsGlobal reports the binding target scope.
func (m Model) IsGlobal() bool { return m.isGlobal }

// SetScope sets the binding target scope (false = local, true = global).
func (m Model) SetScope(global bool) Model { m.isGlobal = global; return m }

// ToggleScope flips local vs global.
func (m Model) ToggleScope() Model { m.isGlobal = !m.isGlobal; return m }

// ReasoningIndex reports the raw reasoning option index.
func (m Model) ReasoningIndex() int { return m.reasoningIdx }

// Models returns the full snapshot list (defensive copy; snapshot untouched).
func (m Model) Models() []registry.ModelDescriptor {
	if m.snap == nil {
		return nil
	}
	return append([]registry.ModelDescriptor(nil), m.snap.Models...)
}

// Filtered returns the current filtered list (defensive copy).
func (m Model) Filtered() []registry.ModelDescriptor {
	return append([]registry.ModelDescriptor(nil), m.filtered...)
}

// Highlighted returns the cursor model, or nil when empty.
func (m Model) Highlighted() *registry.ModelDescriptor {
	if len(m.filtered) == 0 || m.cursor < 0 || m.cursor >= len(m.filtered) {
		return nil
	}
	return &m.filtered[m.cursor]
}

// Cursor returns the cursor index.
func (m Model) Cursor() int { return m.cursor }

// Loading reports whether the background load is pending.
func (m Model) Loading() bool { return m.loading }

// Err reports the last load/binding error.
func (m Model) Err() error { return m.err }

// Status reports the last binding message.
func (m Model) Status() string { return m.status }

// Done reports whether the picker resolved (enter pressed).
func (m Model) Done() bool { return m.done }

// MoveCursor shifts the highlight, clamped to the filtered list.
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
	m.resetReasoning()
	return m
}

// SetCursor jumps to an index, clamped to the filtered list.
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
	m.resetReasoning()
	return m
}

// Select marks the picker done on the highlighted model.
func (m Model) Select() Model {
	if m.Highlighted() == nil {
		return m
	}
	m.done = true
	return m
}

// BuildBindCommand constructs the domain command for binding the highlighted
// model to roleName. It carries model ID, provider, the selected reasoning
// effort option (nil for Fixed/None modes), and target scope. Pure: no I/O.
func (m Model) BuildBindCommand(roleName string) (modelapp.BindModelToRoleCommand, bool) {
	hl := m.Highlighted()
	if hl == nil {
		return modelapp.BindModelToRoleCommand{}, false
	}
	return modelapp.BindModelToRoleCommand{
		Role:      role.Role(roleName),
		ModelID:   hl.ID,
		Provider:  hl.Provider,
		Reasoning: m.CurrentReasoningSelection(),
		IsGlobal:  m.isGlobal,
	}, true
}

// EmitBindCommand returns a non-blocking tea.Cmd yielding the domain command
// for the highlighted model. The parent routes it to ApplicationService.
// Nil highlighted model yields nil (no-op).
func (m Model) EmitBindCommand(roleName string) tea.Cmd {
	cmd, ok := m.BuildBindCommand(roleName)
	if !ok {
		return nil
	}
	return func() tea.Msg { return cmd }
}

// QueueBind records a queued-bind status (no persistence, no badge mutation)
// and returns the emission command. Badges update only on
// BindingSucceededMsg from the app layer.
func (m Model) QueueBind(roleName string) (Model, tea.Cmd) {
	hl := m.Highlighted()
	if hl == nil {
		m.status = "no model highlighted"
		return m, nil
	}
	scope := "local"
	if m.isGlobal {
		scope = "global"
	}
	m.status = fmt.Sprintf("queued bind %s → %s (%s)", roleName, hl.ID, scope)
	return m, m.EmitBindCommand(roleName)
}

// refilter recomputes the filtered list against the snapshot RAM only
// (zero I/O). Filtering mirrors Registry.Filter semantics: case-insensitive
// substring on ID+Name with exact provider match.
func (m *Model) refilter() {
	var src []registry.ModelDescriptor
	if m.snap != nil {
		src = m.snap.Models
	}
	m.filtered = filterLocal(src, m.query, m.provider)
	if m.cursor >= len(m.filtered) {
		m.cursor = len(m.filtered) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

// refreshBadges is a no-op anchor: badges derive live from m.roles +
// classifier in View, so no cached state needs rebuilding. It exists to make
// the badge-update dataflow explicit at binding-confirmation time.
func (m *Model) refreshBadges() {}

// filterLocal mirrors Registry.Filter for snapshot slices: case-insensitive
// substring on ID+Name with exact provider match. Zero I/O.
func filterLocal(models []registry.ModelDescriptor, query, providerFilter string) []registry.ModelDescriptor {
	q := strings.ToLower(strings.TrimSpace(query))
	pf := strings.ToLower(strings.TrimSpace(providerFilter))
	out := make([]registry.ModelDescriptor, 0, len(models))
	for _, d := range models {
		if pf != "" && strings.ToLower(d.Provider) != pf {
			continue
		}
		if q == "" {
			out = append(out, d)
			continue
		}
		if strings.Contains(strings.ToLower(d.ID), q) || strings.Contains(strings.ToLower(d.Name), q) {
			out = append(out, d)
		}
	}
	return out
}

// BadgesFor renders the badge set for one descriptor: role bindings
// ([DEFAULT]/[PLAN]/[SMOL]/[VISION-role]/[ADVISER]) plus classifier-derived
// capability badges ([THINKING]/[VISION]). It is exported for tests.
func BadgesFor(d registry.ModelDescriptor, roles map[string]string) []string {
	var badges []string
	for roleName, boundID := range roles {
		if boundID != "" && (boundID == d.ID || strings.EqualFold(boundID, d.ID)) {
			badges = append(badges, "["+strings.ToUpper(roleName)+"]")
		}
	}
	if role.EffectiveIsThinking(d) {
		badges = append(badges, "[THINKING]")
	}
	caps := role.EffectiveCapabilities(d)
	if role.HasCapability(caps, registry.CapVision) {
		// Avoid doubling when the vision role itself is bound: the role
		// badge already signals assignment; the capability badge signals
		// vision support.
		hasVisionRole := false
		for _, b := range badges {
			if b == "[VISION]" {
				hasVisionRole = true
				break
			}
		}
		if !hasVisionRole {
			badges = append(badges, "[VISION]")
		}
	}
	return badges
}

func cloneRoles(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

var (
	titleStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#cba6f7"))
	mutedStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#6c7086"))
	accentStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#f5a623")).Bold(true)
	defaultBadge = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#a6e3a1"))
	planBadge    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#89b4fa"))
	thinkBadge   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#cba6f7"))
	visionBadge  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#89dceb"))
	otherBadge   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#f5a623"))
	errStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#f38ba8"))
)
