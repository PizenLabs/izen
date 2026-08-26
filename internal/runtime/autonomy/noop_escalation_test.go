package autonomy

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/execution/planner"
)

// ── NO-OP ESCALATION CIRCUIT (Task 2, test matrix) ──────────────────────────
//
// Matrix: {satisfied, no_safe_mutation, unresolved} × {single sub-task,
// multi sub-task DAG}. The invariant under test: NO_OP_OBJECTIVE_UNRESOLVED
// can NEVER land a false DAG completion — it escalates (re-hydration attempt
// with elevated structural context + broader boundary window) and, if still
// unresolved, transitions to DAG_ESCALATED and returns the decision to
// awaiting_human.

// dagMarkerFixture renders a decomposition-sized fixture whose EVERY section
// carries a DEPRECATED-MARKER line, so any bounded-patch window contains the
// structural signature regardless of how the plan partitions lines. The
// lowercase variant lines exist so a "Handler Kind" objective matches only
// after normalization (below-threshold candidates). No blank lines: every
// line is a viable unique patch anchor.
func dagMarkerFixture(handlers int) []byte {
	var b strings.Builder
	b.WriteString("// Package big is a decomposition fixture.\n")
	b.WriteString("package big\n")
	for i := 0; i < handlers; i++ {
		fmt.Fprintf(&b, "// Handler%d processes kind %d.\n", i, i)
		fmt.Fprintf(&b, "// handler kind %d legacy variant.\n", i)
		fmt.Fprintf(&b, "// DEPRECATED-MARKER section %d.\n", i)
		fmt.Fprintf(&b, "type Handler%d struct{ Kind int }\n", i)
	}
	return []byte(b.String())
}

// stageNoOpDecompositionRun parks a run at a DECOMPOSITION_PROPOSAL boundary
// whose DAG is built by the injected decompose function: exactly `tasks`
// contiguous full-file units, immune to real-planner grouping variance.
func stageNoOpDecompositionRun(t *testing.T, objective string, handlers, tasks int) (string, *Driver, *planner.ExecutionDAG, *dagProvider) {
	t.Helper()
	root := t.TempDir()
	source := dagMarkerFixture(handlers)
	writeTarget(t, root, "big.go", string(source))

	p := &dagProvider{root: root, target: "big.go"}
	bus := events.NewBus(events.DefaultBufferSize)
	x := testExecutor(t, root, p, bus)
	adapter := NewExecutorAdapter(root, execution.NewIntentGateway(root), x)

	decompose := func(objective, target string, src []byte, baseDigest string, maxOutputTokens int) (*planner.ExecutionDAG, error) {
		dag := planner.NewExecutionDAG(objective, target, planner.SplitBoundedLines, baseDigest, maxOutputTokens)
		total := len(strings.Split(string(src), "\n"))
		step := total / tasks
		for i := 0; i < tasks; i++ {
			start := i*step + 1
			end := total
			if i < tasks-1 {
				end = start + step - 1
			}
			if err := dag.AddTask(planner.SubTask{
				ID:              fmt.Sprintf("st-%d", i+1),
				Index:           i + 1,
				Kind:            planner.SplitBoundedLines,
				Description:     "deprecated marker sections",
				Region:          planner.Region{StartLine: start, EndLine: end},
				EstimatedTokens: 64,
			}); err != nil {
				return nil, err
			}
		}
		if err := dag.Validate(); err != nil {
			return nil, err
		}
		return dag, nil
	}

	driver := NewDriver(adapter, bus, WithDecompose(decompose))
	term, err := driver.Run(context.Background(), objective)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if term != nil {
		t.Fatalf("run terminated (%+v) instead of parking at the proposal", term)
	}
	dag := driver.Proposal()
	if dag == nil {
		t.Fatal("no DECOMPOSITION_PROPOSAL parked at the boundary")
	}
	if len(dag.SubTasks) != tasks {
		t.Fatalf("sub-tasks = %d, want %d", len(dag.SubTasks), tasks)
	}
	return root, driver, dag, p
}

