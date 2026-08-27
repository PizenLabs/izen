package autonomy

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/execution/planner"
)

// html10KBFixture builds an ~10KB HTML document with semantic sections.
// Used to verify that small-surface manifests bypass decomposition.
func html10KBFixture() []byte {
	var b strings.Builder
	b.WriteString("<!DOCTYPE html>\n<html>\n<head><title>fixture</title></head>\n<body>\n")
	for i := 0; i < 30; i++ {
		b.WriteString("<section id=\"panel-")
		b.WriteString(strings.Repeat("x", 1))
		b.WriteString("\">\n<h2>Panel ")
		b.WriteString(strings.Repeat("x", 1))
		b.WriteString("</h2>\n<p>")
		b.WriteString(strings.Repeat("lorem ipsum ", 20))
		b.WriteString("</p>\n</section>\n")
	}
	b.WriteString("</body>\n</html>\n")
	// Pad to ~10KB if needed
	for b.Len() < 10*1024 {
		b.WriteString("<section id=\"pad\"><p>padding</p></section>\n")
	}
	return []byte(b.String())
}

// htmlMultiSectionFixture builds a large multi-section HTML file whose total
// size exceeds max_output but whose individual sections fit the budget.
func htmlMultiSectionFixture() []byte {
	var b strings.Builder
	b.WriteString("<!DOCTYPE html>\n<html>\n<head><title>big</title></head>\n<body>\n")
	for i := 0; i < 40; i++ {
		b.WriteString("<section id=\"section-")
		b.WriteString(strings.Repeat("s", 2))
		b.WriteString("\">\n<h2>Section ")
		b.WriteString(strings.Repeat("s", 1))
		b.WriteString("</h2>\n<p>")
		b.WriteString(strings.Repeat("content line ", 15))
		b.WriteString("</p>\n</section>\n")
	}
	b.WriteString("</body>\n</html>\n")
	return []byte(b.String())
}

// TestAdaptiveDecomposition_BypassDecompositionForSmallSurface simulates a
// request on a 10KB HTML file with a manifest targeting a 10-line deletion.
// The mutation surface (10 lines) is far below max_output, so the engine must
// bypass DAG fragmentation and return a single atomic sub-task.
func TestAdaptiveDecomposition_BypassDecompositionForSmallSurface(t *testing.T) {
	source := html10KBFixture()
	if len(source) < 8*1024 {
		t.Fatalf("fixture size = %d, want ~10KB", len(source))
	}
	manifest := &MutationManifest{
		TargetFile: "index.html",
		Intent:     "delete redundant content",
		Mutations: []MutationSpec{
			{Selector: "#redundant", Action: "delete", EstimatedLines: 10},
		},
	}
	// Also verify Parse round-trip (Pass 1 read-only)
	raw, _ := json.Marshal(manifest)
	parsed, err := ParseMutationManifest(raw)
	if err != nil {
		t.Fatalf("ParseMutationManifest: %v", err)
	}
	if parsed.TargetFile != "index.html" {
		t.Fatalf("parsed target = %q, want index.html", parsed.TargetFile)
	}
	surface := EstimateMutationSurface(parsed, source)
	const maxOutput = 4096
	if surface > maxOutput {
		t.Fatalf("surface %d exceeds max_output %d for small mutation (should bypass)", surface, maxOutput)
	}
	dag, err := AdaptiveDecompose("delete redundant content @index.html", "index.html", source, "base-digest", maxOutput, parsed)
	if err != nil {
		t.Fatalf("AdaptiveDecompose: %v", err)
	}
	if len(dag.SubTasks) != 1 {
		t.Fatalf("sub_task count = %d, want 1 (no DAG fragmentation for small surface)", len(dag.SubTasks))
	}
	if dag.SubTasks[0].Region.StartLine != 1 {
		t.Fatalf("single task region start = %d, want 1 (atomic unit covers file)", dag.SubTasks[0].Region.StartLine)
	}
	if dag.SubTasks[0].Kind == planner.SplitBoundedLines {
		t.Fatalf("single task kind = %s, want semantic/atomic not line-range fallback", dag.SubTasks[0].Kind)
	}
}

