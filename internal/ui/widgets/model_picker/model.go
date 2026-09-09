// Package model_picker is the Phase 3 contextual command surface TUI picker.
//
// Pure-view contract: zero I/O. The picker renders exclusively from the
// immutable *registry.ModelSnapshot (lock-free RAM read via Registry.Load at
// construction). It never touches the filesystem, network, or config store.
// Role bindings are emitted as tea.Cmd messages carrying
// modelapp.BindModelToRoleCommand (with monotonic Seq) for the top-level app
// layer to persist via ApplicationService.BindRole. State updates arrive via
// snapshot pointer replacement (SetSnapshot), BindingResultMsg confirmations,
// or non-mutating view models.
//
// Semantic separation:
//   - SELECT (up/down): changes focus/highlight in local TUI state.
//   - BIND (d/p/s/v/a): emits persistence command to assign model to role.
//   - ACTIVATE (Enter): emits runtime session command to execute with model.
package model_picker

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	modelapp "github.com/PizenLabs/izen/internal/app/model"
	"github.com/PizenLabs/izen/internal/domain/role"
	"github.com/PizenLabs/izen/internal/provider/registry"
)

// searchInputModel mirrors the task spec's TextInput lock: width is forced
// to searchInputWidth (16) on every render so typing never expands header.
type searchInputModel struct {
	Width   int
	focused bool
	value   string
}

// Focus marks the search input as focused.
func (s *searchInputModel) Focus() { s.focused = true }

// Blur marks the search input as blurred.
func (s *searchInputModel) Blur() { s.focused = false }

// Focused reports whether the search input is focused.
func (s searchInputModel) Focused() bool { return s.focused }

// Value returns the current input value.
func (s searchInputModel) Value() string { return s.value }

// SetValue sets the input value.
func (s *searchInputModel) SetValue(v string) { s.value = v }

// Update handles a key message for the search input (bubbles compatible shim).
func (s searchInputModel) Update(msg tea.KeyMsg) (searchInputModel, tea.Cmd) {
	switch msg.Type {
	case tea.KeyBackspace:
		if len(s.value) > 0 {
			r := []rune(s.value)
			s.value = string(r[:len(r)-1])
		}
	case tea.KeyRunes:
		s.value += string(msg.Runes)
	case tea.KeySpace:
		s.value += " "
	}
	return s, nil
}

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
// Seq matches the originating BindModelToRoleCommand; Seq==0 accepts legacy
// callers without sequence tracking.
type BindingSucceededMsg struct {
	Role    string
	ModelID string
	Seq     uint64
}

// BindingFailedMsg surfaces a persistence failure from the app layer.
// Seq matches the originating command; Seq==0 accepts legacy callers.
type BindingFailedMsg struct {
	Role string
	Err  error
	Seq  uint64
}

// PendingBind tracks one in-flight BIND round-trip for truth-over-fabrication
// rendering: saving... until the persistence authority confirms.
type PendingBind struct {
	ModelID string
	Seq     uint64
	State   string // "saving", "ok", "fail"
	ErrMsg  string
}

// Pending state constants for the bindings line.
const (
	PendingSaving = "saving"
	PendingOK     = "ok"
	PendingFail   = "fail"
)

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
	// only on persistence confirmation — never persisted here.
	roles map[string]string

	// focus selects key routing: FocusSearch routes printable runes into
	// the search query (zero-latency RAM filter); FocusList routes d/p/s/v/a
	// into role-binding command emission. Tab toggles. searchFocused is the
	// legacy bool mirror kept in sync for backward compatibility.
	focus         FocusScope
	searchFocused bool

	// state is the two-step picker state machine (browsing vs detail).
	state PickerState

	// activeWorkspace is the contextual workspace target for fast-path assignment.
	activeWorkspace WorkspaceTarget

	// targetCursor is the assignment drawer cursor in StateDetail.
	targetCursor int

	// reasoningPolicy is the workspace reasoning policy for assignment.
	reasoningPolicy string

	// reasoningIdx selects within the highlighted model's permitted options
	// (adapter.OptionsForMode). Fixed/None modes ignore it.
	reasoningIdx int
	// isGlobal selects the BindModelToRoleCommand target scope.
	isGlobal bool

	// seq is the monotonic BIND sequence counter. Every QueueBind bumps it
	// and stamps the emitted BindModelToRoleCommand.
	seq uint64
	// pending tracks the latest in-flight/confirmed state per role for the
	// BINDINGS line (saving... -> ok/fail). Never grants mutation authority:
	// roles mutate only on success confirmation.
	pending map[string]PendingBind

	// activated records the last ACTIVATE (Enter) target for the session
	// layer. Set on Enter alongside done.
	activatedModelID  string
	activatedProvider string

	loading bool
	err     error
	status  string

	width  int
	height int
	done   bool

	// searchInput mirrors the task spec's TextInput lock: width is forced
	// to searchInputWidth (16) on every render so typing never expands header.
	searchInput searchInputModel

	// Box-model geometry: outer modal dimensions and strict inner bounds.
	// W_inner = modalW - 4 (border 2 + padding 2), H_inner = modalH - 2 (border 2)
	modalW        int
	modalH        int
	innerWidth    int
	innerHeight   int
	listRowBudget int
}

