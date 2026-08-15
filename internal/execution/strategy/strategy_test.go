package strategy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeWorkspace implements Workspace over a temp directory for tests.
type fakeWorkspace struct {
	root string
}

func newFakeWorkspace(t *testing.T, files map[string]string) *fakeWorkspace {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return &fakeWorkspace{root: root}
}

func (w *fakeWorkspace) Root() string { return w.root }

func (w *fakeWorkspace) Exists(path string) bool {
	info, err := os.Stat(filepath.Join(w.root, path))
	return err == nil && !info.IsDir()
}

func (w *fakeWorkspace) ResolveFuzzy(name string, max int) []string {
	var out []string
	_ = filepath.WalkDir(w.root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return filepath.SkipDir
		}
		if d.IsDir() {
			return nil
		}
		if len(out) >= max {
			return filepath.SkipAll
		}
		if strings.EqualFold(d.Name(), name) {
			rel, _ := filepath.Rel(w.root, path)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	return out
}

func (w *fakeWorkspace) Size(path string) int64 {
	info, err := os.Stat(filepath.Join(w.root, path))
	if err != nil {
		return -1
	}
	return info.Size()
}

func (w *fakeWorkspace) Read(path string) string {
	b, err := os.ReadFile(filepath.Join(w.root, path))
	if err != nil {
		return ""
	}
	return string(b)
}

func deps(t *testing.T, files map[string]string) Deps {
	w := newFakeWorkspace(t, files)
	return Deps{Root: w.root, Workspace: w}
}

func TestSelectSimplePromptExplicitTarget(t *testing.T) {
	d := deps(t, map[string]string{"index.html": "<html><body>hello</body></html>"})
	p := Select("fix extra contents in @index.html", d)

	if p.Strategy != TargetedMutation {
		t.Fatalf("Strategy = %s, want targeted_mutation (reason: %s)", p.Strategy, p.StrategyReason)
	}
	if !p.ModelRequired {
		t.Fatal("ModelRequired = false, want true")
	}
	if got := p.TargetCount(); got != 1 {
		t.Fatalf("TargetCount = %d, want 1", got)
	}
	tgt := p.Targets[0]
	if tgt.Status != TargetExplicit || !tgt.Exists || tgt.Resolved != "index.html" {
		t.Fatalf("target = %+v, want explicit resolved index.html", tgt)
	}
	if p.Artifact.Kind != "replace_block" {
		t.Fatalf("Artifact.Kind = %s, want replace_block", p.Artifact.Kind)
	}
	if p.MaxOutputTokens == 0 {
		t.Fatal("MaxOutputTokens = 0, want a bounded budget")
	}
	if !p.HasContext(ContextTargetContent) {
		t.Fatal("profile must require target content context")
	}
	if !p.HasContext(ContextArtifactContract) {
		t.Fatal("profile must require artifact contract context")
	}
	if p.Escalation {
		t.Fatalf("Escalation = true (reason: %s), want false for a simple task", p.EscalationReason)
	}
}

func TestSelectDeterministicTemplateCreate(t *testing.T) {
	d := deps(t, nil)
	p := Select("create a MIT LICENSE file", d)

	if p.Strategy != DirectDeterministic {
		t.Fatalf("Strategy = %s, want direct_deterministic (reason: %s)", p.Strategy, p.StrategyReason)
	}
	if p.ModelRequired {
		t.Fatal("ModelRequired = true, want false (zero model invocations)")
	}
	if !p.Deterministic {
		t.Fatal("Deterministic = false, want true")
	}
}

func TestSelectHumanClarificationUnresolvedTarget(t *testing.T) {
	d := deps(t, map[string]string{"index.html": "x"})
	p := Select("remove the footer from @missing.html", d)

	if p.Strategy != HumanClarification {
		t.Fatalf("Strategy = %s, want human_clarification (reason: %s)", p.Strategy, p.StrategyReason)
	}
	if p.ModelRequired {
		t.Fatal("ModelRequired = true, want false — no model call for an unresolved target")
	}
	if !p.Escalation {
		t.Fatal("Escalation = false, want true (human clarification)")
	}
}

func TestSelectFuzzyResolvedCaseInsensitive(t *testing.T) {
	d := deps(t, map[string]string{"README.md": "# readme"})
	p := Select("fix the typo in @readme.md", d)

	if p.Strategy != TargetedMutation {
		t.Fatalf("Strategy = %s, want targeted_mutation (reason: %s)", p.Strategy, p.StrategyReason)
	}
	if len(p.Targets) == 0 || p.Targets[0].Resolved != "README.md" || !p.Targets[0].Exists {
		t.Fatalf("targets = %+v, want canonicalized existing README.md", p.Targets)
	}
}

func TestSelectAmbiguousTarget(t *testing.T) {
	d := deps(t, map[string]string{
		"src/index.html":    "a",
		"public/index.html": "b",
	})
	p := Select("fix the layout in @index.html", d)

	if p.Strategy != HumanClarification {
		t.Fatalf("Strategy = %s, want human_clarification (ambiguous: %s)", p.Strategy, p.StrategyReason)
	}
	if p.ModelRequired {
		t.Fatal("ModelRequired = true, want false — ambiguity stops before the model")
	}
}

func TestSelectRepositoryInvestigation(t *testing.T) {
	d := deps(t, map[string]string{"main.go": "package main"})
	p := Select("why is the build failing", d)

	if p.Strategy != RepositoryInvestigation {
		t.Fatalf("Strategy = %s, want repository_investigation (reason: %s)", p.Strategy, p.StrategyReason)
	}
	if p.Artifact.Kind != "investigation" {
		t.Fatalf("Artifact.Kind = %s, want investigation", p.Artifact.Kind)
	}
}

func TestSelectMultiFilePlanningNoTarget(t *testing.T) {
	d := deps(t, map[string]string{"main.go": "package main"})
	p := Select("add a rate limiter to the API", d)

	if p.Strategy != MultiFilePlanning {
		t.Fatalf("Strategy = %s, want multi_file_planning (reason: %s)", p.Strategy, p.StrategyReason)
	}
	if p.Artifact.Kind != "plan" {
		t.Fatalf("Artifact.Kind = %s, want plan", p.Artifact.Kind)
	}
}

func TestSelectTargetedReasoningExplicitTarget(t *testing.T) {
	d := deps(t, map[string]string{"auth.go": "package auth"})
	p := Select("explain how the login flow works in @auth.go", d)

	if p.Strategy != TargetedReasoning {
		t.Fatalf("Strategy = %s, want targeted_reasoning (reason: %s)", p.Strategy, p.StrategyReason)
	}
	if p.Artifact.Kind != "explanation" {
		t.Fatalf("Artifact.Kind = %s, want explanation", p.Artifact.Kind)
	}
}

func TestSelectInferredBareFilename(t *testing.T) {
	d := deps(t, map[string]string{"LICENSE": "Copyright 2023"})
	p := Select("update the LICENSE year to 2026", d)

	if p.Strategy != TargetedMutation {
		t.Fatalf("Strategy = %s, want targeted_mutation (reason: %s)", p.Strategy, p.StrategyReason)
	}
	if len(p.Targets) == 0 {
		t.Fatal("no inferred target found")
	}
	if p.Targets[0].Status != TargetInferred && p.Targets[0].Status != TargetExplicit {
		t.Fatalf("target status = %s, want inferred", p.Targets[0].Status)
	}
}

func TestSelectMultiFileExplicitTargets(t *testing.T) {
	d := deps(t, map[string]string{"a.html": "a", "b.css": "b"})
	p := Select("restyle the page using @a.html and @b.css", d)

	if p.Strategy != TargetedMutation {
		t.Fatalf("Strategy = %s, want targeted_mutation (reason: %s)", p.Strategy, p.StrategyReason)
	}
	if got := p.FileCount(); got != 2 {
		t.Fatalf("FileCount = %d, want 2", got)
	}
	if p.Complexity.Level == ComplexityLow {
		t.Fatalf("complexity = low for a 2-file change, want medium+")
	}
}

func TestSelectNewFileCreationExplicitTarget(t *testing.T) {
	d := deps(t, nil)
	p := Select("create @docs/architecture.md with a summary of the design", d)

	if p.Strategy != TargetedMutation {
		t.Fatalf("Strategy = %s, want targeted_mutation (reason: %s)", p.Strategy, p.StrategyReason)
	}
	if p.Artifact.Kind != "create_file" {
		t.Fatalf("Artifact.Kind = %s, want create_file", p.Artifact.Kind)
	}
	if len(p.Targets) == 0 || p.Targets[0].Exists {
		t.Fatalf("targets = %+v, want a missing-but-created target", p.Targets)
	}
}

func TestSelectComplexityExecutionFactors(t *testing.T) {
	// Simple single-file content change → low.
	low := Assess(ComplexityInputs{Operation: OperationContent, TargetCount: 1, FileCount: 1,
		ExplicitTargets: true, VerificationDepth: 1})
	if low.Level != ComplexityLow {
		t.Fatalf("simple content change = %s, want low (score=%d)", low.Level, low.Score)
	}

	// Rename across many files → high.
	high := Assess(ComplexityInputs{Operation: OperationRefactor, TargetCount: 1, FileCount: 20,
		DependencyCount: 3, CrossFileCoupling: true, VerificationDepth: 3, RepositoryScope: true})
	if high.Level != ComplexityHigh {
		t.Fatalf("20-file rename = %s, want high (score=%d)", high.Level, high.Score)
	}

	// CSS + component → medium.
	med := Assess(ComplexityInputs{Operation: OperationContent, TargetCount: 2, FileCount: 2,
		CrossFileCoupling: true, VerificationDepth: 2})
	if med.Level != ComplexityMedium {
		t.Fatalf("css+component = %s, want medium (score=%d)", med.Level, med.Score)
	}
}

func TestComplexityReasonsAuditable(t *testing.T) {
	c := Assess(ComplexityInputs{Operation: OperationRefactor, TargetCount: 1, FileCount: 5,
		DependencyCount: 2, VerificationDepth: 3})
	if len(c.Factors) == 0 {
		t.Fatal("no factors recorded")
	}
	for _, f := range c.Factors {
		if f.Reason == "" {
			t.Fatalf("factor %s has no reason", f.Name)
		}
	}
}

func TestInvocationContract(t *testing.T) {
	d := deps(t, map[string]string{"index.html": "<p>hi</p>"})
	p := Select("remove the duplicate paragraph in @index.html", d)

	ic := For(p, 1)
	if ic.Number != 1 {
		t.Fatalf("Number = %d, want 1", ic.Number)
	}
	if ic.Reason == "" || ic.Decision == "" {
		t.Fatal("contract must name the reason and the decision")
	}
	if ic.Artifact.Kind != "replace_block" {
		t.Fatalf("Artifact.Kind = %s, want replace_block", ic.Artifact.Kind)
	}
	if ic.MaxOutput <= 0 {
		t.Fatal("MaxOutput must be a positive bounded budget")
	}
	if len(ic.Excluded) == 0 {
		t.Fatal("contract must list intentionally excluded context")
	}
	if !strings.Contains(ic.Success, "applies cleanly") {
		t.Fatalf("Success = %q, want an apply criterion", ic.Success)
	}
}

func TestInvocationContractNoModel(t *testing.T) {
	d := deps(t, nil)
	p := Select("create a .gitignore file", d)
	ic := For(p, 1)
	if ic.Number != 0 {
		t.Fatalf("Number = %d, want 0 for a no-model strategy", ic.Number)
	}
}

func TestContextEnvelopeMinimumSufficient(t *testing.T) {
	d := deps(t, map[string]string{"index.html": "<html><body>hi</body></html>"})
	p := Select("fix extra contents in @index.html", d)
	compiler := NewCompiler(d)
	env := compiler.Compile(p)

	if !env.Has(ContextUserIntent) {
		t.Fatal("envelope must carry the user intent")
	}
	if !env.Has(ContextExplicitTargets) {
		t.Fatal("envelope must carry the explicit targets")
	}
	if !env.Has(ContextTargetContent) {
		t.Fatal("envelope must carry the target content")
	}
	if env.Has(ContextDependencyEvidence) {
		t.Fatal("envelope must NOT carry dependency evidence for a single-file mutation")
	}
	if env.Has(ContextRepositoryConstraints) {
		t.Fatal("envelope must NOT carry repository constraints for a single-file mutation")
	}
	for _, it := range env.Items {
		if it.Owner == "" || it.Source == "" || it.ReasonForInclusion == "" {
			t.Fatalf("context item %s missing ownership/source/reason: %+v", it.Kind, it)
		}
	}
}

func TestEscalatorExpandsOnEvidence(t *testing.T) {
	d := deps(t, map[string]string{"a.css": "body{}"})
	p := Select("fix extra contents in @index.html", d)
	env := NewCompiler(d).Compile(p)

	esc := NewEscalator(env)
	esc.Expand("model reasoning revealed a dependency", ContextItem{
		Kind: ContextDependencyEvidence, Owner: "engine", Source: SourceFileGraph,
		Relevance: "target depends on a.css", Authority: "structural graph",
		ReasonForInclusion: "the mutation ripples to a coupled file",
	})

	if !esc.Envelope().Expanded {
		t.Fatal("envelope must be marked expanded")
	}
	if esc.Envelope().ExpansionReason == "" {
		t.Fatal("expansion reason must be recorded")
	}
	if !esc.Envelope().Has(ContextDependencyEvidence) {
		t.Fatal("envelope must carry the added dependency evidence")
	}
	// Escalating the same kind again must not duplicate.
	esc.Expand("second trigger", ContextItem{Kind: ContextDependencyEvidence})
	n := 0
	for _, it := range esc.Envelope().Items {
		if it.Kind == ContextDependencyEvidence {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("dependency evidence appears %d times, want 1 (no duplication)", n)
	}
}

func TestContextEnvelopeLargeFileBounded(t *testing.T) {
	big := strings.Repeat("x", 200*1024)
	d := deps(t, map[string]string{"big.go": big})
	p := Select("fix the function in @big.go", d)

	if p.Strategy != TargetedMutation {
		t.Fatalf("Strategy = %s, want targeted_mutation", p.Strategy)
	}
	env := NewCompiler(d).Compile(p)
	item, ok := env.ItemOf(ContextTargetContent)
	if !ok {
		t.Fatal("target content context missing")
	}
	// A 200KB file must record its size, never inline the full bytes — the
	// provider path supplies the located block.
	if strings.Contains(item.Content, "xxxx") {
		t.Fatalf("large target content was dumped into the account (%d bytes), want size-only", len(item.Content))
	}
	if !strings.Contains(item.Content, "bytes") {
		t.Fatalf("large target content = %q, want a size marker", item.Content)
	}
}

func TestSelectNeverScansRepositoryForTargeted(t *testing.T) {
	d := deps(t, map[string]string{"index.html": "<p>hi</p>", "other.go": "package other"})
	p := Select("remove the extra paragraph in @index.html", d)
	if p.Strategy != TargetedMutation {
		t.Fatalf("Strategy = %s, want targeted_mutation", p.Strategy)
	}
	// The targeted strategy never demands repository-wide evidence.
	if p.HasContext(ContextRepositoryConstraints) {
		t.Fatal("targeted mutation must not require repository constraints")
	}
	if p.HasContext(ContextDependencyEvidence) {
		t.Fatal("targeted mutation must not require dependency evidence")
	}
	if p.HasContext(ContextRelevantHistory) {
		t.Fatal("targeted mutation must not require conversation history")
	}
}
