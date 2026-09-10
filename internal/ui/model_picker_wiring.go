package ui

import (
	"context"
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	modelapp "github.com/PizenLabs/izen/internal/app/model"
	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/provider/discovery"
	"github.com/PizenLabs/izen/internal/provider/registry"
	"github.com/PizenLabs/izen/internal/runtime/authority"
	model_picker "github.com/PizenLabs/izen/internal/ui/widgets/model_picker"
)

// commitModelAssignment runs the atomic control-plane transaction pipeline
// for a ModelAssignmentRequestedMsg. It is shared by the modal-open fast path
// (internal/ui/update.go picker routing) and the modal-closed fallback path
// (main switch), so an assignment can never be silently dropped regardless of
// delivery order:
//
//  1. Validate binding via authority.ValidateBinding.
//  2. Persist must succeed before runtime commit (abort w/o mutate).
//  3. Commit is deterministic (no validation failures).
//  4. Transcript logs event.ToTranscriptLog() as a system message.
//  5. Viewport re-renders from ActiveBinding() and scrolls to bottom.
//  6. Modal tears down deterministically via CloseModalCmd.
//
// msg.ModelID/Provider flow straight from the event payload into
// ValidateBinding and PersistActiveBinding: no fallback layer overrides the
// assigned model with a default.
func (m *model) commitModelAssignment(msg model_picker.ModelAssignmentRequestedMsg) tea.Cmd {
	auth := m.ensureModelAuthority()
	binding := authority.ModelBinding{
		ProviderID: authority.ProviderID(msg.Provider),
		ModelID:    authority.ModelID(msg.ModelID),
	}
	if err := authority.ValidateBinding(binding); err != nil {
		m.push(roleError, fmt.Sprintf("[✗] Model assignment rejected: %s", err.Error()))
		m.refreshViewportContent()
		m.gotoBottomIfAllowed()
		return nil
	}
	if err := config.PersistActiveBinding(msg.Provider, msg.ModelID, string(msg.Policy.Reasoning)); err != nil {
		m.push(roleError, fmt.Sprintf("[✗] Model assignment persist failed: %s", err.Error()))
		m.refreshViewportContent()
		m.gotoBottomIfAllowed()
		return nil
	}
	auth.Activate(binding)
	// Sync pipeline intent tiers to the new binding.
	m.syncPipelineTiers()
	// Provider switch if needed.
	var followCmd tea.Cmd
	if msg.Provider != "" {
		followCmd = m.switchProviderIfNeeded(msg.Provider)
	}
	m.push(roleSystem, fmt.Sprintf("✓ Model set to %s/%s", msg.Provider, msg.ModelID))
	m.refreshViewportContent()
	m.gotoBottomIfAllowed()
	m.showModelPicker = false
	m.ti.Focus()
	if followCmd != nil {
		return tea.Batch(followCmd, model_picker.CloseModalCmd())
	}
	return model_picker.CloseModalCmd()
}

// newModelPickerFromCache builds the Phase 3 contextual picker cache-first:
// reads synchronously from the atomic Registry RAM snapshot (<2ms), zero
// network I/O, zero Fetching screen. The registry is lazily created from the
// local JSON cache; background sync arrives later as SnapshotMsg.
func newModelPickerFromCache(m *model) model_picker.Model {
	if m.modelRegistry == nil {
		m.modelRegistry = registry.NewRegistry()
		_ = m.modelRegistry.LoadCache()
	}
	mp := model_picker.NewFromRegistry(m.modelRegistry)
	if m != nil && m.resolver != nil {
		mp = mp.SetActiveWorkspace(model_picker.WorkspaceTarget(m.resolver.Current().String()))
		m.ensureModelAuthority()
	}
	if m.width > 0 || m.height > 0 {
		var cmd tea.Cmd
		_ = cmd
		updated, _ := mp.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height})
		if um, ok := updated.(model_picker.Model); ok {
			mp = um
		}
	}
	return mp
}

// applyPickerActivation applies an ACTIVATE (Enter) selection from the widget
// picker: closes over the highlighted model, sets the active binding,
// switches providers when needed, and toasts. Idempotent: empty IDs are a
// no-op so duplicate Activate commands are safe.
func (m *model) applyPickerActivation(um model_picker.Model) tea.Cmd {
	id := um.ActivatedModelID()
	if id == "" {
		if hl := um.Highlighted(); hl != nil {
			id = hl.ID
		}
	}
	if id == "" {
		return nil
	}
	provider := um.ActivatedProvider()
	if provider == "" {
		if hl := um.Highlighted(); hl != nil {
			provider = hl.Provider
		}
	}

	auth := m.ensureModelAuthority()
	binding := authority.ModelBinding{
		ProviderID: authority.ProviderID(provider),
		ModelID:    authority.ModelID(id),
	}
	if err := authority.ValidateBinding(binding); err != nil {
		m.push(roleError, fmt.Sprintf("[✗] Model assignment rejected: %s", err.Error()))
		return nil
	}
	if err := config.PersistActiveBinding(provider, id, ""); err != nil {
		m.push(roleError, fmt.Sprintf("[✗] Model assignment persist failed: %s", err.Error()))
		return nil
	}
	auth.Activate(binding)
	m.syncPipelineTiers()

	var cmds []tea.Cmd
	if provider != "" {
		current := ""
		if m.provider != nil {
			current = m.provider.Name()
		}
		if provider != current {
			if _, ok := validProviders[provider]; ok || provider == "ollama" {
				if m.isProviderAvailable(provider, validProviders[provider]) || provider == "ollama" {
					cmds = append(cmds, m.switchProvider(provider))
				} else {
					m.push(roleError, fmt.Sprintf("[✗] Provider %q not configured — model set but provider unchanged", provider))
				}
			} else if m.cfg != nil {
				if _, ok := m.cfg.AI.Providers[provider]; ok {
					cmds = append(cmds, m.switchProvider(provider))
				} else {
					m.push(roleError, fmt.Sprintf("[✗] Provider %q not configured — model set but provider unchanged", provider))
				}
			}
		}
	}
	m.push(roleSystem, accentStyle.Render(fmt.Sprintf("✓ Model set to %s", id)))
	m.refreshViewportContent()
	m.gotoBottomIfAllowed()
	if len(cmds) == 0 {
		return nil
	}
	return tea.Batch(cmds...)
}

