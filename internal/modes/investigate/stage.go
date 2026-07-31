package investigate

import (
	"context"
	"fmt"

	"github.com/PizenLabs/izen/internal/core/contract"
	wscap "github.com/PizenLabs/izen/internal/workspace/capability"
	wssnapshot "github.com/PizenLabs/izen/internal/workspace/snapshot"
)

const (
	CtxKeyWorkspaceSnapshot  = "workspace_snapshot"
	CtxKeyCapabilityRegistry = "capability_registry"
)

type InvestigateStage struct {
	engine *Engine
}

func NewInvestigateStage(engine *Engine) *InvestigateStage {
	return &InvestigateStage{engine: engine}
}

func (s *InvestigateStage) Contract() contract.StageContract {
	return contract.StageContract{
		Name:           "investigator",
		AllowedPerms:   []contract.PermissionLevel{contract.PermReadOnly, contract.PermExec, contract.PermTest},
		HasSideEffects: false,
		CanRetry:       true,
	}
}

func (s *InvestigateStage) Execute(ctx context.Context, in contract.StageInput) (contract.StageOutput, error) {
	problem, ok := in.Payload.(string)
	if !ok || problem == "" {
		if p, ok := in.Context["problem"].(string); ok && p != "" {
			problem = p
		} else {
			err := fmt.Errorf("investigate stage: payload must be a non-empty problem string")
			return contract.StageOutput{
				Success:     false,
				Error:       err,
				Recoverable: false,
			}, err
		}
	}

	// Wire snapshot cache and capability registry from context if present.
	if snapCache, ok := in.Context[CtxKeyWorkspaceSnapshot].(*wssnapshot.SnapshotCache); ok && snapCache != nil {
		s.engine.WithSnapshotCache(snapCache)
	}
	if capReg, ok := in.Context[CtxKeyCapabilityRegistry].(*wscap.ArchetypeCapabilityRegistry); ok && capReg != nil {
		s.engine.WithCapabilityRegistry(capReg)
	}

	s.engine.Problem = problem
	result, err := s.engine.RunContext(ctx)
	if err != nil {
		return contract.StageOutput{Success: false, Error: err, Recoverable: true}, err
	}
	return contract.StageOutput{Success: true, Data: result}, nil
}
