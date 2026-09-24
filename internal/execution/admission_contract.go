package execution

import (
	"errors"
	"fmt"
	"strings"

	"github.com/PizenLabs/izen/internal/execution/strategy"
	"github.com/PizenLabs/izen/internal/protocol"
)

// ErrAuthorityExceeded is the execution-admission sentinel for a concrete
// action that crosses the active InteractionContract authority/capability
// ceiling. It is deliberately separate from ErrRiskScopeExceeded: the runtime
// capability set may admit a broad risk class while the active semantic
// contract still forbids that class of action.
var ErrAuthorityExceeded = errors.New("execution: operation exceeds interaction contract authority ceiling")

// ErrAuthorityCeilingExceeded is the protocol-compatible spelling retained for
// callers that already consume the G3 sentinel. Admission errors match both
// identities through AuthorityExceededError.Is, so adding the execution-layer
// boundary does not fork the existing error family.
var ErrAuthorityCeilingExceeded = protocol.ErrAuthorityCeilingExceeded

// AuthorityExceededError is the typed carrier for a contract-bound admission
// rejection. It records only bounded policy facts; it never contains model
// output, command bytes, or workspace contents.
type AuthorityExceededError struct {
	Contract         protocol.InteractionContract
	Operation        string
	AuthorityCeiling protocol.AuthorityCeiling
	Capability       protocol.Capability
	Reason           string
}

func (e *AuthorityExceededError) Error() string {
	if e == nil {
		return ""
	}
	contract := e.Contract
	if contract == "" {
		contract = "unknown"
	}
	operation := e.Operation
	if operation == "" {
		operation = "unknown"
	}
	ceiling := e.AuthorityCeiling
	if ceiling == "" {
		ceiling = "unknown"
	}
	reason := e.Reason
	if reason == "" {
		reason = "operation is outside the active interaction contract capability boundary"
	}
	if e.Capability != "" {
		return fmt.Sprintf("%s: contract=%s ceiling=%s operation=%s capability=%s: %s",
			ErrAuthorityExceeded, contract, ceiling, operation, e.Capability, reason)
	}
	return fmt.Sprintf("%s: contract=%s ceiling=%s operation=%s: %s",
		ErrAuthorityExceeded, contract, ceiling, operation, reason)
}

// Unwrap preserves the execution-admission sentinel identity.
func (e *AuthorityExceededError) Unwrap() error { return ErrAuthorityExceeded }

// Is keeps the execution and protocol authority-ceiling sentinels
// interoperable. The G3 planner/dispatcher uses
// protocol.ErrAuthorityCeilingExceeded; the execution admission layer uses
// ErrAuthorityExceeded.
func (e *AuthorityExceededError) Is(target error) bool {
	return target == ErrAuthorityExceeded || target == protocol.ErrAuthorityCeilingExceeded
}

// AdmissionAuthorityError is a descriptive alias for integrations that name
// the carrier after the admission boundary.
type AdmissionAuthorityError = AuthorityExceededError

// IsAuthorityExceeded reports whether err is an execution admission contract
// rejection (including the protocol-compatible G3 sentinel spelling).
func IsAuthorityExceeded(err error) bool { return errors.Is(err, ErrAuthorityExceeded) }

// AdmissionDecision is the observable verdict of one admission pass.
type AdmissionDecision struct {
	// Allowed reports whether the intent may proceed to execution.
	Allowed bool
	// Requested is the evaluated risk scope of the intent.
	Requested RiskScope
	// Reason explains the verdict deterministically.
	Reason string
	// Snapshot is the verified context snapshot the intent carries forward
	// (nil when context fidelity failed).
	Snapshot *ContextSnapshot
	// InteractionContract and Contract carry the normalized descriptor that
	// was actually checked. They are evidence of the admission boundary, not
	// an authorization grant.
	InteractionContract protocol.InteractionContract
	Contract            *protocol.ContractDescriptor
	// Operation identifies the first requested action rejected by the
	// contract gate, when one was present.
	Operation protocol.Operation
}

// admissionAction is the normalized, request-local action fact consumed by
// both the contract gate and the risk-scope gate. Keeping the source label
// makes rejection evidence deterministic without exposing command contents.
type admissionAction struct {
	operation protocol.Operation
	source    string
	command   string
}

func operationForStrategy(profile strategy.ExecutionStrategyProfile) protocol.Operation {
	switch profile.Strategy {
	case strategy.TargetedMutation, strategy.DirectDeterministic, strategy.MultiFilePlanning:
		return protocol.OperationFileMutate
	case strategy.RepositoryInvestigation, strategy.TargetedReasoning, strategy.DirectResponse, strategy.HumanClarification:
		return protocol.OperationRead
	default:
		return protocol.OperationRead
	}
}

