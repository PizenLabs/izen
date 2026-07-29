package agent

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/PizenLabs/izen/internal/agent/checkpoint"
	"github.com/PizenLabs/izen/internal/controlplane/failure"
	"github.com/PizenLabs/izen/internal/core/budget"
	"github.com/PizenLabs/izen/internal/core/runtime"
	"github.com/PizenLabs/izen/internal/retrieval"
)

type AgentState int

const (
	StateIdle             AgentState = iota
	StatePlanning                    // Phase 1: generating execution plan
	StateRetrieving                  // Phase 2: context retrieval from orchestrator
	StateGuarding                    // Phase 3: guard check before execution
	StateExecuting                   // Phase 4: tool execution
	StateRecovering                  // Phase 5: failure recovery / auto-repair
	StateAwaitingApproval            // Blocked waiting for human approval
	StateFinished                    // Terminal success
	StateFailed                      // Terminal failure
)

func (s AgentState) String() string {
	switch s {
	case StateIdle:
		return "idle"
	case StatePlanning:
		return "planning"
	case StateRetrieving:
		return "retrieving"
	case StateGuarding:
		return "guarding"
	case StateExecuting:
		return "executing"
	case StateRecovering:
		return "recovering"
	case StateAwaitingApproval:
		return "awaiting-approval"
	case StateFinished:
		return "finished"
	case StateFailed:
		return "failed"
	default:
		return fmt.Sprintf("AgentState(%d)", int(s))
	}
}

func (s AgentState) IsTerminal() bool {
	return s == StateFinished || s == StateFailed
}

func (s AgentState) Valid() bool {
	return s >= StateIdle && s <= StateFailed
}

type EventType int

const (
	EventPlanStart        EventType = iota // Planning phase began
	EventPlanComplete                      // Plan generated successfully
	EventRetrieveStart                     // Retrieval phase began
	EventRetrieveComplete                  // Retrieval phase complete
	EventGuardCheck                        // Guard validation started
	EventGuardApproved                     // Guard passed — no approval needed
	EventRequireApproval                   // Guard returned ActionRequireApproval
	EventApprovalGranted                   // Human approved the action
	EventApprovalDenied                    // Human rejected the action
	EventExecuteStart                      // Execution phase began
	EventExecuteComplete                   // Step executed successfully
	EventExecuteFailed                     // Step failed with error
	EventRecoverStart                      // Recovery phase began
	EventRecoverComplete                   // Recovery successful
	EventRecoverFailed                     // Recovery exhausted
	EventTurnComplete                      // Full turn completed
	EventLoopCancelled                     // Loop cancelled by user
	EventStateChanged                      // Generic state transition
	EventError                             // Non-recoverable error
)

func (e EventType) String() string {
	switch e {
	case EventPlanStart:
		return "plan-start"
	case EventPlanComplete:
		return "plan-complete"
	case EventRetrieveStart:
		return "retrieve-start"
	case EventRetrieveComplete:
		return "retrieve-complete"
	case EventGuardCheck:
		return "guard-check"
	case EventGuardApproved:
		return "guard-approved"
	case EventRequireApproval:
		return "require-approval"
	case EventApprovalGranted:
		return "approval-granted"
	case EventApprovalDenied:
		return "approval-denied"
	case EventExecuteStart:
		return "execute-start"
	case EventExecuteComplete:
		return "execute-complete"
	case EventExecuteFailed:
		return "execute-failed"
	case EventRecoverStart:
		return "recover-start"
	case EventRecoverComplete:
		return "recover-complete"
	case EventRecoverFailed:
		return "recover-failed"
	case EventTurnComplete:
		return "turn-complete"
	case EventLoopCancelled:
		return "loop-cancelled"
	case EventStateChanged:
		return "state-changed"
	case EventError:
		return "error"
	default:
		return fmt.Sprintf("EventType(%d)", int(e))
	}
}

type Event struct {
	Type    EventType
	State   AgentState
	Payload interface{}
	Time    time.Time
}

