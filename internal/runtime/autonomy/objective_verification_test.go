package autonomy

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/execution/planner"
)

// ── POST-DAG GLOBAL VERIFICATION integration tests ──────────────────────────
//
// The invariant under test: per-unit isolation cannot see aggregate
// regressions, so the post-DAG global structural verifier must. st-1 deletes
// (renames away) a CSS definition whose consumer lives in st-4's region —
// every unit gate passes and the DAG would claim completion. With the global
// verifier wired, the run instead overrides to OBJECTIVE_UNRESOLVED and
// returns the decision to awaiting_human.

// htmlAuditFixture renders a decomposition-sized HTML document: a leading
// <style> block defining .card/.panel rules and many independent sections,
// one of which consumes class="card" far outside st-1's window. Sections are
// large enough that the whole file trips Boundary-2 preflight as one unit.
func htmlAuditFixture(sections int) []byte {
	var b strings.Builder
	b.WriteString("<!doctype html>\n<html>\n<head>\n<style>\n")
	b.WriteString("  .card { color: red }\n")
	b.WriteString("  .panel { color: blue }\n")
	b.WriteString("</style>\n</head>\n<body>\n")
	for i := 0; i < sections; i++ {
		fmt.Fprintf(&b, "<section id=\"zone-%d\">\n", i)
		if i == sections/2 {
			b.WriteString("<div class=\"card\">the only card</div>\n")
		} else {
			fmt.Fprintf(&b, "<p>filler paragraph %d keeps every window viable.</p>\n", i)
		}
		fmt.Fprintf(&b, "<p>secondary line %d pads the section structurally.</p>\n", i)
		b.WriteString("</section>\n")
	}
	b.WriteString("</body>\n</html>\n")
	return []byte(b.String())
}

// htmlAuditProvider synthesizes one valid SEARCH/REPLACE patch per sub-task.
// Its behavior models the model-under-test's refactor plan:
//
//   - the window containing the .card CSS rule always renames the rule;
//   - in "consistent" mode the window carrying class="card" renames the
//     consumer to match (a correct coordinated refactor);
//   - in "regress" mode the consumer is left untouched (the cross-subtask
//     regression) and every other window applies an inert comment patch.
type htmlAuditProvider struct {
	mu      sync.Mutex
	root    string
	target  string
	mode    string // "regress" | "consistent"
	calls   int
	prompts []string
}

func (p *htmlAuditProvider) Name() string { return "html-audit-mock" }

func (p *htmlAuditProvider) Execute(_ context.Context, req ai.Request) (*ai.Response, error) {
	p.mu.Lock()
	p.calls++
	call := p.calls
	mode := p.mode
	prompt := userPrompt(req)
	p.prompts = append(p.prompts, prompt)
	p.mu.Unlock()

	start, ok := windowStart(prompt)
	if !ok {
		return &ai.Response{Content: "", Usage: ai.ProviderUsage{Known: true, FinishReason: "stop"}}, nil
	}
	lines := readLines(p.root, p.target)
	end := len(lines)
	if m := regionRe.FindStringSubmatch(prompt); m != nil {
		if v, err := strconv.Atoi(m[2]); err == nil && v <= len(lines) {
			end = v
		}
	}
	stID := "st-?"
	if m := stRe.FindStringSubmatch(prompt); m != nil {
		stID = m[1]
	}
	for i := start - 1; i < end && i < len(lines); i++ {
		switch {
		case strings.Contains(lines[i], ".card {"):
			repl := strings.Replace(lines[i], ".card", ".legacy", 1)
			return &ai.Response{Content: searchReplace(lines[i], repl), Usage: okUsage()}, nil
		case strings.Contains(lines[i], `class="card"`) && mode == "consistent":
			repl := strings.Replace(lines[i], `class="card"`, `class="legacy"`, 1)
			return &ai.Response{Content: searchReplace(lines[i], repl), Usage: okUsage()}, nil
		}
	}
	// Inert comment patch anchored on the FIRST UNIQUE line of the window:
	// the bounded-patch contract requires an exact-once SEARCH anchor across
	// the whole file, so repeated lines (</section>, <p> fillers) qualify.
	for i := start - 1; i < end && i < len(lines); i++ {
		if n := strings.Count(strings.Join(lines, "\n"), lines[i]); lines[i] != "" && n == 1 {
			line := lines[i]
			content := searchReplace(line, line+" <!-- audited "+stID+" call-"+fmt.Sprint(call)+" -->")
			return &ai.Response{Content: content, Usage: okUsage()}, nil
		}
	}
	return &ai.Response{Content: "", Usage: okUsage()}, nil
}

