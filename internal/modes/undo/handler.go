package undo

import (
	"fmt"
	"strings"

	"github.com/PizenLabs/izen/internal/checkpoint"
)

// Mode defines the scope of an undo operation.
type Mode int

const (
	// ModeSingleStep restores the working tree to the state before the most
	// recent mutation command ($hot, $fix, /build).
	ModeSingleStep Mode = iota
	// ModeSession restores the working tree to the initial state captured
	// when the current Izen session started.
	ModeSession
)

// Result carries the outcome of an undo operation.
type Result struct {
	Success    bool
	Message    string
	RestoredID string
	Label      string
}

// Handler performs multi-level undo operations using the shadow checkpoint
// engine.
type Handler struct {
	engine *checkpoint.Engine
}

// NewHandler creates an undo handler backed by the given checkpoint engine.
func NewHandler(engine *checkpoint.Engine) *Handler {
	return &Handler{engine: engine}
}

// Parse parses an undo input string and returns the requested undo mode.
// Accepts:
//
//	"/undo"            → ModeSingleStep
//	"/undo session"    → ModeSession
//	"/undo --all"      → ModeSession
//	"/undo --session"  → ModeSession
//	"undo"             → ModeSingleStep
func Parse(input string) Mode {
	input = strings.TrimSpace(input)
	input = strings.TrimPrefix(input, "/")
	fields := strings.Fields(input)
	if len(fields) < 1 {
		return ModeSingleStep
	}
	if len(fields) < 2 {
		return ModeSingleStep
	}
	arg := strings.ToLower(fields[1])
	switch arg {
	case "session", "--all", "--session":
		return ModeSession
	default:
		return ModeSingleStep
	}
}

// Undo performs a single-step undo: restores the working tree to the state
// captured in the most recent shadow checkpoint.
func (h *Handler) Undo() (*Result, error) {
	cp, err := h.engine.Latest()
	if err != nil {
		return nil, fmt.Errorf("load latest checkpoint: %w", err)
	}
	if cp == nil {
		return &Result{
			Success: false,
			Message: "No checkpoints found. Nothing to undo.",
		}, nil
	}
	if cp.ID == checkpoint.SessionStartKey {
		return &Result{
			Success: false,
			Message: "Only the session-start checkpoint exists. To restore to session start, use /undo session.",
		}, nil
	}

	if err := h.engine.RestoreCheckpoint(cp); err != nil {
		return nil, fmt.Errorf("restore checkpoint %s: %w", cp.ID, err)
	}

	label := cp.Label
	if label == "" {
		label = "previous state"
	}

	// Remove the checkpoint after successful restore.
	_ = h.engine.RemoveCheckpoint(cp.ID)

	return &Result{
		Success:    true,
		Message:    fmt.Sprintf("Restored working tree to state before \"%s\"", label),
		RestoredID: cp.ID,
		Label:      label,
	}, nil
}

// UndoSession restores the working tree to the initial state captured when
// the current Izen session started.
func (h *Handler) UndoSession() (*Result, error) {
	if err := h.engine.RestoreSessionStart(); err != nil {
		return nil, fmt.Errorf("restore session start: %w", err)
	}

	return &Result{
		Success: true,
		Message: "Restored working tree to initial session state.",
	}, nil
}

// UndoByMode dispatches to the correct undo handler based on the given mode.
func (h *Handler) UndoByMode(mode Mode) (*Result, error) {
	switch mode {
	case ModeSession:
		return h.UndoSession()
	default:
		return h.Undo()
	}
}
