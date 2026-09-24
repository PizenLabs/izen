package execution

import (
	"fmt"
	"strings"

	"github.com/PizenLabs/izen/internal/execution/strategy"
	"github.com/PizenLabs/izen/internal/protocol"
)

// SetInteractionContract binds the semantic contract used by the legacy
// Engine facade. The descriptor is copied at the boundary; callers cannot
// mutate an active contract while a command or patch is being admitted.
func (e *Engine) SetInteractionContract(contract protocol.InteractionContract, descriptor *protocol.ContractDescriptor) error {
	if e == nil {
		return fmt.Errorf("execution: nil engine")
	}
	var candidate protocol.ContractDescriptor
	if descriptor != nil {
		candidate = descriptor.Clone()
	} else {
		if !contract.Valid() {
			return fmt.Errorf("%w: unknown interaction contract %q", protocol.ErrInvalidContract, contract)
		}
		candidate = protocol.Describe(contract)
	}
	normalized, err := candidate.Normalize()
	if err != nil {
		return err
	}
	if contract.Valid() && normalized.Contract != contract {
		return fmt.Errorf("%w: engine contract %q does not match descriptor %q", protocol.ErrInvalidContract, contract, normalized.Contract)
	}
	e.contract.Store(&normalized)
	check := func(operation string) error { return e.AdmitOperation(operation) }
	if e.Runner != nil {
		e.Runner.SetAdmissionCheck(check)
	}
	if e.Patches != nil {
		e.Patches.SetAdmissionCheck(check)
	}
	return nil
}

// Admission returns the legacy Engine's contract-bound admission gateway.
func (e *Engine) Admission() *AdmissionGateway {
	if e == nil {
		return nil
	}
	return e.admissionGateway()
}

// SetAdmittedCapabilities replaces the legacy Engine's runtime capability
// grant. A nil value restores the standard read/workspace-mutation set.
func (e *Engine) SetAdmittedCapabilities(caps *AdmittedCapabilities) {
	if e == nil {
		return
	}
	if e.admission == nil {
		e.admission = NewAdmissionGateway(nil)
	}
	e.admission.SetCapabilities(caps)
}

// ActiveInteractionContract returns a defensive copy of the descriptor bound
// to the legacy Engine, or nil when no contract has been selected.
func (e *Engine) ActiveInteractionContract() *protocol.ContractDescriptor {
	if e == nil {
		return nil
	}
	descriptor := e.contract.Load()
	if descriptor == nil {
		return nil
	}
	copy := descriptor.Clone()
	return &copy
}

func (e *Engine) admissionGateway() *AdmissionGateway {
	if e.admission == nil {
		e.admission = NewAdmissionGateway(nil)
	}
	return e.admission
}

func legacyProfileForOperation(operation protocol.Operation) strategy.ExecutionStrategyProfile {
	switch operation {
	case protocol.OperationFileMutate, protocol.OperationFileEdit, protocol.OperationGitAction:
		return strategy.ExecutionStrategyProfile{Strategy: strategy.TargetedMutation}
	case protocol.OperationShell, protocol.OperationDestructive:
		// The explicit operation is the authority fact here; use a read-only
		// strategy profile so the engine does not infer a second FILE_MUTATE.
		return strategy.ExecutionStrategyProfile{Strategy: strategy.TargetedReasoning}
	default:
		return strategy.ExecutionStrategyProfile{Strategy: strategy.TargetedReasoning}
	}
}

// AdmitOperation verifies one legacy Engine action against its active contract
// and runtime capability grant. It is intentionally a pure admission call: no
// authorization token, lock, command, or filesystem write is touched here.
func (e *Engine) AdmitOperation(operation string) error {
	if e == nil {
		return fmt.Errorf("execution: nil engine")
	}
	descriptor := e.contract.Load()
	if descriptor == nil {
		return nil
	}
	op := protocol.NormalizeOperation(operation)
	if op == "" {
		op = protocol.OperationRead
	}
	profile := legacyProfileForOperation(op)
	_, err := e.admissionGateway().Admit(ExecuteRequest{
		Operation:           string(op),
		InteractionContract: descriptor.Contract,
		Contract:            cloneExecutionDescriptor(descriptor),
	}, e.root, profile, descriptor)
	return err
}

// Run is the contract-aware legacy command facade. Callers that need a
// contract-bound shell operation should use this method rather than reaching
// directly into Runner; a nil active contract preserves legacy behavior.
func (e *Engine) Run(command string) (*RunResult, error) {
	if err := e.AdmitOperation(string(protocol.OperationShell)); err != nil {
		return nil, err
	}
	if e == nil || e.Runner == nil {
		return nil, fmt.Errorf("execution: engine runner is not wired")
	}
	return e.Runner.Run(command)
}

// RunInDir is the directory-scoped contract-aware command facade.
func (e *Engine) RunInDir(command, dir string) (*RunResult, error) {
	if err := e.AdmitOperation(string(protocol.OperationShell)); err != nil {
		return nil, err
	}
	if e == nil || e.Runner == nil {
		return nil, fmt.Errorf("execution: engine runner is not wired")
	}
	return e.Runner.RunInDir(command, dir)
}

// Apply is the contract-aware legacy patch facade. The admission check runs
// before PatchManager can acquire its mutation boundary or touch disk.
func (e *Engine) Apply(patch *Patch) error {
	if err := e.AdmitOperation(string(protocol.OperationFileMutate)); err != nil {
		return err
	}
	if e == nil || e.Patches == nil {
		return fmt.Errorf("execution: engine patch manager is not wired")
	}
	return e.Patches.Apply(patch)
}

// ContractAllowsOperation exposes the legacy Engine's contract-only check for
// callers that need to classify an operation before constructing a command.
func (e *Engine) ContractAllowsOperation(operation string) bool {
	if e == nil {
		return false
	}
	descriptor := e.ActiveInteractionContract()
	if descriptor == nil {
		return true
	}
	return descriptor.AllowsOperation(strings.TrimSpace(operation))
}