// New builds a pure-view picker from an immutable snapshot. A nil snapshot
// yields an empty picker. Models populate instantly (no blocking fetch);
// Init returns nil.
func New(snap *registry.ModelSnapshot) Model {
	if snap == nil {
		snap = &registry.ModelSnapshot{}
	}
	m := Model{
		snap:            snap,
		roles:           make(map[string]string),
		pending:         make(map[string]PendingBind),
		focus:           FocusList,
		searchFocused:   false,
		searchInput:     searchInputModel{Width: searchInputWidth, focused: false},
		state:           StateBrowsing,
		activeWorkspace: TargetAsk,
		targetCursor:    0,
		reasoningPolicy: "default",
	}
	m.refilter()
	m.resetReasoning()
	return m
}

// NewFromRegistry builds a picker by reading the registry's current snapshot
// via the lock-free Load() (O(1) RAM, zero I/O). It stores only the snapshot
// pointer, never the registry itself. Cache-first: populates synchronously
// before any background sync goroutine is spawned; never blocks on network.
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

// Init implements tea.Model. Pure view: never performs I/O itself. On a
// cold start (zero snapshot models) it emits SyncRequestedMsg so the parent
// immediately pulls provider APIs in the background without blocking the TUI;
// populated pickers return nil. Fresh snapshots arrive as SnapshotMsg.
func (m Model) Init() tea.Cmd {
	if m.snap == nil || len(m.snap.Models) == 0 {
		return func() tea.Msg { return modelapp.SyncRequestedMsg{} }
	}
	return nil
}

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

// Pending returns a copy of the pending/confirmed bind states per role.
func (m Model) Pending() map[string]PendingBind {
	out := make(map[string]PendingBind, len(m.pending))
	for k, v := range m.pending {
		out[k] = v
	}
	return out
}

// PendingFor reports the pending state for one role.
func (m Model) PendingFor(roleName string) (PendingBind, bool) {
	pb, ok := m.pending[roleName]
	return pb, ok
}

// LastSeq reports the last emitted BIND sequence (0 = none yet).
func (m Model) LastSeq() uint64 { return m.seq }

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

// ProviderFilter returns the current provider filter.
func (m Model) ProviderFilter() string { return m.provider }

