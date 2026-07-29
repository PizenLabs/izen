package agent

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/agent/checkpoint"
	"github.com/PizenLabs/izen/internal/core/budget"
)

// ── TUI Agent Event Messages ──────────────────────────────────────────────

// AgentEventMsg wraps a raw agent Event for the TUI event loop.
type AgentEventMsg struct {
	Type      EventType
	AgentTime time.Time
	Payload   interface{}
}

// AgentStateChangedMsg is emitted on every agent state transition.
type AgentStateChangedMsg struct {
	From AgentState
	To   AgentState
}

// AgentPlanStartedMsg signals the planning phase began.
type AgentPlanStartedMsg struct{ StartedAt time.Time }

// AgentPlanCompletedMsg signals the plan was generated.
type AgentPlanCompletedMsg struct {
	StepsCount int
}

// AgentRetrieveStartedMsg signals retrieval phase began.
type AgentRetrieveStartedMsg struct{ StartedAt time.Time }

// AgentRetrieveCompletedMsg signals retrieval completed.
type AgentRetrieveCompletedMsg struct {
	ResultCount int
	Duration    string
}

// AgentGuardCheckMsg signals guard validation started.
type AgentGuardCheckMsg struct{}

// AgentGuardApprovedMsg signals guard passed without human intervention.
type AgentGuardApprovedMsg struct{}

// AgentRequireApprovalMsg signals the agent is blocked waiting for human approval.
type AgentRequireApprovalMsg struct {
	Action     string
	Target     string
	Capability string
	Budget     budget.BudgetDelta
	Reason     string
}

// AgentApprovalGrantedMsg signals the human approved the pending action.
type AgentApprovalGrantedMsg struct {
	Reason string
}

// AgentApprovalDeniedMsg signals the human rejected the pending action.
type AgentApprovalDeniedMsg struct {
	Reason string
}

// AgentExecuteStartedMsg signals execution phase began.
type AgentExecuteStartedMsg struct{ StartedAt time.Time }

// AgentExecuteCompletedMsg signals a step completed successfully.
type AgentExecuteCompletedMsg struct {
	Step string
}

// AgentExecuteFailedMsg signals a step failed.
type AgentExecuteFailedMsg struct {
	Step  string
	Error string
}

// AgentRecoverStartedMsg signals the recovery phase began.
type AgentRecoverStartedMsg struct{ StartedAt time.Time }

// AgentRecoverCompletedMsg signals recovery succeeded.
type AgentRecoverCompletedMsg struct {
	Action     string
	AttemptNum int
}

// AgentRecoverFailedMsg signals recovery was exhausted.
type AgentRecoverFailedMsg struct {
	Action     string
	AttemptNum int
}

// AgentTurnCompleteMsg signals a full agent turn completed.
type AgentTurnCompleteMsg struct {
	TurnCount int
	Summary   string
}

// AgentCheckpointSummaryMsg carries the checkpoint summary after compaction.
type AgentCheckpointSummaryMsg struct {
	Summary    string
	Compacted  bool
	TokenUsage int
	MaxTokens  int
	UsageRatio float64
	TurnCount  int
}

// AgentFailedMsg signals the agent entered a terminal failed state.
type AgentFailedMsg struct {
	Phase   AgentState
	Message string
}

// AgentFinishedMsg signals the agent completed successfully.
type AgentFinishedMsg struct{}

// AgentCheckpointMsg carries a snapshot of the CheckpointManager state.
type AgentCheckpointMsg struct {
	Snapshot   checkpoint.CheckpointState
	Checkpoint string
}

// ── Bridge ────────────────────────────────────────────────────────────────