// TestAdaptiveDecomposition_SemanticASTBoundary simulates a large multi-section
// file exceeding max_output. Sub-tasks must be split by HTML sections or
// top-level symbols, NOT arbitrary line numbers.
func TestAdaptiveDecomposition_SemanticASTBoundary(t *testing.T) {
	source := htmlMultiSectionFixture()
	const maxOutput = 4096
	// Manifest that touches many sections so surface exceeds budget.
	manifest := &MutationManifest{
		TargetFile: "index.html",
		Intent:     "restyle every section",
		Mutations: []MutationSpec{
			{Selector: "#section-1", Action: "modify", EstimatedLines: 80},
			{Selector: "#section-2", Action: "modify", EstimatedLines: 80},
			{Selector: "#section-3", Action: "modify", EstimatedLines: 80},
			{Selector: "#section-4", Action: "modify", EstimatedLines: 80},
			{Selector: "#section-5", Action: "modify", EstimatedLines: 80},
			{Selector: "#section-6", Action: "modify", EstimatedLines: 80},
			{Selector: "#section-7", Action: "modify", EstimatedLines: 80},
			{Selector: "#section-8", Action: "modify", EstimatedLines: 80},
		},
	}
	surface := EstimateMutationSurface(manifest, source)
	if surface <= maxOutput {
		t.Fatalf("surface %d should exceed max_output %d to trigger decomposition", surface, maxOutput)
	}
	dag, err := AdaptiveDecompose("restyle every section @index.html", "index.html", source, "base-digest", maxOutput, manifest)
	if err != nil {
		t.Fatalf("AdaptiveDecompose: %v", err)
	}
	if len(dag.SubTasks) < 2 {
		t.Fatalf("sub_task count = %d, want >=2 for large surface", len(dag.SubTasks))
	}
	// Assert semantic boundaries: every sub-task description should name a
	// structural identity (section tag) and no sub-task should be a pure
	// arbitrary line-range window (SplitBoundedLines) as primary strategy.
	// At least the majority must be semantic.
	semanticCount := 0
	for _, st := range dag.SubTasks {
		if st.Kind == planner.SplitSemantic || st.Kind == planner.SplitBlock || st.Kind == planner.SplitAST {
			semanticCount++
		}
		// Description must not be a raw line interval like "lines 1–4"
		if strings.HasPrefix(st.Description, "lines ") {
			t.Errorf("sub-task %s description %q is a raw line-range, want semantic block", st.ID, st.Description)
		}
		if st.Region.Lines() <= 0 {
			t.Errorf("sub-task %s has empty region", st.ID)
		}
	}
	if semanticCount == 0 {
		t.Fatalf("no semantic sub-tasks found; all are line-range splits")
	}
	// Ensure the DAG covers the whole file contiguously (semantic tiling).
	if err := dag.Validate(); err != nil {
		t.Fatalf("DAG Validate: %v", err)
	}
	// Verify Lea scan would have produced semantic units for this HTML.
	scan := planner.LeaStructuralScan("index.html", source)
	if scan == nil || len(scan.Units) < 2 {
		t.Fatalf("LeaStructuralScan produced %d units, want >=2 semantic units", len(scan.Units))
	}
}

// TestAdaptiveDecomposition_FallbackOnInvalidManifest passes corrupt JSON and
// asserts the engine safely falls back to a single bounded inspection pass
// without panicking.
func TestAdaptiveDecomposition_FallbackOnInvalidManifest(t *testing.T) {
	source := html10KBFixture()
	const maxOutput = 4096
	corrupt := []byte(`{ invalid json`)
	// Parse must fail but not panic.
	if _, err := ParseMutationManifest(corrupt); err == nil {
		t.Fatal("ParseMutationManifest should fail on corrupt JSON")
	}
	// Adaptive path via raw manifest must not panic and must return a single task.
	dag, err := DecomposeWithRawManifest("delete @index.html", "index.html", source, "base-digest", maxOutput, corrupt)
	if err != nil {
		t.Fatalf("DecomposeWithRawManifest: %v", err)
	}
	if dag == nil {
		t.Fatal("fallback DAG is nil, want single bounded inspection DAG")
	}
	if len(dag.SubTasks) != 1 {
		t.Fatalf("fallback sub_task count = %d, want 1 (single bounded inspection)", len(dag.SubTasks))
	}
	if err := dag.Validate(); err != nil {
		t.Fatalf("fallback DAG Validate: %v", err)
	}
	// Also test empty manifest payload fallback.
	if _, err := ParseMutationManifest([]byte(``)); err == nil {
		t.Fatal("empty manifest should fail")
	}
	dag2, err := DecomposeWithRawManifest("delete @index.html", "index.html", source, "base-digest", maxOutput, []byte(``))
	if err != nil {
		t.Fatalf("empty raw fallback: %v", err)
	}
	if len(dag2.SubTasks) != 1 {
		t.Fatalf("empty raw fallback count = %d, want 1", len(dag2.SubTasks))
	}
}

