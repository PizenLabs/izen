package architecture

// ── Objective-execution contract lock ───────────────────────────────────────
//
// The failure this file prevents is architectural, so it is pinned
// architecturally: by reading the source, not by running a benchmark.
//
// Each test below states one structural fact about the runtime. Together they
// encode the invariant the whole objective contract exists for:
//
//	"a valid mutation happened"  ≠  "the user's objective was satisfied"
//
// A contributor who reintroduces a completion shortcut, a domain heuristic, or a
// model-as-authority path breaks one of these tests rather than quietly
// resurrecting the bug the benchmark exposed.

import (
	"go/ast"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// ── 1. The authority must consult the objective contract ────────────────────

// TestObjectiveAuthorityEvaluatesConditionsBeforeTaskKind pins the precedence
// order inside Evaluate: the OUTCOME contract is judged before the EXECUTION-SHAPE
// contract.
//
// If the per-kind clauses ran first, a mutation of the right shape could satisfy
// them while the objective's own conditions went unchecked — which is precisely
// the minimum-patch completion the runtime used to report.
func TestObjectiveAuthorityEvaluatesConditionsBeforeTaskKind(t *testing.T) {
	root := repoRoot(t)
	src := sourceOf(t, filepath.Join(root, "internal/execution/objective_authority.go"))

	unmetAt := strings.Index(src, "if unmet := UnmetCondition(ev); unmet != nil {")
	kindAt := strings.Index(src, "// ── 5 — the contract's own clauses")
	if unmetAt < 0 {
		t.Fatal("Evaluate does not consult UnmetCondition; the objective completion contract is not in force")
	}
	if kindAt < 0 {
		t.Fatal("Evaluate lost its per-kind clause section")
	}
	if unmetAt > kindAt {
		t.Error("Evaluate judges the task-kind clauses before the objective completion contract; " +
			"a mutation of the right shape could then satisfy them alone")
	}
}

// TestObjectiveEvidenceCarriesTheOutcomeContract asserts the evidence bundle
// carries the outcome half. Without these fields the authority cannot ask the
// question at all.
func TestObjectiveEvidenceCarriesTheOutcomeContract(t *testing.T) {
	root := repoRoot(t)
	f, _ := parseFile(t, filepath.Join(root, "internal/execution/objective_authority.go"))

	var found map[string]bool
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok || ts.Name.Name != "ObjectiveEvidence" {
				continue
			}
			found = map[string]bool{}
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				continue
			}
			for _, field := range st.Fields.List {
				for _, name := range field.Names {
					found[name.Name] = true
				}
			}
		}
	}
	for _, field := range []string{
		"Conditions",
		"PostMutationObserved",
		"DischargedRequirements",
		"ClaimedRequirements",
	} {
		if !found[field] {
			t.Errorf("ObjectiveEvidence lacks %q; the runtime cannot bind outcome evidence without it", field)
		}
	}
}

// ── 2. The runtime must re-inspect the result ───────────────────────────────

// TestDriverReReadsTheDeclaredTargetsAfterMutation pins that the runtime reads
// the workspace back after a mutation, on EVERY path that can apply one — the
// autonomous dispatch, the human approval gate and the decomposed plan.
//
// Reading the result is a structural step, not an aspiration: without it the
// runtime has no basis for any claim about what it produced.
func TestDriverReReadsTheDeclaredTargetsAfterMutation(t *testing.T) {
	root := repoRoot(t)
	for _, file := range []string{
		"internal/runtime/autonomy/driver.go",
		"internal/runtime/autonomy/decomposition.go",
	} {
		src := sourceOf(t, filepath.Join(root, file))
		if !strings.Contains(src, "d.bindStepEvidence()") {
			t.Errorf("%s applies mutations but never folds the result into the objective lifecycle", file)
		}
	}
	// The re-read itself must go through the adapter's filesystem read, not
	// through a claim.
	lifecycle := sourceOf(t, filepath.Join(root, "internal/runtime/autonomy/objective_lifecycle.go"))
	if !strings.Contains(lifecycle, "d.adapter.ReadTargetFile(t)") {
		t.Error("the post-mutation re-inspection does not read the file; it asserts rather than observes")
	}
}