// SetSize adapts the picker to its modal/dialog bounds. It recalculates the
// list scrolling budget on the next render (via visibleWindow) so resize and
// split-pane events never clip text or break borders. Non-positive dimensions
// are floored to 1; cursor is clamped to the filtered list.
//
// Geometry (spec: inner/outer rectification):
//
//	W_inner = modalW - 4 (border 2 + padding 1+1)
//	H_inner = modalH - 2 (border 2, padding 0 vertical)
//
// Chrome = 6 lines (Title, Search, Divider, Reasoning, Bindings, Footer)
// listRowBudget = max(3, innerHeight - 6)
func (m Model) SetSize(w, h int) Model {
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	m.modalW = w
	m.modalH = h
	m.innerWidth = max(20, w-4)
	m.innerHeight = max(5, h-2)
	m.listRowBudget = max(3, m.innerHeight-6)
	// Legacy aliases: width/height now represent inner bounds for all
	// rendering helpers (clipLine, padFooter, render*).
	m.width = m.innerWidth
	m.height = m.innerHeight
	if m.cursor >= len(m.filtered) {
		m.cursor = len(m.filtered) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	m.clampScrollOffset()
	return m
}

func (m Model) clampScrollOffset() {
	// Cursor-following offset is derived dynamically in visibleWindow
	// from listRowBudget; no persistent offset field to clamp.
}

// Size reports the outer modal bounds (0 = unbounded). Kept for
// compatibility with callers that probe dialog size.
func (m Model) Size() (w, h int) {
	if m.modalW > 0 || m.modalH > 0 {
		return m.modalW, m.modalH
	}
	return m.width, m.height
}

// InnerSize reports the strict inner content bounds (modal - border/padding).
func (m Model) InnerSize() (w, h int) {
	if m.innerWidth > 0 || m.innerHeight > 0 {
		return m.innerWidth, m.innerHeight
	}
	return m.width, m.height
}

// Focus returns the current FocusScope.
func (m Model) Focus() FocusScope { return m.focus }

// FocusSearch routes printable runes into the search box.
func (m Model) FocusSearch() Model {
	m.focus = FocusSearch
	m.searchFocused = true
	m.searchInput.Focus()
	return m
}

// FocusList routes d/p/s/v/a into role-binding command emission.
func (m Model) FocusList() Model {
	m.focus = FocusList
	m.searchFocused = false
	m.searchInput.Blur()
	return m
}

// SearchFocused reports the current focus.
func (m Model) SearchFocused() bool { return m.focus == FocusSearch }

// State returns the current PickerState.
func (m Model) State() PickerState { return m.state }

// SetState sets the picker state.
func (m Model) SetState(s PickerState) Model { m.state = s; return m }

// ActiveWorkspace returns the active workspace context.
func (m Model) ActiveWorkspace() WorkspaceTarget { return m.activeWorkspace }

// SetActiveWorkspace sets the active workspace context.
func (m Model) SetActiveWorkspace(t WorkspaceTarget) Model { m.activeWorkspace = t; return m }

// TargetCursor returns the detail view target cursor index.
func (m Model) TargetCursor() int { return m.targetCursor }

// SetTargetCursor sets the detail view target cursor.
func (m Model) SetTargetCursor(i int) Model { m.targetCursor = i; return m }

// ReasoningPolicy returns the reasoning policy string.
func (m Model) ReasoningPolicy() string { return m.reasoningPolicy }

// ReasoningPolicyValue is an alias for ReasoningPolicy.
func (m Model) ReasoningPolicyValue() string { return m.reasoningPolicy }

// SetReasoningPolicy sets the reasoning policy.
func (m Model) SetReasoningPolicy(p string) Model { m.reasoningPolicy = p; return m }

// SelectedModel returns the highlighted model or nil (spec alias of Highlighted).
func (m Model) SelectedModel() *registry.ModelDescriptor { return m.Highlighted() }

// getInitialTargetIndex returns the cursor init position for the assignment drawer.
func (m Model) getInitialTargetIndex() int {
	for i, t := range AllWorkspaceTargets {
		if t == m.activeWorkspace {
			return i
		}
	}
	return 0
}

// isModelAssignedToTarget reports whether modelID is bound to target.
func (m Model) isModelAssignedToTarget(modelID string, target WorkspaceTarget) bool {
	if modelID == "" {
		return false
	}
	if bound, ok := m.roles[string(target)]; ok && bound != "" {
		return bound == modelID
	}
	return false
}

// cycleReasoningPolicy rotates through default/off/auto/on.
func (m *Model) cycleReasoningPolicy() {
	policies := []string{"default", "off", "auto", "on"}
	idx := 0
	for i, p := range policies {
		if p == m.reasoningPolicy {
			idx = i
			break
		}
	}
	m.reasoningPolicy = policies[(idx+1)%len(policies)]
}

// clearSearch empties the query and reapplies filter.
func (m *Model) clearSearch() {
	m.query = ""
	m.searchInput.SetValue("")
	m.searchInput.value = ""
	m.applyFilter()
}

// moveCursor shifts cursor clamped to filtered list (lowercase alias for MoveCursor).
func (m *Model) moveCursor(delta int) {
	*m = m.MoveCursor(delta)
}

// emitAssignmentCmd builds a ModelAssignmentRequestedMsg command.
func (m Model) emitAssignmentCmd(model *registry.ModelDescriptor, target WorkspaceTarget) tea.Cmd {
	if model == nil {
		return nil
	}
	policy := m.reasoningPolicy
	if policy == "" {
		policy = "default"
	}
	return func() tea.Msg {
		return ModelAssignmentRequestedMsg{
			ModelID:  model.ID,
			Provider: model.Provider,
			Target:   target,
			Policy:   InvocationPolicy{Reasoning: policy},
		}
	}
}

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

// Syncing is the spec-named alias of Loading: true while a Ctrl+R
// background refresh is in flight (⟳ syncing header state).
func (m Model) Syncing() bool { return m.loading }

// Err reports the last load/binding error.
func (m Model) Err() error { return m.err }

// Status reports the last binding message.
func (m Model) Status() string { return m.status }

// Done reports whether the picker resolved (enter pressed).
func (m Model) Done() bool { return m.done }

// ActivatedModelID reports the last ACTIVATE target (Enter), "" if none.
func (m Model) ActivatedModelID() string { return m.activatedModelID }

// ActivatedProvider reports the provider of the last ACTIVATE target.
func (m Model) ActivatedProvider() string { return m.activatedProvider }

// Providers returns the snapshot provider summaries (for the header line).
func (m Model) Providers() []registry.ProviderSummary {
	if m.snap == nil {
		return nil
	}
	return append([]registry.ProviderSummary(nil), m.snap.Providers...)
}

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

// Select marks the picker done on the highlighted model and records the
// ACTIVATE target. Pure: emits no command by itself; use EmitActivateCommand
// to dispatch the runtime session command.
func (m Model) Select() Model {
	hl := m.Highlighted()
	if hl == nil {
		return m
	}
	m.done = true
	m.activatedModelID = hl.ID
	m.activatedProvider = hl.Provider
	return m
}

// BuildBindCommand constructs the domain command for binding the highlighted
// model to roleName. It carries model ID, provider, the selected reasoning
// effort option (nil for Fixed/None modes), target scope, and the latest Seq
// (call QueueBind for a fresh sequence). Pure: no I/O.
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
		Seq:       m.seq,
	}, true
}

