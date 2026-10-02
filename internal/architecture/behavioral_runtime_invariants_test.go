package architecture

import (
	"fmt"
	"go/ast"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// ── Behavioral capability substrate: architectural locks ────────────────────
//
// The behavioral substrate added three things to the runtime: an executable
// capability layer, a loop that repairs from observed evidence, and a gate that
// requires behavioral proof before an objective may complete.
//
// Each of those can silently become a parallel architecture, so each gets a
// structural lock here. A lock is stronger than a code review because it fails
// the build when the property breaks — including when a future change breaks it
// innocently.

// localImports returns the import paths of one parsed file.
func localImports(f *ast.File) []string {
	var out []string
	for _, imp := range f.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		out = append(out, path)
	}
	return out
}

// funcSource renders the source text of one declaration so a lock can assert on
// its shape without re-deriving an AST walk.
func funcSource(t *testing.T, fset *token.FileSet, node ast.Node) string {
	t.Helper()
	start := fset.Position(node.Pos())
	end := fset.Position(node.End())
	data, err := readFileRange(start.Filename, start.Offset, end.Offset)
	if err != nil {
		t.Fatalf("architecture: cannot read source of %s: %v", start.Filename, err)
	}
	return string(data)
}

// TestBehaviorCapabilityLayerHoldsNoAuthority is the load-bearing lock.
//
// The capability package is a PRIMITIVE layer: it executes real operations and
// returns real evidence, and it decides nothing. If it ever grows authority —
// its own authorization, its own mutation path, its own process spawn — then
// "the Control Plane authorized this" and "the capability layer thought it was
// fine" become indistinguishable, which is the exact confusion the capability
// model exists to prevent.
func TestBehaviorCapabilityLayerHoldsNoAuthority(t *testing.T) {
	root := repoRoot(t)
	for _, rel := range goFilesUnder(root) {
		if !strings.HasPrefix(rel, "internal/execution/capability/") {
			continue
		}
		f, fset := parseFile(t, filepath.Join(root, rel))

		// (a) No direct workspace mutation. Every byte written must go through the
		// mutation authority, not through a capability that "knows it is allowed".
		for _, banned := range []string{"os.WriteFile", "os.Create", "os.Remove", "os.Rename", "os.OpenFile", "os.MkdirAll"} {
			if sites := findCalls(f, fset, map[string]bool{banned: true}); len(sites) > 0 {
				t.Errorf("architecture: capability layer performs a direct workspace mutation via %s at %s:%d — every write must pass the mutation authority",
					banned, rel, sites[0].line)
			}
		}

		// (b) No process execution. Commands cross the authorized shell port, so a
		// capability that spawned its own process would be a second, quieter
		// authorization path.
		for _, banned := range []string{"exec.Command", "exec.CommandContext", "exec.LookPath"} {
			if sites := findCalls(f, fset, map[string]bool{banned: true}); len(sites) > 0 {
				t.Errorf("architecture: capability layer executes a process via %s at %s:%d — commands must cross the authorized shell port",
					banned, rel, sites[0].line)
			}
		}
	}
}

// TestBehaviorCapabilityLayerImportsNoAuthority pins the dependency direction. A
// primitive layer that imports the authority can grow a back door into it.
func TestBehaviorCapabilityLayerImportsNoAuthority(t *testing.T) {
	root := repoRoot(t)
	forbidden := []string{
		moduleImport("internal/runtime/substrate"),
		moduleImport("internal/runtime/executor"),
		moduleImport("internal/runtime/autonomy"),
		moduleImport("internal/core/authorization"),
		moduleImport("internal/execution"),
	}
	for _, rel := range goFilesUnder(root) {
		if !strings.HasPrefix(rel, "internal/execution/capability/") {
			continue
		}
		f, _ := parseFile(t, filepath.Join(root, rel))
		for _, imp := range localImports(f) {
			for _, banned := range forbidden {
				if imp == banned {
					t.Errorf("architecture: capability layer imports authority package %s at %s — the dependency direction must be authority → primitives",
						banned, rel)
				}
			}
		}
	}
}

