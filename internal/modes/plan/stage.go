package plan

import (
	"context"
	"fmt"

	"github.com/PizenLabs/izen/internal/core/contract"
)

const (
	CtxKeyProblem     = "problem"
	CtxKeyModelName   = "model_name"
	CtxKeyFastTrack   = "fast_track"
	CtxKeyFastPrompt  = "fast_prompt"
	CtxKeyLedgerInput = "ledger_input"
)

type PlanStage struct {
	engine *Engine
}

func NewPlanStage(engine *Engine) *PlanStage {
	return &PlanStage{engine: engine}
}

func (s *PlanStage) Contract() contract.StageContract {
	return contract.StageContract{
		Name:           "planner",
		AllowedPerms:   []contract.PermissionLevel{contract.PermReadOnly},
		HasSideEffects: false,
		CanRetry:       true,
	}
}

func (s *PlanStage) Execute(ctx context.Context, in contract.StageInput) (contract.StageOutput, error) {
	ledgerInput, _ := in.Context[CtxKeyLedgerInput].(string)
	problem, _ := in.Context[CtxKeyProblem].(string)
	modelName, _ := in.Context[CtxKeyModelName].(string)

	fastTrack, _ := in.Context[CtxKeyFastTrack].(bool)
	if fastTrack {
		fastPrompt, _ := in.Context[CtxKeyFastPrompt].(string)
		tasks, err := s.engine.ProcessFromLedgerFastTrack(ctx, fastPrompt, modelName)
		if err != nil {
			return contract.StageOutput{Success: false, Error: err, Recoverable: true}, err
		}
		return contract.StageOutput{Success: true, Data: tasks}, nil
	}

	if ledgerInput == "" && problem == "" {
		err := fmt.Errorf("plan stage: neither ledger input nor problem provided")
		return contract.StageOutput{
			Success:     false,
			Error:       err,
			Recoverable: false,
		}, err
	}

	tasks, err := s.engine.ProcessFromLedger(ctx, ledgerInput, problem, modelName)
	if err != nil {
		return contract.StageOutput{Success: false, Error: err, Recoverable: true}, err
	}
	return contract.StageOutput{Success: true, Data: tasks}, nil
}
