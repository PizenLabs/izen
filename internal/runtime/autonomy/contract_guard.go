package autonomy

import (
	"fmt"
	"strings"

	core "github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/execution/planner"
	"github.com/PizenLabs/izen/internal/protocol"
)

// ErrAuthorityCeilingExceeded is the autonomy-package alias for a staged
// operation rejected by the active interaction contract.
var ErrAuthorityCeilingExceeded = protocol.ErrAuthorityCeilingExceeded

// ErrInvalidContract is the autonomy-boundary alias for malformed semantic
// metadata.
var ErrInvalidContract = protocol.ErrInvalidContract

// ValidateDispatchContract is the autonomy dispatcher's fail-closed guard. It
// runs after target/workspace resolution but before the adapter submits an
// ExecuteRequest, so a forbidden staged operation cannot consume a provider
// call, create a patch, or alter the workspace.
//
// The contract is a semantic ceiling, not an authorization grant. Even an
// operation that passes this guard is still evaluated by execution admission,
// human approval, and the canonical RuntimeExecutor.
func ValidateDispatchContract(req core.LoopRequest) error {
	contract, descriptor, err := normalizedRequestContract(req)
	if err != nil {
		return err
	}
	if descriptor.MaxOutputTokens > 0 {
		budget := req.MaxOutputTokens
		if req.ExplicitOutputBudget > budget {
			budget = req.ExplicitOutputBudget
		}
		if budget > descriptor.MaxOutputTokens {
			return &protocol.ContractViolationError{
				Contract:  contract,
				Operation: "output_budget",
				Reason:    fmt.Sprintf("requested %d tokens exceeds descriptor ceiling %d", budget, descriptor.MaxOutputTokens),
			}
		}
	}

	if req.StagedPlan != nil {
		return validateStagedDAGContract(contract, descriptor, req.StagedPlan)
	}
	if len(req.StagedSubTasks) > 0 {
		for _, scope := range req.StagedSubTasks {
			operation := protocol.NormalizeOperation(scope.Operation)
			if operation == "" {
				operation = protocol.OperationFileMutate
			}
			if err := requireOperation(contract, descriptor, operation, "staged_subtasks"); err != nil {
				return err
			}
			if descriptor.Archetype == protocol.ArchetypeVanillaWeb && operation == protocol.OperationShell {
				return &protocol.ContractViolationError{Contract: contract, Operation: scope.Operation, Reason: "VANILLA_WEB contract cannot dispatch a shell operation"}
			}
		}
	}

	// A recovery request that names a mutation strategy is an explicit request
	// to cross the dispatch boundary even when the objective text is terse.
	if req.MutationStrategy != "" && req.MutationStrategy != StrategyInspectOnly.String() {
		if err := requireOperation(contract, descriptor, protocol.OperationFileMutate, "mutation_strategy"); err != nil {
			return err
		}
	}
	if req.RecoveryStrategy != "" && req.RecoveryStrategy != StrategyInspectOnly.String() {
		if err := requireOperation(contract, descriptor, protocol.OperationFileMutate, "recovery_strategy"); err != nil {
			return err
		}
	}

	// The direct adapter path has no explicit task type. Classify the objective
	// with the same canonical intent classifier used by the driver; a mutation
	// objective therefore cannot be smuggled through a read-only descriptor.
	intent := req.Intent
	if intent == "" {
		intent = req.Prompt
	}
	classified := core.Classify(intent, nil)
	if canonical := core.ParseIntent(intent); canonical.RequiresMutation() {
		classified.Intent = canonical
	}
	if looksLikeMutationObjective(intent) {
		classified.Intent = core.IntentModification
	}
	if classified.Intent.RequiresMutation() {
		if err := requireOperation(contract, descriptor, protocol.OperationFileMutate, "intent"); err != nil {
			return err
		}
	}
	return nil
}

// ValidateObjectiveContract applies the contract ceiling to the classified
// objective before preflight, manifest generation, or any provider-capable
// dispatcher is entered.
func ValidateObjectiveContract(objective string, contract protocol.InteractionContract, descriptor *protocol.ContractDescriptor) error {
	d := protocol.ContractDescriptor{}
	if descriptor != nil {
		d = descriptor.Clone()
	} else {
		d = protocol.Describe(contract)
	}
	normalized, err := d.Normalize()
	if err != nil {
		return err
	}
	if contract.Valid() && normalized.Contract != contract {
		return fmt.Errorf("autonomy: %w: objective contract %q does not match descriptor %q", protocol.ErrInvalidContract, contract, normalized.Contract)
	}
	classified := core.Classify(objective, nil)
	if canonical := core.ParseIntent(objective); canonical.RequiresMutation() {
		classified.Intent = canonical
	}
	if !classified.Intent.RequiresMutation() {
		return nil
	}
	return requireOperation(normalized.Contract, normalized, protocol.OperationFileMutate, "objective")
}

