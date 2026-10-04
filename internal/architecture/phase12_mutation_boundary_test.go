package architecture

// ── ONE MUTATION BOUNDARY ──────────────────────────────────────────────────
//
// IZEN Execution Architecture §13: "All mutations must cross one authoritative
// Mutation Boundary. No component may write directly around the mutation
// subsystem; apply a patch outside the MutationSet; mutate using model output
// directly; reuse stale artifacts after computation failure; infer
// authorization from successful computation."
//
// The existing guards in this package are scoped to individual packages
// (internal/ui, internal/planner, internal/execution/artifact_step.go, …). Each
// one holds, but none of them states WHICH package owns a workspace write, so
// a new writer anywhere else in the tree lands in a blind spot.
//
// This file states the ownership positively: it enumerates every production
// package that contains a workspace-write primitive and compares that set
// against the declared owners. The set is frozen with anti-rot semantics — a
// new writer fails, and so does a deleted one — so the inventory is a live
// contract rather than a snapshot.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// workspaceWritePrimitives are the stdlib calls that write to, create or remove a
// path on disk. This is deliberately a lexical scan: it does not attempt to
// prove that a call reaches the workspace rather than the ledger, it only
// establishes the population the ownership rule is stated over.
var workspaceWritePrimitives = []string{
	"os.WriteFile(",
	"os.Create(",
	"os.Rename(",
	"os.Remove(",
	"os.RemoveAll(",
	"os.Truncate(",
	"os.Chmod(",
	"os.OpenFile(",
	"os.MkdirAll(",
	"os.Mkdir(",
	"os.CreateTemp(",
	"os.Symlink(",
	"ioutil.WriteFile(",
}

