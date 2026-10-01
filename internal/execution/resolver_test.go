package execution

// ── PHASE 16: EVIDENCE-BOUND TARGET RESOLUTION ──────────────────────────────
//
// The defect these tests pin is a PROVIDER CALL against an unknown target. The
// original pipeline treated a missing target as a soft condition: the strategy
// selector filled it in, a fallback filled it in, and when nothing did the
// model was dispatched anyway. Whatever it returned was then written to a file
// the runtime had invented.
//
// So the assertions here are about ORDER and DISPATCHABILITY, not about
// correctness of a path: discovery must run, and the provider must NOT be
// reachable until a target is proven or the human names one.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// writeTree materialises a workspace from a map of relative path -> content and
// returns its root.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, body := range files {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// TestPhase16_UnboundTargetHaltsProviderCall is ACCEPTANCE TEST B.
//
// Input: prompt = "Redesign portfolio page", target unspecified, directory
// empty/unindexed.
//
// The required behaviour is that WorkspaceDiscovery RUNS, and that the outcome
// is not dispatchable. There is nothing to dispatch against, so the only honest
// terminal state is UNSUBSTANTIATED — not a guess, and not a provider call that
// might return something for the runtime to bind by content type.
func TestPhase16_UnboundTargetHaltsProviderCall(t *testing.T) {
	root := writeTree(t, nil) // empty workspace: no candidate can exist
	resolver := NewTargetResolver(root)

	res := resolver.Resolve(context.Background(), "Redesign portfolio page", nil)

	// Workspace discovery MUST have run — this is the whole point of I4.
	if !res.DiscoveryPerformed {
		t.Fatal("workspace discovery must precede any planner/provider invocation for an unresolved mutation target")
	}
	if res.Evidence == nil {
		t.Fatal("a discovery pass must leave structured WorkspaceEvidence behind")
	}
	if res.Evidence.MaxDepth != ResolverMaxDepth {
		t.Errorf("discovery depth bound = %d, want %d (D=3)", res.Evidence.MaxDepth, ResolverMaxDepth)
	}
	if res.Evidence.Root != root {
		t.Errorf("evidence root = %q, want %q", res.Evidence.Root, root)
	}
	// The deny rules must be reported, so an empty candidate set is never read
	// as "the workspace is empty" when it is merely "everything was excluded".
	if len(res.Evidence.IgnoreRules) == 0 {
		t.Error("evidence must report the ignore rules the scan applied")
	}

	// The prompt names NO file. "portfolio page" is a description, not a path,
	// and turning it into one is precisely the heuristic I7 forbids.
	if named := promptFileTokens("Redesign portfolio page"); len(named) != 0 {
		t.Errorf("a descriptive prompt must yield no file tokens, got %v", named)
	}

	if res.Dispatchable() {
		t.Fatalf("an unresolved target must never be dispatchable: %+v", res)
	}
	if res.Status != BindingUnresolved {
		t.Errorf("status = %q, want %q", res.Status, BindingUnresolved)
	}
	if res.Phase != PhaseUnsubstantiated {
		t.Errorf("phase = %q, want %q", res.Phase, PhaseUnsubstantiated)
	}
	if res.Binding != nil {
		t.Error("an unresolved result must carry no TargetBinding")
	}
	if res.Reason == "" {
		t.Error("an unresolved result must explain itself in runtime vocabulary")
	}
}