func normalizedAdmissionOperation(raw string) protocol.Operation {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	return protocol.NormalizeOperation(raw)
}

func admissionActions(req ExecuteRequest, profile strategy.ExecutionStrategyProfile) []admissionAction {
	actions := make([]admissionAction, 0, len(req.StagedSubTasks)+2)
	add := func(raw, source, command string) {
		operation := normalizedAdmissionOperation(raw)
		if operation == "" {
			return
		}
		actions = append(actions, admissionAction{operation: operation, source: source, command: command})
	}

	// Explicit operation/task fields are checked before strategy inference.
	// Checking both declarations prevents a caller from smuggling SHELL_EXEC
	// through a FILE_MUTATE label (or the reverse) by filling only one field.
	if strings.TrimSpace(req.Operation) != "" {
		add(req.Operation, "request.operation", req.Command)
	}
	if strings.TrimSpace(req.TaskType) != "" {
		add(req.TaskType, "request.task_type", req.Command)
	}
	for _, scope := range req.StagedSubTasks {
		operation := scope.Operation
		if strings.TrimSpace(operation) == "" {
			// Empty staged operations retain the historical FILE_MUTATE
			// default, but remain an explicit mutation request for contract
			// admission purposes.
			operation = string(protocol.OperationFileMutate)
		}
		command := req.Command
		if normalizedAdmissionOperation(operation) == protocol.OperationShell && strings.TrimSpace(command) == "" {
			command = req.Prompt
		}
		add(operation, "staged_subtask", command)
	}
	// The selected strategy is an independent dispatch fact. Never let an
	// explicit READ/VERIFY label erase a mutation strategy: the executor still
	// takes the mutation path for TargetedMutation/DirectDeterministic.
	if profile.Strategy == strategy.TargetedMutation || profile.Strategy == strategy.DirectDeterministic {
		hasMutation := false
		for _, action := range actions {
			if action.operation == protocol.OperationFileMutate || action.operation == protocol.OperationFileEdit || action.operation == protocol.OperationGitAction {
				hasMutation = true
				break
			}
		}
		if !hasMutation {
			add(string(protocol.OperationFileMutate), "strategy", "")
		}
	}
	if len(actions) == 0 {
		if strings.TrimSpace(req.Command) != "" {
			add(string(protocol.OperationShell), "request.command", req.Command)
		} else {
			// Presentation modes are hard read-only ceilings. A legacy
			// classifier may label a no-target explanation as MultiFilePlanning;
			// that plan is not a mutation request in ASK/PLAN mode.
			mode := strings.ToLower(strings.TrimSpace(req.Mode))
			if mode == "ask" || mode == "review" || mode == "investigate" || mode == "plan" {
				add(string(protocol.OperationRead), "mode_ceiling", "")
			} else {
				add(string(operationForStrategy(profile)), "strategy", "")
			}
		}
	}
	return actions
}

func planningAdmissionActions(req ExecuteRequest, profile strategy.ExecutionStrategyProfile) []admissionAction {
	if profile.Strategy != strategy.MultiFilePlanning ||
		strings.TrimSpace(req.Operation) != "" ||
		strings.TrimSpace(req.TaskType) != "" ||
		len(req.StagedSubTasks) > 0 ||
		strings.TrimSpace(req.Command) != "" {
		return admissionActions(req, profile)
	}
	// MultiFilePlanning is a proposal/plan turn, not an executed workspace
	// mutation. The legacy risk classifier already treats it as read-only;
	// keep the contract gate's inferred operation aligned with that meaning.
	return []admissionAction{{operation: protocol.OperationRead, source: "strategy"}}
}

func capabilityForOperation(operation protocol.Operation) protocol.Capability {
	switch operation {
	case protocol.OperationRead:
		return protocol.CapabilityRead
	case protocol.OperationVerify:
		return protocol.CapabilityVerify
	case protocol.OperationFileMutate, protocol.OperationFileEdit, protocol.OperationGitAction:
		return protocol.CapabilityMutate
	case protocol.OperationShell:
		return protocol.CapabilityShell
	case protocol.OperationTool:
		return protocol.CapabilityTool
	case protocol.OperationDestructive:
		return protocol.CapabilityDestructive
	default:
		return ""
	}
}

func admissionContractError(descriptor protocol.ContractDescriptor, action admissionAction, reason string) error {
	return &AuthorityExceededError{
		Contract:         descriptor.Contract,
		Operation:        string(action.operation),
		AuthorityCeiling: descriptor.AuthorityCeiling,
		Capability:       capabilityForOperation(action.operation),
		Reason:           reason,
	}
}