// EmitBindCommand returns a non-blocking tea.Cmd yielding the domain command
// for the highlighted model using the current Seq. The parent routes it to
// ApplicationService. Nil highlighted model yields nil (no-op).
func (m Model) EmitBindCommand(roleName string) tea.Cmd {
	cmd, ok := m.BuildBindCommand(roleName)
	if !ok {
		return nil
	}
	return func() tea.Msg { return cmd }
}

// QueueBind stamps a fresh Seq, records the saving... pending state (no
// persistence, no badge mutation), and returns the emission command carrying
// the same Seq. Badges update only on persistence confirmation.
func (m Model) QueueBind(roleName string) (Model, tea.Cmd) {
	hl := m.Highlighted()
	if hl == nil {
		m.status = "no model highlighted"
		return m, nil
	}
	m.seq++
	if m.pending == nil {
		m.pending = make(map[string]PendingBind)
	}
	m.pending[roleName] = PendingBind{ModelID: hl.ID, Seq: m.seq, State: PendingSaving}
	scope := "local"
	if m.isGlobal {
		scope = "global"
	}
	// Keep the legacy "queued bind" prefix for backward compatibility while
	// surfacing the Phase 3 saving... transition truthfully.
	m.status = fmt.Sprintf("queued bind %s → %s (%s) · saving...", roleName, hl.ID, scope)
	cmd := modelapp.BindModelToRoleCommand{
		Role:      role.Role(roleName),
		ModelID:   hl.ID,
		Provider:  hl.Provider,
		Reasoning: m.CurrentReasoningSelection(),
		IsGlobal:  m.isGlobal,
		Seq:       m.seq,
	}
	return m, func() tea.Msg { return cmd }
}

