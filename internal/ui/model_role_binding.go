package ui

import (
	"context"
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	modelapp "github.com/PizenLabs/izen/internal/app/model"
)

// RoleBindSuccessMsg is the UI toast dispatched after ApplicationService
// persists a role binding in the background. Seq correlates the toast with
// the originating BindModelToRoleCommand for stale-confirmation filtering.
type RoleBindSuccessMsg struct {
	Role    string
	ModelID string
	Seq     uint64
}

// RoleBindFailureMsg surfaces a background persistence failure as a toast.
// Seq correlates the toast with the originating command.
type RoleBindFailureMsg struct {
	Role string
	Err  error
	Seq  uint64
}

// ModelServiceBinder is the minimal domain boundary the UI needs for role
// bindings. *modelapp.ApplicationService satisfies it; tests may stub it.
type ModelServiceBinder interface {
	BindRole(ctx context.Context, cmd modelapp.BindModelToRoleCommand) error
}

// SetModelService wires the domain ApplicationService for role bindings.
// Nil disables persistence (commands surface a fail-closed toast).
func (m *model) SetModelService(svc ModelServiceBinder) {
	m.modelAppSvc = svc
}

// handleBindModelToRole routes a modelapp.BindModelToRoleCommand to
// ApplicationService.BindRole in a non-blocking background tea.Cmd and
// dispatches a toast/status message on success or error. Seq is propagated
// so the contextual picker can match confirmations to pending saving states.
func (m *model) handleBindModelToRole(cmd modelapp.BindModelToRoleCommand) tea.Cmd {
	svc := m.modelAppSvc
	if svc == nil {
		role := string(cmd.Role)
		seq := cmd.Seq
		return func() tea.Msg {
			return RoleBindFailureMsg{Role: role, Err: fmt.Errorf("model: no application service wired"), Seq: seq}
		}
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := svc.BindRole(ctx, cmd); err != nil {
			return RoleBindFailureMsg{Role: string(cmd.Role), Err: err, Seq: cmd.Seq}
		}
		return RoleBindSuccessMsg{Role: string(cmd.Role), ModelID: cmd.ModelID, Seq: cmd.Seq}
	}
}

// handleRoleBindSuccess projects a successful binding as a status toast.
func (m *model) handleRoleBindSuccess(msg RoleBindSuccessMsg) {
	m.push(roleSystem, accentStyle.Render(fmt.Sprintf("✓ Role %s → %s", msg.Role, msg.ModelID)))
	m.refreshViewportContent()
	m.gotoBottomIfAllowed()
}

// handleRoleBindFailure projects a binding error as a status toast.
func (m *model) handleRoleBindFailure(msg RoleBindFailureMsg) {
	errText := "<nil>"
	if msg.Err != nil {
		errText = msg.Err.Error()
	}
	m.push(roleError, fmt.Sprintf("[✗] Role %s bind failed: %s", msg.Role, errText))
	m.refreshViewportContent()
	m.gotoBottomIfAllowed()
}