// refreshModelRegistryCmd refreshes the picker catalog in the background
// (Ctrl+R / /models). It prefers the domain ApplicationService (owns network
// + provenance); otherwise it runs live ENV + local-runtime discovery
// directly on the UI registry (concurrent errgroup fetch across detected
// keys/endpoints, Ollama included). Either way it resolves to a SnapshotMsg
// carrying the fresh RAM snapshot — never blocks the TUI. Merged models
// persist per-provider to ~/.izen/cache/models.json inside Registry.Sync.
func (m *model) refreshModelRegistryCmd() tea.Cmd {
	reg := m.modelRegistry
	if reg == nil {
		return nil
	}
	if svc, ok := m.modelAppSvc.(interface {
		RefreshRegistry(context.Context) error
	}); ok && svc != nil {
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			_ = svc.RefreshRegistry(ctx)
			_ = reg.LoadCache()
			return model_picker.SnapshotMsg{Snap: reg.Load()}
		}
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = reg.Sync(ctx, discovery.DiscoverProviders(ctx))
		return model_picker.SnapshotMsg{Snap: reg.Load()}
	}
}

// forwardBindConfirmation projects a persistence-authority outcome into the
// widget picker so its BINDINGS strip renders saving... -> ✓/✕ truthfully.
// It preserves the existing toast behavior and is a no-op when the picker is
// closed.
func (m *model) forwardBindConfirmation(msg tea.Msg) {
	if !m.showModelPicker {
		return
	}
	updated, _ := m.modelPicker.Update(msg)
	if um, ok := updated.(model_picker.Model); ok {
		m.modelPicker = um
	}
}

// pickerActivateCmd handles modelapp.ActivateModelCommand from the widget:
// closes the picker and applies the session. Safe when already closed.
func (m *model) pickerActivateCmd(cmd modelapp.ActivateModelCommand) tea.Cmd {
	m.showModelPicker = false
	if cmd.ModelID == "" {
		m.ti.Focus()
		return nil
	}
	um := m.modelPicker
	m.ti.Focus()
	if um.ActivatedModelID() == "" {
		auth := m.ensureModelAuthority()
		binding := authority.ModelBinding{
			ProviderID: authority.ProviderID(cmd.Provider),
			ModelID:    authority.ModelID(cmd.ModelID),
		}
		if err := authority.ValidateBinding(binding); err != nil {
			m.push(roleError, fmt.Sprintf("[✗] Model assignment rejected: %s", err.Error()))
			return nil
		}
		if err := config.PersistActiveBinding(cmd.Provider, cmd.ModelID, ""); err != nil {
			m.push(roleError, fmt.Sprintf("[✗] Model assignment persist failed: %s", err.Error()))
			return nil
		}
		auth.Activate(binding)
		m.syncPipelineTiers()
		m.push(roleSystem, accentStyle.Render(fmt.Sprintf("✓ Model set to %s", cmd.ModelID)))
		m.refreshViewportContent()
		m.gotoBottomIfAllowed()
		if cmd.Provider != "" {
			return m.switchProviderIfNeeded(cmd.Provider)
		}
		return nil
	}
	return m.applyPickerActivation(um)
}

// switchProviderIfNeeded switches providers when the activated model belongs
// to a different configured provider. Returns nil when no switch is needed.
func (m *model) switchProviderIfNeeded(provider string) tea.Cmd {
	if provider == "" {
		return nil
	}
	current := ""
	if m.provider != nil {
		current = m.provider.Name()
	}
	if provider == current {
		return nil
	}
	if _, ok := validProviders[provider]; ok || provider == "ollama" {
		if m.isProviderAvailable(provider, validProviders[provider]) || provider == "ollama" {
			return m.switchProvider(provider)
		}
		m.push(roleError, fmt.Sprintf("[✗] Provider %q not configured — model set but provider unchanged", provider))
		return nil
	}
	if m.cfg != nil {
		if _, ok := m.cfg.AI.Providers[provider]; ok {
			return m.switchProvider(provider)
		}
	}
	m.push(roleError, fmt.Sprintf("[✗] Provider %q not configured — model set but provider unchanged", provider))
	return nil
}
