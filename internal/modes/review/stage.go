package review

import (
	"context"

	"github.com/PizenLabs/izen/internal/core/contract"
)

const (
	CtxKeyTarget = "target"
)

type ReviewStage struct {
	engine *Engine
}

func NewReviewStage(engine *Engine) *ReviewStage {
	return &ReviewStage{engine: engine}
}

func (s *ReviewStage) Contract() contract.StageContract {
	return contract.StageContract{
		Name:           "reviewer",
		AllowedPerms:   []contract.PermissionLevel{contract.PermReadOnly},
		HasSideEffects: false,
		CanRetry:       false,
	}
}

func (s *ReviewStage) Execute(ctx context.Context, in contract.StageInput) (contract.StageOutput, error) {
	target, _ := in.Context[CtxKeyTarget].(string)
	if target == "" {
		if t, ok := in.Payload.(string); ok {
			target = t
		}
	}

	var result *ReviewResult
	var err error

	if target != "" {
		//nolint:contextcheck // RunTarget predates context propagation
		result, err = s.engine.RunTarget(target)
	} else {
		//nolint:contextcheck // Run predates context propagation
		result, err = s.engine.Run()
	}

	if err != nil {
		return contract.StageOutput{Success: false, Error: err, Recoverable: false}, err
	}
	return contract.StageOutput{Success: true, Data: result}, nil
}