// TestBehaviorRepairUsesTheSingleMutationAuthority pins that an observed-defect
// repair is applied through the ONE mutation authority. A behavioral repair that
// wrote files directly would be the most consequential duplicate authority
// possible: it would act on model output.
func TestBehaviorRepairUsesTheSingleMutationAuthority(t *testing.T) {
	root := repoRoot(t)
	loopFile := filepath.Join(root, "internal", "execution", "behavior_loop.go")
	f, _ := parseFile(t, loopFile)

	apply := findFuncDecl(f, "apply")
	if apply == nil {
		t.Fatal("architecture: BehaviorLoop.apply must exist as the single mutation seam")
	}
	callees := calleeNamesInNode(apply)
	if callees["Execute"] == 0 {
		t.Error("architecture: BehaviorLoop.apply must submit its proposal to the substrate mutation authority")
	}
	for _, banned := range []string{"os.WriteFile", "os.Create", "os.Remove", "os.Rename", "os.OpenFile"} {
		if callees[banned] > 0 {
			t.Errorf("architecture: BehaviorLoop.apply writes to disk directly via %s — every repair must pass the mutation authority", banned)
		}
	}
	// The apply seam must be reachable from the loop, not orphaned.
	reachable := false
	ast.Inspect(f, func(n ast.Node) bool {
		if reachable {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "apply" {
			return true
		}
		recv, ok := sel.X.(*ast.Ident)
		if !ok || recv.Name != "l" {
			return true
		}
		reachable = true
		return false
	})
	if !reachable {
		t.Error("architecture: BehaviorLoop.apply is unreachable — the repair seam must actually be used")
	}
}

// TestBehaviorStageHoldsNoMutationAuthority is the driver-side counterpart. The
// stage observes and decides; it must not write.
func TestBehaviorStageHoldsNoMutationAuthority(t *testing.T) {
	root := repoRoot(t)
	for _, rel := range goFilesUnder(root) {
		if !strings.HasPrefix(rel, "internal/runtime/autonomy/") {
			continue
		}
		f, fset := parseFile(t, filepath.Join(root, rel))
		for _, banned := range []string{"os.WriteFile", "os.Create", "os.Remove", "os.Rename"} {
			if sites := findCalls(f, fset, map[string]bool{banned: true}); len(sites) > 0 {
				t.Errorf("architecture: the autonomy layer writes to disk directly via %s at %s:%d — behavioral repairs must route through the mutation authority",
					banned, rel, sites[0].line)
			}
		}
	}
}

// TestBehaviorStageIsWiredOnce pins that the behavioral stage is constructed in
// exactly one place, from the composition root. Two wirings would mean two
// behavioral loops, and therefore two answers to "is this objective proven?".
func TestBehaviorStageIsWiredOnce(t *testing.T) {
	root := repoRoot(t)
	total := 0
	for _, rel := range goFilesUnder(root) {
		f, fset := parseFile(t, filepath.Join(root, rel))
		sites := findCalls(f, fset, map[string]bool{"WithBehaviorProposer": true})
		if len(sites) == 0 {
			continue
		}
		if rel != "internal/runtime/compose/compose.go" {
			t.Errorf("architecture: the behavioral stage is wired at %s:%d — only the composition root may wire it", rel, sites[0].line)
		}
		total += len(sites)
	}
	if total != 1 {
		t.Fatalf("architecture: the behavioral stage must be wired exactly once in the composition root, found %d site(s)", total)
	}
}

// TestBehaviorGateCanOnlyRemoveCompletion pins the direction of the gate at the
// source level: the gate's ONLY effect is to mutate a decision that was already
// LoopComplete. A gate that could also set LoopComplete would be a second
// completion authority.
func TestBehaviorGateCanOnlyRemoveCompletion(t *testing.T) {
	root := repoRoot(t)
	f, fset := parseFile(t, filepath.Join(root, "internal", "runtime", "autonomy", "objective_completion.go"))
	gate := findFuncDecl(f, "authorizeBehavioralCompletion")
	if gate == nil {
		t.Fatal("architecture: authorizeBehavioralCompletion must exist as the behavioral completion gate")
	}
	src := funcSource(t, fset, gate)
	if !strings.Contains(src, "LoopComplete") {
		t.Error("architecture: the behavioral gate must be scoped to a proposed LoopComplete decision")
	}
	if !strings.Contains(src, "*autonomy.LoopDecision") {
		t.Error("architecture: the behavioral gate must mutate the proposed decision, not drive the loop state itself")
	}
	for _, banned := range []string{"d.loop.Complete", "d.loop.Step", "d.step("} {
		if strings.Contains(src, banned) {
			t.Errorf("architecture: the behavioral gate calls %s — it must only downgrade a decision, never drive a transition", banned)
		}
	}
	// The gate must also be gated on the stage being present AND the objective
	// requiring behavioral proof, so read-only objectives are untouched.
	for _, required := range []string{"d.behavior == nil", "BehaviorRequired"} {
		if !strings.Contains(src, required) {
			t.Errorf("architecture: the behavioral gate must check %q so unrelated objectives are unaffected", required)
		}
	}
}

// TestBehaviorCapabilityVocabularyIsClosed pins the capability vocabulary as a
// closed set. An open vocabulary is how a runtime grows capabilities nobody
// reasoned about.
func TestBehaviorCapabilityVocabularyIsClosed(t *testing.T) {
	root := repoRoot(t)
	f, fset := parseFile(t, filepath.Join(root, "internal", "execution", "capability", "capability.go"))

	ids := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		spec, ok := n.(*ast.TypeSpec)
		if !ok || spec.Name.Name != "ID" {
			return true
		}
		assign, ok := spec.Type.(*ast.Ident)
		if !ok || assign.Name != "string" {
			return true
		}
		ast.Inspect(f, func(m ast.Node) bool {
			vs, ok := m.(*ast.ValueSpec)
			if !ok {
				return true
			}
			for _, val := range vs.Values {
				bl, ok := val.(*ast.BasicLit)
				if !ok || bl.Kind != token.STRING {
					continue
				}
				text, unErr := strconv.Unquote(bl.Value)
				if unErr != nil {
					continue
				}
				if strings.Contains(text, ".") && !strings.Contains(text, " ") {
					ids[text] = true
				}
			}
			return true
		})
		return false
	})

	expected := []string{
		"workspace.discover", "file.read", "file.search",
		"runtime.serve", "runtime.fetch", "runtime.inspect", "command.run",
	}
	for _, id := range expected {
		if !ids[id] {
			t.Errorf("architecture: capability vocabulary is missing %q", id)
		}
		delete(ids, id)
	}
	if len(ids) > 0 {
		t.Errorf("architecture: capability vocabulary declares undeclared ids %v — a runtime that can name another capability can execute one nobody reasoned about", sortedKeysOf(ids))
	}
	_ = fset
}