func (p *htmlAuditProvider) ExecuteStream(_ context.Context, _ ai.Request) (io.ReadCloser, error) {
	return nil, fmt.Errorf("stream not supported")
}

func searchReplace(from, to string) string {
	return "<<<<<<< SEARCH\n" + from + "\n=======\n" + to + "\n>>>>>>>"
}

func okUsage() ai.ProviderUsage {
	return ai.ProviderUsage{Known: true, PromptTokens: 10, CompletionTokens: 20, FinishReason: "stop"}
}

func windowStart(prompt string) (int, bool) {
	m := regionRe.FindStringSubmatch(prompt)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, false
	}
	return n, true
}

func readLines(root, target string) []string {
	data, err := os.ReadFile(filepath.Join(root, target))
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

// stageHTMLAuditRun parks a run at a DECOMPOSITION_PROPOSAL boundary for the
// HTML fixture, with the DAG cut into `tasks` contiguous units by an injected
// decompose (immune to real-planner grouping variance).
func stageHTMLAuditRun(t *testing.T, objective string, p *htmlAuditProvider, opts ...Option) *Driver {
	t.Helper()
	root := t.TempDir()
	writeTarget(t, root, p.target, string(htmlAuditFixture(48)))
	// Re-point the provider at the actual temp root.
	p.root = root

	bus := events.NewBus(events.DefaultBufferSize)
	x := testExecutor(t, root, p, bus)
	adapter := NewExecutorAdapter(root, execution.NewIntentGateway(root), x)

	decompose := func(objective, target string, src []byte, baseDigest string, maxOutputTokens int) (*planner.ExecutionDAG, error) {
		dag := planner.NewExecutionDAG(objective, target, planner.SplitBoundedLines, baseDigest, maxOutputTokens)
		total := len(strings.Split(strings.TrimSuffix(string(src), "\n"), "\n"))
		const tasks = 4
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
				Description:     "audit fixture window",
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

	driver := NewDriver(adapter, bus, append([]Option{WithDecompose(decompose)}, opts...)...)
	term, err := driver.Run(context.Background(), objective)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if term != nil {
		t.Fatalf("run terminated (%+v) instead of parking at the proposal", term)
	}
	if driver.Proposal() == nil {
		t.Fatal("no DECOMPOSITION_PROPOSAL parked at the boundary")
	}
	return driver
}

const htmlAuditObjective = `restyle every zone @index.html`

// TestGlobalVerifierCatchesCrossSubtaskRegression proves the Phase-3 core:
// st-1 removes a CSS definition still used in a later sub-task's region. All
// unit gates pass; the global audit catches the aggregate regression,
// overrides the DAG to OBJECTIVE_UNRESOLVED and parks at awaiting_human.
func TestGlobalVerifierCatchesCrossSubtaskRegression(t *testing.T) {
	SetDiagnosticLog(func(format string, args ...interface{}) {
		t.Logf("[diag] "+format, args...)
	})
	defer SetDiagnosticLog(nil)
	p := &htmlAuditProvider{target: "index.html", mode: "regress"}
	driver := stageHTMLAuditRun(t, htmlAuditObjective, p)

	beforeVerifyCalls := p.calls
	term, err := driver.ResumeApproveProposal(context.Background())
	if err != nil {
		t.Fatalf("ResumeApproveProposal: %v", err)
	}

	// NOT a completion: the decision returned to awaiting_human.
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

	dag := driver.Plan()
	if dag.Status != planner.ObjectiveUnresolved {
		t.Fatalf("plan status = %s, want %s", dag.Status, planner.ObjectiveUnresolved)
	}
	if !strings.Contains(dag.FailureReason, "orphaned_definition") ||
		!strings.Contains(dag.FailureReason, ".card") {
		t.Fatalf("failure reason lacks audit evidence: %q", dag.FailureReason)
	}
	if !strings.Contains(dag.FailureReason, "all 4 sub-tasks applied") {
		t.Fatalf("failure reason must name the applied unit count: %q", dag.FailureReason)
	}

	// The boundary carries the typed evidence for the human decision.
	b := driver.Boundary()
	if b == nil {
		t.Fatal("no human boundary parked after the failed audit")
	}
	if b.Action != autonomy.HumanBoundaryInform {
		t.Fatalf("boundary action = %s, want inform", b.Action)
	}
	if !strings.Contains(b.Reason, "OBJECTIVE_UNRESOLVED") || !strings.Contains(b.Reason, "orphaned_definition") {
		t.Fatalf("boundary reason lacks OBJECTIVE_UNRESOLVED evidence: %q", b.Reason)
	}

	// Applied units are preserved — never silently rolled back.
	after := readTarget(t, driverRoot(t, driver), "index.html")
	if !strings.Contains(after, ".legacy { color: red }") {
		t.Fatal("applied units must stay in place on OBJECTIVE_UNRESOLVED")
	}
	if beforeVerifyCalls > p.calls {
		t.Fatal("verification consumed provider invocations")
	}
}

// TestGlobalVerifierPassesCoordinatedRefactor proves the positive control:
// when the consumer is renamed together with its definition across sub-tasks,
// the audit verifies and the DAG completes normally.
func TestGlobalVerifierPassesCoordinatedRefactor(t *testing.T) {
	p := &htmlAuditProvider{target: "index.html", mode: "consistent"}
	driver := stageHTMLAuditRun(t, htmlAuditObjective, p)

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
	after := readTarget(t, driverRoot(t, driver), "index.html")
	if strings.Contains(after, ".card") || strings.Contains(after, `class="card"`) {
		t.Fatal("coordinated rename left stale .card identities behind")
	}
	if !strings.Contains(after, ".legacy { color: red }") || !strings.Contains(after, `class="legacy"`) {
		t.Fatal("coordinated rename never landed on both sides")
	}
}

// TestGlobalVerifierDisableRestoresLegacyCompletion documents the opt-out:
// with global verification disabled, the regressing DAG claims completion
// exactly as before the verifier existed.
func TestGlobalVerifierDisableRestoresLegacyCompletion(t *testing.T) {
	p := &htmlAuditProvider{target: "index.html", mode: "regress"}
	driver := stageHTMLAuditRun(t, htmlAuditObjective, p, WithGlobalVerify(nil))

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
}

// driverRoot recovers the workspace root the driver's adapter was built over.
func driverRoot(t *testing.T, d *Driver) string {
	t.Helper()
	return d.adapter.root
}

// ── EXECUTION INERTIA CIRCUIT BREAKER (false-positive no-op resolution) ─────

// stageInertiaRun parks a run at a DECOMPOSITION_PROPOSAL boundary over a
// pre-broken 4-sub-task DAG, wired so every sub-task may be answered with the
// NO_CHANGES_REQUIRED sentinel.
func stageInertiaRun(t *testing.T) (string, *Driver, *planner.ExecutionDAG, *dagProvider) {
	t.Helper()
	root := t.TempDir()
	source := brokenBaselineFixture()
	writeTarget(t, root, "index.html", string(source))

	p := &dagProvider{root: root, target: "index.html"}
	bus := events.NewBus(events.DefaultBufferSize)
	x := testExecutor(t, root, p, bus)
	adapter := NewExecutorAdapter(root, execution.NewIntentGateway(root), x)

	decompose := func(objective, target string, src []byte, baseDigest string, maxOutputTokens int) (*planner.ExecutionDAG, error) {
		dag := planner.NewExecutionDAG(objective, target, planner.SplitBoundedLines, baseDigest, maxOutputTokens)
		total := len(strings.Split(strings.TrimSuffix(string(src), "\n"), "\n"))
		const tasks = 4
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
				Description:     "inertia fixture window",
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
	term, err := driver.Run(context.Background(), "check this file @index.html and remove redundant content")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if term != nil {
		t.Fatalf("run terminated (%+v) instead of parking at the proposal", term)
	}
	if driver.Proposal() == nil {
		t.Fatal("no DECOMPOSITION_PROPOSAL parked at the boundary")
	}
	return root, driver, driver.Proposal(), p
}

// TestVerifyDAG_RejectsAllNoOpOnModificationIntent proves the Active Intent
// Integrity Invariant: a DAG staged under an ACTIVE MODIFICATION intent whose
// every sub-task evaluates to no_op_objective_satisfied — ZERO mutated bytes
// across the plan — must NEVER report OBJECTIVE_RESOLVED, even when the
// pre-DAG baseline was already syntactically invalid (the pre-existing
// baseline relaxation is no longer allowed to mask execution inertia). The
// run fail-fasts with EXECUTION_INERTIA_NO_OP and parks at awaiting_human for
// a retry or a scope escalation.
func TestVerifyDAG_RejectsAllNoOpOnModificationIntent(t *testing.T) {
	root, driver, dag, p := stageInertiaRun(t)

	// The preflight snapshot records the pre-existing defect.
	if dag.BaselineSyntaxValid {
		t.Fatal("BaselineSyntaxValid = true, want false — the baseline HTML was already broken at staging time")
	}
	before := readTarget(t, root, "index.html")

	// ALL FOUR sub-tasks evaluate to no_op_objective_satisfied (zero mutations).
	p.noop = map[int]bool{1: true, 2: true, 3: true, 4: true}

	term, err := driver.ResumeApproveProposal(context.Background())
	if err != nil {
		t.Fatalf("ResumeApproveProposal: %v", err)
	}

	// NOT a completion: the decision returns to awaiting_human.
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

	// The DAG is marked EXECUTION_INERTIA_NO_OP — never OBJECTIVE_RESOLVED and
	// never a completed plan.
	if dag.Status != planner.ExecutionInertiaNoOp {
		t.Fatalf("plan status = %s, want %s", dag.Status, planner.ExecutionInertiaNoOp)
	}
	if dag.Status == planner.DagExecutionCompleted || dag.Status == planner.ObjectiveUnresolved {
		t.Fatalf("plan must not resolve as completed/OBJECTIVE_RESOLVED: %s", dag.Status)
	}
	if !strings.Contains(dag.FailureReason, "active modification requested but all sub-tasks evaluated to no-op") ||
		!strings.Contains(dag.FailureReason, "zero mutations applied") {
		t.Fatalf("failure reason lacks the inertia evidence: %q", dag.FailureReason)
	}
	if dag.NoOpSatisfiedSubTasks != 4 {
		t.Fatalf("NoOpSatisfiedSubTasks = %d, want 4", dag.NoOpSatisfiedSubTasks)
	}

	// The boundary carries the typed evidence for the human decision.
	b := driver.Boundary()
	if b == nil {
		t.Fatal("no human boundary parked after the inertia halt")
	}
	if !strings.Contains(b.Reason, "EXECUTION_INERTIA_NO_OP") {
		t.Fatalf("boundary reason missing EXECUTION_INERTIA_NO_OP: %q", b.Reason)
	}

	// Zero diffs applied: the workspace is byte-for-byte unchanged.
	if got := readTarget(t, root, "index.html"); got != before {
		t.Fatal("an all-no-op modification run mutated the workspace")
	}
}
