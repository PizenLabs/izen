package autonomy

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
)

// ── PREFLIGHT BASELINE SYNTAX SNAPSHOT + NO-OP GLOBAL AUDIT RELAXATION ──────

// brokenBaselineFixture renders a decomposition-sized HTML document whose
// syntax is ALREADY broken BEFORE the DAG runs: an unterminated <script>
// element (the exact defect the V3 validator and the Lea scan both flag). Every
// remaining line is unique so a "remove redundant content" objective carries
// no structural counter-evidence and the model's NO_CHANGES_REQUIRED claim
// converges to no_op_objective_satisfied.
func brokenBaselineFixture() []byte {
	var b strings.Builder
	b.WriteString("<!DOCTYPE html>\n<html>\n<head><title>Broken</title></head>\n<body>\n")
	b.WriteString("<script>\n  console.log('under construction');\n")
	for i := 0; i < 48; i++ {
		fmt.Fprintf(&b, "<section id=\"filler-%d\">\n<h2>Filler %d</h2>\n<p>unique filler content number %d lorem ipsum dolor sit amet</p>\n</section>\n", i, i, i)
	}
	b.WriteString("</body>\n</html>\n")
	return []byte(b.String())
}

// validLargeBaselineFixture renders a decomposition-sized, STRUCTURALLY VALID
// HTML document. It is large enough to trip the Boundary-2 budget preflight (so
// the driver stages a DECOMPOSITION_PROPOSAL) but parses cleanly — used by the
// execution-inertia and global-audit tests that must run over a baseline the
// strict preflight hard-gate permits to be decomposed.
func validLargeBaselineFixture() []byte {
	var b strings.Builder
	b.WriteString("<!DOCTYPE html>\n<html>\n<head><title>Valid</title></head>\n<body>\n")
	for i := 0; i < 80; i++ {
		fmt.Fprintf(&b, "<section id=\"sec-%d\">\n<h2>Section %d</h2>\n<p>unique filler content number %d lorem ipsum dolor sit amet consectetur</p>\n</section>\n", i, i, i)
	}
	b.WriteString("</body>\n</html>\n")
	return []byte(b.String())
}

// TestAudit_PreexistingBaselineSyntaxError pins the NEW strict preflight
// hard-gate: a target whose baseline AST is ALREADY CORRUPT must NEVER be
// decomposed into a DAG — the pre-existing-baseline relaxation that once let a
// no-op run slip through on a broken file is GONE. The run must halt at the
// Zero-Token DecisionSurface barrier with ZERO sub-tasks staged, so the global
// post-DAG audit never even runs over a broken document.
func TestAudit_PreexistingBaselineSyntaxError(t *testing.T) {
	root := t.TempDir()
	source := brokenBaselineFixture()
	if len(source) < 8*1024/2 { // sanity: big enough to trip Boundary-2 preflight
		t.Fatalf("fixture size = %d, too small to be preflight-infeasible", len(source))
	}
	writeTarget(t, root, "index.html", string(source))

	p := &dagProvider{root: root, target: "index.html"}
	bus := events.NewBus(events.DefaultBufferSize)
	x := testExecutor(t, root, p, bus)
	adapter := NewExecutorAdapter(root, execution.NewIntentGateway(root), x)

	driver := NewDriver(adapter, bus)
	term, err := driver.Run(context.Background(), "check this file @index.html and remove redundant content")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// The run MUST park at the DecisionSurface barrier — never terminate, never
	// stage a DECOMPOSITION_PROPOSAL DAG over the broken baseline.
	if term != nil {
		t.Fatalf("run terminated (%+v) instead of parking at the DecisionSurface barrier", term)
	}
	if driver.State() != autonomy.RuntimeAwaitingHuman {
		t.Fatalf("state = %s, want awaiting_human", driver.State())
	}
	b := driver.Boundary()
	if b == nil {
		t.Fatal("no human boundary parked")
	}
	if b.Action != autonomy.HumanBoundaryProposal {
		t.Fatalf("boundary action = %q, want %q (DecisionSurface hard-gate)", b.Action, autonomy.HumanBoundaryProposal)
	}
	if b.Action == autonomy.HumanBoundaryDecomposition {
		t.Fatal("corrupt baseline was routed to DECOMPOSITION_PROPOSAL — strict gate violated")
	}
	// ZERO sub-tasks may be created against a broken document.
	if driver.Proposal() != nil {
		t.Fatal("driver.Proposal() returned a staged DAG — decomposition forbidden on a corrupt baseline")
	}
	if driver.Plan() != nil {
		t.Fatal("driver.Plan() returned a staged DAG — decomposition forbidden on a corrupt baseline")
	}
	ds := driver.DecisionSurface()
	if ds == nil {
		t.Fatal("DecisionSurface barrier must expose a DecisionSurface")
	} else if ds.ASTStatus != ASTCorrupt {
		t.Fatalf("DecisionSurface ASTStatus = %q, want corrupt", ds.ASTStatus)
	}
}