// ── 3. Completion must be the authority's, not the matrix's ─────────────────

// TestDriverCompletionGateIsAuthoritative pins that a proposed completion is
// passed through the authority, and that the continuation router can only
// DOWNGRADE it.
//
// The router never writes LoopComplete; if it could, the loop would terminate
// on progress arithmetic instead of on evidence.
func TestDriverCompletionGateIsAuthoritative(t *testing.T) {
	root := repoRoot(t)
	driver := sourceOf(t, filepath.Join(root, "internal/runtime/autonomy/driver.go"))
	if !strings.Contains(driver, "d.authorizeObjectiveCompletion(&decision)") {
		t.Error("the loop does not pass a proposed completion through the authority")
	}
	if !strings.Contains(driver, "d.routeObjectiveContinuation(&decision)") {
		t.Error("the loop does not route a refused completion through the objective continuation")
	}

	lifecycle := sourceOf(t, filepath.Join(root, "internal/runtime/autonomy/objective_lifecycle.go"))
	router := funcBetween(lifecycle, "func (d *Driver) routeObjectiveContinuation", "\nfunc ")
	if router == "" {
		t.Fatal("routeObjectiveContinuation is missing")
	}
	if strings.Contains(router, "autonomy.LoopComplete") {
		t.Error("the continuation router can grant completion; it must only be able to downgrade one")
	}
	if !strings.Contains(router, "autonomy.LoopUnsubstantiate") {
		t.Error("the continuation router no longer guards the refused-completion state")
	}
}

// ── 4. No domain heuristics, no change-volume proxy ─────────────────────────

// bannedObjectiveVocabulary is the vocabulary a domain-agnostic runtime must not
// acquire. It is a NEGATIVE test over the runtime's DECISION sites: the whole
// point of the objective contract is that the runtime reasons in terms of
// objective, requirements, evidence and progress — not subject matter, file types
// or change volume.
var bannedObjectiveVocabulary = []string{
	// Subject matter.
	"portfolio", "website", "webpage", "html", "css", "javascript", "typescript",
	"react", "stylesheet", "landing page", "hero section", "responsive design",
	// Quality judgements the runtime has no authority to make.
	"professional", "beautiful", "aesthetic", "aesthetics", "polish", "delight",
	"elegant", "modern-looking",
	// Change-volume proxies. Every one of these is the shape of the anti-pattern
	// this work exists to remove.
	"minchangedlines", "minchangedfiles", "minlineschanged", "minfileschanged",
	"changethreshold", "volume threshold", "minimum change", "enough change",
}

// objectiveDecisionSources are the production files that DECIDE anything about an
// objective: the contract, the condition reducer, the authority, the driver
// lifecycle, the completion seam and the scope resolution.
//
// The lexical segmenter (which enumerates English change/inquiry verbs) and the
// requirement-pass PROMPT (which tells the model not to invent quality
// requirements) are deliberately NOT scanned. Neither makes a decision about the
// objective: one parses the user's words with a closed, domain-neutral verb table,
// the other is a wire instruction. Scanning them would force the runtime to hide
// its own honest vocabulary in order to satisfy a test.
var objectiveDecisionSources = []string{
	"internal/execution/objective_conditions.go",
	"internal/execution/objective_authority.go",
	"internal/execution/objective_operation.go",
	"internal/runtime/autonomy/objective_lifecycle.go",
	"internal/runtime/autonomy/objective_completion.go",
	"internal/runtime/autonomy/scope_resolution.go",
}

// TestRuntimeHasNoDomainOrVolumeHeuristics reads every production DECISION source
// in the objective-execution path and fails on any domain vocabulary or
// change-volume proxy.
//
// It is deliberately a source scan rather than a behavioural test: a behavioural
// test can only prove one scenario is not special-cased, whereas a source scan
// proves the vocabulary is absent.
func TestRuntimeHasNoDomainOrVolumeHeuristics(t *testing.T) {
	root := repoRoot(t)
	for _, file := range objectiveDecisionSources {
		path := filepath.Join(root, file)
		f, fset := parseFile(t, path)
		// Comments may DISCUSS the anti-pattern; code may not CONTAIN it. Only
		// string literals are scanned.
		for _, lit := range stringLiterals(f) {
			lower := strings.ToLower(lit)
			for _, banned := range bannedObjectiveVocabulary {
				if strings.Contains(lower, banned) {
					t.Errorf("%s: decision-site string literal %q contains banned objective vocabulary %q",
						fset.Position(f.Pos()).Filename, lit, banned)
				}
			}
		}
	}
}

