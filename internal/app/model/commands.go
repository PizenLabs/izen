// Package model is the domain application service boundary for Izen's
// model registry. It performs role binding operations via the
// ApplicationService abstraction without exposing file I/O or storage
// details to callers. Zero UI dependencies: this package must never import
// the presentation layer or Bubbletea messages.
package model

import (
	"context"

	"github.com/PizenLabs/izen/internal/domain/role"
	"github.com/PizenLabs/izen/internal/provider/adapter"
	"github.com/PizenLabs/izen/internal/provider/registry"
)

// BindModelToRoleCommand is the domain command binding a model to a role.
// Seq is a monotonic per-picker sequence tracking the persistence round-trip:
// the picker emits Seq on BIND, shows saving... until the app layer confirms
// with BindingResultMsg carrying the same Seq. Stale confirmations (Seq
// mismatch) are ignored so out-of-order writes never corrupt local state.
type BindModelToRoleCommand struct {
	Role      role.Role                   `json:"role"`
	ModelID   string                      `json:"model_id"`
	Provider  string                      `json:"provider"`
	Reasoning *adapter.ReasoningSelection `json:"reasoning,omitempty"`
	IsGlobal  bool                        `json:"is_global"`
	Seq       uint64                      `json:"seq"`
}

// ActivateModelCommand is the runtime session command emitted on ACTIVATE
// (Enter): execute the current session with the highlighted model. It carries
// no persistence semantics; the app layer routes it to session startup.
type ActivateModelCommand struct {
	ModelID   string                      `json:"model_id"`
	Provider  string                      `json:"provider"`
	Reasoning *adapter.ReasoningSelection `json:"reasoning,omitempty"`
}

// BindingResultMsg is the persistence-authority confirmation for a BIND.
// Err == nil confirms the bind; Err != nil reports the failure and the UI
// must revert to the prior binding with transient feedback. Seq matches the
// originating BindModelToRoleCommand for stale-confirmation filtering.
type BindingResultMsg struct {
	Role    string `json:"role"`
	ModelID string `json:"model_id"`
	Seq     uint64 `json:"seq"`
	Err     error  `json:"-"`
}

// SyncRequestedMsg requests a background registry refresh (Ctrl+R). The
// picker emits it as a pure-view command; the app layer owns the network.
type SyncRequestedMsg struct{}

// RegistryUpdatedMsg carries a fresh registry snapshot from background
// workers into the picker event loop. It is the cross-layer alias of the
// widget-local SnapshotMsg: handling is identical (pointer swap +
// re-filter). Defined here so background services never import the UI.
type RegistryUpdatedMsg struct {
	Snap *registry.ModelSnapshot
}

// ConfigRepository abstracts role-binding persistence. Implementations own
// all file I/O and storage details; callers only issue domain commands.
type ConfigRepository interface {
	// SaveBinding persists a role binding. Implementations decide the
	// scope (local vs global from cmd.IsGlobal) and storage format.
	SaveBinding(ctx context.Context, cmd BindModelToRoleCommand) error
}