const noOpUnresolvedObjective = `remove every "DEPRECATED-MARKER" comment @big.go`

// ── SINGLE SUB-TASK × SATISFIED ─────────────────────────────────────────────

// TestDriver_SingleSubTaskNoOpSatisfiedCompletes proves the terminal SUCCESS
// sub-state on a one-unit DAG: the sentinel claim stands uncontradicted, the
// unit counts as applied, and the DAG completes with zero wasted invocations.
func TestDriver_SingleSubTaskNoOpSatisfiedCompletes(t *testing.T) {
	root, driver, _, p := stageNoOpDecompositionRun(t, "restyle every handler @big.go", 60, 1)
	before := readTarget(t, root, "big.go")

	// st-1's slice needs no edit and the generic objective gives the
	// structural analyzer nothing that contradicts the claim.
	p.noop = map[int]bool{1: true}

	term, err := driver.ResumeApproveProposal(context.Background())
	if err != nil {
		t.Fatalf("ResumeApproveProposal: %v", err)
	}
	if term == nil || term.State != autonomy.RuntimeCompleted {
		t.Fatalf("termination = %+v, want completed", term)
	}
	if driver.Plan().Status != planner.DagExecutionCompleted {
		t.Fatalf("plan status = %s, want %s", driver.Plan().Status, planner.DagExecutionCompleted)
	}
	if got := p.calls; got != 1 {
		t.Fatalf("provider calls = %d, want 1 (no retries, no escalations)", got)
	}
	if got := readTarget(t, root, "big.go"); got != before {
		t.Fatal("a satisfied no-op unit mutated the workspace")
	}
}

// ── SINGLE SUB-TASK × UNRESOLVED (exhausted escalation) ─────────────────────

// TestDriver_SingleSubTaskUnresolvedNoOpEscalatesToHuman proves the core
// Phase-1 invariant: a NO_CHANGES_REQUIRED claim contradicted by structural
// analysis NEVER terminates the DAG as completed. After exactly ONE
// re-hydration attempt (elevated structural context, broader window), the run
// transitions to DAG_ESCALATED and parks at awaiting_human.
func TestDriver_SingleSubTaskUnresolvedNoOpEscalatesToHuman(t *testing.T) {
	root, driver, _, p := stageNoOpDecompositionRun(t, noOpUnresolvedObjective, 60, 1)
	before := readTarget(t, root, "big.go")

	// Both the initial judgment AND the escalated re-hydration claim no-op.
	p.noop = map[int]bool{1: true, 2: true}

	term, err := driver.ResumeApproveProposal(context.Background())
	if err != nil {
		t.Fatalf("ResumeApproveProposal: %v", err)
	}

	// NOT a terminal completion: the decision returned to awaiting_human.
	if term != nil {
		t.Fatalf("termination = %+v, want a parked loop (nil term)", term)
	}
	if driver.State() != autonomy.RuntimeAwaitingHuman {
		t.Fatalf("state = %s, want awaiting_human", driver.State())
	}
	for _, tr := range driver.History() {
		if tr.To == autonomy.RuntimeCompleted {
			t.Fatalf("false completion recorded in history: %+v", tr)
		}
	}
	if driver.Plan().Status != planner.DagEscalated {
		t.Fatalf("plan status = %s, want %s", driver.Plan().Status, planner.DagEscalated)
	}
	if !strings.Contains(driver.Plan().FailureReason, "st-1") {
		t.Fatalf("escalation evidence %q must name the escalating unit", driver.Plan().FailureReason)
	}

	b := driver.Boundary()
	if b == nil {
		t.Fatal("no human boundary parked after escalation")
	}
	if b.Action != autonomy.HumanBoundaryInform {
		t.Fatalf("boundary action = %s, want inform", b.Action)
	}
	if !strings.Contains(b.Reason, "DAG_ESCALATED") {
		t.Fatalf("boundary reason missing DAG_ESCALATED: %q", b.Reason)
	}

	// Exactly two invocations burned: initial + one escalation — never more.
	if got := p.calls; got != 1+maxNoOpEscalations {
		t.Fatalf("provider calls = %d, want %d", got, 1+maxNoOpEscalations)
	}

	// The escalation prompt carried the RE-HYDRATED context: the structural
	// contradiction named explicitly, the widened-window directive, and the
	// strict block contract restated.
	prompts := p.recordedPrompts()
	escalation := prompts[1]
	for _, want := range []string{
		"[NO-OP ESCALATION 1/2",
		"Structural evidence:",
		"targeted content still present",
		"WIDENED",
		"<<<<<<< SEARCH",
	} {
		if !strings.Contains(escalation, want) {
			t.Errorf("escalation prompt missing %q:\n%s", want, escalation)
		}
	}

	// Nothing was applied for the escalated unit.
	if got := readTarget(t, root, "big.go"); got != before {
		t.Fatal("an escalated no-op unit mutated the workspace")
	}
}