// TestPhase16_AmbiguousTargetHaltsLoop is ACCEPTANCE TEST D.
//
// Input: the directory contains ./index.html AND ./src/index.html, prompt =
// "Redesign index.html".
//
// The named target matches BOTH files. Nothing about that is hard — ranking the
// shallower one is a guess dressed as a heuristic — so the pipeline must halt
// in AWAITING_DISAMBIGUATION and hand the human the candidate list.
func TestPhase16_AmbiguousTargetHaltsLoop(t *testing.T) {
	root := writeTree(t, map[string]string{
		"index.html":     "<html><body>root</body></html>\n",
		"src/index.html": "<html><body>src</body></html>\n",
	})
	resolver := NewTargetResolver(root)

	res := resolver.Resolve(context.Background(), "Redesign index.html", nil)

	if res.Phase != PhaseAwaitingDisambiguation {
		t.Fatalf("phase = %q, want %q", res.Phase, PhaseAwaitingDisambiguation)
	}
	if res.Status != BindingAmbiguous {
		t.Errorf("status = %q, want %q", res.Status, BindingAmbiguous)
	}
	// No provider call and no mutation may be reachable from this state.
	if res.Dispatchable() {
		t.Fatal("an ambiguous target must never be dispatchable")
	}
	if res.Binding != nil {
		t.Error("an ambiguous result must not bind to the 'most likely' file")
	}
	// The human must be able to answer the question the runtime could not, so
	// the candidate list is mandatory and complete.
	if len(res.Candidates) != 2 {
		t.Fatalf("candidates = %v, want both matching files", res.Candidates)
	}
	found := map[string]bool{}
	for _, c := range res.Candidates {
		found[c] = true
	}
	for _, want := range []string{"index.html", "src/index.html"} {
		if !found[want] {
			t.Errorf("disambiguation candidates missing %q: %v", want, res.Candidates)
		}
	}
	if !res.DiscoveryPerformed {
		t.Error("ambiguity must be discovered from evidence, not assumed")
	}
}

// TestPhase16_ExplicitTargetIsAuthoritative: a stated path is evidence and is
// never overridden by a discovery result, even when discovery would have
// returned something else.
func TestPhase16_ExplicitTargetIsAuthoritative(t *testing.T) {
	root := writeTree(t, map[string]string{
		"index.html":     "<html>a</html>\n",
		"src/index.html": "<html>b</html>\n",
	})
	resolver := NewTargetResolver(root)

	res := resolver.Resolve(context.Background(), "Redesign it", []string{"@src/index.html"})

	if !res.Dispatchable() {
		t.Fatalf("an explicit, existing target must resolve: %+v", res)
	}
	if res.Binding.Path != "src/index.html" {
		t.Errorf("bound path = %q, want the EXPLICIT src/index.html", res.Binding.Path)
	}
	if !res.Binding.Explicit {
		t.Error("a stated target must be recorded as explicit evidence")
	}
	// The binding must carry the source digest: without it a later artifact
	// binding could not detect that the file moved under it.
	if res.Binding.SourceSHA256 != sourceSHA256([]byte("<html>b</html>\n")) {
		t.Errorf("binding digest = %q, want the digest of the target's current bytes", res.Binding.SourceSHA256)
	}
	if res.DiscoveryPerformed {
		t.Error("an explicit target must short-circuit discovery; scanning first would be wasted work on a path we already know")
	}
}

// TestPhase16_ExplicitTargetOutsideRootIsRefused: a stated path that escapes the
// workspace is not a weaker target, it is a different target. The resolver
// fails closed rather than silently substituting a file inside the root.
func TestPhase16_ExplicitTargetOutsideRootIsRefused(t *testing.T) {
	root := writeTree(t, map[string]string{"index.html": "<html>a</html>\n"})
	resolver := NewTargetResolver(root)

	for _, escape := range []string{"../outside.html", "../../etc/passwd"} {
		res := resolver.Resolve(context.Background(), "Redesign", []string{escape})
		if res.Dispatchable() {
			t.Errorf("target %q escaped the workspace and still resolved: %+v", escape, res)
		}
		if res.Phase != PhaseUnsubstantiated {
			t.Errorf("target %q phase = %q, want %q", escape, res.Phase, PhaseUnsubstantiated)
		}
	}
}

// TestPhase16_ExplicitNonexistentTargetIsNotInvented: a stated path that does
// not exist is a deterministic failure unless it is a well-known creation
// template. Creating "src/portfolio.tsx" because a prompt mentioned it would be
// the runtime inventing a file.
func TestPhase16_ExplicitNonexistentTargetIsNotInvented(t *testing.T) {
	root := writeTree(t, map[string]string{"index.html": "<html>a</html>\n"})
	resolver := NewTargetResolver(root)

	res := resolver.Resolve(context.Background(), "create it", []string{"src/portfolio.tsx"})
	if res.Dispatchable() {
		t.Fatalf("a non-existent non-template target must not be created: %+v", res)
	}
	if res.Phase != PhaseUnsubstantiated {
		t.Errorf("phase = %q, want %q", res.Phase, PhaseUnsubstantiated)
	}
}

