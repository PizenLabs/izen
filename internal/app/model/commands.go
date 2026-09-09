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
)

// BindModelToRoleCommand is the domain command binding a model to a role.
type BindModelToRoleCommand struct {
	Role      role.Role                   `json:"role"`
	ModelID   string                      `json:"model_id"`
	Provider  string                      `json:"provider"`
	Reasoning *adapter.ReasoningSelection `json:"reasoning,omitempty"`
	IsGlobal  bool                        `json:"is_global"`
}

// ConfigRepository abstracts role-binding persistence. Implementations own
// all file I/O and storage details; callers only issue domain commands.
type ConfigRepository interface {
	// SaveBinding persists a role binding. Implementations decide the
	// scope (local vs global from cmd.IsGlobal) and storage format.
	SaveBinding(ctx context.Context, cmd BindModelToRoleCommand) error
}
