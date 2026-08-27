package autonomy

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/execution/planner"
)

// ── Pass 1 Manifest Auto-Hook + No-Op Pruning + AST Audit Context ──────────

// preflightManifestFixture builds an ~8KB HTML document with distinct section
// ids so a Pass 1 manifest can target one section deterministically.
func preflightManifestFixture() []byte {
	var b strings.Builder
	b.WriteString("<!DOCTYPE html>\n<html>\n<head><title>manifest</title></head>\n<body>\n")
	for i := 0; i < 20; i++ {
		b.WriteString("<section id=\"panel-")
		b.WriteString(strconv.Itoa(i))
		b.WriteString("\">\n<h2>Panel ")
		b.WriteString(strconv.Itoa(i))
		b.WriteString("</h2>\n<p>")
		b.WriteString(strings.Repeat("lorem ipsum dolor ", 30))
		b.WriteString("</p>\n</section>\n")
	}
	b.WriteString("</body>\n</html>\n")
	return []byte(b.String())
}

// heroPruneFixture builds an HTML document whose <section id="hero"> opens at
// exactly line 100 (lines 1-99 are untouched filler) so pruning must drop
// every block before the hero.
func heroPruneFixture() []byte {
	var b strings.Builder
	b.WriteString("<!DOCTYPE html>\n<html>\n<head><title>prune</title></head>\n<body>\n")
	// 23 filler sections x 4 lines = lines 5..96.
	for i := 0; i < 23; i++ {
		b.WriteString("<section id=\"filler-")
		b.WriteString(strconv.Itoa(i))
		b.WriteString("\">\n<h2>Filler ")
		b.WriteString(strconv.Itoa(i))
		b.WriteString("</h2>\n<p>filler content filler content filler content</p>\n</section>\n")
	}
	// Tail section: lines 97..99.
	b.WriteString("<section id=\"filler-23\">\n<p>tail</p>\n</section>\n")
	// Hero section: lines 100..110.
	b.WriteString("<section id=\"hero\">\n<h2>Hero</h2>\n<p>hero content</p>\n")
	b.WriteString("<p>hero line 2</p>\n<p>hero line 3</p>\n<p>hero line 4</p>\n")
	b.WriteString("<p>hero line 5</p>\n<p>hero line 6</p>\n<p>hero line 7</p>\n")
	b.WriteString("<p>hero line 8</p>\n</section>\n")
	b.WriteString("</body>\n</html>\n")
	return []byte(b.String())
}