// BridgeEvent adapts a single agent Event into the corresponding tea.Msg.
func BridgeEvent(ev Event) tea.Msg {
	switch ev.Type {
	case EventStateChanged:
		payload, ok := ev.Payload.(struct {
			From AgentState
			To   AgentState
			At   time.Time
		})
		if ok {
			return AgentStateChangedMsg{From: payload.From, To: payload.To}
		}
		return AgentStateChangedMsg{}

	case EventPlanStart:
		return AgentPlanStartedMsg{}
	case EventPlanComplete:
		p, ok := ev.Payload.(PlanResult)
		if ok {
			return AgentPlanCompletedMsg{StepsCount: len(p.Steps)}
		}
		return AgentPlanCompletedMsg{}

	case EventRetrieveStart:
		return AgentRetrieveStartedMsg{}
	case EventRetrieveComplete:
		p, ok := ev.Payload.(RetrieveResult)
		if ok && p.Result != nil {
			count := 0
			if p.Result.ResultSet != nil {
				count = len(p.Result.ResultSet.Results)
			}
			return AgentRetrieveCompletedMsg{
				ResultCount: count,
				Duration:    p.Result.TotalDuration.String(),
			}
		}
		return AgentRetrieveCompletedMsg{}

	case EventGuardCheck:
		return AgentGuardCheckMsg{}
	case EventGuardApproved:
		return AgentGuardApprovedMsg{}
	case EventRequireApproval:
		p, ok := ev.Payload.(ApprovalRequest)
		if ok {
			return AgentRequireApprovalMsg(p)
		}
		return AgentRequireApprovalMsg{}

	case EventApprovalGranted:
		p, ok := ev.Payload.(ApprovalResponse)
		if ok {
			return AgentApprovalGrantedMsg{Reason: p.Reason}
		}
		return AgentApprovalGrantedMsg{}
	case EventApprovalDenied:
		p, ok := ev.Payload.(ApprovalResponse)
		if ok {
			return AgentApprovalDeniedMsg{Reason: p.Reason}
		}
		return AgentApprovalDeniedMsg{}

	case EventExecuteStart:
		return AgentExecuteStartedMsg{}
	case EventExecuteComplete:
		p, ok := ev.Payload.(ExecuteResult)
		if ok {
			return AgentExecuteCompletedMsg{Step: p.Step}
		}
		return AgentExecuteCompletedMsg{}
	case EventExecuteFailed:
		p, ok := ev.Payload.(ExecuteResult)
		if ok {
			return AgentExecuteFailedMsg{Step: p.Step, Error: p.Output}
		}
		return AgentExecuteFailedMsg{}

	case EventRecoverStart:
		return AgentRecoverStartedMsg{}
	case EventRecoverComplete:
		p, ok := ev.Payload.(RecoverResult)
		if ok {
			return AgentRecoverCompletedMsg{
				Action:     p.Action.String(),
				AttemptNum: p.AttemptNum,
			}
		}
		return AgentRecoverCompletedMsg{}
	case EventRecoverFailed:
		p, ok := ev.Payload.(RecoverResult)
		if ok {
			return AgentRecoverFailedMsg{
				Action:     p.Action.String(),
				AttemptNum: p.AttemptNum,
			}
		}
		return AgentRecoverFailedMsg{}

	case EventTurnComplete:
		p, ok := ev.Payload.(struct {
			TurnCount int
			Summary   string
		})
		if ok {
			return AgentTurnCompleteMsg{TurnCount: p.TurnCount, Summary: p.Summary}
		}
		return AgentTurnCompleteMsg{TurnCount: 1}

	case EventError:
		p, ok := ev.Payload.(ErrorPayload)
		if ok {
			return AgentFailedMsg{Phase: p.Phase, Message: p.Message}
		}
		return AgentFailedMsg{Message: "agent error"}

	case EventLoopCancelled:
		return AgentFailedMsg{Phase: ev.State, Message: "loop cancelled"}

	default:
		return AgentEventMsg{Type: ev.Type, AgentTime: ev.Time, Payload: ev.Payload}
	}
}

// ── tea.Cmd Constructors ──────────────────────────────────────────────────

// SubscribeEvents creates a tea.Cmd that polls the agent's EventStream and
// forwards events as bridged tea.Msg values to the TUI event loop.
func SubscribeEvents(stream *EventStream) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-stream.Events()
		if !ok {
			return AgentFinishedMsg{}
		}
		return BridgeEvent(ev)
	}
}

// ListenAgentEvents creates a tea.Cmd that continuously drains the agent
// EventStream and returns the first bridged event. Call this in a loop
// within the TUI Update method to keep the stream drained.
func ListenAgentEvents(stream *EventStream) tea.Cmd {
	return func() tea.Msg {
		select {
		case ev, ok := <-stream.Events():
			if !ok {
				return AgentFinishedMsg{}
			}
			return BridgeEvent(ev)
		default:
			return nil
		}
	}
}

// BatchListenAgentEvents creates a batch of all pending agent events,
// returning the first one found. This is more efficient than polling
// one at a time in a tight Update loop.
func BatchListenAgentEvents(stream *EventStream) tea.Cmd {
	return func() tea.Msg {
		select {
		case ev, ok := <-stream.Events():
			if !ok {
				return AgentFinishedMsg{}
			}
			return BridgeEvent(ev)
		default:
			return nil
		}
	}
}

// ── Non-blocking Command Actions ──────────────────────────────────────────

// ApproveAction returns a tea.Cmd that sends an approval response to the
// agent loop, granting the pending action.
func ApproveAction(al *AgentLoop, reason string) tea.Cmd {
	return func() tea.Msg {
		respCh := make(chan ApprovalResponse, 1)
		respCh <- ApprovalResponse{Granted: true, Reason: reason}
		close(respCh)

		select {
		case al.approvalCh <- ApprovalPending{
			Request: ApprovalRequest{Action: "tui-approve"},
			RespCh:  respCh,
		}:
		default:
		}

		return AgentApprovalGrantedMsg{Reason: reason}
	}
}

// RejectAction returns a tea.Cmd that sends an approval response to the
// agent loop, denying the pending action.
func RejectAction(al *AgentLoop, reason string) tea.Cmd {
	return func() tea.Msg {
		respCh := make(chan ApprovalResponse, 1)
		respCh <- ApprovalResponse{Granted: false, Reason: reason}
		close(respCh)

		select {
		case al.approvalCh <- ApprovalPending{
			Request: ApprovalRequest{Action: "tui-reject"},
			RespCh:  respCh,
		}:
		default:
		}

		return AgentApprovalDeniedMsg{Reason: reason}
	}
}

// CancelLoop returns a tea.Cmd that cancels the agent loop context.
func CancelLoop(al *AgentLoop) tea.Cmd {
	return func() tea.Msg {
		al.Cancel()
		return nil
	}
}

// SendPrompt returns a tea.Cmd that sends a prompt/query to the agent via
// the RunTurn method. The agent must be in StateIdle for this to proceed.
func SendPrompt(al *AgentLoop, planFn func(context.Context) error, retrieveFn func(context.Context) error) tea.Cmd {
	return func() tea.Msg {
		if al.State() != StateIdle {
			return AgentFailedMsg{
				Phase:   al.State(),
				Message: "agent not idle; cannot send prompt",
			}
		}

		ctx := al.ctx
		err := al.RunTurn(ctx, planFn, retrieveFn)
		if err != nil {
			return AgentFailedMsg{
				Phase:   al.State(),
				Message: err.Error(),
			}
		}
		return AgentFinishedMsg{}
	}
}
