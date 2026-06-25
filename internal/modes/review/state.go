package review

import "fmt"

type StateMachine struct {
	current State
	history []State
	config  StateConfig
}

type StateConfig struct {
	MaxIterations int
	DetailedAudit bool
}

func DefaultStateConfig() StateConfig {
	return StateConfig{
		MaxIterations: 3,
		DetailedAudit: true,
	}
}

func NewStateMachine(cfg StateConfig) *StateMachine {
	return &StateMachine{
		current: StateCollect,
		history: []State{StateCollect},
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
	sm.current = StateCollect
	sm.history = []State{StateCollect}
}

func (sm *StateMachine) IterationCount() int {
	count := 0
	for i := 1; i < len(sm.history); i++ {
		if sm.history[i] == StateAnalyzeDiff {
			count++
		}
	}
	return count
}

func (sm *StateMachine) IsTerminal() bool {
	return sm.current == StateDone
}

func (sm *StateMachine) ShouldStop() bool {
	return sm.IsTerminal() || sm.IterationCount() >= sm.config.MaxIterations
}

func (sm *StateMachine) canTransition(to State) bool {
	switch sm.current {
	case StateCollect:
		return to == StateAnalyzeDiff || to == StateDone
	case StateAnalyzeDiff:
		return to == StateImpactRadius || to == StateReport
	case StateImpactRadius:
		return to == StateRiskAudit || to == StateAnalyzeDiff
	case StateRiskAudit:
		return to == StateReport || to == StateImpactRadius
	case StateReport:
		return to == StateDone || to == StateAnalyzeDiff
	case StateDone:
		return false
	default:
		return false
	}
}