// TestPhase16_AnUnnamedObjectiveNeverResolves is the rule the whole resolver
// exists to protect: the runtime does not get to choose the target.
//
// "The workspace happens to contain exactly one file" is a fact about the
// REPOSITORY, not a statement about the OBJECTIVE. Reading one as the other is
// the same class of error as reading an HTML body as "index.html" — a guess
// about meaning dressed up as a fact about structure. A temp directory that
// happens to hold one file is the purest possible demonstration: the runtime
// would be picking the target purely because nothing else was there.
func TestPhase16_AnUnnamedObjectiveNeverResolves(t *testing.T) {
	root := writeTree(t, map[string]string{"notes.md": "# notes\n"})
	resolver := NewTargetResolver(root)

	res := resolver.Resolve(context.Background(), "Tidy this up", nil)
	if res.Dispatchable() {
		t.Fatalf("an objective that names no file must never resolve: %+v", res)
	}
	// A candidate the human could choose IS offered — the halt is a question,
	// not a dead end.
	if res.Phase != PhaseAwaitingDisambiguation {
		t.Fatalf("phase = %q, want %q", res.Phase, PhaseAwaitingDisambiguation)
	}
	if len(res.Candidates) != 1 || res.Candidates[0] != "notes.md" {
		t.Errorf("the single available candidate must still be offered, got %v", res.Candidates)
	}
	if res.Binding != nil {
		t.Error("no binding may exist behind a disambiguation halt")
	}
}

// TestPhase16_MultipleCandidatesWithoutANamedFileIsAmbiguous: two files and a
// prompt that names neither is not a ranking problem, it is a missing fact.
func TestPhase16_MultipleCandidatesWithoutANamedFileIsAmbiguous(t *testing.T) {
	root := writeTree(t, map[string]string{
		"a.md": "a\n",
		"b.md": "b\n",
	})
	resolver := NewTargetResolver(root)

	res := resolver.Resolve(context.Background(), "Tidy this up", nil)
	if res.Phase != PhaseAwaitingDisambiguation {
		t.Fatalf("phase = %q, want %q", res.Phase, PhaseAwaitingDisambiguation)
	}
	if len(res.Candidates) != 2 {
		t.Errorf("candidates = %v, want both files", res.Candidates)
	}
}

// TestPhase16_DiscoveryRespectsIgnoreRules: ignored trees are not candidates.
// Otherwise "node_modules" would be offered as a plausible redesign target on
// any JavaScript workspace.
func TestPhase16_DiscoveryRespectsIgnoreRules(t *testing.T) {
	root := writeTree(t, map[string]string{
		"index.html":                     "<html>a</html>\n",
		"node_modules/left-pad/index.js": "module.exports = 1\n",
		".git/config":                    "[core]\n",
		"build/index.html":               "<html>generated</html>\n",
		// Depth 3 — the D=3 bound reaches this and stops.
		"src/deep/page.html": "<html>deep</html>\n",
		// Depth 4 — outside the bound, and proven out by
		// TestPhase16_DiscoveryIsDepthBounded.
		"src/deep/nested/page.html": "<html>too deep</html>\n",
	})
	resolver := NewTargetResolver(root)

	ev := resolver.Discover()
	if len(ev.Candidates) != 2 {
		t.Fatalf("candidates = %v, want exactly index.html and src/deep/page.html", ev.Candidates)
	}
	got := map[string]bool{}
	for _, c := range ev.Candidates {
		got[c] = true
	}
	if !got["index.html"] || !got["src/deep/page.html"] {
		t.Errorf("expected candidates not found in %v", ev.Candidates)
	}
	for _, unwanted := range []string{
		"node_modules/left-pad/index.js", ".git/config", "build/index.html", "src/deep/nested/page.html",
	} {
		if got[unwanted] {
			t.Errorf("excluded path %q leaked into the candidate set", unwanted)
		}
	}
}

