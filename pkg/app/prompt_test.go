package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/pkg/capability"
	"github.com/PizenLabs/izen/pkg/knowledge"
	"github.com/PizenLabs/izen/pkg/op"
)

// resolvedCaps resolves the portfolio capability set used by prompt tests.
func resolvedCaps(t *testing.T) []capability.Capability {
	t.Helper()
	reg := capability.NewRegistry()
	if err := capability.RegisterDefaults(reg); err != nil {
		t.Fatalf("RegisterDefaults: %v", err)
	}
	caps, err := reg.Resolve(capability.CapPortfolioWebsite, capability.CapSemanticHTML)
	if err != nil {
		t.Fatalf("resolve capabilities: %v", err)
	}
	return caps
}

// scannedBuilder returns a PromptBuilder over a knowledge graph scanned on
// root. When kg is nil a fresh graph scanning root is created.
func scannedBuilder(t *testing.T, root string, kg *knowledge.KnowledgeGraph) *PromptBuilder {
	t.Helper()
	if kg == nil {
		kg = knowledge.NewKnowledgeGraph()
		kg.Ensure(root)
	}
	return NewPromptBuilder(op.NewStrategyRegistry(), kg,
		WithBuilderRoot(root),
		WithBuilderModelTier("full"))
}

func writeFixture(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPromptBuilderRewriteStripsObsoleteContents(t *testing.T) {
	root := t.TempDir()
	const obsolete = "obsolete secret layout <div id=\"old\">LEGACY</div>"
	writeFixture(t, root, "index.html", obsolete)

	builder := scannedBuilder(t, root, nil)
	system := builder.BuildSystem(op.PolicyRewrite, resolvedCaps(t), []string{"index.html"})

	if !strings.Contains(system, "index.html") {
		t.Fatalf("rewrite context must name the target path, got:\n%s", system)
	}
	if !strings.Contains(system, "CONTEXT POLICY: REWRITE") {
		t.Fatalf("rewrite context block missing, got:\n%s", system)
	}
	if !strings.Contains(system, RewriteDirective) {
		t.Fatalf("rewrite directive missing, got:\n%s", system)
	}
	if strings.Contains(system, "LEGACY") || strings.Contains(system, obsolete) {
		t.Fatalf("obsolete file contents leaked into the prompt context:\n%s", system)
	}
	if strings.Contains(system, "<<<FILE") {
		t.Fatalf("rewrite must not inject baseline boundaries:\n%s", system)
	}

	user := builder.BuildUser(op.PolicyRewrite, "redesign my portfolio", []string{"index.html"})
	if !strings.Contains(strings.ToLower(user), "do not preserve existing code") {
		t.Fatalf("rewrite user prompt must forbid preserving existing code, got:\n%s", user)
	}
}

func TestPromptBuilderRewriteOmitsContentsEvenWhenTargetsAbsent(t *testing.T) {
	root := t.TempDir()
	const obsolete = "TOP SECRET OLD IMPLEMENTATION"
	writeFixture(t, root, "src/app.ts", obsolete)

	builder := scannedBuilder(t, root, nil)
	system := builder.BuildSystem(op.PolicyRewrite, resolvedCaps(t), nil)

	if !strings.Contains(system, "src/app.ts") {
		t.Fatalf("knowledge-graph paths must be listed in rewrite context, got:\n%s", system)
	}
	if strings.Contains(system, obsolete) {
		t.Fatalf("obsolete content from knowledge graph leaked into the prompt:\n%s", system)
	}
}

func TestPromptBuilderEditInjectsBaselineWithBoundaries(t *testing.T) {
	root := t.TempDir()
	const baseline = "package main\n\nfunc legacy() { println(\"keep me\") }\n"
	writeFixture(t, root, "main.go", baseline)

	builder := scannedBuilder(t, root, nil)
	system := builder.BuildSystem(op.PolicyEdit, resolvedCaps(t), []string{"main.go"})

	if !strings.Contains(system, "CONTEXT POLICY: EDIT") {
		t.Fatalf("edit context block missing, got:\n%s", system)
	}
	if !strings.Contains(system, "<<<FILE main.go>>>") || !strings.Contains(system, "</FILE main.go>>>") {
		t.Fatalf("edit context must wrap baseline in explicit boundary markers, got:\n%s", system)
	}
	if !strings.Contains(system, "legacy") {
		t.Fatalf("edit context must inject baseline code, got:\n%s", system)
	}
	if strings.Contains(system, RewriteDirective) {
		t.Fatalf("edit context must not carry the rewrite directive:\n%s", system)
	}
}

func TestPromptBuilderPatchInjectsBaselineSnippets(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "main.go", "package main\n\nfunc broken() {}\n")

	builder := scannedBuilder(t, root, nil)
	system := builder.BuildSystem(op.PolicyPatch, resolvedCaps(t), []string{"main.go"})

	if !strings.Contains(system, "CONTEXT POLICY: PATCH") {
		t.Fatalf("patch context block missing, got:\n%s", system)
	}
	if !strings.Contains(system, "<<<FILE main.go>>>") {
		t.Fatalf("patch context must inject bounded diff snippets, got:\n%s", system)
	}
	if !strings.Contains(system, "error trace") {
		t.Fatalf("patch context must reference the error trace, got:\n%s", system)
	}
}