// TestObjectiveSegmenterUsesOnlyDomainNeutralVerbs pins the one lexical surface
// the runtime owns: its clause segmenter. The verb tables there must be generic
// change/inquiry vocabulary, never subject matter — otherwise segmentation would
// quietly become a domain classifier.
func TestObjectiveSegmenterUsesOnlyDomainNeutralVerbs(t *testing.T) {
	root := repoRoot(t)
	f, fset := parseFile(t, filepath.Join(root, "internal/execution/objective_contract.go"))
	for _, lit := range stringLiterals(f) {
		lower := strings.ToLower(strings.TrimSpace(lit))
		if len(lower) < 3 || strings.ContainsAny(lower, " \n\t") {
			// Multi-word literals are the prompt/schema prose, not verb entries.
			continue
		}
		for _, banned := range []string{"portfolio", "website", "html", "css", "javascript", "react"} {
			if lower == banned {
				t.Errorf("%s: the clause segmenter's verb tables contain the domain token %q",
					fset.Position(f.Pos()).Filename, banned)
			}
		}
	}
}

// TestNoChangeVolumeThresholdsExistAnywhere is the blunt companion: no
// production source in the objective-execution path may compare a line count or a
// changed-file count against a constant.
func TestNoChangeVolumeThresholdsExistAnywhere(t *testing.T) {
	root := repoRoot(t)
	for _, file := range []string{
		"internal/execution/objective_contract.go",
		"internal/execution/objective_conditions.go",
		"internal/execution/objective_authority.go",
		"internal/runtime/autonomy/objective_lifecycle.go",
	} {
		src := sourceOf(t, filepath.Join(root, file))
		for _, banned := range []string{
			"LinesChanged", "ChangedLines", "ChangedFiles", "FilesChanged",
			"MinLines", "MinFiles", "MinBytes",
		} {
			if strings.Contains(src, banned) {
				t.Errorf("%s references %q; change volume is not evidence of adequacy", file, banned)
			}
		}
	}
}

// ── 5. The model proposes, the runtime decides ───────────────────────────────

// TestModelRequirementsNeverSelfAdmit pins that the wire type carries no status
// and no satisfaction flag, so a model response cannot arrive pre-admitted.
func TestModelRequirementsNeverSelfAdmit(t *testing.T) {
	root := repoRoot(t)
	f, _ := parseFile(t, filepath.Join(root, "internal/execution/requirement_pass.go"))
	var fields map[string]bool
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok || ts.Name.Name != "ProposedRequirement" {
				continue
			}
			fields = map[string]bool{}
			st, _ := ts.Type.(*ast.StructType)
			for _, field := range st.Fields.List {
				for _, name := range field.Names {
					fields[name.Name] = true
				}
			}
		}
	}
	if fields == nil {
		t.Fatal("ProposedRequirement is missing; the wire type is the thing that must stay powerless")
	}
	for _, banned := range []string{"Status", "Satisfied", "Admitted", "Proven", "Done", "Complete"} {
		if fields[banned] {
			t.Errorf("ProposedRequirement carries %q; the wire type must not be able to assert its own admission", banned)
		}
	}
}

// TestRequirementGroundingIsScopeBoundOnly pins that a proposal can only be
// grounded against the RUNTIME's resolved scope.
//
// If a proposal's own declared targets joined the grounding pool, a model could
// ground an invented obligation against a file the runtime never resolved.
func TestRequirementGroundingIsScopeBoundOnly(t *testing.T) {
	root := repoRoot(t)
	src := sourceOf(t, filepath.Join(root, "internal/execution/objective_contract.go"))
	fn := funcBetween(src, "func groundTargets", "\nfunc ")
	if fn == "" {
		t.Fatal("groundTargets is missing")
	}
	if !strings.Contains(fn, "if len(scope) == 0 {") {
		t.Error("groundTargets does not require a non-empty runtime scope")
	}
	if strings.Contains(fn, "declared...)") && strings.Contains(fn, "pool :=") {
		t.Error("groundTargets builds a pool from the proposal's own declared targets; a proposal must never widen its own scope")
	}
	if strings.Contains(fn, "range pool") {
		t.Error("groundTargets iterates a merged pool instead of the runtime scope")
	}
}