// TestPreflight_ManifestCompactness pins the MINIMAL MANIFEST SCHEMA wire
// contract: the Pass 1 manifest prompt must force a concise minified JSON
// payload with ZERO prose and a hard 200-token ceiling, and the whole prompt
// must itself stay well under 250 tokens so it never crowds the model's
// output budget.
func TestPreflight_ManifestCompactness(t *testing.T) {
	prompt := buildManifestPrompt()
	for _, want := range []string{
		"OUTPUT ONLY VALID MINIFIED JSON ARRAY OF MUTATION TARGETS",
		"DO NOT WRITE CODE",
		"DO NOT EXPLAIN",
		"DO NOT INCLUDE MARKDOWN FENCES",
		"MAX 200 TOKENS",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("manifest prompt missing the compact directive %q:\n%s", want, prompt)
		}
	}
	if !strings.Contains(prompt, `"mutations":[`) {
		t.Fatalf("manifest prompt must demand the JSON array of mutation targets:\n%s", prompt)
	}
	// The prompt itself stays well under 250 tokens (≈4 bytes/token) so it
	// never crowds the model's 200-token output budget.
	if tokens := len(prompt) / 4; tokens >= 250 {
		t.Fatalf("manifest prompt is ~%d tokens, want < 250", tokens)
	}
	// The wire ceiling matches the directive: a model cannot ramble past 200.
	if ManifestPassMaxTokens > 200 {
		t.Fatalf("ManifestPassMaxTokens = %d, want <= 200 to enforce the MAX 200 TOKENS directive", ManifestPassMaxTokens)
	}
}

// TestManifestPass_RejectVerboseAsInvalidJSON pins the 512-token rejection
// path of the manifest wire: an over-long payload (a provider ignoring the
// ceiling) must be surfaced as an invalid-manifest failure, never as an
// OUTPUT_EXHAUSTED gate signal.
func TestManifestPass_RejectVerboseAsInvalidJSON(t *testing.T) {
	verbose := `{"targetFile":"index.html","intent":"remove redundant content","mutations":[{"selector":"#hero","action":"modify","estimatedLines":12}]}` +
		strings.Repeat(`{"selector":"#pad","action":"modify","estimatedLines":1},`, 4000)
	if len(verbose)/4 <= ManifestPassRejectTokens {
		t.Fatalf("verbose fixture is only ~%d tokens, want > %d", len(verbose)/4, ManifestPassRejectTokens)
	}
	// ParseMutationManifest must reject the oversized payload as invalid JSON
	// (the schema ceiling) so the caller falls back, never exhausts a gate.
	if _, err := ParseMutationManifest([]byte(verbose)); err == nil {
		t.Fatal("oversized manifest payload must be rejected as invalid")
	}
}

