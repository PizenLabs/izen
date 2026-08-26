package autonomy

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
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

// ── decomposition fixtures ──────────────────────────────────────────────────

// dagGoFixture renders a deterministic Go source whose per-section estimates
// are small enough for every plausible grouping: the file trips Boundary 2 as
// a whole while each sub-task fits the strict 0.7 ceiling.
func dagGoFixture(handlers int) []byte {
	var b strings.Builder
	b.WriteString("// Package big is a decomposition fixture.\npackage big\n\n")
	for i := 0; i < handlers; i++ {
		fmt.Fprintf(&b, "// Handler%d processes kind %d.\n", i, i)
		fmt.Fprintf(&b, "type Handler%d struct{ Kind int }\n\n", i)
		fmt.Fprintf(&b, "// NewHandler%d builds handler %d.\nfunc NewHandler%d() *Handler%d {\n", i, i, i, i)
		fmt.Fprintf(&b, "\treturn &Handler%d{Kind: %d}\n}\n\n", i, i)
	}
	return []byte(b.String())
}

// regionRe extracts the change window the runner asked for.
var regionRe = regexp.MustCompile(`Change window: lines (\d+)–(\d+)`)

// stRe extracts the sub-task id from a scoped prompt.
var stRe = regexp.MustCompile(`\[DECOMPOSITION (st-\d+)`)

// dagProvider synthesizes one valid SEARCH/REPLACE patch per sub-task by
// reading the LIVE file and anchoring on its first window line — so every
// apply succeeds regardless of how the planner grouped sections. A non-nil
// poison index makes that call return malformed bytes (Boundary-4 failure);
// a mutate hook runs BEFORE the response is produced (simulates an
// out-of-band writer between sub-tasks).
type dagProvider struct {
	mu      sync.Mutex
	root    string
	target  string
	calls   int
	poison  map[int]bool // call number (1-based) → garbage response
	onCall  func(call int)
	prompts []string // flattened user prompt of every call, oldest first
}

func (p *dagProvider) Name() string { return "dag-mock" }

func (p *dagProvider) Execute(_ context.Context, req ai.Request) (*ai.Response, error) {
	p.mu.Lock()
	p.calls++
	call := p.calls
	poison := p.poison[call]
	hook := p.onCall
	p.prompts = append(p.prompts, userPrompt(req))
	p.mu.Unlock()

	if hook != nil {
		hook(call)
	}
	if poison {
		return &ai.Response{
			Content: "this is not a SEARCH/REPLACE artifact at all",
			Usage:   ai.ProviderUsage{Known: true, FinishReason: "stop"},
		}, nil
	}
	prompt := userPrompt(req)
	line, ok := p.windowLine(prompt)
	if !ok {
		return &ai.Response{Content: "", Usage: ai.ProviderUsage{Known: true, FinishReason: "stop"}}, nil
	}
	stID := "st-?"
	if m := stRe.FindStringSubmatch(prompt); m != nil {
		stID = m[1]
	}
	replacement := line + " // patched-by-" + stID
	content := "<<<<<<< SEARCH\n" + line + "\n=======\n" + replacement + "\n>>>>>>>"
	return &ai.Response{
		Content: content,
		Usage:   ai.ProviderUsage{Known: true, PromptTokens: 10, CompletionTokens: 20, FinishReason: "stop"},
	}, nil
}

// userPrompt flattens the request's user messages.
func userPrompt(req ai.Request) string {
	var b strings.Builder
	for _, m := range req.Messages {
		b.WriteString(m.Content)
		b.WriteString("\n")
	}
	return b.String()
}

func (p *dagProvider) ExecuteStream(_ context.Context, _ ai.Request) (io.ReadCloser, error) {
	return nil, fmt.Errorf("stream not supported")
}

// recordedPrompts returns the flattened user prompt of every invocation,
// oldest first.
func (p *dagProvider) recordedPrompts() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.prompts...)
}

