package plan

import (
	"context"
	"fmt"

	"github.com/PizenLabs/izen/internal/core/contract"
	"github.com/PizenLabs/izen/internal/protocol"
	wscap "github.com/PizenLabs/izen/internal/workspace/capability"
	wssnapshot "github.com/PizenLabs/izen/internal/workspace/snapshot"
)

const (
	CtxKeyProblem             = "problem"
	CtxKeyModelName           = "model_name"
	CtxKeyFastTrack           = "fast_track"
	CtxKeyFastPrompt          = "fast_prompt"
	CtxKeyLedgerInput         = "ledger_input"
	CtxKeyWorkspaceSnapshot   = "workspace_snapshot"
	CtxKeyCapabilityRegistry  = "capability_registry"
	CtxKeyInteractionContract = "interaction_contract"
	CtxKeyContractDescriptor  = "contract_descriptor"
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
	if s == nil || s.engine == nil {
		err := fmt.Errorf("plan stage: engine is not configured")
		return contract.StageOutput{Success: false, Error: err}, err
	}
	ledgerInput, _ := in.Context[CtxKeyLedgerInput].(string)
	problem, _ := in.Context[CtxKeyProblem].(string)
	modelName, _ := in.Context[CtxKeyModelName].(string)

	// Wire snapshot cache and capability registry from context if present.
	if snapCache, ok := in.Context[CtxKeyWorkspaceSnapshot].(*wssnapshot.SnapshotCache); ok && snapCache != nil {
		s.engine.WithSnapshotCache(snapCache)
	}
	if capReg, ok := in.Context[CtxKeyCapabilityRegistry].(*wscap.ArchetypeCapabilityRegistry); ok && capReg != nil {
		s.engine.WithCapabilityRegistry(capReg)
	}

	if ledgerInput == "" && problem == "" {
		err := fmt.Errorf("plan stage: neither ledger input nor problem provided")
		return contract.StageOutput{
			Success:     false,
			Error:       err,
			Recoverable: false,
		}, err
	}

	fastTrack, _ := in.Context[CtxKeyFastTrack].(bool)
	interaction, _ := in.Context[CtxKeyInteractionContract].(protocol.InteractionContract)
	var descriptor *protocol.ContractDescriptor
	switch value := in.Context[CtxKeyContractDescriptor].(type) {
	case *protocol.ContractDescriptor:
		descriptor = value
	case protocol.ContractDescriptor:
		copy := value.Clone()
		descriptor = &copy
	}
	synthesis := SynthesisRequest{
		LedgerContent:       ledgerInput,
		Problem:             problem,
		ModelName:           modelName,
		FastTrack:           fastTrack,
		InteractionContract: interaction,
		Descriptor:          descriptor,
	}
	if fastTrack {
		synthesis.FastPrompt, _ = in.Context[CtxKeyFastPrompt].(string)
	}
	tasks, err := s.engine.Synthesize(ctx, synthesis)
	if err != nil {
		return contract.StageOutput{Success: false, Error: err, Recoverable: true}, err
	}
	return contract.StageOutput{Success: true, Data: tasks}, nil
}