func TestPromptBuilderGenerateInjectsNoBaseline(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "main.go", "package main\nfunc legacy() {}\n")

	builder := scannedBuilder(t, root, nil)
	system := builder.BuildSystem(op.PolicyGenerate, resolvedCaps(t), nil)

	if strings.Contains(system, "<<<FILE") {
		t.Fatalf("generate context must not inject baseline boundaries:\n%s", system)
	}
	if strings.Contains(system, "CONTEXT POLICY:") {
		t.Fatalf("generate context must not carry a policy block:\n%s", system)
	}
	if strings.Contains(system, "legacy") {
		t.Fatalf("generate context must not leak baseline code:\n%s", system)
	}
}

func TestPromptBuilderCompilePolicy(t *testing.T) {
	builder := NewPromptBuilder(op.NewStrategyRegistry(), nil)
	cases := []struct {
		semantics op.OperationSemantics
		want      op.ContextPolicy
	}{
		{op.SemanticCreateProject, op.PolicyGenerate},
		{op.SemanticRewriteProject, op.PolicyRewrite},
		{op.SemanticAddFeature, op.PolicyEdit},
		{op.SemanticRefactor, op.PolicyEdit},
		{op.SemanticFixBug, op.PolicyPatch},
		{op.OperationSemantics("unknown"), op.DefaultContextPolicy},
	}
	for _, c := range cases {
		if got := builder.CompilePolicy(c.semantics); got != c.want {
			t.Errorf("CompilePolicy(%s) = %s, want %s", c.semantics, got, c.want)
		}
	}
}

func TestPromptBuilderNilReceiverFallsBackToDefault(t *testing.T) {
	var nilBuilder *PromptBuilder
	if got := nilBuilder.CompilePolicy(op.SemanticRewriteProject); got != op.DefaultContextPolicy {
		t.Fatalf("nil builder must fall back to the default policy, got %s", got)
	}
}

func TestPromptBuilderBaselineEscapingPathsRefused(t *testing.T) {
	root := t.TempDir()
	sentinel := "SENTINEL-MUST-NOT-LEAK"
	builder := NewPromptBuilder(op.NewStrategyRegistry(), nil,
		WithBuilderRoot(root),
		WithBaselineReader(func(rel string) ([]byte, error) { return []byte(sentinel), nil }))

	system := builder.BuildSystem(op.PolicyEdit, resolvedCaps(t), []string{"../evil.go"})
	if strings.Contains(system, sentinel) {
		t.Fatalf("escaping baseline path was read and leaked into the prompt:\n%s", system)
	}
}

func TestPromptBuilderContextPathsDedupesAndNormalises(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "a.go", "package a")
	writeFixture(t, root, "b.go", "package b")

	builder := scannedBuilder(t, root, nil)
	paths := builder.contextPaths([]string{"a.go", "./a.go", "b.go"})

	if len(paths) != 2 || paths[0] != "a.go" || paths[1] != "b.go" {
		t.Fatalf("contextPaths = %v, want [a.go b.go]", paths)
	}
}