// windowLine anchors the synthetic patch on the first line of the requested
// region of the CURRENT file content.
func (p *dagProvider) windowLine(prompt string) (string, bool) {
	m := regionRe.FindStringSubmatch(prompt)
	if m == nil {
		return "", false
	}
	start, err := strconv.Atoi(m[1])
	if err != nil {
		return "", false
	}
	data, err := os.ReadFile(filepath.Join(p.root, p.target))
	if err != nil {
		return "", false
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if start < 1 || start > len(lines) || strings.TrimSpace(lines[start-1]) == "" {
		return "", false
	}
	return lines[start-1], true
}

// ── test harness ────────────────────────────────────────────────────────────

// stageDecompositionRun drives a fresh run over a decomposition-sized Go
// fixture until the loop parks at the DECOMPOSITION_PROPOSAL boundary. The
// provider is wired to the freshly created root so patches can anchor on the
// live file content.
func stageDecompositionRun(t *testing.T, handlers int) (string, *Driver, *planner.ExecutionDAG, *dagProvider) {
	t.Helper()
	root := t.TempDir()
	source := dagGoFixture(handlers)
	writeTarget(t, root, "big.go", string(source))

	p := &dagProvider{root: root, target: "big.go"}
	bus := events.NewBus(events.DefaultBufferSize)
	x := testExecutor(t, root, p, bus)
	driver := NewDriver(NewExecutorAdapter(root, execution.NewIntentGateway(root), x), bus)

	term, err := driver.Run(context.Background(), "refactor every handler @big.go")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if term != nil {
		t.Fatalf("run terminated (%+v) instead of parking at the proposal", term)
	}
	if driver.State() != autonomy.RuntimeAwaitingHuman {
		t.Fatalf("state = %s, want awaiting_human at the proposal gate", driver.State())
	}
	dag := driver.Proposal()
	if dag == nil {
		t.Fatal("no DECOMPOSITION_PROPOSAL parked at the boundary")
	}
	if dag.Status != planner.PlanStaged {
		t.Fatalf("plan status = %s, want %s", dag.Status, planner.PlanStaged)
	}
	if len(dag.SubTasks) < 2 {
		t.Fatalf("sub-tasks = %d, want a real decomposition", len(dag.SubTasks))
	}
	return root, driver, dag, p
}

// ── the atomic happy path ───────────────────────────────────────────────────

func TestDriver_DecompositionApprovalExecutesAllSubTasksAtomically(t *testing.T) {
	root, driver, dag, p := stageDecompositionRun(t, 60)

	// Zero provider requests crossed BEFORE the human approved the plan.
	if got := p.calls; got != 0 {
		t.Fatalf("provider calls at proposal = %d, want 0", got)
	}

	before := readTarget(t, root, "big.go")

	term, err := driver.ResumeApproveProposal(context.Background())
	if err != nil {
		t.Fatalf("ResumeApproveProposal: %v", err)
	}
	if term == nil || term.State != autonomy.RuntimeCompleted {
		t.Fatalf("termination = %+v, want completed", term)
	}

	// EVERY sub-task executed exactly once — one provider call each.
	if got := p.calls; got != len(dag.SubTasks) {
		t.Fatalf("provider calls = %d, want one per sub-task (%d)", got, len(dag.SubTasks))
	}

	// The plan is completed and the file actually changed.
	if driver.Plan().Status != planner.DagExecutionCompleted {
		t.Fatalf("plan status = %s, want %s", driver.Plan().Status, planner.DagExecutionCompleted)
	}
	after := readTarget(t, root, "big.go")
	if after == before || !strings.Contains(after, "patched-by-st-1") {
		t.Fatal("approved sub-tasks never mutated the workspace")
	}

	// Boundary-5 closure: the live digest equals the digest captured after
	// the last apply — no drift occurred across the transaction.
	targets := dag.Targets()
	final := driver.adapter.WorkspaceVersion(targets)
	if final == "" || final == dag.BaseTreeDigest && after == before {
		t.Fatal("digest bookkeeping inconsistent with a landed mutation")
	}

	// The loop history records per-sub-task execution transitions.
	transitions := 0
	for _, tr := range driver.History() {
		if strings.Contains(tr.Reason, "DAG_EXECUTING") {
			transitions++
		}
	}
	if transitions < len(dag.SubTasks) {
		t.Fatalf("DAG_EXECUTING transitions = %d, want >= %d", transitions, len(dag.SubTasks))
	}
}

// ── Boundary-4 failure mid-plan: retries exhausted → abort + rollback ───────

func TestDriver_DecompositionArtifactFailureRollsBackToBaseDigest(t *testing.T) {
	root, driver, dag, p := stageDecompositionRun(t, 60)

	original := readTarget(t, root, "big.go")

	// Sub-task 2 produces garbage on EVERY attempt: Boundary 4 rejects the
	// artifact; after maxSubTaskAttempts contract retries the DAG aborts.
	p.poison = map[int]bool{2: true, 3: true, 4: true}

	term, err := driver.ResumeApproveProposal(context.Background())
	if err != nil {
		t.Fatalf("ResumeApproveProposal: %v", err)
	}
	if term == nil || term.State != autonomy.RuntimeAborted {
		t.Fatalf("termination = %+v, want aborted", term)
	}
	if !strings.Contains(term.Reason, "DAG_EXECUTION_FAILED") {
		t.Fatalf("termination reason %q missing DAG_EXECUTION_FAILED", term.Reason)
	}
	if !strings.Contains(term.Reason, "artifact_retryable_rejected") {
		t.Fatalf("termination reason %q should name the exhausted outcome", term.Reason)
	}

	// The plan is marked failed and names the failing unit.
	if driver.Plan().Status != planner.DagExecutionFailed {
		t.Fatalf("plan status = %s, want %s", driver.Plan().Status, planner.DagExecutionFailed)
	}
	if !strings.Contains(driver.Plan().FailureReason, "st-2") {
		t.Fatalf("failure reason %q should name the failing sub-task", driver.Plan().FailureReason)
	}

	// ATOMICITY: exactly st-1 (1 call) + st-2's full attempt budget
	// (maxSubTaskAttempts calls) ran; remaining sub-tasks NEVER reached the
	// provider — exhausted retries abort instead of continuing the plan.
	if got := p.calls; got != 1+maxSubTaskAttempts {
		t.Fatalf("provider calls = %d, want %d (st-2 consumed its full attempt budget)", got, 1+maxSubTaskAttempts)
	}
	prompts := p.recordedPrompts()
	if strings.Contains(prompts[1], "[RETRY") {
		t.Error("st-2 attempt 1 must carry no retry marker")
	}
	for i := 3; i <= 4; i++ {
		if !strings.Contains(prompts[i-1], "[RETRY") {
			t.Errorf("call %d must be a contract retry carrying feedback context", i)
		}
	}

	// The workspace provably rolled back to the BaseTreeDigest.
	if got := readTarget(t, root, "big.go"); got != original {
		t.Fatal("workspace did not roll back to its original content")
	}
	if digest := driver.adapter.WorkspaceVersion(dag.Targets()); digest != dag.BaseTreeDigest {
		t.Fatalf("post-rollback digest %q… != base %q…", short(digest), short(dag.BaseTreeDigest))
	}
}

// ── Boundary-5 failure mid-plan: out-of-band writer aborts the DAG ──────────

func TestDriver_DecompositionWorkspaceDriftAbortsAndRollsBack(t *testing.T) {
	root, driver, dag, p := stageDecompositionRun(t, 60)

	original := readTarget(t, root, "big.go")

	// Before sub-task 2's response lands, an out-of-band writer moves the
	// file: the executor's OCC commit gate must refuse the apply.
	p.onCall = func(call int) {
		if call == 2 {
			f := filepath.Join(root, "big.go")
			data, _ := os.ReadFile(f)
			_ = os.WriteFile(f, append(data, []byte("\n// out-of-band edit\n")...), 0o644)
		}
	}

	term, err := driver.ResumeApproveProposal(context.Background())
	if err != nil {
		t.Fatalf("ResumeApproveProposal: %v", err)
	}
	if term == nil || term.State != autonomy.RuntimeAborted {
		t.Fatalf("termination = %+v, want aborted", term)
	}
	if !strings.Contains(term.Reason, "DAG_EXECUTION_FAILED") {
		t.Fatalf("termination reason %q missing DAG_EXECUTION_FAILED", term.Reason)
	}
	if driver.Plan().Status != planner.DagExecutionFailed {
		t.Fatalf("plan status = %s, want DAG_EXECUTION_FAILED", driver.Plan().Status)
	}

	// Rollback restored EXACTLY the base content (sub-task 1's landed patch
	// AND the out-of-band tail are gone).
	if got := readTarget(t, root, "big.go"); got != original {
		t.Fatal("rollback did not restore the base tree content byte-for-byte")
	}
	if digest := driver.adapter.WorkspaceVersion(dag.Targets()); digest != dag.BaseTreeDigest {
		t.Fatalf("post-rollback digest mismatch vs BaseTreeDigest")
	}
}

// ── rejection & guards ──────────────────────────────────────────────────────

func TestDriver_DecompositionRejectTerminatesWithoutExecution(t *testing.T) {
	root, driver, _, p := stageDecompositionRun(t, 60)
	before := readTarget(t, root, "big.go")

	term, err := driver.ResumeRejectProposal(context.Background(), "too invasive")
	if err != nil {
		t.Fatalf("ResumeRejectProposal: %v", err)
	}
	if term == nil || term.State != autonomy.RuntimeAborted {
		t.Fatalf("termination = %+v, want aborted", term)
	}
	if got := p.calls; got != 0 {
		t.Fatalf("provider calls = %d, want 0 on rejection", got)
	}
	if got := readTarget(t, root, "big.go"); got != before {
		t.Fatal("rejection mutated the workspace")
	}
}

func TestDriver_DecompositionResumeRequiresParkedProposal(t *testing.T) {
	p := &dagProvider{root: t.TempDir(), target: "note.txt"}
	root := p.root
	writeTarget(t, root, "note.txt", sampleOriginal)
	x := testExecutor(t, root, p, nil)
	driver := NewDriver(NewExecutorAdapter(root, execution.NewIntentGateway(root), x), nil)

	if _, err := driver.ResumeApproveProposal(context.Background()); err == nil {
		t.Fatal("approve without a parked proposal must fail")
	}
	if _, err := driver.ResumeRejectProposal(context.Background(), "x"); err == nil {
		t.Fatal("reject without a parked proposal must fail")
	}
}

func TestDriver_DecompositionDisabledKeepsLegacyReScopePark(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "big.go", string(dagGoFixture(60))) // preflight-infeasible size
	mock := &mockProvider{responses: []*ai.Response{{Content: "unused"}}}
	x := testExecutor(t, root, mock, nil)
	driver := NewDriver(
		NewExecutorAdapter(root, execution.NewIntentGateway(root), x), nil,
		WithDecompose(nil)) // explicit disable

	if _, err := driver.Run(context.Background(), "refactor every handler @big.go"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if driver.State() != autonomy.RuntimeAwaitingHuman {
		t.Fatalf("state = %s, want awaiting_human", driver.State())
	}
	if b := driver.Boundary(); b == nil || b.Proposal != nil {
		t.Fatalf("boundary = %+v, want the legacy re-scope park without a proposal", b)
	}
	if mock.calls() != 0 {
		t.Fatalf("provider calls = %d, want 0", mock.calls())
	}
}

// ── pure helpers ────────────────────────────────────────────────────────────

func TestDagOutcomeSuccessClassification(t *testing.T) {
	success := []autonomy.ExecutionOutcome{
		autonomy.OutcomeChanged, autonomy.OutcomeCreated,
		autonomy.OutcomeNoChange, autonomy.OutcomeCompleted,
	}
	for _, o := range success {
		if !dagOutcomeSuccess(autonomy.Observation{Outcome: o}) {
			t.Errorf("%s must count as applied", o)
		}
	}
	failures := []autonomy.ExecutionOutcome{
		autonomy.OutcomeTruncated, autonomy.OutcomeArtifactRejected,
		"occ_aborted", autonomy.OutcomeApplyFailed,
		autonomy.OutcomeVerifyFailed, autonomy.OutcomeFailed,
		autonomy.OutcomeCancelled, autonomy.OutcomeSkipped,
	}
	for _, o := range failures {
		if dagOutcomeSuccess(autonomy.Observation{Outcome: o}) {
			t.Errorf("%s must abort the DAG", o)
		}
	}
}

func TestSubTaskPromptIsBoundedAndScoped(t *testing.T) {
	dag := &planner.ExecutionDAG{Objective: "obj", Target: "f.go", MaxOutputTokens: 1000}
	st := planner.SubTask{ID: "st-2", Index: 2, Region: planner.Region{StartLine: 10, EndLine: 20},
		Description: "type Handler1 struct"}
	got := subTaskPrompt("rewrite @f.go", dag, st, 2, 5)
	for _, want := range []string{"st-2", "sub-task 2/5", "lines 10–20", "SEARCH"} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt missing %q:\n%s", want, got)
		}
	}
	if len(got) > 2048 {
		t.Errorf("prompt unbounded: %d chars", len(got))
	}
}