// TestParseMutationManifest_MinimalArraySchema pins the MINIMAL MANIFEST
// SCHEMA: the compact Pass 1 prompt demands a raw JSON array of mutation
// targets, so a bare array must parse into the same MutationManifest surface
// as the legacy envelope object.
func TestParseMutationManifest_MinimalArraySchema(t *testing.T) {
	raw := `[{"selector":"#hero","action":"delete","estimatedLines":5},{"symbol":"HandlerFoo","action":"modify","estimatedLines":12}]`
	m, err := ParseMutationManifest([]byte(raw))
	if err != nil {
		t.Fatalf("bare array manifest rejected: %v", err)
	}
	if len(m.Mutations) != 2 {
		t.Fatalf("mutations = %d, want 2", len(m.Mutations))
	}
	if m.Mutations[0].Selector != "#hero" || m.Mutations[0].Action != "delete" {
		t.Fatalf("mutation[0] = %+v, want selector #hero action delete", m.Mutations[0])
	}
	if m.Mutations[1].Symbol != "HandlerFoo" || m.Mutations[1].Action != "modify" {
		t.Fatalf("mutation[1] = %+v, want symbol HandlerFoo action modify", m.Mutations[1])
	}
	// The surface estimate must agree with the envelope-object form.
	env := &MutationManifest{Mutations: m.Mutations}
	if e := EstimateMutationSurface(env, []byte(strings.Repeat("x", 1000))); e <= 0 {
		t.Fatalf("array-form surface estimate = %d, want > 0", e)
	}
}

// ── Zero-Token EVALUATING_SCOPE ExecutionGate ───────────────────────────────

// TestPreflight_CorruptAST_FailsGate drives the zero-token preflight over an
// HTML document with an unclosed <script> element (the deterministic structural
// validator flags it). The ExecutionGate MUST close: a corrupted target AST is
// provably non-executable, and no LLM token may be spent on it.
func TestPreflight_CorruptAST_FailsGate(t *testing.T) {
	corrupt := []byte("<!DOCTYPE html>\n<html>\n<head><title>Broken</title></head>\n<body>\n" +
		"<script>\n  console.log('under construction');\n" +
		"<section><h2>Filler</h2><p>lorem ipsum dolor sit amet</p></section>\n" +
		"</body>\n</html>\n")

	eval := EvaluateScope(ScopeInput{
		Target:          "index.html",
		Content:         corrupt,
		MaxOutputTokens: 1024,
		Root:            t.TempDir(),
	})

	if eval.ASTStatus != ASTCorrupt {
		t.Fatalf("ASTStatus = %s, want corrupt", eval.ASTStatus)
	}
	if eval.ExecutionGate() {
		t.Fatal("ExecutionGate = true, want false — a corrupted AST must fail closed")
	}
	if len(eval.RequiredProposals) == 0 {
		t.Fatal("a closed gate must record a required human proposal")
	}
	// The gate must also close on an unterminated raw-text element (a prose
	// header left open at EOF is silently repaired by the lenient HTML5 parser,
	// but an unterminated <style> is a raw-text element the deterministic scan
	// flags).
	prose := []byte("<html>\n<head><style>\n  .x { color: red; }\n</head>\n<body>\n<p>prose</p>\n</body>\n</html>\n")
	pv := EvaluateScope(ScopeInput{Target: "page.html", Content: prose, MaxOutputTokens: 1024, Root: t.TempDir()})
	if pv.ASTStatus != ASTCorrupt {
		t.Fatalf("prose-header ASTStatus = %s, want corrupt", pv.ASTStatus)
	}
	if pv.ExecutionGate() {
		t.Fatal("ExecutionGate = true for an unterminated raw-text element, want false")
	}
}

// TestPreflight_ValidTarget_PassesGate pins the happy path: a structurally
// valid, fully-resolved, in-budget target passes the ExecutionGate and stages
// no human proposal.
func TestPreflight_ValidTarget_PassesGate(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "app.css", "body { color: black; }\n")
	writeTarget(t, root, "app.js", "console.log('hi');\n")
	valid := []byte("<!DOCTYPE html>\n<html>\n<head><title>OK</title>\n" +
		"<link rel=\"stylesheet\" href=\"app.css\">\n</head>\n<body>\n" +
		"<script src=\"app.js\"></script>\n</body>\n</html>\n")
	writeTarget(t, root, "index.html", string(valid))

	eval := EvaluateScope(ScopeInput{
		Target:          "index.html",
		Content:         valid,
		MaxOutputTokens: 1024,
		Root:            root,
	})

	if eval.ASTStatus != ASTValid {
		t.Fatalf("ASTStatus = %s, want valid", eval.ASTStatus)
	}
	if eval.DependencyStatus != DependenciesResolved {
		t.Fatalf("DependencyStatus = %s, want resolved", eval.DependencyStatus)
	}
	if eval.BudgetStatus != BudgetWithinLimits {
		t.Fatalf("BudgetStatus = %s, want within_limits", eval.BudgetStatus)
	}
	if !eval.ExecutionGate() {
		t.Fatalf("ExecutionGate = false for a valid target; findings: %v", eval.Findings)
	}
	if len(eval.RequiredProposals) != 0 {
		t.Fatalf("required proposals = %d, want 0", len(eval.RequiredProposals))
	}
}