type ApprovalRequest struct {
	Action     string
	Target     string
	Capability string
	Budget     budget.BudgetDelta
	Reason     string
}

type ApprovalResponse struct {
	Granted bool
	Reason  string
}

type ApprovalPending struct {
	Request ApprovalRequest
	RespCh  chan ApprovalResponse
}

type PlanResult struct {
	Steps   []string
	Summary string
}

type RetrieveResult struct {
	Result *retrieval.PipelineResult
	Query  retrieval.Query
}

type ExecuteResult struct {
	Step    string
	Output  string
	Success bool
}

type RecoverResult struct {
	Action     failure.RecoveryAction
	Succeeded  bool
	AttemptNum int
}

type ErrorPayload struct {
	Phase   AgentState
	Message string
	Err     error
}

type EventStream struct {
	ch chan Event
}

func NewEventStream(bufferSize int) *EventStream {
	if bufferSize <= 0 {
		bufferSize = 64
	}
	return &EventStream{ch: make(chan Event, bufferSize)}
}

func (es *EventStream) Events() <-chan Event {
	return es.ch
}

func (es *EventStream) Emit(typ EventType, state AgentState, payload interface{}) {
	es.ch <- Event{
		Type:    typ,
		State:   state,
		Payload: payload,
		Time:    time.Now(),
	}
}

func (es *EventStream) Close() {
	close(es.ch)
}

type AgentLoop struct {
	mu                sync.RWMutex
	state             AgentState
	stream            *EventStream
	ctx               context.Context
	cancel            context.CancelFunc
	checkpointMgr     *checkpoint.CheckpointManager
	runtimeCtx        *runtime.RuntimeContext
	failureClassifier *failure.Classifier
	recoveryMgr       *failure.RecoveryManager
	retrievalOrch     *retrieval.Orchestrator
	approvalCh        chan ApprovalPending
	repairAttempts    map[failure.FailureClass]int
	turnCount         int
	waitGroup         sync.WaitGroup
}

type AgentLoopConfig struct {
	RuntimeCtx        *runtime.RuntimeContext
	FailureClassifier *failure.Classifier
	RecoveryMgr       *failure.RecoveryManager
	RetrievalOrch     *retrieval.Orchestrator
	CheckpointMgr     *checkpoint.CheckpointManager
	StreamBufferSize  int
}

func NewAgentLoop(cfg AgentLoopConfig) *AgentLoop {
	ctx, cancel := context.WithCancel(context.Background())

	if cfg.CheckpointMgr == nil {
		cfg.CheckpointMgr = checkpoint.NewCheckpointManager()
	}

	bufSize := cfg.StreamBufferSize
	if bufSize <= 0 {
		bufSize = 64
	}

	return &AgentLoop{
		state:             StateIdle,
		stream:            NewEventStream(bufSize),
		ctx:               ctx,
		cancel:            cancel,
		checkpointMgr:     cfg.CheckpointMgr,
		runtimeCtx:        cfg.RuntimeCtx,
		failureClassifier: cfg.FailureClassifier,
		recoveryMgr:       cfg.RecoveryMgr,
		retrievalOrch:     cfg.RetrievalOrch,
		approvalCh:        make(chan ApprovalPending, 1),
		repairAttempts:    make(map[failure.FailureClass]int),
	}
}

func (al *AgentLoop) State() AgentState {
	al.mu.RLock()
	defer al.mu.RUnlock()
	return al.state
}

func (al *AgentLoop) Events() *EventStream {
	return al.stream
}

func (al *AgentLoop) CheckpointManager() *checkpoint.CheckpointManager {
	return al.checkpointMgr
}

func (al *AgentLoop) emit(typ EventType, payload interface{}) {
	al.mu.RLock()
	s := al.state
	al.mu.RUnlock()
	al.stream.Emit(typ, s, payload)
}