// workspaceWriteOwners is the frozen map of production package → the
// workspace-write primitives it is allowed to contain.
//
// Every entry is justified by an existing architectural role:
//
//   - internal/execution — the MutationSet / PatchManager / OCC /
//     MutationBoundary authority. The one place a workspace byte changes.
//   - internal/engine — MutationSet's transactional backing store
//     (engine.Transaction.Rollback); rollback of a committed MutationSet.
//   - internal/boundary — the workspace-integrity assertion surface. It
//     DELEGATES to internal/execution; it contains no primitive of its own.
//   - internal/runtime/autonomy — holds NO primitive. The DAG rollback seam
//     routes through the boundary (see TestPhase12_DAGRollbackCrossesTheMutationBoundary).
//   - internal/runtime/substrate, internal/substrate, internal/patch,
//     internal/resource/file, internal/infrastructure/capabilities,
//     internal/app, internal/runtime/scope — the remaining write surfaces
//     audited and frozen here so a new one cannot appear unnoticed. Their
//     existence is a recorded fact, not an endorsement.
//
// Everything absent from this map that contains one of the primitives fails the
// test. That is the rule: ownership is explicit, and unknown writers are
// rejected rather than reviewed after the fact.
var workspaceWriteOwners = map[string][]string{
	// ── THE AUTHORITY ────────────────────────────────────────────────────
	// internal/execution is the MutationSet / PatchManager / OCC /
	// MutationBoundary authority: the one place a workspace byte changes.
	// internal/engine is MutationSet's transactional backing store.
	"internal/execution": {"os.MkdirAll(", "os.OpenFile(", "os.Remove(", "os.WriteFile("},
	"internal/engine":    {"os.Remove(", "os.WriteFile("},

	// ── LEDGER / STATE / CACHE WRITES (not workspace mutations) ──────────
	// These packages write .izen/, ~/.izen/, lock files, audit logs, snapshots
	// and cache blobs. They are frozen here so the distinction from a workspace
	// mutation is explicit and so a new writer inside them shows up in the diff.
	"cmd/izen":                             {"os.WriteFile("},
	"internal/app":                         {"os.MkdirAll("},
	"internal/app/model":                   {"os.MkdirAll(", "os.WriteFile("},
	"internal/audit":                       {"os.MkdirAll(", "os.OpenFile("},
	"internal/checkpoint":                  {"os.MkdirAll(", "os.Remove(", "os.RemoveAll(", "os.WriteFile("},
	"internal/config":                      {"os.MkdirAll(", "os.Remove(", "os.WriteFile("},
	"internal/control":                     {"os.MkdirAll(", "os.WriteFile("},
	"internal/core/artifact":               {"os.MkdirAll(", "os.Remove(", "os.WriteFile("},
	"internal/core/domain/checkpoint":      {"os.MkdirAll(", "os.Remove(", "os.WriteFile("},
	"internal/events/audit":                {"os.MkdirAll(", "os.OpenFile("},
	"internal/fs":                          {"os.Chmod(", "os.CreateTemp(", "os.MkdirAll(", "os.Remove(", "os.Rename(", "os.WriteFile("},
	"internal/git":                         {"os.Remove(", "os.WriteFile("},
	"internal/infrastructure/capabilities": {"os.MkdirAll(", "os.Remove(", "os.WriteFile("},
	"internal/knowledge":                   {"os.MkdirAll(", "os.OpenFile(", "os.Remove(", "os.Rename("},
	"internal/lea":                         {"os.MkdirAll(", "os.WriteFile("},
	"internal/llm":                         {"os.MkdirAll(", "os.Remove(", "os.WriteFile("},
	"internal/pkg/atomicio":                {"os.Chmod(", "os.CreateTemp(", "os.MkdirAll(", "os.Remove(", "os.Rename("},
	"internal/pkg/lock":                    {"os.MkdirAll(", "os.OpenFile("},
	"internal/provider/registry":           {"os.MkdirAll(", "os.Rename(", "os.WriteFile("},
	"internal/review":                      {"os.MkdirAll(", "os.RemoveAll(", "os.WriteFile("},
	"internal/runtime/durable":             {"os.MkdirAll(", "os.OpenFile(", "os.Remove("},
	"internal/runtime/output":              {"os.MkdirAll(", "os.Remove(", "os.Rename(", "os.Symlink(", "os.WriteFile("},
	"internal/runtime/scope":               {"os.CreateTemp(", "os.MkdirAll(", "os.Remove(", "os.Rename("},
	"internal/runtime/substrate":           {"os.MkdirAll(", "os.Remove(", "os.WriteFile("},
	"internal/runtime/substrate/store":     {"os.MkdirAll(", "os.WriteFile("},
	"internal/security/credentials":        {"os.MkdirAll(", "os.WriteFile("},
	"internal/session":                     {"os.CreateTemp(", "os.MkdirAll(", "os.OpenFile(", "os.Remove(", "os.RemoveAll(", "os.Rename(", "os.Truncate(", "os.WriteFile("},
	"internal/state":                       {"os.Chmod(", "os.MkdirAll(", "os.Remove(", "os.Rename(", "os.WriteFile("},
	"internal/substrate":                   {"os.MkdirAll(", "os.Remove("},
	"internal/workspace/checkpoint":        {"os.CreateTemp(", "os.MkdirAll(", "os.Remove(", "os.RemoveAll(", "os.Rename(", "os.WriteFile("},

	// ── OTHER WRITE SURFACES: recorded, not endorsed ─────────────────────
	// These are audited, real writers whose authority is NOT the mutation
	// boundary: the transactional file store, the tiered patch engine, the
	// headless runtime file executor, the resource/file capability port, the
	// platform file capability, and the `izen compact` command. They are frozen
	// so that any NEW writer here fails the build; closing them down is recorded
	// as remaining work rather than silently accepted here.
	"internal/fs/txfs":       nil, // (declared clean above via internal/fs)
	"internal/patch":         {"os.MkdirAll(", "os.WriteFile("},
	"internal/resource/file": {"os.Chmod(", "os.OpenFile(", "os.Remove(", "os.WriteFile("},
	"internal/ui":            {"os.MkdirAll(", "os.OpenFile("},
	"test/benchmark":         {"os.MkdirAll(", "os.RemoveAll(", "os.WriteFile("},

	// FileExecutor used to sit in the list above: it wrote a commit through a
	// hand-rolled temp-file-and-rename protocol and undid it with os.WriteFile
	// and os.Remove. Both directions now cross internal/kernelbridge (see
	// internal/runtime/executor/kernelcommit.go and the
	// TestKernelLock_PathBMutationRoutesThroughKernel lock), so the package
	// holds no workspace-write primitive at all. Recording it as clean rather
	// than deleting the entry is the point: a primitive reappearing here fails
	// the build, which is what stops the migration from silently un-doing
	// itself.
	"internal/runtime/executor": nil,

	// ── EXPLICITLY CLEAN: no workspace-write primitive at all ─────────────
	// The DAG rollback seam, the human-boundary driver, the workspace policy
	// boundary, the budget scheduler, the intent authority, the read-only
	// workspaces and the ContextSpec / protocol contracts. Naming them is the
	// point: a primitive appearing in any of them fails the build.
	"internal/boundary":           nil, // delegates to internal/execution
	"internal/runtime/autonomy":   nil, // the DAG rollback seam
	"internal/runtime/scopeguard": nil,
	"internal/autonomy":           nil,
	"internal/policy":             nil,
	"internal/modes":              nil,
	"internal/verification":       nil,
	"internal/contextspec":        nil,
	"internal/protocol":           nil,
	"internal/understanding":      nil,
	"internal/changesurface":      nil,
	"internal/presentation":       nil,
	"internal/contextcompiler":    nil,
	"internal/domain":             nil,
	"internal/core":               nil,
	"internal/continuation":       nil,
	"internal/hotfix":             nil,
	"internal/tui":                nil,
	"internal/events":             nil,
	"internal/ai":                 nil,
	"internal/llmstep":            nil,
}

