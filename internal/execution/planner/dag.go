package planner

import (
	"fmt"
	"strings"
)

// ── Budget rule (strict per-sub-task ceiling) ───────────────────────────────
//
// Every SubTask must satisfy:
//
//	SubTask.EstimatedTokens <= MaxOutputTokens × SubTaskBudgetFactor
//
// The 0.7 factor keeps each sub-task STRICTLY inside the Boundary-2 ceiling,
// reserving headroom for prompt echo and structural overhead so a sub-task can
// never reproduce the original failure (a generation truncated at max_output).
const (
	// SubTaskBudgetFactorNum / SubTaskBudgetFactorDen == 0.7. Integer math
	// keeps the ceiling deterministic across platforms (no float drift).
	SubTaskBudgetFactorNum = 7
	SubTaskBudgetFactorDen = 10

	// MaxSubTasks caps a staged DAG. A decomposition that would need more
	// units than this is refused — an unbounded plan is an unbounded risk.
	MaxSubTasks = 64
)

// SubTaskBudget returns the strict per-sub-task token ceiling for the given
// max_output: floor(max_output × 0.7). A non-positive budget yields 0 (no
// decomposition is feasible without a known output ceiling).
func SubTaskBudget(maxOutputTokens int) int {
	if maxOutputTokens <= 0 {
		return 0
	}
	return maxOutputTokens * SubTaskBudgetFactorNum / SubTaskBudgetFactorDen
}

// PlanStatus is the lifecycle status of a staged decomposition plan. The
// values are canonical evidence strings surfaced on boundaries and
// terminations.
type PlanStatus string

const (
	// PlanStaged: the DAG is validated and parked at the proposal boundary.
	PlanStaged PlanStatus = "PLAN_STAGED"
	// DagExecuting: the atomic transaction loop is running sub-tasks.
	DagExecuting PlanStatus = "DAG_EXECUTING"
	// DagExecutionCompleted: every sub-task applied; the objective is satisfied.
	DagExecutionCompleted PlanStatus = "DAG_EXECUTION_COMPLETED"
	// DagExecutionFailed: a sub-task failed at Boundary 3, 4 or 5 (or drift /
	// cancellation); the DAG aborted, the workspace rolled back to the base
	// tree digest and NO further sub-task executed.
	DagExecutionFailed PlanStatus = "DAG_EXECUTION_FAILED"
	// DagEscalated: a sub-task's NO_CHANGES_REQUIRED claim could not be
	// reconciled with structural evidence (no_op_objective_unresolved after
	// re-hydration) or fell below the safety threshold
	// (no_op_no_safe_mutation). The DAG is NOT terminally completed and NOT
	// failed: already-applied units stay in place, remaining units never
	// executed, and the decision returns to a human boundary.
	DagEscalated PlanStatus = "DAG_ESCALATED"
)

// Terminal reports whether the plan reached a terminal status.
func (s PlanStatus) Terminal() bool {
	return s == DagExecutionCompleted || s == DagExecutionFailed || s == DagEscalated
}

// SubTask is one executable unit of a decomposed objective. It is scoped to a
// contiguous region of exactly one target file and carries its own budget
// estimate under the strict sub-task ceiling.
type SubTask struct {
	// ID is the stable ordinal identity ("st-1", "st-2", ...).
	ID string
	// Index is the 1-based execution position within the DAG.
	Index int
	// Kind names the splitting strategy that produced this unit.
	Kind SplitKind
	// Target is the workspace-relative file this sub-task mutates.
	Target string
	// Description is the bounded instruction describing the change window.
	Description string
	// Region is the inclusive 1-indexed line window of the original artifact.
	Region Region
	// EstimatedTokens is this sub-task's generation estimate under the same
	// accounting as Boundary 2 (bytes/4 × FullRewriteTokenMultiplier).
	EstimatedTokens int
	// Dependencies lists sub-task IDs that must complete first (execution
	// order is the topological order of these edges).
	Dependencies []string
}

// String renders the compact proposal line for one sub-task.
func (st SubTask) String() string {
	return fmt.Sprintf("%s [%s] %s — %s (~%d tok)", st.ID, st.Kind, st.Description, st.Region, st.EstimatedTokens)
}