// Additional coverage: manifest parsing and surface estimation edge cases.

func TestParseMutationManifest_Validation(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		ok   bool
	}{
		{"valid delete", `{"targetFile":"index.html","intent":"x","mutations":[{"selector":"#a","action":"delete","estimatedLines":5}]}`, true},
		{"missing target", `{"intent":"x","mutations":[{"selector":"#a","action":"delete","estimatedLines":5}]}`, false},
		{"invalid action", `{"targetFile":"f.html","mutations":[{"selector":"#a","action":"explode","estimatedLines":5}]}`, false},
		{"missing selector/symbol", `{"targetFile":"f.html","mutations":[{"action":"delete","estimatedLines":5}]}`, false},
		{"negative lines", `{"targetFile":"f.html","mutations":[{"selector":"#a","action":"delete","estimatedLines":-1}]}`, false},
	}
	for _, c := range cases {
		_, err := ParseMutationManifest([]byte(c.raw))
		if c.ok && err != nil {
			t.Errorf("%s: unexpected error %v", c.name, err)
		}
		if !c.ok && err == nil {
			t.Errorf("%s: expected error, got nil", c.name)
		}
	}
}

func TestEstimateMutationSurface_NilManifestFallback(t *testing.T) {
	source := []byte(strings.Repeat("a", 8000))
	surfaceNil := EstimateMutationSurface(nil, source)
	surfaceEmpty := EstimateMutationSurface(&MutationManifest{TargetFile: "f.html"}, source)
	if surfaceNil != surfaceEmpty {
		t.Fatalf("nil vs empty mutations surface mismatch: %d vs %d", surfaceNil, surfaceEmpty)
	}
	if surfaceNil <= 0 {
		t.Fatalf("fallback surface should be >0, got %d", surfaceNil)
	}
}

func TestAdaptiveDecompose_GoFunctionsSemanticBoundary(t *testing.T) {
	// Go source with multiple top-level functions — semantic units are functions.
	var fixed strings.Builder
	fixed.WriteString("package big\n\n")
	for i := 0; i < 30; i++ {
		fixed.WriteString("func Handler")
		fixed.WriteString(string(rune('A' + i%26)))
		if i >= 26 {
			fixed.WriteString(string(rune('0' + i/26)))
		}
		fixed.WriteString("() {\n")
		for j := 0; j < 5; j++ {
			fixed.WriteString("\tprintln(\"handler body line\")\n")
		}
		fixed.WriteString("}\n")
		fixed.WriteString("type T")
		fixed.WriteString(string(rune('A' + i%26)))
		fixed.WriteString(" struct{}\n")
	}
	source := []byte(fixed.String())
	const maxOutput = 1024
	manifest := &MutationManifest{
		TargetFile: "big.go",
		Intent:     "refactor all handlers",
		Mutations: []MutationSpec{
			{Symbol: "HandlerX", Action: "modify", EstimatedLines: 50},
			{Symbol: "HandlerY", Action: "modify", EstimatedLines: 50},
			{Symbol: "HandlerZ", Action: "modify", EstimatedLines: 50},
			{Symbol: "T1", Action: "modify", EstimatedLines: 50},
			{Symbol: "T2", Action: "modify", EstimatedLines: 50},
			{Symbol: "T3", Action: "modify", EstimatedLines: 50},
			{Symbol: "T4", Action: "modify", EstimatedLines: 50},
			{Symbol: "T5", Action: "modify", EstimatedLines: 50},
		},
	}
	surface := EstimateMutationSurface(manifest, source)
	if surface <= maxOutput {
		t.Fatalf("surface %d should exceed max_output %d", surface, maxOutput)
	}
	dag, err := AdaptiveDecompose("refactor @big.go", "big.go", source, "digest", maxOutput, manifest)
	if err != nil {
		t.Fatalf("AdaptiveDecompose: %v", err)
	}
	if len(dag.SubTasks) < 2 {
		t.Fatalf("sub_tasks = %d, want semantic split >=2", len(dag.SubTasks))
	}
	for _, st := range dag.SubTasks {
		if strings.HasPrefix(st.Description, "lines ") {
			t.Errorf("Go sub-task %s description %q is line-range, want function symbol", st.ID, st.Description)
		}
	}
}
