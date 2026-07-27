package command

import (
	"os"
	"path/filepath"
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

func TestResolveTargetFile_WorkspaceRejected(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "templates"), 0755)
	_ = os.WriteFile(filepath.Join(root, "templates", "layout.tmpl"), []byte("test"), 0644)

	// "workspace" is explicitly rejected even when it appears in the prompt.
	result := ResolveTargetFile(root, "move workspace navigation to header")
	if result == "workspace" {
		t.Error("ResolveTargetFile returned 'workspace' — must be rejected")
	}
}

func TestResolveTargetFile_ExplicitPath(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "templates"), 0755)
	_ = os.WriteFile(filepath.Join(root, "templates", "layout.tmpl"), []byte("test"), 0644)

	result := ResolveTargetFile(root, "modify templates/layout.tmpl")
	if result != "templates/layout.tmpl" {
		t.Errorf("expected templates/layout.tmpl, got %q", result)
	}
}

func TestResolveTargetFile_EmptyRoot(t *testing.T) {
	result := ResolveTargetFile("", "move navigation")
	if result != "" {
		t.Errorf("expected empty result for empty root, got %q", result)
	}
}