// TestPreflight_AutoManifestTrigger drives the preflight autonomy loop against
// an 8KB target: the Boundary-2 guard refuses the full rewrite (zero provider
// calls), then the loop MUST automatically issue the read-only Pass 1 manifest
// request before determining the DAG strategy. The manifest's tiny mutation
// surface yields a 1-step atomic DAG — never 5 line blocks.
func TestPreflight_AutoManifestTrigger(t *testing.T) {
	root := t.TempDir()
	source := preflightManifestFixture()
	if len(source) < 8*1024 {
		t.Fatalf("fixture size = %d, want >= 8KB to trip Boundary 2", len(source))
	}
	writeTarget(t, root, "index.html", string(source))

	manifestInvoked := false
	manifestPass := func(_ context.Context, objective string, targetContent []byte) (*MutationManifest, error) {
		manifestInvoked = true
		if !strings.Contains(objective, "remove redundant content") {
			t.Errorf("manifest pass objective = %q, want the user's objective", objective)
		}
		if !strings.Contains(string(targetContent), "panel-0") {
			t.Errorf("manifest pass target content missing the file body")
		}
		return &MutationManifest{
			TargetFile: "index.html",
			Intent:     "remove redundant content",
			Mutations: []MutationSpec{
				{Selector: "#panel-0", Action: "delete", EstimatedLines: 5},
			},
		}, nil
	}

	bus := events.NewBus(events.DefaultBufferSize)
	mock := &mockProvider{} // the executor's provider must never be invoked
	x := testExecutor(t, root, mock, bus)
	driver := NewDriver(
		NewExecutorAdapter(root, execution.NewIntentGateway(root), x),
		bus,
		WithManifestPass(manifestPass),
	)

	term, err := driver.Run(context.Background(), "check this file @index.html and remove redundant content")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if term != nil {
		t.Fatalf("run terminated (%+v) instead of parking at the proposal", term)
	}

	// Invariant 1: the automatic Pass 1 manifest request was issued BEFORE the
	// DAG strategy decision, and it was the ONLY provider-side interaction.
	if !manifestInvoked {
		t.Fatal("ExecuteManifestPass was never invoked for a preflight-infeasible target")
	}
	if got := mock.calls(); got != 0 {
		t.Fatalf("executor provider calls = %d, want 0 (Boundary 2 trapped before any execution)", got)
	}

	dag := driver.Proposal()
	if dag == nil {
		t.Fatal("no DECOMPOSITION_PROPOSAL staged")
	}
	if dag.Status != planner.PlanStaged {
		t.Fatalf("plan status = %s, want %s", dag.Status, planner.PlanStaged)
	}
	// The manifest-scoped surface fits the budget: exactly ONE atomic step,
	// never the 5 line blocks a naive slicer would emit.
	if len(dag.SubTasks) != 1 {
		t.Fatalf("sub-tasks = %d, want exactly 1 (single atomic execution, not line blocks)", len(dag.SubTasks))
	}
	if dag.SubTasks[0].Kind == planner.SplitBoundedLines {
		t.Fatalf("sub-task kind = %s, want semantic/atomic — the naive line slicer must never be the primary strategy", dag.SubTasks[0].Kind)
	}
	if !dag.ManifestScoped {
		t.Fatal("plan must be manifest-scoped when staged from a Pass 1 manifest")
	}
	if !strings.Contains(dag.ProposalSummary(), "manifest") &&
		!strings.Contains(dag.SubTasks[0].Description, "panel-0") {
		t.Fatalf("proposal does not name the manifest-scoped mutation: %s", dag.ProposalSummary())
	}
}

// TestPreflight_AutoManifestFallbackToBoundedInspection pins the fail-soft
// path: when the Pass 1 manifest generation fails or returns empty mutations,
// the loop falls back to a single-pass bounded inspection — it NEVER falls back
// to the naive line slicer.
func TestPreflight_AutoManifestFallbackToBoundedInspection(t *testing.T) {
	root := t.TempDir()
	source := preflightManifestFixture()
	writeTarget(t, root, "index.html", string(source))

	fail := false
	manifestPass := func(context.Context, string, []byte) (*MutationManifest, error) {
		if fail {
			return nil, errors.New("manifest: provider refused the read-only pass")
		}
		return &MutationManifest{TargetFile: "index.html", Intent: "x", Mutations: nil}, nil
	}

	run := func() *planner.ExecutionDAG {
		bus := events.NewBus(events.DefaultBufferSize)
		mock := &mockProvider{}
		x := testExecutor(t, root, mock, bus)
		driver := NewDriver(
			NewExecutorAdapter(root, execution.NewIntentGateway(root), x),
			bus, WithManifestPass(manifestPass))
		if _, err := driver.Run(context.Background(), "check @index.html and remove redundant content"); err != nil {
			t.Fatalf("Run: %v", err)
		}
		return driver.Proposal()
	}

	for name, wantFail := range map[string]bool{"empty mutations": false, "generation failure": true} {
		fail = wantFail
		dag := run()
		if dag == nil {
			t.Fatalf("%s: no proposal staged", name)
		}
		if len(dag.SubTasks) != 1 {
			t.Fatalf("%s: sub-tasks = %d, want 1 single-pass bounded inspection", name, len(dag.SubTasks))
		}
		if dag.SubTasks[0].Kind == planner.SplitBoundedLines {
			t.Fatalf("%s: fell back to the naive line slicer", name)
		}
	}
}

