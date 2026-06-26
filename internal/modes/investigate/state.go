package investigate

import "fmt"

type State int

const (
	StateObserve State = iota
	StateHypothesize
	StateSearch
	StateGather
	StateEvaluate
	StateNarrow
	StateVerify
	StatePropose
	StateDone
)

func (s State) String() string {
	switch s {
	case StateObserve:
		return "observe"
	case StateHypothesize:
		return "hypothesize"
	case StateSearch:
		return "search"
	case StateGather:
		return "gather"
	case StateEvaluate:
		return "evaluate"
	case StateNarrow:
		return "narrow"
	case StateVerify:
		return "verify"
	case StatePropose:
		return "propose"
	case StateDone:
		return "done"
	default:
		return "unknown"
	}
}

func (s State) Description() string {
	switch s {
	case StateObserve:
		return "Observe the problem — collect initial error output, stack traces, and failure context"
	case StateHypothesize:
		return "Form a debugging theory based on observed evidence"
	case StateSearch:
		return "Execute targeted searches across graph, semantic, and text tiers"
	case StateGather:
		return "Collect and compile evidence from search results"
	case StateEvaluate:
		return "Evaluate evidence against the active hypothesis — confirm or reject"
	case StateNarrow:
		return "Narrow the search space — focus on specific files, symbols, or conditions"
	case StateVerify:
		return "Run tests or checks to verify the proposed fix"
	case StatePropose:
		return "Propose a solution based on confirmed hypothesis and evidence"
	case StateDone:
		return "Investigation complete"
	default:
		return ""
	}
}

type StateMachine struct {
	current State
	history []State
	config  StateConfig
}

type StateConfig struct {
	MaxLoops int
}

func DefaultStateConfig() StateConfig {
	return StateConfig{
		MaxLoops: 5,
	}
}

func NewStateMachine(cfg StateConfig) *StateMachine {
	return &StateMachine{
		current: StateObserve,
		history: []State{StateObserve},
		config:  cfg,
	}
}

func (sm *StateMachine) Current() State {
	return sm.current
}

func (sm *StateMachine) History() []State {
	return sm.history
}

func (sm *StateMachine) Transition(to State) error {
	if !sm.canTransition(to) {
		return fmt.Errorf("invalid transition: %s -> %s", sm.current, to)
	}
	sm.current = to
	sm.history = append(sm.history, to)
	return nil
}

func (sm *StateMachine) Reset() {
	sm.current = StateObserve
	sm.history = []State{StateObserve}
}

func (sm *StateMachine) IterationCount() int {
	count := 0
	for i := 1; i < len(sm.history); i++ {
		if sm.history[i] == StateHypothesize {
			count++
		}
	}
	return count
}

func (sm *StateMachine) canTransition(to State) bool {
	switch sm.current {
	case StateObserve:
		return to == StateHypothesize
	case StateHypothesize:
		return to == StateSearch
	case StateSearch:
		return to == StateGather || to == StateEvaluate
	case StateGather:
		return to == StateEvaluate
	case StateEvaluate:
		return to == StateNarrow || to == StateVerify || to == StateHypothesize
	case StateNarrow:
		return to == StateHypothesize || to == StateSearch
	case StateVerify:
		return to == StatePropose || to == StateHypothesize || to == StateDone
	case StatePropose:
		return to == StateDone
	case StateDone:
		return false
	default:
		return false
	}
}

func (sm *StateMachine) IsTerminal() bool {
	return sm.current == StateDone
}

func (sm *StateMachine) ShouldStop() bool {
	return sm.IsTerminal() || sm.IterationCount() >= sm.config.MaxLoops
}
