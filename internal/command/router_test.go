package command

import (
	"testing"

	"github.com/PizenLabs/izen/internal/modes"
	undopkg "github.com/PizenLabs/izen/internal/modes/undo"
	"github.com/PizenLabs/izen/internal/session"
)

type mockUndo struct{}

func (m *mockUndo) Undo() (*undopkg.Result, error) {
	return &undopkg.Result{Success: true, Message: "undo ok"}, nil
}

func (m *mockUndo) UndoSession() (*undopkg.Result, error) {
	return &undopkg.Result{Success: true, Message: "undo session ok"}, nil
}

func (m *mockUndo) UndoByMode(_ undopkg.Mode) (*undopkg.Result, error) {
	return &undopkg.Result{Success: true, Message: "undo mode ok"}, nil
}

func newTestRouter() *Router {
	sess := session.New()
	resolver := modes.NewResolver()
	undo := &mockUndo{}
	return NewCommandRouter(sess, resolver, undo)
}

func TestRouteModeRemoved(t *testing.T) {
	r := newTestRouter()

	tests := []struct {
		input string
		name  string
	}{
		{"mode", "bare mode command"},
		{"/mode", "slash mode command (no arg)"},
		{"/mode ask", "slash mode with arg"},
		{"/mode build", "slash mode with build arg"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			handled, cmd := r.Route(tc.input)
			if handled {
				t.Errorf("Route(%q) = (true, _), want (false, nil) — /mode was removed", tc.input)
			}
			if cmd != nil {
				t.Errorf("Route(%q) cmd != nil, want nil", tc.input)
			}
		})
	}
}

func TestRouteModelNotHandledByRouter(t *testing.T) {
	r := newTestRouter()
	handled, cmd := r.Route("/model")
	if handled {
		t.Errorf("Route(%q) = (true, _), want (false, nil) — /model is handled by TUI layer", "/model")
	}
	if cmd != nil {
		t.Errorf("Route(%q) cmd != nil, want nil", "/model")
	}
}

func TestRouteDirectModeCommandsAreTUIOnly(t *testing.T) {
	r := newTestRouter()

	directCmds := []string{
		"/ask", "/plan", "/investigate", "/review",
	}
	for _, cmd := range directCmds {
		t.Run(cmd, func(t *testing.T) {
			handled, _ := r.Route(cmd)
			if handled {
				t.Errorf("Route(%q) = (true, _), want (false, nil) — mode switch cmds are TUI-only", cmd)
			}
		})
	}
}