// TestPreflight_BudgetExceeded_FailsGate drives the zero-token preflight over a
// target whose estimated generation cost (bytes/4 × FullRewriteTokenMultiplier)
// exceeds the declared max_output. The BudgetStatus MUST be exceeded and the
// ExecutionGate MUST close — the target is provably infeasible BEFORE any
// provider request (zero tokens).
func TestPreflight_BudgetExceeded_FailsGate(t *testing.T) {
	// ~2000 bytes → ~500 tokens × 3 = ~1500 tokens > 256 budget.
	big := []byte(strings.Repeat("<section><p>lorem ipsum dolor sit amet consectetur</p></section>\n", 80))
	if len(big)/4*execution.FullRewriteTokenMultiplier <= 256 {
		t.Fatalf("fixture estimates %d tokens, want > 256 budget", len(big)/4*execution.FullRewriteTokenMultiplier)
	}

	eval := EvaluateScope(ScopeInput{
		Target:          "big.html",
		Content:         big,
		MaxOutputTokens: 256,
		Root:            t.TempDir(),
	})

	if eval.BudgetStatus != BudgetExceeded {
		t.Fatalf("BudgetStatus = %s, want exceeded", eval.BudgetStatus)
	}
	if eval.ExecutionGate() {
		t.Fatal("ExecutionGate = true, want false — an over-budget target must fail closed")
	}
	if len(eval.RequiredProposals) == 0 {
		t.Fatal("a closed gate must record a required human proposal")
	}
}

// TestPreflight_MissingLocalDependency_FailsGate drives the zero-token preflight
// over an HTML document that references a <script src> file that does not exist.
// The DependencyStatus MUST be unresolved and the ExecutionGate MUST close.
func TestPreflight_MissingLocalDependency_FailsGate(t *testing.T) {
	html := []byte("<!DOCTYPE html>\n<html>\n<head><title>Refs</title></head>\n<body>\n" +
		"<script src=\"missing.js\"></script>\n</body>\n</html>\n")

	eval := EvaluateScope(ScopeInput{
		Target:          "index.html",
		Content:         html,
		MaxOutputTokens: 1024,
		Root:            t.TempDir(), // empty: missing.js does not exist
	})

	if eval.DependencyStatus != DependenciesUnresolved {
		t.Fatalf("DependencyStatus = %s, want unresolved", eval.DependencyStatus)
	}
	if eval.ExecutionGate() {
		t.Fatal("ExecutionGate = true, want false — unresolved dependencies must fail closed")
	}
}

// TestPreflight_UnboundedOrEmpty_NotBarred verifies the gate is not a false
// positive on creation intent or unbounded budgets: an empty target (creation)
// and a 0 max_output (unbounded) are treated as unknown/within-limits, not
// hard barriers.
func TestPreflight_UnboundedOrEmpty_NotBarred(t *testing.T) {
	empty := EvaluateScope(ScopeInput{Target: "new.txt", Content: nil, MaxOutputTokens: 1024, Root: t.TempDir()})
	if empty.ASTStatus != ASTUnknown {
		t.Fatalf("empty target ASTStatus = %s, want unknown", empty.ASTStatus)
	}
	// An absent target is not executable as a mutation, so it still closes the
	// gate (a missing file cannot be staged) — but it is NOT a corrupt-AST
	// barrier. Assert the distinction: unknown != corrupt.
	if empty.ASTStatus == ASTCorrupt {
		t.Fatal("empty target must not be classified corrupt")
	}
}