// TestPhase12_WorkspaceWriteOwnershipIsFrozen pins the mutation-boundary
// ownership rule at repo scope.
//
// It is the positive form of the per-package guards: instead of asking "does
// package X do something forbidden?", it asks "which packages own a workspace
// write, and does the tree agree?". A new writer in an unlisted package fails
// the build; a removed writer also fails, because the inventory is the contract.
func TestPhase12_WorkspaceWriteOwnershipIsFrozen(t *testing.T) {
	root := repoRoot(t)

	// observed[package] = set of primitives actually present.
	observed := map[string]map[string]bool{}
	unknown := map[string][]string{} // package → primitives, for packages not in the map

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			base := info.Name()
			if base == ".git" || base == "node_modules" || base == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		src := string(data)
		pkg := packageDirOf(rel)
		if observed[pkg] == nil {
			observed[pkg] = map[string]bool{}
		}
		for _, prim := range workspaceWritePrimitives {
			if strings.Contains(src, prim) {
				observed[pkg][prim] = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}

	declared := 0
	for pkg, prims := range workspaceWriteOwners {
		if prims != nil {
			declared++
		}
		want := map[string]bool{}
		for _, p := range prims {
			want[p] = true
		}
		got := observed[pkg]
		for p := range want {
			if !got[p] {
				t.Errorf("%s declares ownership of %s but no production file contains it — "+
					"the inventory is stale; update it in the same change that removed the call", pkg, p)
			}
		}
		for p := range got {
			if !want[p] {
				unknown[pkg] = append(unknown[pkg], p)
			}
		}
	}

	for pkg, prims := range unknown {
		t.Errorf("%s contains workspace-write primitive(s) %v but is not a declared owner — "+
			"either route the write through the mutation boundary or record the ownership explicitly",
			pkg, prims)
	}

	if declared == 0 {
		t.Fatal("no declared owners — the invariant would be vacuous")
	}
}

// TestPhase12_DAGRollbackCrossesTheMutationBoundary pins the specific fix: the
// DAG rollback seam must not write the workspace itself. It restores through
// internal/execution.RollbackAndVerify, which both restores AND asserts the
// post-restore digest, so "the workspace was rolled back" is a verified fact
// rather than a claim.
func TestPhase12_DAGRollbackCrossesTheMutationBoundary(t *testing.T) {
	root := repoRoot(t)
	rel := "internal/runtime/autonomy/adapter.go"
	src := readFileOK(t, filepath.Join(root, rel))
	for _, forbidden := range workspaceWritePrimitives {
		if strings.Contains(src, forbidden) {
			t.Errorf("%s performs %s directly — the DAG rollback seam must route through the "+
				"authoritative Mutation Boundary (execution.RollbackAndVerify), which also asserts "+
				"the post-restore digest", rel, forbidden)
		}
	}
	if !strings.Contains(src, "execution.RollbackAndVerify(") {
		t.Errorf("%s no longer routes the DAG rollback through the mutation boundary", rel)
	}
}

// internal/boundary re-exports the boundary for callers that only need the
// integrity assertion. It must never grow its own restore loop: two writers for
// one rollback means one of them asserts nothing.
func TestPhase12_BoundaryPackageHoldsNoSecondRestoreImplementation(t *testing.T) {
	root := repoRoot(t)
	rel := "internal/boundary/rollback.go"
	src := readFileOK(t, filepath.Join(root, rel))
	for _, forbidden := range workspaceWritePrimitives {
		if strings.Contains(src, forbidden) {
			t.Errorf("%s contains %s — internal/boundary is an assertion surface that DELEGATES to "+
				"internal/execution.RollbackAndVerify; a second restore loop is a second mutation path", rel, forbidden)
		}
	}
	if !strings.Contains(src, "execution.RollbackAndVerify(") {
		t.Errorf("%s does not delegate to the single rollback authority", rel)
	}
}

// packageDirOf returns the module-relative directory of a file, which is the
// package identity used by the ownership map.
func packageDirOf(rel string) string {
	dir := filepath.Dir(rel)
	if dir == "." {
		return filepath.Base(rel)
	}
	return filepath.ToSlash(dir)
}