// ── 6. Domain independence of the contract machinery ────────────────────────

// TestObjectiveContractIsHeadless pins that the contract, the progress reducer
// and the continuation decision live in DOMAIN packages that import no
// presentation surface. A TUI-only objective contract would be unexercisable
// without a terminal, and unexercisable contracts rot.
func TestObjectiveContractIsHeadless(t *testing.T) {
	root := repoRoot(t)
	got := importsOfDir(t, root, "internal/execution")
	for _, forbidden := range []string{
		moduleImport("internal/ui"),
		moduleImport("internal/presentation"),
		moduleImport("internal/tui"),
	} {
		if got[forbidden] {
			t.Errorf("internal/execution imports %q; the objective contract must stay headless", forbidden)
		}
	}
}

// TestObjectiveProgressVocabularyIsDistinctFromExecutionOutcome pins the three
// state vocabularies stay separate. Collapsing them is how "the provider
// finished" starts reading as "the objective is done".
func TestObjectiveProgressVocabularyIsDistinctFromExecutionOutcome(t *testing.T) {
	root := repoRoot(t)
	f, _ := parseFile(t, filepath.Join(root, "internal/execution/objective_conditions.go"))
	// The progress vocabulary is the set of constants declared with the
	// ObjectiveProgress type.
	seen := map[string]bool{}
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			id, ok := vs.Type.(*ast.Ident)
			if !ok || id.Name != "ObjectiveProgress" {
				continue
			}
			for _, name := range vs.Names {
				if seen[name.Name] {
					t.Errorf("duplicate progress state %q", name.Name)
				}
				seen[name.Name] = true
			}
		}
	}
	if len(seen) == 0 {
		t.Fatal("no ObjectiveProgress constants were found; the vocabulary may have been renamed away")
	}
	// Every state the acceptance criteria require to be distinguishable.
	for _, state := range []string{
		"ProgressDiscovered", "ProgressUnderstood", "ProgressRequirementsDerived",
		"ProgressReady", "ProgressInProgress", "ProgressPartiallySatisfied",
		"ProgressBlocked", "ProgressRequiresContinuation", "ProgressProven",
		"ProgressFailed", "ProgressUnsubstantiated", "ProgressRequiresAuthorization",
	} {
		if !seen[state] {
			t.Errorf("ObjectiveProgress lacks %q; the progress model must not collapse distinguishable states", state)
		}
	}
	// A PROVEN projection must be reachable ONLY from a PROVEN verdict, and the
	// reducer must be the single place that decides it.
	src := sourceOf(t, filepath.Join(root, "internal/execution/objective_conditions.go"))
	if strings.Count(src, "return ProgressProven") != 2 {
		t.Errorf("ProgressProven is returned %d times; it must be reachable from exactly one verdict and one satisfied-conditions branch",
			strings.Count(src, "return ProgressProven"))
	}
}

// ── helpers ─────────────────────────────────────────────────────────────────

// funcBetween returns the source of the function starting at `start` and ending
// at the next top-level `end` marker.
func funcBetween(src, start, end string) string {
	i := strings.Index(src, start)
	if i < 0 {
		return ""
	}
	rest := src[i+len(start):]
	j := strings.Index(rest, end)
	if j < 0 {
		return rest
	}
	return rest[:j]
}

// sourceOf reads a production source file verbatim.
func sourceOf(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("architecture: read %s: %v", path, err)
	}
	return string(data)
}

// stringLiterals collects every untyped string literal in a parsed file.
func stringLiterals(f *ast.File) []string {
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		if v, err := strconv.Unquote(lit.Value); err == nil {
			out = append(out, v)
		}
		return true
	})
	return out
}
