package autonomy

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/execution/ingestion"
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
// size exceeds max_output but whose individual sections fit the budget. Each
// section carries a DISTINCT id so the Pass 1 manifest's selectors resolve to
// unique semantic units under pruning.
func htmlMultiSectionFixture() []byte {
	var b strings.Builder
	b.WriteString("<!DOCTYPE html>\n<html>\n<head><title>big</title></head>\n<body>\n")
	for i := 0; i < 40; i++ {
		b.WriteString("<section id=\"section-")
		b.WriteString(strconv.Itoa(i))
		b.WriteString("\">\n<h2>Section ")
		b.WriteString(strconv.Itoa(i))
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
// asserts the engine safely falls back WITHOUT panicking — never one giant
// whole-file sub-task. On a structured HTML target the fallback prefers
// SEMANTIC BLOCK boundaries (structural sections, not arbitrary 40-line cuts);
// on a target with no structural topology it degrades to bounded line windows.
func TestAdaptiveDecomposition_FallbackOnInvalidManifest(t *testing.T) {
	source := html10KBFixture()
	const maxOutput = 4096
	corrupt := []byte(`{ invalid json`)
	// Parse must fail but not panic.
	if _, err := ParseMutationManifest(corrupt); err == nil {
		t.Fatal("ParseMutationManifest should fail on corrupt JSON")
	}
	// Adaptive path via raw manifest must not panic and must stage a fallback
	// DAG split along semantic blocks — never one giant whole-file sub-task.
	dag, err := DecomposeWithRawManifest("delete @index.html", "index.html", source, "base-digest", maxOutput, corrupt)
	if err != nil {
		t.Fatalf("DecomposeWithRawManifest: %v", err)
	}
	if dag == nil {
		t.Fatal("fallback DAG is nil, want a semantic-block fallback DAG")
	}
	if len(dag.SubTasks) < 2 {
		t.Fatalf("fallback sub_task count = %d, want >= 2 semantic blocks (never one giant whole-file unit)", len(dag.SubTasks))
	}
	semanticCount := 0
	for _, st := range dag.SubTasks {
		if st.Kind == planner.SplitSemantic || st.Kind == planner.SplitBlock || st.Kind == planner.SplitAST {
			semanticCount++
		}
		if strings.HasPrefix(st.Description, "bounded fallback window") || strings.HasPrefix(st.Description, "lines ") {
			t.Errorf("fallback sub-task %s description %q is a raw line window over a structured document", st.ID, st.Description)
		}
		if st.EstimatedTokens <= 0 || st.EstimatedTokens > dag.Budget() {
			t.Fatalf("fallback sub-task %s estimate %d outside the strict ceiling (%d)", st.ID, st.EstimatedTokens, dag.Budget())
		}
	}
	if semanticCount == 0 {
		t.Fatalf("no semantic-block fallback sub-task; all are blind line windows")
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
	if len(dag2.SubTasks) < 2 {
		t.Fatalf("empty raw fallback count = %d, want >= 2 semantic blocks", len(dag2.SubTasks))
	}
	for _, st := range dag2.SubTasks {
		if st.EstimatedTokens <= 0 || st.EstimatedTokens > dag2.Budget() {
			t.Fatalf("empty raw fallback sub-task %s estimate %d outside the strict ceiling", st.ID, st.EstimatedTokens)
		}
	}
	if err := dag2.Validate(); err != nil {
		t.Fatalf("empty raw fallback DAG Validate: %v", err)
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

// strictPruneFixture builds a 136-line HTML document whose FIRST 78 lines are
// an untouched wrapper/anatomy zone (a <div id="shell"> container, header/nav
// and filler content) and whose lines 79-133 carry EXACTLY THREE mutated AST
// sections (<section id="a">, #b, #c). The wrapper is a single top-level Lea
// unit that merely CONTAINS the mutated nodes — the exact shape that lets a
// naive slice of the kept wrapper leak line-bound sub-tasks over lines 1-78.
func strictPruneFixture() []byte {
	var b strings.Builder
	b.WriteString("<!DOCTYPE html>\n")        // 1
	b.WriteString("<html>\n")                 // 2
	b.WriteString("<head>\n")                 // 3
	b.WriteString("<title>fixture</title>\n") // 4
	b.WriteString("</head>\n")                // 5
	b.WriteString("<body>\n")                 // 6
	b.WriteString("<div id=\"shell\">\n")     // 7
	b.WriteString("<header id=\"nav\">\n")    // 8
	b.WriteString("<h1>Nav</h1>\n")           // 9
	// Lines 10-78: unmapped filler inside the wrapper (no mutation targets them).
	for i := 10; i <= 78; i++ {
		b.WriteString("<p>unmapped filler line ")
		b.WriteString(strconv.Itoa(i))
		b.WriteString("</p>\n")
	}
	// Lines 79-133: the three AST mutation targets.
	b.WriteString("<section id=\"a\">\n") // 79
	for i := 80; i <= 96; i++ {
		b.WriteString("<p>a content ")
		b.WriteString(strconv.Itoa(i))
		b.WriteString("</p>\n")
	}
	b.WriteString("</section>\n") // 97
	b.WriteString("<section id=\"b\">\n")
	for i := 99; i <= 115; i++ {
		b.WriteString("<p>b content ")
		b.WriteString(strconv.Itoa(i))
		b.WriteString("</p>\n")
	}
	b.WriteString("</section>\n") // 116
	b.WriteString("<section id=\"c\">\n")
	for i := 118; i <= 132; i++ {
		b.WriteString("<p>c content ")
		b.WriteString(strconv.Itoa(i))
		b.WriteString("</p>\n")
	}
	b.WriteString("</section>\n") // 133
	b.WriteString("</div>\n")
	b.WriteString("</body>\n")
	b.WriteString("</html>\n")
	return []byte(b.String())
}

// firstLineOf returns the 1-indexed line on which the exact substring first
// occurs, or -1 when absent.
func firstLineOf(content, needle string) int {
	for i, line := range strings.Split(content, "\n") {
		if strings.Contains(line, needle) {
			return i + 1
		}
	}
	return -1
}

// TestDecomposition_StrictUnmappedLinePruning asserts the ZERO UNMAPPED LINE
// TASK invariant: a file whose lines 1-78 carry no manifest mutation and whose
// lines 79-133 carry three AST mutations must stage strictly <= 3 sub-tasks,
// all of them directly on the mutation surface. Lines 1-78 (and the wrapper
// that merely CONTAINS the mutations) are completely omitted — the AST
// fallback slicer must never convert the unmapped range into line-bound
// sub-tasks like "lines 1-4" or "lines 77-78".
func TestDecomposition_StrictUnmappedLinePruning(t *testing.T) {
	source := strictPruneFixture()
	lines := len(strings.Split(strings.TrimSuffix(string(source), "\n"), "\n"))
	if lines != 136 {
		t.Fatalf("fixture line count = %d, want 136 (lines 1-78 unmapped, 79-133 mutated)", lines)
	}
	// Sanity: the mutated sections open at exactly lines 79/98/117.
	for tag, wantLine := range map[string]int{"<section id=\"a\">": 79, "<section id=\"b\">": 98, "<section id=\"c\">": 117} {
		if line := firstLineOf(string(source), tag); line != wantLine {
			t.Fatalf("%s opens at line %d, want %d", tag, line, wantLine)
		}
	}

	manifest := &MutationManifest{
		TargetFile: "index.html",
		Intent:     "modify three sections",
		Mutations: []MutationSpec{
			{Selector: "#a", Action: "modify", EstimatedLines: 60},
			{Selector: "#b", Action: "modify", EstimatedLines: 60},
			{Selector: "#c", Action: "modify", EstimatedLines: 60},
		},
	}
	const maxOutput = 2048
	if s := EstimateMutationSurface(manifest, source); s <= maxOutput {
		t.Fatalf("surface %d must exceed max_output %d to force decomposition", s, maxOutput)
	}
	dag, err := AdaptiveDecompose("modify @index.html", "index.html", source, "digest", maxOutput, manifest)
	if err != nil {
		t.Fatalf("AdaptiveDecompose: %v", err)
	}
	if !dag.ManifestScoped {
		t.Fatal("plan must be manifest-scoped")
	}
	// INVARIANT: strictly <= 3 sub-tasks — the 3 mutation surfaces, nothing else.
	if len(dag.SubTasks) > 3 {
		t.Fatalf("sub-tasks = %d, want <= 3 (zero unmapped line windows staged)", len(dag.SubTasks))
	}
	// INVARIANT: lines 1-78 are COMPLETELY omitted — every staged region sits
	// inside the mutated span 79-133.
	seen := map[planner.Region]bool{}
	for _, st := range dag.SubTasks {
		if st.Region.StartLine < 79 || st.Region.EndLine > 133 {
			t.Fatalf("sub-task %s region %s covers unmapped lines — lines 1-78 must be omitted",
				st.ID, st.Region)
		}
		seen[st.Region] = true
	}
	// The three mutation sections must actually be staged.
	for _, want := range []planner.Region{
		{StartLine: 79, EndLine: 97},
		{StartLine: 98, EndLine: 116},
		{StartLine: 117, EndLine: 133},
	} {
		if !seen[want] {
			t.Fatalf("mutation section %s not staged; staged regions = %v", want, seen)
		}
	}
	// No sub-task may be a pure line-window over an untouched block.
	for _, st := range dag.SubTasks {
		if strings.HasPrefix(st.Description, "lines ") {
			t.Errorf("sub-task %s description %q is a raw line-range over unmapped lines", st.ID, st.Description)
		}
	}
	if err := dag.Validate(); err != nil {
		t.Fatalf("DAG Validate: %v", err)
	}
}

// TestIngestion_SanitizeMalformedTransportEnvelope asserts TRANSPORT LAYER
// RESILIENCE: a raw LLM frame wrapped in MALFORMED triple-backticks (an
// unterminated opening fence, a closer that lost a tick, or a payload whose
// quotes arrived transport-escaped) must be sanitized BEFORE envelope
// validation. Envelope normalization succeeds — Process must NEVER return the
// terminal "transport normalization produced a syntactically invalid payload"
// error for a wrapper that simply failed to close.
func TestIngestion_SanitizeMalformedTransportEnvelope(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{
			name: "unterminated opening fence",
			raw:  "```html\n<html>\n  <body>hello</body>\n</html>\n",
		},
		{
			name: "closing fence lost a backtick",
			raw:  "```html\n<html>\n  <body>hello</body>\n</html>\n``",
		},
		{
			name: "escaped quotes + json fence",
			raw:  "```json\n{\"mutations\": [{\"a\": 1}]}\n```\n",
		},
		{
			name: "prose around an unterminated fence",
			raw:  "Here is the fixed markup:\n```html\n<html><body>ok</body></html>\n",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			trace, err := ingestion.Process(c.raw)
			if err != nil {
				t.Fatalf("Process(%q) returned error %v — envelope normalization must succeed after transport sanitization", c.raw, err)
			}
			if errors.Is(err, ingestion.ErrSyntaxInvalid) {
				t.Fatalf("Process(%q) raised the terminal syntax-invalid error", c.raw)
			}
			if trace == nil {
				t.Fatal("Process returned a nil trace")
			}
			if trace.Classification == ingestion.ClassSyntaxInvalid {
				t.Fatalf("classification = %s, want a valid class after transport sanitization", trace.Classification)
			}
			if strings.Contains(trace.NormalizedPayload, "```") {
				t.Fatalf("normalized payload still carries a fence marker: %q", trace.NormalizedPayload)
			}
			if strings.TrimSpace(trace.NormalizedPayload) == "" {
				t.Fatalf("normalized payload emptied by sanitization: %q", trace.RawOutput)
			}
			// Raw output is preserved verbatim for forensics.
			if trace.RawOutput != c.raw {
				t.Fatalf("RawOutput not preserved: got %q want %q", trace.RawOutput, c.raw)
			}
		})
	}
}