// TestPhase16_DiscoveryHonoursGitignore: the workspace's own .gitignore narrows
// the evidence set. A generated directory the project already declared as
// ignorable must never be offered as a mutation target.
func TestPhase16_DiscoveryHonoursGitignore(t *testing.T) {
	root := writeTree(t, map[string]string{
		".gitignore":     "tmp/\n*.generated.ts\n",
		"index.html":     "<html>a</html>\n",
		"tmp/out.txt":    "scratch\n",
		"a.generated.ts": "export const x = 1\n",
	})
	resolver := NewTargetResolver(root)

	ev := resolver.Discover()
	got := map[string]bool{}
	for _, c := range ev.Candidates {
		got[c] = true
	}
	if got["tmp/out.txt"] {
		t.Error("a .gitignore'd directory must not appear as a candidate")
	}
	if got["a.generated.ts"] {
		t.Error("a .gitignore'd glob must not appear as a candidate")
	}
	if !got["index.html"] {
		t.Errorf("the real target must survive the ignore rules: %v", ev.Candidates)
	}
}

// TestPhase16_DiscoveryIsDepthBounded: depth 4 is outside the D=3 bound, so a
// deeply nested file is not evidence. The bound is what makes discovery cheap
// enough to run before every unresolved mutation.
func TestPhase16_DiscoveryIsDepthBounded(t *testing.T) {
	root := writeTree(t, map[string]string{
		"a/b/c/page.html": "<html>depth4</html>\n",
		"a/b/page.html":   "<html>depth3</html>\n",
	})
	resolver := NewTargetResolver(root)

	ev := resolver.Discover()
	if len(ev.Candidates) != 1 || ev.Candidates[0] != "a/b/page.html" {
		t.Fatalf("candidates = %v, want only a/b/page.html (depth 3)", ev.Candidates)
	}
}

// TestPhase16_DiscoveryIsDeterministic: the same workspace must yield the same
// candidate ORDER on every run, or an ambiguity report is unreproducible and
// the human cannot tell whether the workspace changed or the walk did.
func TestPhase16_DiscoveryIsDeterministic(t *testing.T) {
	root := writeTree(t, map[string]string{
		"z.html": "z\n", "a.html": "a\n", "m/b.html": "b\n", "m/a.html": "a\n",
	})
	resolver := NewTargetResolver(root)

	first := resolver.Discover().Candidates
	for i := 0; i < 5; i++ {
		next := NewTargetResolver(root).Discover().Candidates
		if len(next) != len(first) {
			t.Fatalf("candidate count changed between runs: %v vs %v", first, next)
		}
		for j := range first {
			if first[j] != next[j] {
				t.Fatalf("candidate order changed between runs at %d: %v vs %v", j, first, next)
			}
		}
	}
	for j := 1; j < len(first); j++ {
		if first[j-1] > first[j] {
			t.Errorf("candidates are not in deterministic order: %v", first)
			break
		}
	}
}

// TestPhase16_PromptNamedTargetResolvesByExactMatch: a prompt that DOES name a
// file resolves against it — by exact path or exact basename, never by fuzzy
// relevance.
func TestPhase16_PromptNamedTargetResolvesByExactMatch(t *testing.T) {
	root := writeTree(t, map[string]string{
		"index.html":     "<html>a</html>\n",
		"src/index.html": "<html>b</html>\n",
		"README.md":      "# readme\n",
	})
	resolver := NewTargetResolver(root)

	// A full relative path is strictly stronger evidence than a basename.
	res := resolver.Resolve(context.Background(), "Redesign src/index.html please", nil)
	if !res.Dispatchable() {
		t.Fatalf("an exact path match must resolve: %+v", res)
	}
	if res.Binding.Path != "src/index.html" {
		t.Errorf("bound path = %q, want src/index.html", res.Binding.Path)
	}

	// README.md has no sibling, so the basename match is unambiguous.
	res = resolver.Resolve(context.Background(), "Update @README.md", nil)
	if !res.Dispatchable() {
		t.Fatalf("an unambiguous basename match must resolve: %+v", res)
	}
	if res.Binding.Path != "README.md" {
		t.Errorf("bound path = %q, want README.md", res.Binding.Path)
	}
}