func (al *AgentLoop) transition(to AgentState, payload interface{}) {
	al.mu.Lock()
	from := al.state
	al.state = to
	al.mu.Unlock()
	al.stream.Emit(EventStateChanged, to, struct {
		From AgentState
		To   AgentState
		At   time.Time
	}{From: from, To: to, At: time.Now()})
	_ = from
}

func (al *AgentLoop) Run(ctx context.Context) {
	defer al.stream.Close()
	defer al.waitGroup.Wait()

	al.ctx, al.cancel = context.WithCancel(ctx)

	al.transition(StateIdle, nil)

	select {
	case <-al.ctx.Done():
		al.transition(StateFailed, ErrorPayload{
			Phase:   StateIdle,
			Message: "context cancelled before execution",
			Err:     al.ctx.Err(),
		})
		return
	default:
	}

	al.runPlanningPhase()
	if al.State().IsTerminal() {
		return
	}

	al.runRetrievalPhase()
	if al.State().IsTerminal() {
		return
	}

	al.runGuardPhase()
	if al.State().IsTerminal() {
		return
	}

	al.runExecutionPhase()
	if al.State().IsTerminal() {
		return
	}

	al.emit(EventTurnComplete, struct {
		TurnCount int
		Summary   string
	}{
		TurnCount: al.turnCount,
		Summary:   al.checkpointMgr.CheckpointSummary(),
	})
}

func (al *AgentLoop) Cancel() {
	al.mu.Lock()
	defer al.mu.Unlock()
	if al.cancel != nil {
		al.cancel()
	}
}

func (al *AgentLoop) ApprovalChannel() chan<- ApprovalPending {
	return al.approvalCh
}

func (al *AgentLoop) runPlanningPhase() {
	al.transition(StatePlanning, nil)
	al.emit(EventPlanStart, nil)
	al.checkpointMgr.IncrementTurn()

	select {
	case <-al.ctx.Done():
		al.transition(StateFailed, ErrorPayload{
			Phase:   StatePlanning,
			Message: "cancelled during planning",
			Err:     al.ctx.Err(),
		})
		return
	default:
	}

	al.emit(EventPlanComplete, PlanResult{
		Steps:   []string{},
		Summary: "plan generated (stub)",
	})
}

func (al *AgentLoop) runRetrievalPhase() {
	al.transition(StateRetrieving, nil)
	al.emit(EventRetrieveStart, nil)

	select {
	case <-al.ctx.Done():
		al.transition(StateFailed, ErrorPayload{
			Phase:   StateRetrieving,
			Message: "cancelled during retrieval",
			Err:     al.ctx.Err(),
		})
		return
	default:
	}

	if al.retrievalOrch != nil {
		query := retrieval.Query{Text: "", Symbol: "", File: "", Package: ""}
		result, err := al.retrievalOrch.Execute(al.ctx, query)
		if err != nil {
			al.emit(EventError, ErrorPayload{
				Phase:   StateRetrieving,
				Message: fmt.Sprintf("retrieval failed: %v", err),
				Err:     err,
			})
		} else {
			al.checkpointMgr.RecordTokens(
				result.TokenEstimate/2,
				result.TokenEstimate/2,
			)
			al.emit(EventRetrieveComplete, RetrieveResult{
				Result: result,
			})
		}
	} else {
		al.emit(EventRetrieveComplete, RetrieveResult{})
	}
}