// TestBehaviorFailureTaxonomyIsComplete pins the eleven required failure classes.
// A blocked objective must be attributable, and an unlisted class is
// indistinguishable from "it broke".
func TestBehaviorFailureTaxonomyIsComplete(t *testing.T) {
	root := repoRoot(t)
	f, fset := parseFile(t, filepath.Join(root, "internal", "execution", "capability", "capability.go"))

	// Collect ONLY the constants explicitly typed FailureClass, so an unrelated
	// SCREAMING_SNAKE string elsewhere in the file (a Verdict, a status) cannot
	// make the taxonomy look larger than it is.
	found := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		vs, ok := n.(*ast.ValueSpec)
		if !ok {
			return true
		}
		typ, ok := vs.Type.(*ast.Ident)
		if !ok || typ.Name != "FailureClass" {
			return true
		}
		for _, val := range vs.Values {
			bl, ok := val.(*ast.BasicLit)
			if !ok || bl.Kind != token.STRING {
				continue
			}
			text, unErr := strconv.Unquote(bl.Value)
			if unErr == nil {
				found[text] = true
			}
		}
		return true
	})

	required := []string{
		"AUTHORIZATION_BLOCKED", "CAPABILITY_MISSING", "CAPABILITY_FAILED",
		"TARGET_UNCERTAIN", "CONTEXT_INSUFFICIENT", "EXECUTION_FAILED",
		"OBSERVATION_FAILED", "DIAGNOSIS_UNCERTAIN", "REPAIR_FAILED",
		"VERIFICATION_FAILED", "OBJECTIVE_UNPROVEN",
	}
	for _, class := range required {
		if !found[class] {
			t.Errorf("architecture: failure taxonomy is missing %q", class)
		}
	}
	if len(found) != len(required) {
		t.Errorf("architecture: failure taxonomy declares %d classes, want exactly %d: %v",
			len(found), len(required), sortedKeysOf(found))
	}
	_ = fset
}

// sortedKeysOf renders a set deterministically so a lock failure is reproducible.
func sortedKeysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// readFileRange reads a byte range from a file.
func readFileRange(name string, start, end int) ([]byte, error) {
	data, err := os.ReadFile(name)
	if err != nil {
		return nil, err
	}
	if start < 0 || end > len(data) || start > end {
		return nil, fmt.Errorf("invalid range %d:%d for %s (%d bytes)", start, end, name, len(data))
	}
	return data[start:end], nil
}