// BuildActivateCommand constructs the runtime session command for the
// highlighted model (ACTIVATE on Enter). Pure: no I/O.
func (m Model) BuildActivateCommand() (modelapp.ActivateModelCommand, bool) {
	hl := m.Highlighted()
	if hl == nil {
		return modelapp.ActivateModelCommand{}, false
	}
	return modelapp.ActivateModelCommand{
		ModelID:   hl.ID,
		Provider:  hl.Provider,
		Reasoning: m.CurrentReasoningSelection(),
	}, true
}

// EmitActivateCommand returns a tea.Cmd yielding ActivateModelCommand.
func (m Model) EmitActivateCommand() tea.Cmd {
	cmd, ok := m.BuildActivateCommand()
	if !ok {
		return nil
	}
	return func() tea.Msg { return cmd }
}

// applyBindSuccess applies a persistence confirmation for role. Stale Seqs
// (older than the latest pending for that role) are ignored. Returns the
// updated model.
func (m Model) applyBindSuccess(roleName, modelID string, seq uint64) Model {
	if m.roles == nil {
		m.roles = make(map[string]string)
	}
	if m.pending == nil {
		m.pending = make(map[string]PendingBind)
	}
	if pb, ok := m.pending[roleName]; ok && seq != 0 && seq < pb.Seq {
		return m // stale confirmation: ignore
	}
	m.roles[roleName] = modelID
	m.pending[roleName] = PendingBind{ModelID: modelID, Seq: seq, State: PendingOK}
	m.refreshBadges()
	m.status = fmt.Sprintf("bind %s → %s ✓", roleName, modelID)
	m.err = nil
	return m
}

// applyBindFailure records a persistence failure without mutating roles
// (truth over fabrication: revert to prior binding, show transient error).
// Stale Seqs are ignored.
func (m Model) applyBindFailure(roleName string, seq uint64, bindErr error) Model {
	if m.pending == nil {
		m.pending = make(map[string]PendingBind)
	}
	if pb, ok := m.pending[roleName]; ok && seq != 0 && seq < pb.Seq {
		return m // stale failure: ignore
	}
	prev := ""
	if m.pending[roleName].ModelID != "" {
		prev = m.pending[roleName].ModelID
	} else if bound, ok := m.roles[roleName]; ok {
		prev = bound
	}
	msg := ""
	if bindErr != nil {
		msg = bindErr.Error()
		m.err = bindErr
	}
	m.pending[roleName] = PendingBind{ModelID: prev, Seq: seq, State: PendingFail, ErrMsg: msg}
	switch {
	case prev != "":
		m.status = fmt.Sprintf("bind %s → %s ✕ %s", roleName, prev, msg)
	case bindErr != nil:
		m.status = fmt.Sprintf("bind %s failed: %v ✕", roleName, bindErr)
	default:
		m.status = fmt.Sprintf("bind %s failed ✕", roleName)
	}
	return m
}

// refilter recomputes the filtered list against the snapshot RAM only
// (zero I/O) using the deterministic multi-field token matcher shared with
// Registry.Filter (ID, context, provider, price, capabilities).
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

// applyFilter is the spec-named alias of refilter used by the FocusScope
// state machine after search input updates. It rebuilds the filtered view and
// resets reasoning selection.
func (m *Model) applyFilter() {
	m.refilter()
	m.resetReasoning()
}

// refreshBadges is a no-op anchor: badges derive live from m.roles +
// classifier in View, so no cached state needs rebuilding. It exists to make
// the badge-update dataflow explicit at binding-confirmation time.
func (m *Model) refreshBadges() {}

// filterLocal mirrors Registry.Filter for snapshot slices via the shared
// MatchesQuery matcher (multi-field tokens) plus exact provider match.
// Zero I/O.
func filterLocal(models []registry.ModelDescriptor, query, providerFilter string) []registry.ModelDescriptor {
	pf := strings.ToLower(strings.TrimSpace(query))
	_ = pf
	prov := strings.ToLower(strings.TrimSpace(providerFilter))
	out := make([]registry.ModelDescriptor, 0, len(models))
	for _, d := range models {
		if prov != "" && strings.ToLower(d.Provider) != prov {
			continue
		}
		if registry.MatchesQuery(d, query) {
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