func (al *AgentLoop) runGuardPhase() {
	al.transition(StateGuarding, nil)
	al.emit(EventGuardCheck, nil)

	select {
	case <-al.ctx.Done():
		al.transition(StateFailed, ErrorPayload{
			Phase:   StateGuarding,
			Message: "cancelled during guard",
			Err:     al.ctx.Err(),
		})
		return
	default:
	}

	if al.runtimeCtx == nil || al.runtimeCtx.Caps == nil {
		al.emit(EventGuardApproved, nil)
		return
	}

	if !al.runtimeCtx.Caps.CanWrite() {
		al.emit(EventGuardApproved, nil)
		return
	}

	targetFiles := []string{}
	delta := budget.BudgetDelta{
		Files:     1,
		Tokens:    100,
		DiffLines: 50,
	}

	if !isWithinMicroBudget(al.runtimeCtx, delta) {
		al.transition(StateAwaitingApproval, nil)
		al.emit(EventRequireApproval, ApprovalRequest{
			Action:     "file mutation",
			Target:     "workspace files",
			Capability: "write",
			Budget:     delta,
			Reason:     "exceeds micro-budget thresholds",
		})

		resp, err := al.waitForApproval()
		if err != nil {
			al.transition(StateFailed, ErrorPayload{
				Phase:   StateGuarding,
				Message: fmt.Sprintf("approval error: %v", err),
				Err:     err,
			})
			return
		}

		if resp.Granted {
			al.emit(EventApprovalGranted, resp)
		} else {
			al.emit(EventApprovalDenied, resp)
			al.transition(StateFinished, resp)
			return
		}
	}

	_ = targetFiles
	al.emit(EventGuardApproved, nil)
}

func isWithinMicroBudget(rc *runtime.RuntimeContext, delta budget.BudgetDelta) bool {
	if rc == nil || rc.Budget == nil {
		return true
	}
	mb := budget.DefaultMicroBudget()
	hasCP := false
	if rc.Caps != nil {
		hasCP = rc.Caps.CanCheckpoint()
	}
	return mb.IsWithinMicroBudget(delta, hasCP)
}

func (al *AgentLoop) waitForApproval() (ApprovalResponse, error) {
	select {
	case pending := <-al.approvalCh:
		respCh := pending.RespCh

		select {
		case resp, ok := <-respCh:
			if !ok {
				return ApprovalResponse{Granted: false, Reason: "response channel closed"}, nil
			}
			return resp, nil
		case <-al.ctx.Done():
			return ApprovalResponse{Granted: false, Reason: "context cancelled"}, al.ctx.Err()
		}

	case <-al.ctx.Done():
		return ApprovalResponse{Granted: false, Reason: "context cancelled"}, al.ctx.Err()
	}
}

func (al *AgentLoop) runExecutionPhase() {
	al.transition(StateExecuting, nil)
	al.emit(EventExecuteStart, nil)

	select {
	case <-al.ctx.Done():
		al.transition(StateFailed, ErrorPayload{
			Phase:   StateExecuting,
			Message: "cancelled during execution",
			Err:     al.ctx.Err(),
		})
		return
	default:
	}

	steps := []string{"step-1"}
	maxAttempts := 3

	for _, step := range steps {
		select {
		case <-al.ctx.Done():
			al.transition(StateFailed, ErrorPayload{
				Phase:   StateExecuting,
				Message: "cancelled during step execution",
				Err:     al.ctx.Err(),
			})
			return
		default:
		}

		stepErr := al.executeStep(step)
		if stepErr == nil {
			al.emit(EventExecuteComplete, ExecuteResult{
				Step:    step,
				Output:  "ok",
				Success: true,
			})
			continue
		}

		al.emit(EventExecuteFailed, ExecuteResult{
			Step:    step,
			Output:  stepErr.Error(),
			Success: false,
		})

		al.runRecoveryPhase(step, stepErr, maxAttempts)
		if al.State().IsTerminal() {
			return
		}
	}

	al.transition(StateFinished, nil)
}

func (al *AgentLoop) executeStep(step string) error {
	_ = step
	return nil
}