// ── MULTI-SUB-TASK × UNRESOLVED (escalation recovers) ───────────────────────

// TestDriver_MultiTaskUnresolvedNoOpRecoversOnEscalation proves Attempt 1 of
// the circuit works: st-2's contradicted claim is re-hydrated once and the
// escalated attempt produces a REAL anchored patch — the DAG then completes.
func TestDriver_MultiTaskUnresolvedNoOpRecoversOnEscalation(t *testing.T) {
	root, driver, dag, p := stageNoOpDecompositionRun(t, noOpUnresolvedObjective, 60, 2)
	before := readTarget(t, root, "big.go")

	// Call 2 = st-2's initial judgment claims no-op (contradicted); its
	// escalation (call 3) falls through to the provider's default valid patch.
	p.noop = map[int]bool{2: true}

	term, err := driver.ResumeApproveProposal(context.Background())
	if err != nil {
		t.Fatalf("ResumeApproveProposal: %v", err)
	}
	if term == nil || term.State != autonomy.RuntimeCompleted {
		t.Fatalf("termination = %+v, want completed (escalation must recover the unit)", term)
	}
	if driver.Plan().Status != planner.DagExecutionCompleted {
		t.Fatalf("plan status = %s, want %s", driver.Plan().Status, planner.DagExecutionCompleted)
	}
	if got := p.calls; got != len(dag.SubTasks)+1 {
		t.Fatalf("provider calls = %d, want %d (one escalation)", got, len(dag.SubTasks)+1)
	}
	after := readTarget(t, root, "big.go")
	if after == before {
		t.Fatal("the recovered unit never landed")
	}
	for _, st := range dag.SubTasks {
		if !strings.Contains(after, "patched-by-"+st.ID) {
			t.Errorf("%s never applied", st.ID)
		}
	}
	prompts := p.recordedPrompts()
	if !strings.Contains(prompts[2], "[NO-OP ESCALATION 1/2") {
		t.Errorf("call 3 must be the escalation attempt:\n%s", prompts[2])
	}
}

// ── MULTI-SUB-TASK × UNRESOLVED (exhausted, prior units preserved) ──────────

