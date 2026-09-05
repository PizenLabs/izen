package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/PizenLabs/izen/internal/runtime/substrate"
	"github.com/PizenLabs/izen/pkg/provider/capability"
	"github.com/PizenLabs/izen/pkg/runtime/executor"
	"github.com/PizenLabs/izen/pkg/runtime/gate"
	"github.com/PizenLabs/izen/pkg/runtime/preflight"
	"github.com/PizenLabs/izen/pkg/runtime/ui/decision"
)

// countingReader is a SnapshotReader that counts every read. It is the
// mock-filesystem call counter used to prove zero disk-read redundancy during
// the extraction and verification sub-cycles: the ONLY read allowed per cycle
// is the Observation-phase snapshot read.
type countingReader struct {
	reads int64
}

func (c *countingReader) ReadSnapshot(_ context.Context, path string) ([]byte, error) {
	atomic.AddInt64(&c.reads, 1)
	return os.ReadFile(path)
}

func (c *countingReader) count() int { return int(atomic.LoadInt64(&c.reads)) }

// writeTarget writes content into dir/name and returns the absolute path.
func writeTarget(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

// readTarget reads content from path, failing the test on error.
func readTarget(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// newLoop wires a real Loop: memory-backed RMAH pipeline, real gate pipeline,
// real executor, and the given snapshot reader, with mandatory substrate.
func newLoop(t *testing.T, targetPath string, reader SnapshotReader) *Loop {
	t.Helper()
	sub := substrate.NewConcreteSubstrate(filepath.Dir(targetPath))
	l := NewLoop(
		NewMemoryBackedExtractor(targetPath),
		gate.NewPipeline(),
		executor.NewExecutor(),
		reader,
	)
	l.substrate = sub
	return l
}

// TestCase1CorruptASTBudgetExceeded renders the DecisionSurface with the
// explicit risk hierarchy: "Repair AST first (recommended)" on a corrupt AST,
// and FULL_REWRITE grayed out with the budget-exceeded label.
func TestCase1CorruptASTBudgetExceeded(t *testing.T) {
	t.Parallel()

	// A structurally corrupt Go baseline (unclosed paren) that also exceeds a
	// small model's output budget under the FULL_REWRITE accounting.
	content := []byte("package main\n\nfunc main( {\n\tprintln(1)\n}\n")
	target := preflight.TargetState{
		Path:      "broken.go",
		Content:   content,
		ASTStatus: preflight.ASTCorrupt,
	}
	caps := capability.ModelCapabilities{MaxOutputTokens: 64}

	// The budget gate is the hard gate: it must forbid FULL_REWRITE before any
	// model invocation.
	res := preflight.EvaluateBudgetGate(target, caps)
	if res.BudgetStatus != preflight.BudgetExceeded {
		t.Fatalf("BudgetStatus = %q, want %q", res.BudgetStatus, preflight.BudgetExceeded)
	}
	if res.FullRewrite != preflight.StrategyForbidden {
		t.Fatalf("FullRewrite = %v, want FORBIDDEN", res.FullRewrite)
	}

	// The surface renders the dynamic annotations from the same verdict.
	surface := decision.Build(target.Path, target.ASTStatus, &res)

	repair := surface.Option(decision.OptionRepairAST)
	if repair == nil {
		t.Fatal("surface must offer Repair AST first")
	}
	if !repair.Recommended {
		t.Error("Repair AST first must be the recommended option")
	}
	if repair.RiskLevel != decision.RiskLevelLow {
		t.Errorf("Repair risk = %v, want low", repair.RiskLevel)
	}
	if !strings.Contains(repair.Label, "(recommended)") {
		t.Errorf("Repair label = %q, want the (recommended) annotation", repair.Label)
	}

	escape := surface.Option(decision.OptionBoundedSearchReplace)
	if escape == nil {
		t.Fatal("surface must offer Bounded textual SEARCH/REPLACE")
	}
	if escape.RiskLevel != decision.RiskLevelHigh {
		t.Errorf("SEARCH/REPLACE risk = %v, want high", escape.RiskLevel)
	}
	if !strings.Contains(escape.Label, "[HIGH RISK]") {
		t.Errorf("SEARCH/REPLACE label = %q, want the [HIGH RISK] annotation", escape.Label)
	}
	if !strings.Contains(escape.Description, "Bypasses AST validation") ||
		!strings.Contains(escape.Description, "May introduce syntax errors") {
		t.Errorf("SEARCH/REPLACE description = %q, want the syntax-corruption warning", escape.Description)
	}

	rewrite := surface.Option(decision.OptionFullRewrite)
	if rewrite == nil {
		t.Fatal("surface must expose the FULL_REWRITE option")
	}
	if !rewrite.Disabled {
		t.Error("FULL_REWRITE must be disabled/grayed out when the budget is exceeded")
	}
	if !strings.Contains(rewrite.Label, "[DISABLED: Exceeds Model Output Budget]") {
		t.Errorf("FULL_REWRITE label = %q, want the disabled annotation", rewrite.Label)
	}

	// The rendered frame carries the annotations for terminal projection.
	rendered := surface.Render(64)
	for _, want := range []string{
		"STRATEGY DECISION",
		"Repair AST first (recommended)",
		"[HIGH RISK]",
		"[DISABLED: Exceeds Model Output Budget]",
		"Bypasses AST validation. May introduce syntax errors.",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("rendered surface missing %q:\n%s", want, rendered)
		}
	}
}

// TestCase2TruncatedOutputHaltsAtAwaitingHuman verifies that truncated / invalid
// model output is captured as evidence by RMAH, halted by the gate, and routed
// to awaiting_human after the bounded retry budget — never an endless
// token-burning loop.
func TestCase2TruncatedOutputHaltsAtAwaitingHuman(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	original := "package main\n\nfunc main() {\n\tprintln(\"existing\")\n}\n"
	path := writeTarget(t, dir, "main.go", original)

	reader := &countingReader{}
	loop := newLoop(t, path, reader)
	ctx := context.Background()

	if err := loop.Observe(ctx, path); err != nil {
		t.Fatalf("Observe: %v", err)
	}

	// Truncated full-text rewrite: cut mid-token, cannot anchor (Tier 2), so
	// RMAH reconstructs it as a Tier 3 inferred candidate carrying Truncated
	// evidence.
	truncated := []byte("package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"hello w")

	// First truncated output: RMAH captures the candidate, the gate halts it as
	// a format error (formatFailures = 1) — one bounded retry remains.
	out1, err := loop.ExecuteCycle(ctx, truncated)
	if err != nil {
		t.Fatalf("first ExecuteCycle: %v", err)
	}
	if out1.State != StateExecuting {
		t.Fatalf("first cycle state = %v, want executing (1 bounded retry remains)", out1.State)
	}
	if !out1.Evidence.Truncated {
		t.Error("first cycle must capture truncated evidence from RMAH")
	}

	// Second consecutive truncated output: formatFailures reaches the budget
	// and the loop fast-fails to awaiting_human with the evidence attached.
	out2, err := loop.ExecuteCycle(ctx, truncated)
	if err != nil {
		t.Fatalf("second ExecuteCycle: %v", err)
	}
	if out2.State != StateAwaitingHuman {
		t.Fatalf("second cycle state = %v, want awaiting_human (fast-fail)", out2.State)
	}
	if loop.State() != StateAwaitingHuman {
		t.Errorf("loop state = %v, want awaiting_human", loop.State())
	}
	if !out2.Evidence.Truncated {
		t.Error("halted cycle must attach the captured truncated evidence")
	}
	if loop.Evidence().Truncated != out2.Evidence.Truncated {
		t.Error("loop evidence accessor must carry the attached ArtifactEvidence")
	}
	if out2.Reason == "" {
		t.Error("halted cycle must carry a bounded diagnostic reason")
	}

	// No retry loop may burn tokens: after the halt, the loop must refuse to
	// keep recovering without an explicit human decision.
	if got := readTarget(t, path); got != original {
		t.Errorf("target content = %q, want untouched %q", got, original)
	}
}

// TestCase3ZeroDiskReadRedundancy verifies the target is loaded into the memory
// snapshot exactly ONCE per cycle (Observation phase) and that the extraction,
// verification, and commit sub-cycles consume memory byte slices — zero
// additional os.ReadFile calls occur.
func TestCase3ZeroDiskReadRedundancy(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	// A non-trivial target so a single added statement stays well within the
	// scope-drift limit of the gate pipeline.
	path := writeTarget(t, dir, "main.go", "package main\n\nimport \"os\"\n\nfunc main() {\n\tprintln(os.Args[0])\n\tprintln(os.Args[1])\n}\n")

	reader := &countingReader{}
	loop := newLoop(t, path, reader)
	ctx := context.Background()

	if err := loop.Observe(ctx, path); err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if reader.count() != 1 {
		t.Fatalf("reads after Observation = %d, want exactly 1", reader.count())
	}

	// A clean Tier 1 strict unified diff: RMAH parses it exactly, the gate
	// approves it (confidence 1.0, low risk, minimal drift), and the executor
	// commits it from the memory snapshot.
	rawDiff := "--- a/main.go\n" +
		"+++ b/main.go\n" +
		"@@ -6,3 +6,4 @@\n" +
		" \tprintln(os.Args[0])\n" +
		" \tprintln(os.Args[1])\n" +
		"+\tprintln(os.Args[2])\n" +
		" }\n"

	out, err := loop.ExecuteCycle(ctx, []byte(rawDiff))
	if err != nil {
		t.Fatalf("ExecuteCycle: %v", err)
	}
	if out.State != StateCommitted {
		t.Fatalf("cycle state = %v, want committed (got: %s)", out.State, out.Reason)
	}
	if loop.State() != StateCommitted {
		t.Errorf("loop state = %v, want committed", loop.State())
	}

	// The whole cycle read the disk exactly once — at the Observation phase.
	// RMAH extraction, gate verification, and the executor commit all consumed
	// the memory snapshot.
	if got := reader.count(); got != 1 {
		t.Fatalf("reads during extraction+verification+commit = %d, want exactly 1 (Observation only)", got)
	}

	// The mutation was applied from the in-memory bytes, not a second read.
	got := readTarget(t, path)
	if !strings.Contains(got, "println(os.Args[2])") {
		t.Errorf("committed content = %q, want the applied mutation", got)
	}
}