// TestDecomposition_PruneUnmodifiedBlocks supplies a manifest targeting ONLY
// the <section id="hero"> (lines 100-110) and verifies the generated DAG
// contains EXACTLY ONE sub-task: lines 1-99 are pruned, so no sub-task is ever
// created solely to evaluate to no_op_objective_satisfied over an untouched
// block.
func TestDecomposition_PruneUnmodifiedBlocks(t *testing.T) {
	source := heroPruneFixture()
	manifest := &MutationManifest{
		TargetFile: "index.html",
		Intent:     "modify the hero section",
		Mutations: []MutationSpec{
			// The estimated surface is deliberately large so AdaptiveDecompose
			// must decompose; pruning still scopes the DAG to the real unit.
			{Selector: "#hero", Action: "modify", EstimatedLines: 500},
		},
	}
	const maxOutput = 1024
	if s := EstimateMutationSurface(manifest, source); s <= maxOutput {
		t.Fatalf("surface %d must exceed max_output %d to exercise decomposition", s, maxOutput)
	}
	dag, err := AdaptiveDecompose("modify the hero @index.html", "index.html", source, "digest", maxOutput, manifest)
	if err != nil {
		t.Fatalf("AdaptiveDecompose: %v", err)
	}
	if len(dag.SubTasks) != 1 {
		t.Fatalf("sub-tasks = %d, want exactly 1 (lines 1-99 pruned)", len(dag.SubTasks))
	}
	st := dag.SubTasks[0]
	if st.Region.StartLine != 100 || st.Region.EndLine != 110 {
		t.Fatalf("sub-task region = %s, want exactly lines 100-110 (hero section)", st.Region)
	}
	if !dag.ManifestScoped {
		t.Fatal("pruned DAG must be manifest-scoped (non-contiguous mutation surface)")
	}
	if err := dag.Validate(); err != nil {
		t.Fatalf("DAG Validate: %v", err)
	}
	// Asserted: zero sub-tasks over unmodified blocks — every unit carries a
	// manifest mutation, so no unit can evaluate to no_op_objective_satisfied.
	for _, st := range dag.SubTasks {
		if !unitCoveredByManifest(manifest, planner.LeaStructuralScan("index.html", source), planner.Section{Region: st.Region, Label: st.Description}) {
			t.Fatalf("sub-task %s (%s) is not covered by any manifest mutation — it could only no-op", st.ID, st.Region)
		}
	}
}

// TestDecomposition_PruneKeepsMultipleTargetedBlocks verifies that pruning
// keeps EVERY targeted block (multiple mutations → multiple sub-tasks) while
// still dropping every untargeted one.
func TestDecomposition_PruneKeepsMultipleTargetedBlocks(t *testing.T) {
	source := heroPruneFixture()
	manifest := &MutationManifest{
		TargetFile: "index.html",
		Intent:     "restyle hero and filler-0",
		Mutations: []MutationSpec{
			{Selector: "#hero", Action: "modify", EstimatedLines: 500},
			{Selector: "#filler-0", Action: "modify", EstimatedLines: 500},
		},
	}
	const maxOutput = 1024
	dag, err := AdaptiveDecompose("restyle @index.html", "index.html", source, "digest", maxOutput, manifest)
	if err != nil {
		t.Fatalf("AdaptiveDecompose: %v", err)
	}
	if len(dag.SubTasks) != 2 {
		t.Fatalf("sub-tasks = %d, want 2 (hero + filler-0; filler-1..filler-23 pruned)", len(dag.SubTasks))
	}
	regions := map[planner.Region]bool{}
	for _, st := range dag.SubTasks {
		regions[st.Region] = true
	}
	if _, ok := regions[planner.Region{StartLine: 100, EndLine: 110}]; !ok {
		t.Fatalf("hero unit (100-110) pruned: regions = %v", regions)
	}
	if _, ok := regions[planner.Region{StartLine: 5, EndLine: 8}]; !ok {
		t.Fatalf("filler-0 unit (5-8) pruned: regions = %v", regions)
	}
}