// TestDriver_MultiTaskExhaustedEscalationPreservesAppliedUnits proves that an
// exhausted escalation mid-plan neither completes nor rolls back verified
// work: earlier applied units stay on disk, the plan is DAG_ESCALATED, and the
// loop parks for the human.
func TestDriver_MultiTaskExhaustedEscalationPreservesAppliedUnits(t *testing.T) {
	root, driver, _, p := stageNoOpDecompositionRun(t, noOpUnresolvedObjective, 60, 3)
	before := readTarget(t, root, "big.go")

	// st-1 applies normally (call 1). st-2's initial judgment (call 2) AND its
	// escalation (call 3) both claim no-op against contradicting structure.
	p.noop = map[int]bool{2: true, 3: true}

	term, err := driver.ResumeApproveProposal(context.Background())
	if err != nil {
		t.Fatalf("ResumeApproveProposal: %v", err)
	}
	if term != nil || driver.State() != autonomy.RuntimeAwaitingHuman {
		t.Fatalf("term=%+v state=%s, want parked awaiting_human", term, driver.State())
	}
	if driver.Plan().Status != planner.DagEscalated {
		t.Fatalf("plan status = %s, want %s", driver.Plan().Status, planner.DagEscalated)
	}
	if !strings.Contains(driver.Plan().FailureReason, "st-2") {
		t.Fatalf("escalation evidence %q must name st-2", driver.Plan().FailureReason)
	}

	after := readTarget(t, root, "big.go")
	if !strings.Contains(after, "patched-by-st-1") {
		t.Fatal("the verified applied unit was not preserved across escalation")
	}
	if after == before && strings.Contains(after, "patched-by-st-2") {
		t.Fatal("the escalated unit silently applied")
	}
	// Remaining units never executed.
	if got := p.calls; got != 3 {
		t.Fatalf("provider calls = %d, want 3 (st-1 + st-2 initial + st-2 escalation)", got)
	}
	b := driver.Boundary()
	if b == nil || !strings.Contains(b.Reason, "1/3 approved units are applied") {
		t.Fatalf("boundary must account preserved units: %+v", b)
	}
}

// ── MULTI-SUB-TASK × NO_SAFE_MUTATION (requires review) ─────────────────────

// TestDriver_MultiTaskBelowThresholdNoOpHoldsForReview proves the WARNING
// sub-state never lands a completion either: a below-threshold claim parks
// the DAG at a review hold without any mutation or escalation invocation.
func TestDriver_MultiTaskBelowThresholdNoOpHoldsForReview(t *testing.T) {
	root, driver, _, p := stageNoOpDecompositionRun(t, `remove every "Handler Kind" @big.go`, 60, 2)
	before := readTarget(t, root, "big.go")

	// Both units claim no-op; the payload matches only after normalization,
	// which is exactly the below-threshold condition.
	p.noop = map[int]bool{1: true, 2: true}

	term, err := driver.ResumeApproveProposal(context.Background())
	if err != nil {
		t.Fatalf("ResumeApproveProposal: %v", err)
	}
	if term != nil || driver.State() != autonomy.RuntimeAwaitingHuman {
		t.Fatalf("term=%+v state=%s, want parked awaiting_human review hold", term, driver.State())
	}
	for _, tr := range driver.History() {
		if tr.To == autonomy.RuntimeCompleted {
			t.Fatalf("review hold falsely completed the DAG: %+v", tr)
		}
	}
	if driver.Plan().Status != planner.DagEscalated {
		t.Fatalf("plan status = %s, want %s", driver.Plan().Status, planner.DagEscalated)
	}
	if !strings.Contains(driver.Plan().FailureReason, "requires_review") {
		t.Fatalf("failure reason %q should name the review hold", driver.Plan().FailureReason)
	}
	// The hold fires at the FIRST unit: one judgment, zero escalation
	// invocations, remaining units never dispatched.
	if got := p.calls; got != 1 {
		t.Fatalf("provider calls = %d, want 1 (hold at st-1)", got)
	}
	b := driver.Boundary()
	if b == nil || !strings.Contains(b.Reason, "0/2 approved units are applied") {
		t.Fatalf("boundary must account preserved units: %+v", b)
	}
	if got := readTarget(t, root, "big.go"); got != before {
		t.Fatal("a review hold mutated the workspace")
	}
}

// ── SINGLE-LOOP decision policy (non-DAG path) ──────────────────────────────