// ExecutionDAG is the validated decomposition plan of ONE infeasible
// objective over ONE target. SubTasks are stored in topological (execution)
// order; dependency edges point strictly backwards, so the plan is acyclic by
// construction and re-validated on every mutation.
//
// The DAG carries its own atomicity contract: BaseTreeDigest is the Boundary-5
// workspace version the plan was staged against. The executing driver must
// verify the live digest against it before and after EVERY sub-task and roll
// back to it when any sub-task fails at Boundary 3, 4 or 5.
type ExecutionDAG struct {
	// Objective is the original user prompt the plan decomposes.
	Objective string
	// Target is the single workspace-relative file all sub-tasks mutate.
	Target string
	// Kind is the splitting strategy selected for the target's format.
	Kind SplitKind
	// BaseTreeDigest is the workspace SHA256(Σ path+hash) captured before any
	// sub-task executes. Rollback restores exactly this state.
	BaseTreeDigest string
	// MaxOutputTokens is the per-invocation budget the plan was staged under.
	MaxOutputTokens int
	// SubTasks are in topological (execution) order.
	SubTasks []SubTask
	// Status is the plan lifecycle status.
	Status PlanStatus
	// FailureReason is the bounded evidence of a DAG_EXECUTION_FAILED abort.
	FailureReason string
}

// NewExecutionDAG constructs an empty DAG staged against the given base state.
func NewExecutionDAG(objective, target string, kind SplitKind, baseDigest string, maxOutputTokens int) *ExecutionDAG {
	return &ExecutionDAG{
		Objective:       objective,
		Target:          target,
		Kind:            kind,
		BaseTreeDigest:  baseDigest,
		MaxOutputTokens: maxOutputTokens,
		Status:          PlanStaged,
	}
}

// Budget returns the strict per-sub-task token ceiling of this plan.
func (d *ExecutionDAG) Budget() int {
	if d == nil {
		return 0
	}
	return SubTaskBudget(d.MaxOutputTokens)
}

// Targets returns the unique workspace-relative target set of the plan (the
// Boundary-5 verification geometry).
func (d *ExecutionDAG) Targets() []string {
	if d == nil || d.Target == "" {
		return nil
	}
	return []string{d.Target}
}

// Task returns the sub-task with the given ID, or nil.
func (d *ExecutionDAG) Task(id string) *SubTask {
	if d == nil {
		return nil
	}
	for i := range d.SubTasks {
		if d.SubTasks[i].ID == id {
			return &d.SubTasks[i]
		}
	}
	return nil
}

// TotalEstimatedTokens sums every sub-task estimate.
func (d *ExecutionDAG) TotalEstimatedTokens() int {
	if d == nil {
		return 0
	}
	total := 0
	for _, st := range d.SubTasks {
		total += st.EstimatedTokens
	}
	return total
}

// AddTask appends a sub-task as the next execution position, chaining it onto
// its predecessor. The strict budget rule is enforced HERE: a sub-task whose
// estimate exceeds the ceiling is refused and the DAG stays unchanged.
func (d *ExecutionDAG) AddTask(st SubTask) error {
	if d == nil {
		return ErrInvalidDAG
	}
	if d.Status != PlanStaged {
		return fmt.Errorf("%w: plan is %s, tasks are immutable after staging", ErrInvalidDAG, d.Status)
	}
	if st.ID == "" {
		st.ID = fmt.Sprintf("st-%d", len(d.SubTasks)+1)
	}
	if st.Index == 0 {
		st.Index = len(d.SubTasks) + 1
	}
	if st.Target == "" {
		st.Target = d.Target
	}
	if st.EstimatedTokens > d.Budget() {
		return fmt.Errorf("%w: %s estimates %d tokens but the sub-task ceiling is %d (max_output=%d × 0.7)",
			ErrInvalidDAG, st.ID, st.EstimatedTokens, d.Budget(), d.MaxOutputTokens)
	}
	if st.EstimatedTokens <= 0 {
		return fmt.Errorf("%w: %s carries no positive token estimate", ErrInvalidDAG, st.ID)
	}
	if len(d.SubTasks) >= MaxSubTasks {
		return fmt.Errorf("%w: more than %d sub-tasks would be required", ErrInvalidDAG, MaxSubTasks)
	}
	if n := len(d.SubTasks); n > 0 {
		st.Dependencies = []string{d.SubTasks[n-1].ID}
	} else {
		st.Dependencies = nil
	}
	d.SubTasks = append(d.SubTasks, st)
	return nil
}