func (al *AgentLoop) runRecoveryPhase(failedStep string, stepErr error, _ int) {
	al.transition(StateRecovering, nil)
	al.emit(EventRecoverStart, nil)

	fc := al.classifyFailure(stepErr)

	for attempt := 0; ; attempt++ {
		select {
		case <-al.ctx.Done():
			al.transition(StateFailed, ErrorPayload{
				Phase:   StateRecovering,
				Message: "cancelled during recovery",
				Err:     al.ctx.Err(),
			})
			return
		default:
		}

		action := al.recoveryMgr.HandleFailure(fc, attempt)
		al.checkpointMgr.RecordTokens(50, 100)

		switch action {
		case failure.ActionAutoRepair:
			al.emit(EventRecoverComplete, RecoverResult{
				Action:     action,
				Succeeded:  true,
				AttemptNum: attempt + 1,
			})
			return

		case failure.ActionEscalateToHuman:
			al.transition(StateAwaitingApproval, nil)
			al.emit(EventRequireApproval, ApprovalRequest{
				Action:     "escalate recovery",
				Target:     failedStep,
				Capability: "write",
				Reason:     fmt.Sprintf("max auto-repair attempts reached for %s", fc),
			})

			resp, err := al.waitForApproval()
			if err != nil || !resp.Granted {
				al.transition(StateFailed, ErrorPayload{
					Phase:   StateRecovering,
					Message: fmt.Sprintf("recovery escalation denied: %v", resp),
				})
				return
			}
			al.emit(EventRecoverComplete, RecoverResult{
				Action:     action,
				Succeeded:  true,
				AttemptNum: attempt + 1,
			})
			return

		case failure.ActionReplan:
			al.emit(EventRecoverComplete, RecoverResult{
				Action:     action,
				Succeeded:  true,
				AttemptNum: attempt + 1,
			})
			al.runPlanningPhase()
			return

		case failure.ActionImmediateRollback:
			al.emit(EventRecoverFailed, RecoverResult{
				Action:     action,
				Succeeded:  false,
				AttemptNum: attempt + 1,
			})
			al.transition(StateFailed, ErrorPayload{
				Phase:   StateExecuting,
				Message: "scope failure: rollback required",
				Err:     stepErr,
			})
			return

		case failure.ActionImmediateRollbackAndBlock:
			al.emit(EventRecoverFailed, RecoverResult{
				Action:     action,
				Succeeded:  false,
				AttemptNum: attempt + 1,
			})
			al.transition(StateFailed, ErrorPayload{
				Phase:   StateExecuting,
				Message: "security issue: rollback and block",
				Err:     stepErr,
			})
			return

		case failure.ActionHaltAndEscalate:
			al.emit(EventRecoverFailed, RecoverResult{
				Action:     action,
				Succeeded:  false,
				AttemptNum: attempt + 1,
			})
			al.transition(StateFailed, ErrorPayload{
				Phase:   StateRecovering,
				Message: "unknown failure: halt and escalate",
				Err:     stepErr,
			})
			return

		default:
			al.emit(EventRecoverFailed, RecoverResult{
				Action:     action,
				Succeeded:  false,
				AttemptNum: attempt + 1,
			})
			al.transition(StateFailed, ErrorPayload{
				Phase:   StateRecovering,
				Message: fmt.Sprintf("unhandled recovery action: %s", action),
			})
			return
		}
	}
}

func (al *AgentLoop) classifyFailure(err error) failure.FailureClass {
	if al.failureClassifier != nil {
		output := ""
		if err != nil {
			output = err.Error()
		}
		return al.failureClassifier.Classify(err, output)
	}
	return failure.UNKNOWN
}

func (al *AgentLoop) RunTurn(ctx context.Context, planFn func(context.Context) error, retrieveFn func(context.Context) error) error {
	al.transition(StatePlanning, nil)
	if err := planFn(ctx); err != nil {
		al.transition(StateFailed, ErrorPayload{Phase: StatePlanning, Message: err.Error(), Err: err})
		return err
	}
	al.emit(EventPlanComplete, nil)

	al.transition(StateRetrieving, nil)
	if retrieveFn != nil {
		if err := retrieveFn(ctx); err != nil {
			al.transition(StateFailed, ErrorPayload{Phase: StateRetrieving, Message: err.Error(), Err: err})
			return err
		}
	}
	al.emit(EventRetrieveComplete, nil)

	al.turnCount++
	al.checkpointMgr.IncrementTurn()
	al.emit(EventTurnComplete, struct{ TurnCount int }{TurnCount: al.turnCount})

	al.transition(StateFinished, nil)
	return nil
}