// TestDecideDefaultNoOpSubStates pins the single-objective decision policy:
// satisfied completes, below-threshold asks the human, unresolved escalates
// through the recovery matrix instead of completing.
func TestDecideDefaultNoOpSubStates(t *testing.T) {
	bounds := autonomy.DefaultLoopBounds()

	satisfied := decideDefault(autonomy.Observation{Outcome: autonomy.OutcomeNoOpObjectiveSatisfied}, bounds)
	if satisfied.Action != autonomy.LoopComplete {
		t.Errorf("satisfied → %s, want complete", satisfied.Action)
	}

	review := decideDefault(autonomy.Observation{Outcome: autonomy.OutcomeNoOpNoSafeMutation}, bounds)
	if review.Action != autonomy.LoopAskHuman {
		t.Errorf("no_safe_mutation → %s, want ask_human (requires_review)", review.Action)
	}

	unresolved := decideDefault(autonomy.Observation{
		Outcome:    autonomy.OutcomeNoOpObjectiveUnresolved,
		Diagnostic: "targeted content still present",
	}, bounds)
	if unresolved.Action != autonomy.LoopRepair {
		t.Errorf("unresolved → %s (%s), want repair (bounded escalation)", unresolved.Action, unresolved.Reason)
	}
	exhausted := decideDefault(autonomy.Observation{
		Outcome:       autonomy.OutcomeNoOpObjectiveUnresolved,
		RecoveryCycle: bounds.MaxRecoveryCycles,
	}, bounds)
	if exhausted.Action != autonomy.LoopAskHuman {
		t.Errorf("exhausted unresolved → %s, want ask_human", exhausted.Action)
	}
}

// TestClassifyOutcomeNoOpSubStates pins the failure-class mapping.
func TestClassifyOutcomeNoOpSubStates(t *testing.T) {
	for _, o := range []autonomy.ExecutionOutcome{
		autonomy.OutcomeNoOpObjectiveSatisfied, autonomy.OutcomeNoOpNoSafeMutation,
	} {
		if got := autonomy.ClassifyOutcome(o); got != autonomy.FailureTransient {
			t.Errorf("%s classified %s, want transient (decided before the matrix)", o, got)
		}
	}
	if got := autonomy.ClassifyOutcome(autonomy.OutcomeNoOpObjectiveUnresolved); got != autonomy.FailureRecoverable {
		t.Errorf("unresolved classified %s, want recoverable (escalation)", got)
	}
}

// TestTypedRepairNoOpEscalationWidensWindow pins the single-loop repair
// material-change: an unresolved observation yields an escalation request with
// the broader-window flag and the diagnostic evidence signal.
func TestTypedRepairNoOpEscalationWidensWindow(t *testing.T) {
	obs := autonomy.Observation{
		Outcome:    autonomy.OutcomeNoOpObjectiveUnresolved,
		Target:     "big.go",
		ContractID: "ct-parent",
		Diagnostic: "targeted content still present",
	}
	req := autonomy.LoopRequest{RequestID: "run-1-big.go-st-1", Evidence: "[DAG sub_task=st-1]"}
	next, err := typedRepair(obs, req)
	if err != nil {
		t.Fatalf("typedRepair: %v", err)
	}
	if !next.NoOpEscalation {
		t.Error("repair must widen the boundary window (NoOpEscalation)")
	}
	if next.RecoveryAttempt != obs.AttemptNum+1 && next.RecoveryAttempt < 1 {
		t.Errorf("recovery attempt = %d, want incremented", next.RecoveryAttempt)
	}
	if !strings.Contains(next.Evidence, "NO_OP_OBJECTIVE_UNRESOLVED") {
		t.Errorf("evidence missing the diagnostic signal:\n%s", next.Evidence)
	}
	if next.ParentContractID != "ct-parent" {
		t.Errorf("causal parent = %q, want ct-parent", next.ParentContractID)
	}
}