// Validate enforces every staged-plan invariant:
//
//	V1  at least one and at most MaxSubTasks sub-tasks
//	V2  IDs unique, indexes form 1..n
//	V3  dependencies reference STRICTLY earlier sub-tasks ⇒ acyclic
//	V4  every estimate obeys the strict 0.7 × max_output ceiling
//	V5  regions are positive, ordered and cover 1..maxLine contiguously
//
// A DAG that fails validation must never be proposed, let alone executed.
func (d *ExecutionDAG) Validate() error {
	if d == nil || len(d.SubTasks) == 0 {
		return fmt.Errorf("%w: empty plan", ErrInvalidDAG)
	}
	if len(d.SubTasks) > MaxSubTasks {
		return fmt.Errorf("%w: %d sub-tasks exceed the cap of %d", ErrInvalidDAG, len(d.SubTasks), MaxSubTasks)
	}
	if d.Target == "" {
		return fmt.Errorf("%w: no target", ErrInvalidDAG)
	}
	budget := d.Budget()
	seen := make(map[string]bool, len(d.SubTasks))
	nextLine := 1
	for i, st := range d.SubTasks {
		if seen[st.ID] {
			return fmt.Errorf("%w: duplicate sub-task id %q", ErrInvalidDAG, st.ID)
		}
		seen[st.ID] = true
		if st.Index != i+1 {
			return fmt.Errorf("%w: sub-task %s index %d, want %d", ErrInvalidDAG, st.ID, st.Index, i+1)
		}
		if st.EstimatedTokens <= 0 || st.EstimatedTokens > budget {
			return fmt.Errorf("%w: %s estimate %d outside the strict ceiling (%d, max_output=%d)",
				ErrInvalidDAG, st.ID, st.EstimatedTokens, budget, d.MaxOutputTokens)
		}
		for _, dep := range st.Dependencies {
			if !seen[dep] {
				return fmt.Errorf("%w: %s depends on %s which is not an earlier sub-task", ErrInvalidDAG, st.ID, dep)
			}
		}
		if st.Region.StartLine != nextLine || st.Region.EndLine < st.Region.StartLine {
			return fmt.Errorf("%w: %s region %s breaks contiguous coverage at line %d",
				ErrInvalidDAG, st.ID, st.Region, nextLine)
		}
		nextLine = st.Region.EndLine + 1
		if st.Target != d.Target {
			return fmt.Errorf("%w: %s targets %q, want plan target %q", ErrInvalidDAG, st.ID, st.Target, d.Target)
		}
	}
	return nil
}

// TopologicalOrder returns the execution order (the stored order; dependency
// edges point strictly backwards after Validate).
func (d *ExecutionDAG) TopologicalOrder() []SubTask {
	if d == nil {
		return nil
	}
	out := make([]SubTask, len(d.SubTasks))
	copy(out, d.SubTasks)
	return out
}

// ProposalSummary renders the typed DECOMPOSITION_PROPOSAL text: the plan
// header plus every sub-task line. Long plans list the first
// maxProposalLines entries and elide the remainder (the full detail always
// travels on the structured Proposal field, never only in prose).
func (d *ExecutionDAG) ProposalSummary() string {
	if d == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "DECOMPOSITION_PROPOSAL status=%s target=%s strategy=%s sub_tasks=%d budget<=%d/sub_task (0.7×max_output=%d) total~%d tok",
		d.Status, d.Target, d.Kind, len(d.SubTasks), d.Budget(), d.MaxOutputTokens, d.TotalEstimatedTokens())
	shown := 0
	for _, st := range d.SubTasks {
		if shown == maxProposalLines {
			fmt.Fprintf(&b, "\n  … +%d more (see proposal payload)", len(d.SubTasks)-shown)
			break
		}
		fmt.Fprintf(&b, "\n  %s", st)
		shown++
	}
	if d.BaseTreeDigest != "" {
		fmt.Fprintf(&b, "\n  base_tree_digest=%s… rollback restores this state on any sub-task failure",
			shortDigest(d.BaseTreeDigest))
	}
	return b.String()
}

const maxProposalLines = 12

// shortDigest renders the leading edge of a hex digest for compact evidence.
func shortDigest(digest string) string {
	if len(digest) > 12 {
		return digest[:12]
	}
	return digest
}