func resolveAdmissionContract(req ExecuteRequest, supplied []*protocol.ContractDescriptor) (protocol.ContractDescriptor, bool, error) {
	if len(supplied) > 1 {
		return protocol.ContractDescriptor{}, false, fmt.Errorf("%w: admission received multiple contract descriptors", protocol.ErrInvalidContract)
	}
	var candidate *protocol.ContractDescriptor
	switch {
	case req.Contract != nil:
		// The descriptor carried on the request is authoritative. An optional
		// argument may supply it for callers that have not attached it yet, but
		// it can never override a stricter request descriptor.
		candidate = req.Contract
	case len(supplied) > 0 && supplied[0] != nil:
		candidate = supplied[0]
	case strings.TrimSpace(string(req.InteractionContract)) != "":
		if !req.InteractionContract.Valid() {
			return protocol.ContractDescriptor{}, false, fmt.Errorf("%w: unknown interaction contract %q", protocol.ErrInvalidContract, req.InteractionContract)
		}
		descriptor := protocol.Describe(req.InteractionContract)
		candidate = &descriptor
	}
	// A staged autonomy request has a semantic contract even when a legacy
	// caller omitted the enum. Derive the conservative agentic descriptor
	// here so the admission layer never treats staged scopes as an untyped
	// legacy mutation.
	if candidate == nil && len(req.StagedSubTasks) > 0 {
		descriptor := protocol.Describe(protocol.AgenticLoop)
		candidate = &descriptor
	}
	if candidate == nil && (strings.TrimSpace(req.Operation) != "" || strings.TrimSpace(req.TaskType) != "" || strings.TrimSpace(req.Command) != "") {
		// An explicit action without a descriptor is not allowed to inherit an
		// untyped legacy lane. Derive the narrowest semantic contract that can
		// represent the action; runtime AdmittedCapabilities remains a separate
		// mandatory grant below.
		operation := normalizedAdmissionOperation(req.Operation)
		if operation == "" {
			operation = normalizedAdmissionOperation(req.TaskType)
		}
		if operation == "" {
			operation = protocol.OperationShell
		}
		contract := protocol.DirectCompletion
		switch operation {
		case protocol.OperationFileMutate, protocol.OperationFileEdit, protocol.OperationGitAction,
			protocol.OperationShell, protocol.OperationTool, protocol.OperationDestructive:
			contract = protocol.AgenticLoop
		}
		descriptor := protocol.Describe(contract)
		candidate = &descriptor
	}
	if candidate == nil {
		return protocol.ContractDescriptor{}, false, nil
	}
	normalized, err := candidate.Clone().Normalize()
	if err != nil {
		return protocol.ContractDescriptor{}, false, err
	}
	if req.InteractionContract != "" && normalized.Contract != req.InteractionContract {
		return protocol.ContractDescriptor{}, false, fmt.Errorf("%w: request contract %q does not match descriptor %q", protocol.ErrInvalidContract, req.InteractionContract, normalized.Contract)
	}
	return normalized, true, nil
}

func validateAdmissionContract(req ExecuteRequest, profile strategy.ExecutionStrategyProfile, descriptor protocol.ContractDescriptor) (protocol.Operation, error) {
	actions := admissionActions(req, profile)
	// G3's direct-completion ceiling test intentionally treats a planning
	// strategy as an attempted FILE_MUTATE. Structured/tool contracts, however,
	// are exactly the contracts that may carry a plan without executing it.
	if descriptor.Contract != protocol.DirectCompletion {
		actions = planningAdmissionActions(req, profile)
	}
	for _, action := range actions {
		if !descriptor.AllowsOperation(string(action.operation)) {
			return action.operation, admissionContractError(descriptor, action, fmt.Sprintf("%s is outside the declared authority/capability boundary", action.source))
		}
		if descriptor.Archetype == protocol.ArchetypeVanillaWeb && action.operation == protocol.OperationShell {
			return action.operation, admissionContractError(descriptor, action, "VANILLA_WEB contract cannot dispatch a shell operation")
		}
	}
	return "", nil
}

// ValidateContractAdmission applies the contract-only admission gate without
// consulting runtime capability grants. It is useful to dispatchers that need
// to reject a forbidden semantic action before constructing a full execution
// request; the canonical RuntimeExecutor still calls AdmissionGateway.Admit so
// context fidelity and the runtime grant are checked in the same pass.
func ValidateContractAdmission(req ExecuteRequest, profile strategy.ExecutionStrategyProfile, descriptor *protocol.ContractDescriptor) error {
	resolved, hasContract, err := resolveAdmissionContract(req, []*protocol.ContractDescriptor{descriptor})
	if err != nil {
		return err
	}
	if !hasContract {
		return fmt.Errorf("%w: admission request carries no interaction contract", protocol.ErrInvalidContract)
	}
	if err := validateAdmissionBudget(req, profile, resolved); err != nil {
		return err
	}
	_, err = validateAdmissionContract(req, profile, resolved)
	return err
}

