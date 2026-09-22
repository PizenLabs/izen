package scheduler

import "github.com/PizenLabs/izen/internal/runtime/durable"

type StepStrategy string

const (
	DIRECT_CREATE     StepStrategy = "DIRECT_CREATE"
	SKELETON_CREATE   StepStrategy = "SKELETON_CREATE"
	BOUNDED_EXPANSION StepStrategy = "BOUNDED_EXPANSION"
	BOUNDED_PATCH     StepStrategy = "BOUNDED_PATCH"
)

const OutputCeilingReason = "OUTPUT_CEILING"

type Task struct {
	Operation        string
	EstimatedWork    int
	RecoveryContext  durable.RecoveryContext
	StateFingerprint string
}

type NoProgressError struct {
	Target           string
	Strategy         StepStrategy
	StateFingerprint string
	Reason           string
}

func (e *NoProgressError) Error() string {
	return "scheduler: no progress for " + e.Target + " with " + string(e.Strategy) + ": " + e.Reason
}

func ResolveStrategy(task Task, budget int, diskSize int64, history []StepOutcome) StepStrategy {
	r := task.RecoveryContext
	partial := r.Phase == durable.RecoveryRequired
	if len(history) > 0 {
		partial = partial || history[len(history)-1] == StepOutcomePartial
	}
	ceiling := partial && r.Reason == OutputCeilingReason
	var strategy StepStrategy
	switch task.Operation {
	case "CREATE":
		switch {
		case diskSize > 0:
			strategy = BOUNDED_EXPANSION
		case diskSize < 0:
			return ""
		case ceiling || (budget > 0 && budget <= 1024 && task.EstimatedWork > budget):
			strategy = SKELETON_CREATE
		default:
			strategy = DIRECT_CREATE
		}
	case "MODIFY":
		if diskSize <= 0 {
			return ""
		}
		strategy = BOUNDED_PATCH
	default:
		return ""
	}
	if ceiling && r.StateFingerprint == task.StateFingerprint && r.Strategy == string(strategy) {
		return ""
	}
	return strategy
}

func strategyStep(spec TaskSpec, target string, typ StepType, budget, size int) ExecutionStep {
	task := Task{
		Operation: spec.Operations[target], EstimatedWork: size,
		StateFingerprint: spec.StateFingerprints[target],
	}
	if spec.State != nil && spec.State.RecoveryContext.Target == target {
		task.RecoveryContext = spec.State.RecoveryContext
	}
	diskSize, known := spec.DiskSizes[target]
	step := ExecutionStep{
		Type: typ, Targets: []string{target}, Operation: task.Operation,
		StateFingerprint: task.StateFingerprint, StepBudget: budget,
		EstimatedMutationSize: size,
	}
	if !known || task.StateFingerprint == "" {
		step.BlockedReason = "missing target disk size or state fingerprint"
		return step
	}
	step.Strategy = ResolveStrategy(task, budget, diskSize, spec.History[target])
	if step.Strategy == "" {
		step.BlockedReason = "unsupported operation, target state conflict, or unchanged output ceiling"
		return step
	}
	if step.Strategy == SKELETON_CREATE {
		step.StepBudget = min(budget, 199)
	}
	step.EstimatedMutationSize = min(size, step.StepBudget)
	return step
}