// TestRetry_ASTErrorContextInjection simulates an HTML syntax error during
// mutation: the V3 pipeline rejects an artifact with an unterminated <script>
// element at a known line, and the retry feedback payload must carry the exact
// line parse diagnostic ([CONTRACT FAILURE] Line <N>: <ParseError> ...) instead
// of resending raw code.
func TestRetry_ASTErrorContextInjection(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "index.html", "<html>\n<body>\n<p>hello</p>\n</body>\n</html>")

	// The model's replacement leaves <script> unterminated on line 3.
	malformed := "<html>\n<body>\n<script>alert(1)\n</body>\n</html>"
	mock := &mockProvider{responses: []*ai.Response{{
		Content: malformed,
		Usage:   ai.ProviderUsage{Known: true, PromptTokens: 120, CompletionTokens: 30, FinishReason: "stop"},
	}}}
	bus := events.NewBus(events.DefaultBufferSize)
	x := testExecutor(t, root, mock, bus)

	res, err := x.Execute(context.Background(), execution.ExecuteRequest{
		RequestID: "ast-audit-1",
		Mode:      "build",
		Prompt:    "fix the script tag",
		Target:    "index.html",
	})
	if err == nil {
		t.Fatal("Execute must reject the malformed HTML artifact")
	}
	if !errors.Is(err, execution.ErrArtifactRetryableRejected) {
		t.Fatalf("err = %v, want ErrArtifactRetryableRejected", err)
	}
	if res == nil {
		t.Fatal("Execute must return a non-nil result")
	}

	// The rejection error carries the exact line parse diagnostic.
	msg := res.Err.Error()
	if !strings.Contains(msg, "[CONTRACT FAILURE] Line 3") {
		t.Fatalf("rejection error missing the exact line diagnostic:\n%s", msg)
	}
	if !strings.Contains(msg, "unterminated <script> element") {
		t.Fatalf("rejection error missing the parse error:\n%s", msg)
	}
	if !strings.Contains(msg, "Re-emit ONLY the corrected SEARCH/REPLACE block fixing the unclosed tag") {
		t.Fatalf("rejection error missing the re-emit directive:\n%s", msg)
	}

	// The retry loop prompt (retryDirective) injects the SAME targeted
	// diagnostic so the successor anchors its correction at line 3.
	obs := autonomy.Observation{
		Outcome:    autonomy.OutcomeArtifactRetryableRejected,
		Diagnostic: msg,
		Target:     "index.html",
	}
	st := planner.SubTask{ID: "st-1", Index: 1, Target: "index.html",
		Region: planner.Region{StartLine: 1, EndLine: 5}}
	directive := retryDirective(st, obs, 2)
	for _, want := range []string{
		"[CONTRACT FAILURE] Line 3: unterminated <script> element",
		"Re-emit ONLY the corrected SEARCH/REPLACE block fixing the unclosed tag",
	} {
		if !strings.Contains(directive, want) {
			t.Fatalf("retry feedback missing %q:\n%s", want, directive)
		}
	}
}

// TestStructuralAuditDirective_NonStructuralPassThrough guards the boundary:
// non-structural rejections keep their existing detail and never pick up a
// fabricated [CONTRACT FAILURE] directive.
func TestStructuralAuditDirective_NonStructuralPassThrough(t *testing.T) {
	cases := []string{
		"executor: mutation artifact rejected with retry directive: f.go: SEARCH anchor matches 3 regions",
		"executor: bounded patch contract requires SEARCH/REPLACE blocks or unified diff hunks; full-file output rejected",
		"",
	}
	for _, c := range cases {
		if got := execution.StructuralAuditDirective(c); got != c {
			t.Errorf("StructuralAuditDirective(%q) = %q, want unchanged", c, got)
		}
	}
}