// ValidateContractDescriptorAdmission is the value-form companion to
// ValidateContractAdmission for callers that keep descriptors by value.
func ValidateContractDescriptorAdmission(req ExecuteRequest, profile strategy.ExecutionStrategyProfile, descriptor protocol.ContractDescriptor) error {
	copy := descriptor.Clone()
	return ValidateContractAdmission(req, profile, &copy)
}

func validateAdmissionBudget(req ExecuteRequest, profile strategy.ExecutionStrategyProfile, descriptor protocol.ContractDescriptor) error {
	if descriptor.MaxOutputTokens <= 0 {
		return nil
	}
	budget := req.MaxOutputTokens
	if budget <= 0 {
		budget = profile.MaxOutputTokens
	}
	if budget > descriptor.MaxOutputTokens {
		return &protocol.ContractViolationError{
			Contract:  descriptor.Contract,
			Operation: "output_budget",
			Reason:    fmt.Sprintf("selected budget %d exceeds descriptor ceiling %d", budget, descriptor.MaxOutputTokens),
		}
	}
	return nil
}

func evaluateAdmissionRisk(req ExecuteRequest, profile strategy.ExecutionStrategyProfile) RiskVerdict {
	targets := req.Targets
	if len(targets) == 0 && req.Target != "" {
		targets = []string{req.Target}
	}
	actions := planningAdmissionActions(req, profile)
	if len(actions) == 0 {
		return EvaluateRiskScope(RiskInput{Strategy: string(profile.Strategy), Targets: targets, Command: req.Command})
	}
	combined := RiskVerdict{Scope: ScopeReadOnly}
	for _, action := range actions {
		verdict := EvaluateRiskScope(RiskInput{
			TaskType: string(action.operation),
			Command:  action.command,
			Targets:  targets,
		})
		if verdict.Scope > combined.Scope {
			combined.Scope = verdict.Scope
		}
		combined.Reasons = append(combined.Reasons, verdict.Reasons...)
	}
	return combined
}

// AdmitWithContract is the explicit descriptor-threading form of Admit. It is
// equivalent to passing the descriptor as Admit's optional final argument and
// keeps call sites self-documenting at dispatch boundaries.
func (g *AdmissionGateway) AdmitWithContract(req ExecuteRequest, root string, profile strategy.ExecutionStrategyProfile, descriptor *protocol.ContractDescriptor) (AdmissionDecision, error) {
	return g.Admit(req, root, profile, descriptor)
}

// Admit runs all admission checks for one intent against the given strategy
// profile: context fidelity first, then the active InteractionContract
// authority/capability ceiling, then the runtime risk-scope grant. The
// optional descriptor is an explicit thread point for callers that normalize a
// contract before constructing the ExecuteRequest; when omitted, the request's
// own Contract/InteractionContract fields are authoritative.
func (g *AdmissionGateway) Admit(req ExecuteRequest, root string, profile strategy.ExecutionStrategyProfile, supplied ...*protocol.ContractDescriptor) (AdmissionDecision, error) {
	snapshot, err := verifyIntentContext(req, root)
	if err != nil {
		return AdmissionDecision{Requested: ScopeDestructive, Reason: "context fidelity verification failed"}, err
	}

	descriptor, hasContract, err := resolveAdmissionContract(req, supplied)
	if err != nil {
		return AdmissionDecision{Requested: ScopeDestructive, Reason: "interaction contract descriptor is invalid"}, err
	}
	verdict := evaluateAdmissionRisk(req, profile)
	decision := AdmissionDecision{Requested: verdict.Scope, Snapshot: snapshot}
	if hasContract {
		descriptorCopy := descriptor.Clone()
		decision.Contract = &descriptorCopy
		decision.InteractionContract = descriptor.Contract
	}

	// Contract admission is a hard ceiling. It runs before the broader
	// capability grant so widening AdmittedCapabilities can never turn a
	// READ_ONLY/PROPOSE contract into an execution grant.
	if hasContract {
		if budgetErr := validateAdmissionBudget(req, profile, descriptor); budgetErr != nil {
			decision.Reason = budgetErr.Error()
			return decision, budgetErr
		}
		operation, contractErr := validateAdmissionContract(req, profile, descriptor)
		if contractErr != nil {
			decision.Operation = operation
			decision.Reason = contractErr.Error()
			return decision, contractErr
		}
	}

	caps := g.caps.Load()
	if caps == nil || !caps.Allows(verdict.Scope) {
		decision.Reason = fmt.Sprintf("intent evaluated as %s exceeds admitted capabilities: %s",
			verdict.Scope, strings.Join(verdict.Reasons, "; "))
		return decision, fmt.Errorf("%w: evaluated %s", ErrRiskScopeExceeded, verdict.Scope)
	}
	decision.Allowed = true
	decision.Reason = strings.Join(verdict.Reasons, "; ")
	return decision, nil
}