// ValidateStagedPlanContract validates a decomposition plan without coupling
// callers to the driver's private request state. It is useful at approval and
// persistence seams as well as in the dispatcher.
func ValidateStagedPlanContract(contract protocol.InteractionContract, descriptor *protocol.ContractDescriptor, dag *planner.ExecutionDAG) error {
	if dag == nil {
		return nil
	}
	var normalized protocol.ContractDescriptor
	if descriptor != nil {
		copy := descriptor.Clone()
		var err error
		normalized, err = copy.Normalize()
		if err != nil {
			return err
		}
	} else {
		normalized = protocol.Describe(contract)
		var err error
		normalized, err = normalized.Normalize()
		if err != nil {
			return err
		}
	}
	if !normalized.Contract.Valid() {
		return fmt.Errorf("autonomy: %w: unknown interaction contract %q", protocol.ErrInvalidContract, normalized.Contract)
	}
	if contract.Valid() && normalized.Contract != contract {
		return fmt.Errorf("autonomy: %w: staged-plan contract %q does not match descriptor %q", protocol.ErrInvalidContract, contract, normalized.Contract)
	}
	return validateStagedDAGContract(normalized.Contract, normalized, dag)
}

func normalizedRequestContract(req core.LoopRequest) (protocol.InteractionContract, protocol.ContractDescriptor, error) {
	contract := req.InteractionContract
	if contract != "" && !contract.Valid() {
		return "", protocol.ContractDescriptor{}, fmt.Errorf("autonomy: %w: unknown interaction contract %q", protocol.ErrInvalidContract, contract)
	}
	if !contract.Valid() && req.Contract != nil {
		contract = req.Contract.Contract
		if !contract.Valid() {
			contract = req.Contract.Kind
		}
	}
	if !contract.Valid() {
		intent := req.Intent
		if intent == "" {
			intent = req.Prompt
		}
		classified := core.Classify(intent, nil)
		caps := make([]string, 0, len(classified.Required))
		for _, capability := range classified.Required {
			caps = append(caps, string(capability))
		}
		contract = protocol.SelectInteractionContract(intent, "autonomy", caps...)
		if looksLikeMutationObjective(intent) {
			contract = protocol.AgenticLoop
		}
	}
	var descriptor protocol.ContractDescriptor
	if req.Contract != nil {
		descriptor = req.Contract.Clone()
	} else {
		descriptor = protocol.Describe(contract)
	}
	if descriptor.Contract != "" && descriptor.Contract != contract {
		return "", protocol.ContractDescriptor{}, fmt.Errorf("autonomy: %w: request contract %q does not match descriptor %q", protocol.ErrInvalidContract, contract, descriptor.Contract)
	}
	normalized, err := descriptor.Normalize()
	if err != nil {
		return "", protocol.ContractDescriptor{}, fmt.Errorf("autonomy: %w: %w", protocol.ErrInvalidContract, err)
	}
	if contract.Valid() && normalized.Contract != contract {
		return "", protocol.ContractDescriptor{}, fmt.Errorf("autonomy: %w: request contract %q does not match descriptor %q", protocol.ErrInvalidContract, contract, normalized.Contract)
	}
	return normalized.Contract, normalized, nil
}

func validateStagedDAGContract(contract protocol.InteractionContract, descriptor protocol.ContractDescriptor, dag *planner.ExecutionDAG) error {
	if err := dag.Validate(); err != nil {
		return fmt.Errorf("autonomy: staged plan violates contract-bound DAG invariants: %w", err)
	}
	if descriptor.MaxOutputTokens > 0 && dag.MaxOutputTokens > descriptor.MaxOutputTokens {
		return &protocol.ContractViolationError{
			Contract:  contract,
			Operation: "output_budget",
			Reason:    fmt.Sprintf("staged plan budget %d exceeds descriptor ceiling %d", dag.MaxOutputTokens, descriptor.MaxOutputTokens),
		}
	}
	for _, subTask := range dag.SubTasks {
		operation := subTask.EffectiveOperation()
		normalizedOperation := protocol.NormalizeOperation(operation)
		if err := requireOperation(contract, descriptor, normalizedOperation, "staged_subtask"); err != nil {
			return err
		}
		if descriptor.Archetype == protocol.ArchetypeVanillaWeb && normalizedOperation == protocol.OperationShell {
			return &protocol.ContractViolationError{Contract: contract, Operation: operation, Reason: "VANILLA_WEB contract cannot dispatch a shell operation"}
		}
	}
	return nil
}

func requireOperation(contract protocol.InteractionContract, descriptor protocol.ContractDescriptor, operation protocol.Operation, source string) error {
	if descriptor.AllowsOperation(string(operation)) {
		return nil
	}
	return &protocol.ContractViolationError{
		Contract:  contract,
		Operation: string(operation),
		Reason:    fmt.Sprintf("%s is outside the declared authority/capability boundary", source),
	}
}

func looksLikeMutationObjective(value string) bool {
	lower := strings.ToLower(strings.TrimSpace(value))
	if lower == "" {
		return false
	}
	for _, verb := range []string{
		"add ", "change ", "create ", "delete ", "edit ", "fix ", "implement ",
		"inject ", "modify ", "move ", "refactor ", "remove ", "replace ",
		"restyle ", "rewrite ", "update ", "write ",
	} {
		if strings.Contains(lower, verb) {
			return true
		}
	}
	return false
}

func cloneContract(descriptor *protocol.ContractDescriptor) *protocol.ContractDescriptor {
	if descriptor == nil {
		return nil
	}
	copy := descriptor.Clone()
	return &copy
}
