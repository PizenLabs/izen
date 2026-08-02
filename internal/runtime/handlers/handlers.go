// Package handlers holds the Application Layer command handlers. Each handler
// is a stub for Phase 1: it is registered with the CommandDispatcher and
// compiles, but the real workflow wiring lands in Phase 2 when the Domain
// WorkflowRuntime exists. Stubs return ErrNotImplemented so callers never
// mistake a stub for a wired handler.
package handlers

import (
	"context"
	"errors"
	"fmt"

	"github.com/PizenLabs/izen/internal/runtime"
)

// ErrNotImplemented is returned by stub handlers until Phase 2 wiring lands.
var ErrNotImplemented = errors.New("handlers: not implemented")

// SubmitPromptHandler is the stub handler for runtime.SubmitPromptCmd.
type SubmitPromptHandler struct{}

// Command returns the command type this handler serves.
func (h *SubmitPromptHandler) Command() runtime.CommandType {
	return runtime.CommandSubmitPrompt
}

// Handle implements runtime.CommandHandler.
func (h *SubmitPromptHandler) Handle(ctx context.Context, cmd runtime.RuntimeCommand) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return fmt.Errorf("%w: %s", ErrNotImplemented, h.Command())
}

// SwitchModeHandler is the stub handler for runtime.SwitchModeCmd.
type SwitchModeHandler struct{}

// Command returns the command type this handler serves.
func (h *SwitchModeHandler) Command() runtime.CommandType {
	return runtime.CommandSwitchMode
}

// Handle implements runtime.CommandHandler.
func (h *SwitchModeHandler) Handle(ctx context.Context, cmd runtime.RuntimeCommand) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return fmt.Errorf("%w: %s", ErrNotImplemented, h.Command())
}

// ApprovePatchHandler is the stub handler for runtime.ApprovePatchCmd.
type ApprovePatchHandler struct{}

// Command returns the command type this handler serves.
func (h *ApprovePatchHandler) Command() runtime.CommandType {
	return runtime.CommandApprovePatch
}

// Handle implements runtime.CommandHandler.
func (h *ApprovePatchHandler) Handle(ctx context.Context, cmd runtime.RuntimeCommand) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return fmt.Errorf("%w: %s", ErrNotImplemented, h.Command())
}

// RejectPatchHandler is the stub handler for runtime.RejectPatchCmd.
type RejectPatchHandler struct{}

// Command returns the command type this handler serves.
func (h *RejectPatchHandler) Command() runtime.CommandType {
	return runtime.CommandRejectPatch
}

// Handle implements runtime.CommandHandler.
func (h *RejectPatchHandler) Handle(ctx context.Context, cmd runtime.RuntimeCommand) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return fmt.Errorf("%w: %s", ErrNotImplemented, h.Command())
}

// CancelHandler is the stub handler for runtime.CancelCmd.
type CancelHandler struct{}

// Command returns the command type this handler serves.
func (h *CancelHandler) Command() runtime.CommandType {
	return runtime.CommandCancel
}

// Handle implements runtime.CommandHandler.
func (h *CancelHandler) Handle(ctx context.Context, cmd runtime.RuntimeCommand) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return fmt.Errorf("%w: %s", ErrNotImplemented, h.Command())
}

// RegisterDefaults wires every Phase 1 stub handler onto the dispatcher. It
// returns the first registration error encountered, leaving the dispatcher in
// a partial state.
func RegisterDefaults(d *runtime.CommandDispatcher) error {
	if d == nil {
		return errors.New("handlers: nil dispatcher")
	}
	stubs := []runtime.CommandHandler{
		&SubmitPromptHandler{},
		&SwitchModeHandler{},
		&ApprovePatchHandler{},
		&RejectPatchHandler{},
		&CancelHandler{},
	}
	for _, h := range stubs {
		if err := d.Register(handlerCommand(h), h); err != nil {
			return err
		}
	}
	return nil
}

// handlerCommand resolves the served command type for a registered stub.
type commandTyped interface {
	Command() runtime.CommandType
}

// handlerCommand extracts the command type from a handler. It is only used by
// RegisterDefaults with Phase 1 stub handlers.
func handlerCommand(h runtime.CommandHandler) runtime.CommandType {
	if t, ok := h.(commandTyped); ok {
		return t.Command()
	}
	return ""
}
