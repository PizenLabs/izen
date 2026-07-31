package build

import (
	"context"
	"fmt"

	"github.com/PizenLabs/izen/internal/core/contract"
)

const (
	CtxKeyMutations = "mutations"
	CtxKeyPackages  = "packages"
)

type BuildStage struct {
	engine   *Engine
	executor *Executor
}

func NewBuildStage(engine *Engine, executor *Executor) *BuildStage {
	return &BuildStage{
		engine:   engine,
		executor: executor,
	}
}

func (s *BuildStage) Contract() contract.StageContract {
	return contract.StageContract{
		Name:           "executor",
		AllowedPerms:   []contract.PermissionLevel{contract.PermWorkspace, contract.PermExec, contract.PermPatch, contract.PermCheckpoint},
		HasSideEffects: true,
		CanRetry:       true,
	}
}

func (s *BuildStage) Execute(ctx context.Context, in contract.StageInput) (contract.StageOutput, error) {
	switch payload := in.Payload.(type) {
	case FileMutation:
		if err := s.executor.ApplyMutation(ctx, payload); err != nil {
			return contract.StageOutput{Success: false, Error: err, Recoverable: true}, err
		}
		return contract.StageOutput{Success: true, Data: fmt.Sprintf("applied mutation to %s", payload.File)}, nil

	case []FileMutation:
		var results []string
		for _, mut := range payload {
			if err := s.executor.ApplyMutation(ctx, mut); err != nil {
				return contract.StageOutput{Success: false, Error: fmt.Errorf("mutation %s failed: %w", mut.File, err), Recoverable: true}, err
			}
			results = append(results, mut.File)
		}
		return contract.StageOutput{Success: true, Data: results}, nil

	default:
		err := fmt.Errorf("build stage: unexpected payload type %T", in.Payload)
		return contract.StageOutput{
			Success:     false,
			Error:       err,
			Recoverable: false,
		}, err
	}
}
