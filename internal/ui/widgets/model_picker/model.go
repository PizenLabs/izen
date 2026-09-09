// Package model_picker is the Phase 2 non-blocking TUI model picker.
//
// It consumes the in-memory provider registry without blocking the Bubble Tea
// event loop: Init dispatches a tea.Cmd that loads the disk cache in the
// background, search filters via Registry.Filter directly against the RAM
// slice (zero disk/network I/O per keystroke), and role hotkeys (d/p/s)
// persist bindings to local .izen/config.json with inline badge updates.
package model_picker

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/PizenLabs/izen/internal/domain/role"
	"github.com/PizenLabs/izen/internal/provider/registry"
)

// Role hotkeys.
const (
	RoleDefaultKey = "d"
	RolePlanKey    = "p"
	RoleSmolKey    = "s"
)

// ModelsLoadedMsg carries the post-load RAM snapshot into Update.
type ModelsLoadedMsg struct {
	Models []registry.ModelDescriptor
}

// ModelsErrMsg carries a background load failure into Update.
type ModelsErrMsg struct {
	Err error
}

// Model is the picker state. It is a value type designed for embedding in a
// parent Bubble Tea model; use UpdateModel for the typed transition.
type Model struct {
	reg     *registry.Registry
	workDir string

	models   []registry.ModelDescriptor
	filtered []registry.ModelDescriptor
	query    string
	provider string
	cursor   int

	// roles maps role name -> bound model ID for badge rendering. It mirrors
	// cfg.Roles and is updated synchronously on every successful BindRole.
	roles map[string]string

	// searchFocused selects key routing: true routes printable runes into
	// the search query (zero-latency Filter); false routes d/p/s into role
	// binding. Tab toggles. This keeps continuous typing free of hotkey
	// collisions (typing "plan" never rebinds the plan role).
	searchFocused bool

	loading bool
	err     error
	status  string

	width  int
	height int
	done   bool
}

// New builds a picker bound to reg (may be nil for pure-local use) and
// workDir (role-binding persistence root; "" disables persistence).
// The picker starts search-focused and in loading state; call Init to dispatch
// the background cache load.
func New(reg *registry.Registry, workDir string) Model {
	return Model{
		reg:           reg,
		workDir:       workDir,
		roles:         make(map[string]string),
		searchFocused: true,
		loading:       true,
	}
}

// NewWithRoles additionally seeds role badges (e.g. from CascadeConfig.Roles).
func NewWithRoles(reg *registry.Registry, workDir string, roles map[string]string) Model {
	m := New(reg, workDir)
	m.roles = cloneRoles(roles)
	return m
}

// Init implements tea.Model. It MUST NOT perform blocking disk/network I/O:
// it dispatches a tea.Cmd that loads the cached models off the main UI
// thread. A nil registry yields a nil command (nothing to load).
func (m Model) Init() tea.Cmd {
	if m.reg == nil {
		return nil
	}
	reg := m.reg
	return func() tea.Msg {
		if err := reg.LoadCache(); err != nil {
			return ModelsErrMsg{Err: err}
		}
		return ModelsLoadedMsg{Models: reg.Snapshot()}
	}
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
	return m
}

// Query returns the current search query.
func (m Model) Query() string { return m.query }

// SetProviderFilter narrows results to one provider ("" = all).
func (m Model) SetProviderFilter(p string) Model {
	m.provider = p
	m.refilter()
	return m
}

// FocusSearch routes printable runes into the search box.
func (m Model) FocusSearch() Model { m.searchFocused = true; return m }

// FocusList routes d/p/s into role binding.
func (m Model) FocusList() Model { m.searchFocused = false; return m }

// SearchFocused reports the current focus.
func (m Model) SearchFocused() bool { return m.searchFocused }

// Models returns the full loaded list (defensive copy).
func (m Model) Models() []registry.ModelDescriptor {
	return append([]registry.ModelDescriptor(nil), m.models...)
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

// BindHighlighted binds the highlighted model to the given role, persists it
// to <workDir>/.izen/config.json, and refreshes badges. It returns the updated
// model and any persistence error (the in-memory badge still updates on
// error so the UI reflects intent; Status carries the failure).
func (m Model) BindHighlighted(roleName string) (Model, error) {
	hl := m.Highlighted()
	if hl == nil {
		m.status = "no model highlighted"
		return m, fmt.Errorf("model_picker: no highlighted model to bind as %q", roleName)
	}
	m.roles[roleName] = hl.ID
	if err := SaveRoleBinding(m.workDir, roleName, hl.ID); err != nil {
		m.status = fmt.Sprintf("bind %s → %s failed: %v", roleName, hl.ID, err)
		m.refreshBadges()
		return m, err
	}
	m.status = fmt.Sprintf("bind %s → %s", roleName, hl.ID)
	m.refreshBadges()
	return m, nil
}

// refilter recomputes the filtered list against RAM only. When a registry is
// attached it calls Registry.Filter (RLock, zero I/O) so every keystroke is a
// sub-millisecond slice scan at 60fps; otherwise it filters the local snapshot
// with the same semantics.
func (m *Model) refilter() {
	var out []registry.ModelDescriptor
	switch {
	case m.reg != nil && m.models != nil:
		// Prefer the live registry so background syncs are visible; Filter
		// never touches disk.
		live := m.reg.Filter(m.query, m.provider)
		// Intersect with the loaded generation guard: when the picker has
		// never loaded (models == nil), live is authoritative.
		out = live
	case m.reg != nil:
		out = m.reg.Filter(m.query, m.provider)
	default:
		out = filterLocal(m.models, m.query, m.provider)
	}
	m.filtered = out
	if m.cursor >= len(m.filtered) {
		m.cursor = len(m.filtered) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

// refreshBadges is a no-op anchor: badges derive live from m.roles +
// classifier in View, so no cached state needs rebuilding. It exists to make
// the badge-update dataflow explicit at binding time.
func (m *Model) refreshBadges() {}

// filterLocal mirrors Registry.Filter for nil-registry use (tests/embedding
// without a live registry): case-insensitive substring on ID+Name with exact
// provider match.
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

// SaveRoleBinding persists role → modelID into <workDir>/.izen/config.json,
// preserving all other keys. A missing file starts from an empty object; an
// empty workDir disables persistence and returns an error.
func SaveRoleBinding(workDir, roleName, modelID string) error {
	if strings.TrimSpace(workDir) == "" {
		return fmt.Errorf("model_picker: empty workDir, role binding not persisted")
	}
	if strings.TrimSpace(roleName) == "" {
		return fmt.Errorf("model_picker: empty role name")
	}
	dir := filepath.Join(workDir, ".izen")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("model_picker: mkdir %s: %w", dir, err)
	}
	path := filepath.Join(dir, "config.json")
	var root map[string]json.RawMessage
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &root) // corrupt file: start over with roles
	}
	if root == nil {
		root = make(map[string]json.RawMessage)
	}
	var roles map[string]string
	if raw, ok := root["roles"]; ok {
		_ = json.Unmarshal(raw, &roles)
	}
	if roles == nil {
		roles = make(map[string]string)
	}
	roles[roleName] = modelID
	encoded, err := json.Marshal(roles)
	if err != nil {
		return fmt.Errorf("model_picker: marshal roles: %w", err)
	}
	root["roles"] = encoded
	data, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return fmt.Errorf("model_picker: marshal config: %w", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("model_picker: write %s: %w", path, err)
	}
	return nil
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
