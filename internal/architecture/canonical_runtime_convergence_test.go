package architecture

// PHASE 8 — canonical runtime convergence (M1–M7) negative tests.
//
// Canonical ownership (Phase 7, frozen):
//
//	autonomy.Driver             = canonical scheduler / orchestration owner
//	execution/planner           = canonical task decomposition owner
//	execution.RuntimeExecutor   = execution authority
//	Driver recovery matrix      = continuation / recovery / re-entry owner
//
// These tests pin the demotions: the experiment scheduler, rival executors,
// duplicate schedulers, and the Derive chain must never regain production
// authority, and the pure policy packages must never import the runtime.

import (
	"go/ast"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestExperimentSchedulerHasNoProductionImporters pins Phase 8 M1: the
// demoted StepScheduler experiment must have zero production (non-test)
// importers. The canonical orchestration owner is autonomy.Driver.
func TestExperimentSchedulerHasNoProductionImporters(t *testing.T) {
	root := repoRoot(t)
	want := moduleImport("internal/architecture_experiment/scheduler")
	foundImporter := false
	for _, rel := range goFilesUnder(root) {
		// goFilesUnder excludes *_test.go; the experiment's own files
		// may self-reference.
		if strings.HasPrefix(rel, "internal/architecture_experiment/scheduler/") {
			continue
		}
		f, _ := parseFile(t, filepath.Join(root, rel))
		if imports(f)[want] {
			t.Errorf("architecture: production file %s imports the demoted experiment scheduler — canonical orchestration owner is autonomy.Driver (Phase 8 M1)", rel)
			foundImporter = true
		}
	}
	if !foundImporter {
		// The invariant is enforced, not vacuous: the experiment package
		// must still exist (its own files import continuation, durable…).
		got := importsOfDir(t, root, "internal/architecture_experiment/scheduler")
		if len(got) == 0 {
			t.Error("architecture: experiment scheduler has no imports at all — demotion check is vacuous, expected pure-library imports")
		}
	}
}

// TestSingleSchedulerType pins Phase 8 M3: exactly one Scheduler type may
// exist in production code (the execution tool-dispatch helper), and the
// duplicate execution/scheduler subpackage must not return. Step
// orchestration lives in autonomy.Driver; the experiment StepScheduler
// lives only under internal/architecture_experiment.
func TestSingleSchedulerType(t *testing.T) {
	root := repoRoot(t)
	if _, err := os.Stat(filepath.Join(root, "internal/execution/scheduler")); !os.IsNotExist(err) {
		t.Error("architecture: internal/execution/scheduler must not exist — duplicate scheduler subpackage (Phase 8 M3)")
	}
	type schedDecl struct {
		rel  string
		name string
	}
	var schedulers []schedDecl
	for _, rel := range goFilesUnder(root) {
		if strings.HasPrefix(rel, "internal/architecture_experiment/") {
			continue
		}
		f, _ := parseFile(t, filepath.Join(root, rel))
		ast.Inspect(f, func(n ast.Node) bool {
			gd, ok := n.(*ast.GenDecl)
			if !ok {
				return true
			}
			for _, spec := range gd.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				if ts.Name.Name == "Scheduler" {
					schedulers = append(schedulers, schedDecl{rel: rel, name: ts.Name.Name})
				}
				if ts.Name.Name == "StepScheduler" {
					t.Errorf("architecture: production StepScheduler found at %s — the experiment scheduler must live only under internal/architecture_experiment (Phase 8 M1/M3)", rel)
				}
			}
			return true
		})
	}
	if len(schedulers) != 1 {
		t.Fatalf("architecture: want exactly one production Scheduler type, found %d: %v", len(schedulers), schedulers)
	}
	if schedulers[0].rel != "internal/execution/scheduler.go" {
		t.Errorf("architecture: the single production Scheduler must be the execution tool-dispatch helper at internal/execution/scheduler.go, found at %s", schedulers[0].rel)
	}
}

// TestDeriveChainHasNoCanonicalImporters pins Phase 8 M7: the demoted
// Derive chain (understanding / problemsurface / problem /
// mutationstrategy, plus its changesurface and adapters/web helpers) is a
// closed derive-helper cluster. No canonical runtime package may import
// it — canonical decomposition is owned by execution/planner. Intra-cluster
// imports (e.g. mutationstrategy → understanding) remain allowed.
func TestDeriveChainHasNoCanonicalImporters(t *testing.T) {
	root := repoRoot(t)
	derivePrefixes := []string{
		"internal/understanding/",
		"internal/problemsurface/",
		"internal/problem/",
		"internal/mutationstrategy/",
		"internal/changesurface/",
		"internal/adapters/web/",
	}
	canonicalDirs := []string{
		"internal/runtime/autonomy",
		"internal/runtime/compose",
		"internal/runtime/orchestrator",
		"internal/runtime/engine.go",
		"internal/execution",
		"internal/ui",
		"internal/autonomy",
		"internal/cli",
		"internal/app",
		"cmd",
	}
	for _, rel := range goFilesUnder(root) {
		inCanonical := false
		for _, dir := range canonicalDirs {
			if rel == dir || strings.HasPrefix(rel, dir+"/") || strings.HasPrefix(rel, dir+".") {
				inCanonical = true
				break
			}
		}
		if !inCanonical {
			continue
		}
		// internal/execution is a directory AND a package file prefix;
		// exclude nothing else: the whole subtree is canonical.
		f, _ := parseFile(t, filepath.Join(root, rel))
		for imp := range imports(f) {
			const modPrefix = "github.com/PizenLabs/izen/"
			if !strings.HasPrefix(imp, modPrefix) {
				continue
			}
			inner := strings.TrimPrefix(imp, modPrefix)
			for _, dp := range derivePrefixes {
				if inner == strings.TrimSuffix(dp, "/") || strings.HasPrefix(inner, dp) {
					t.Errorf("architecture: canonical file %s imports demoted derive-helper %s — canonical decomposition is execution/planner (Phase 8 M7)", rel, imp)
				}
			}
		}
	}
}

// TestDriverPureReuseDirection pins Phase 8 M6: the pure policy packages
// consulted by the Driver as libraries must never import the runtime back.
// Driver/executor → pure package is the only allowed direction.
func TestDriverPureReuseDirection(t *testing.T) {
	root := repoRoot(t)
	purePkgs := []string{
		"internal/stepadmission",
		"internal/continuation",
		"internal/mutationstrategy",
	}
	forbidden := []string{
		moduleImport("internal/runtime/autonomy"),
		moduleImport("internal/autonomy"),
		moduleImport("internal/execution"),
		moduleImport("internal/runtime/executor"),
		moduleImport("internal/runtime/substrate"),
		moduleImport("internal/events"),
	}
	for _, pkg := range purePkgs {
		got := importsOfDir(t, root, pkg)
		for _, f := range forbidden {
			if got[f] {
				t.Errorf("architecture: %s imports %q — pure policy packages must never import the runtime (one-way: Driver → pure, Phase 8 M6)", pkg, f)
			}
		}
	}
}