// TestPhase16_FullPathMatchDisambiguatesABasenameCollision: when the prompt
// names a full path, the bare basename elsewhere in the tree is not a candidate
// for it. A path statement is more specific than a filename statement, and the
// two are not in conflict. This is the converse of
// TestPhase16_AmbiguousTargetHaltsLoop: a BARE name is ambiguous, a PATH is not.
func TestPhase16_FullPathMatchDisambiguatesABasenameCollision(t *testing.T) {
	root := writeTree(t, map[string]string{
		"index.html":     "<html>a</html>\n",
		"src/index.html": "<html>b</html>\n",
	})
	resolver := NewTargetResolver(root)

	res := resolver.Resolve(context.Background(), "edit src/index.html", nil)
	if !res.Dispatchable() {
		t.Fatalf("a full-path statement must resolve despite a basename collision: %+v", res)
	}
	if res.Binding.Path != "src/index.html" {
		t.Errorf("bound path = %q, want src/index.html", res.Binding.Path)
	}
}

// TestPhase16_PromptFileTokensAreFilesOnly pins the token extractor itself. A
// free word that merely resembles a concept is not a file, and admitting one
// would let "portfolio" become "portfolio/index.html" by similarity — which is
// the content-type heuristic in a different costume.
func TestPhase16_PromptFileTokensAreFilesOnly(t *testing.T) {
	cases := []struct {
		prompt string
		want   []string
	}{
		{"Redesign index.html", []string{"index.html"}},
		{"Redesign @src/app.tsx", []string{"src/app.tsx"}},
		{`update "styles/main.css"`, []string{"styles/main.css"}},
		{"Redesign portfolio page", nil},
		{"Make the homepage look better", nil},
		{"Refactor internal/auth/token.go and add a test", []string{"internal/auth/token.go"}},
	}
	for _, tc := range cases {
		got := promptFileTokens(tc.prompt)
		if len(got) != len(tc.want) {
			t.Errorf("promptFileTokens(%q) = %v, want %v", tc.prompt, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("promptFileTokens(%q) = %v, want %v", tc.prompt, got, tc.want)
				break
			}
		}
	}
}

// TestPhase16_ResolverIsNilSafe: the resolver is consulted on the hot path of
// every execution, including headless ones with no workspace bound. A nil
// receiver must refuse, not panic.
func TestPhase16_ResolverIsNilSafe(t *testing.T) {
	var resolver *TargetResolver
	res := resolver.Resolve(context.Background(), "anything", nil)
	if res.Dispatchable() {
		t.Error("a nil resolver must never report a dispatchable target")
	}
	if resolver.Root() != "" {
		t.Error("a nil resolver has no root")
	}
}

// TestPhase16_CancelledResolutionFailsClosed: a cancelled resolution is not a
// resolved one. Treating ctx cancellation as "no target found" is correct;
// treating it as "use the previous target" would not be.
func TestPhase16_CancelledResolutionFailsClosed(t *testing.T) {
	root := writeTree(t, map[string]string{"index.html": "<html>a</html>\n"})
	resolver := NewTargetResolver(root)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res := resolver.Resolve(ctx, "Redesign index.html", nil)
	if res.Dispatchable() {
		t.Error("a cancelled resolution must not be dispatchable")
	}
	if res.Phase != PhaseUnsubstantiated {
		t.Errorf("phase = %q, want %q", res.Phase, PhaseUnsubstantiated)
	}
}

// TestPhase16_PhaseVocabularyIsClosed: the three binding phases are the whole
// vocabulary. A new state must be added here deliberately, not invented at a
// call site.
func TestPhase16_PhaseVocabularyIsClosed(t *testing.T) {
	closed := map[TargetBindingPhase]bool{
		PhaseTargetBindingResolved:  true,
		PhaseAwaitingDisambiguation: true,
		PhaseUnsubstantiated:        true,
	}
	for _, phase := range []TargetBindingPhase{
		PhaseTargetBindingResolved, PhaseAwaitingDisambiguation, PhaseUnsubstantiated,
	} {
		if !closed[phase] {
			t.Errorf("phase %q is outside the closed binding vocabulary", phase)
		}
	}
	if string(PhaseAwaitingDisambiguation) != "AWAITING_DISAMBIGUATION" {
		t.Errorf("AWAITING_DISAMBIGUATION literal drifted: %q", PhaseAwaitingDisambiguation)
	}
	if string(PhaseUnsubstantiated) != "UNSUBSTANTIATED" {
		t.Errorf("UNSUBSTANTIATED literal drifted: %q", PhaseUnsubstantiated)
	}
}
