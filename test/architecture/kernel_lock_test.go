package architecture

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// repoRoot locates the module root from the test's working directory.
//
// It exists because an architecture lock that resolves its paths relative to the
// working directory will happily scan nothing and pass. That is not a
// hypothetical: a relative-path walk whose root does not exist returns an error
// the caller usually discards, and the resulting test is green forever while
// checking no files at all. A lock that cannot prove it read something is not a
// lock, so this walks up until it finds go.mod and fails loudly if it cannot.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("resolving the working directory: %v", err)
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod found above %s; the lock cannot locate the module root", dir)
		}
		dir = parent
	}
}

// This file is the strangler lock.
//
// The Runtime Kernel is being adopted by strangling: the three legacy execution
// stacks keep working while the slice-by-slice traffic moves onto the kernel, and
// each slice deletes the old implementation in the same change it adds the new
// one. A strangler only stays a strangler while the new route is the ONLY
// reachable route. Without a lock, the migration decays within a few releases —
// one convenience helper, one "temporary" direct syscall, and the kernel is
// advisory again while every document still claims it is authoritative.
//
// So there are three locks here, and each one closes a different way back in:
//
//  1. TestKernelLock_SingleKernelEntryPoint — only internal/kernelbridge may
//     import the kernel. This stops a NEW caller from reaching past the bridge,
//     which is what makes the bridge the seam rather than one more path around
//     it.
//
//  2. TestKernelLock_NoUnregisteredWorkspaceExistenceDecision — a ratchet over
//     every remaining existence decision in the execution packages. The
//     allowlist below is the strangler's remaining work list: every entry is a
//     site that answers "does this workspace target exist" with a raw syscall
//     instead of with kernel evidence. It may only shrink. Both directions fail:
//     a new decision site is a new bypass, and a stale entry means the list has
//     stopped describing reality.
//
//  3. TestKernelLock_MigratedResolutionRoutesThroughKernel — the first slice
//     cannot regress. The deleted os.Stat must not come back, and the file must
//     stay wired to the bridge.

// kernelImportPaths are the kernel modules a legacy caller must reach through the
// bridge rather than importing directly.
//
// runtime/capabilities/filesystem is listed alongside runtime/kernel because a
// caller can bypass the bridge just as effectively by constructing a capability
// and calling Invoke itself: it would get an Observation with no Spec, no Grant,
// no event and no verdict.
var kernelImportPaths = []string{
	"github.com/PizenLabs/izen/runtime/kernel",
	"github.com/PizenLabs/izen/runtime/capabilities/filesystem",
}

// kernelBridgePackage is the single sanctioned entry point.
const kernelBridgePackage = "internal/kernelbridge"

// lockedExecutionDirs are the packages that make decisions about executing
// against a workspace. Existence decisions outside these packages are not part of
// the strangler scope: session stores, config discovery and credential vaults are
// allowed to ask the operating system whether their own files exist, because
// those files are runtime bookkeeping rather than mutation targets.
var lockedExecutionDirs = []string{
	"internal/ui",
	"internal/execution",
	"internal/runtime",
	"internal/modes",
	"internal/engine",
	"internal/app",
}

// existencePrimitives are the calls that answer "does this path exist" directly.
//
// The vocabulary is deliberately narrow. os.Stat and os.Lstat are the shapes this
// lock exists to catch: a boolean derived from a stat result, used in a branch.
// Adding os.Open would sweep in every file read and turn the lock into noise, and
// a lock nobody reads is not a lock.
var existencePrimitives = map[string]string{
	"os.Stat":  "os.Stat",
	"os.Lstat": "os.Lstat",
}

// knownExistenceBypasses is the strangler's remaining work list.
//
// Every entry is a place where the runtime decides something about a workspace
// target by calling the operating system directly, with no event, no state, no
// evidence and no verification behind the answer. Each one becomes a slice.
//
// Each entry carries the reason it still exists, so a reader can tell a deferral
// from a mistake:
//
//   - "workspace-probe": asks about the runtime's OWN bookkeeping (".izen",
//     ".git", a config file, a session slot). It is not about a mutation target,
//     and routing it through the kernel would be ceremony.
//   - "type-gate": asks what KIND of entry a path is (a directory, a mode to
//     preserve), not whether it exists.
//   - "target-existence": the real thing. A decision about whether a named
//     workspace target exists, feeding execution. Strangle next.
var knownExistenceBypasses = map[string]string{
	"internal/ui/agents.go:624":                      "workspace-probe: does .izen exist",
	"internal/ui/program.go:664":                     "workspace-probe: is the root a git repository",
	"internal/ui/update_init.go:125":                 "workspace-probe: config file at boot",
	"internal/ui/update_init.go:149":                 "workspace-probe: session slot at boot",
	"internal/ui/update_init.go:169":                 "workspace-probe: git dir at boot",
	"internal/ui/update_init.go:504":                 "workspace-probe: session slot during init",
	"internal/ui/utils.go:63":                        "target-existence: @file composer expansion",
	"internal/ui/utils.go:74":                        "target-existence: @file composer expansion",
	"internal/execution/executor.go:1069":            "target-existence: workspace evidence for context compilation",
	"internal/execution/capability/serve.go:253":     "type-gate: refusing to serve a non-directory",
	"internal/runtime/autonomy/adapter.go:777":       "target-existence: TargetExists/TargetAbsent evidence",
	"internal/runtime/autonomy/adapter.go:800":       "target-existence: TargetExistence pre-dispatch evidence",
	"internal/runtime/autonomy/preflight.go:526":     "target-existence: local dependency feasibility",
	"internal/runtime/executor/file_executor.go:104": "type-gate: preserving the existing file's permission bits",
	"internal/runtime/handlers/handlers.go:605":      "target-existence: filtering @file references",
	"internal/engine/context/providers.go:210":       "workspace-probe: git repository for context providers",
	"internal/engine/layer0/resolver.go:218":         "type-gate: refusing a root that is not a directory",
}

// TestKernelLock_SingleKernelEntryPoint keeps the bridge the only door.
func TestKernelLock_SingleKernelEntryPoint(t *testing.T) {
	root := repoRoot(t)
	var offenders []string

	err := filepath.Walk(filepath.Join(root, "internal"), func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if name := info.Name(); name != "." && strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		file, parseErr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if parseErr != nil {
			return nil //nolint:nilerr // an unparseable file cannot import anything
		}
		rel := relativeTo(t, root, path)
		dir := filepath.ToSlash(filepath.Dir(rel))
		for _, spec := range file.Imports {
			quoted := strings.Trim(spec.Path.Value, "`\"")
			for _, forbidden := range kernelImportPaths {
				if quoted != forbidden {
					continue
				}
				if dir == kernelBridgePackage {
					continue
				}
				offenders = append(offenders, fmt.Sprintf(
					"%s imports %s — reach the kernel through %s, or widen kernelBridgePackage if this really is a new seam",
					rel, quoted, kernelBridgePackage))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking internal/: %v", err)
	}

	if len(offenders) > 0 {
		t.Errorf("kernel bypass: %d caller(s) import the kernel directly:\n%s",
			len(offenders), strings.Join(offenders, "\n"))
	}
}

// TestKernelLock_NoUnregisteredWorkspaceExistenceDecision is the ratchet.
func TestKernelLock_NoUnregisteredWorkspaceExistenceDecision(t *testing.T) {
	root := repoRoot(t)
	found := scanExistenceDecisions(t, root)

	var unregistered, stale []string
	for site := range found {
		if _, ok := knownExistenceBypasses[site]; !ok {
			unregistered = append(unregistered, fmt.Sprintf("%s — %s", site, found[site]))
		}
	}
	for site, reason := range knownExistenceBypasses {
		if _, ok := found[site]; !ok {
			stale = append(stale, fmt.Sprintf("%s — allowlisted as %q but no longer exists there", site, reason))
		}
	}
	sort.Strings(unregistered)
	sort.Strings(stale)

	if len(unregistered) > 0 {
		t.Errorf("%d unregistered workspace existence decision(s).\n"+
			"A branch that decides whether a workspace target exists from a raw syscall has no event,\n"+
			"no state, no evidence and no verification behind it. Route it through %s:\n\n%s\n\n"+
			"If this is genuinely not a target-existence decision, add it to knownExistenceBypasses\n"+
			"with a reason of workspace-probe or type-gate — not target-existence.",
			len(unregistered), kernelBridgePackage, strings.Join(unregistered, "\n\n"))
	}

	if len(stale) > 0 {
		t.Errorf("%d stale allowlist entr(ies). The strangler's work list must shrink, never rot:\n%s",
			len(stale), strings.Join(stale, "\n"))
	}

	// A target-existence entry that has already been strangled should be promoted
	// to a probe or removed. This check keeps the two categories from drifting
	// into one another.
	for site, reason := range knownExistenceBypasses {
		if strings.HasPrefix(reason, "target-existence:") {
			continue
		}
		if strings.Contains(reason, "target-existence") {
			t.Errorf("allowlist entry %s mixes categories: %q", site, reason)
		}
	}
}

// TestKernelLock_MigratedResolutionRoutesThroughKernel pins the first slice.
//
// Slice 1 moved the autonomy build target resolution onto the kernel and deleted
// its os.Stat. These assertions are what make that deletion permanent: without
// them the next person to touch this file can quietly restore the syscall, and
// the function will look unchanged because it behaves identically on a
// well-behaved filesystem.
func TestKernelLock_MigratedResolutionRoutesThroughKernel(t *testing.T) {
	root := repoRoot(t)
	const migrated = "internal/ui/autonomy_target.go"

	if _, ok := knownExistenceBypasses[migrated+":53"]; ok {
		t.Errorf("%s is still allowlisted; slice 1 deleted that os.Stat, so the entry is stale", migrated)
	}

	if site, _ := scanFileExistenceDecisions(t, filepath.Join(root, migrated)); len(site) > 0 {
		t.Errorf("%s decides workspace existence with %s.\n"+
			"The autonomy build target resolution must route through %s so the file a\n"+
			"mutation lands on is backed by kernel evidence, verification and a PROVEN outcome.",
			migrated, strings.Join(relativeSites(t, root, site), ", "), kernelBridgePackage)
	}

	source, err := os.ReadFile(filepath.Join(root, migrated))
	if err != nil {
		t.Fatalf("reading %s: %v", migrated, err)
	}
	if !strings.Contains(string(source), kernelBridgePackage) {
		t.Errorf("%s no longer references %s; the resolution is not routed through the kernel any more",
			migrated, kernelBridgePackage)
	}
}

// ── scanning ────────────────────────────────────────────────────────────────

// scanExistenceDecisions returns every workspace existence decision in the locked
// packages, keyed by "file:line".
func scanExistenceDecisions(t *testing.T, root string) map[string]string {
	t.Helper()
	found := map[string]string{}
	for _, dir := range lockedExecutionDirs {
		abs := filepath.Join(root, filepath.FromSlash(dir))
		if _, err := os.Stat(abs); err != nil {
			t.Fatalf("locked package %q does not exist; the lock is silently covering nothing", dir)
		}
		scanned := 0
		err := filepath.Walk(abs, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				if name := info.Name(); name != "." && strings.HasPrefix(name, ".") {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			scanned++
			_, sites := scanFileExistenceDecisions(t, path)
			for site, snippet := range sites {
				relSite, relSnippet := relativeSite(t, root, site, snippet)
				found[relSite] = relSnippet
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walking %s: %v", dir, err)
		}
		if scanned == 0 {
			t.Fatalf("locked package %q contains no production Go files; the lock is covering nothing", dir)
		}
	}
	return found
}

// relativeSite rewrites an absolute scan site into the repo-relative "file:line"
// form the allowlist uses, so the list stays readable and portable.
func relativeSite(t *testing.T, root, site, snippet string) (string, string) {
	t.Helper()
	idx := strings.LastIndex(site, ":")
	abs, line := site[:idx], site[idx+1:]
	return filepath.ToSlash(relativeTo(t, root, abs)) + ":" + line, snippet
}

// relativeSites is relativeSite over a list, keeping failure messages free of
// machine-specific absolute paths.
func relativeSites(t *testing.T, root string, sites []string) []string {
	t.Helper()
	out := make([]string, 0, len(sites))
	for _, site := range sites {
		rel, _ := relativeSite(t, root, site, "")
		out = append(out, rel)
	}
	return out
}

// relativeTo expresses path relative to the module root.
func relativeTo(t *testing.T, root, path string) string {
	t.Helper()
	rel, err := filepath.Rel(root, path)
	if err != nil {
		t.Fatalf("relativising %s against %s: %v", path, root, err)
	}
	return rel
}

// scanFileExistenceDecisions returns the existence decisions in one file.
//
// The shape it looks for is a branch whose init or condition contains a stat call.
// Both positions count: `if _, err := os.Stat(p); err == nil` puts the call in the
// init statement, and `if os.Stat(p) == nil` puts it in the condition. Missing
// either is how a lock gets quietly defeated.
func scanFileExistenceDecisions(t *testing.T, path string) ([]string, map[string]string) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}

	sites := map[string]string{}
	ast.Inspect(file, func(node ast.Node) bool {
		branch, ok := node.(*ast.IfStmt)
		if !ok {
			return true
		}
		if call, found := firstExistenceCall(branch.Init); found {
			recordExistenceSite(fset, sites, path, branch, call)
		}
		if call, found := firstExistenceCall(branch.Cond); found {
			recordExistenceSite(fset, sites, path, branch, call)
		}
		return true
	})

	out := make([]string, 0, len(sites))
	for site := range sites {
		out = append(out, site)
	}
	sort.Strings(out)
	return out, sites
}

func recordExistenceSite(fset *token.FileSet, sites map[string]string, path string, branch *ast.IfStmt, call *ast.CallExpr) {
	line := fset.Position(branch.Pos()).Line
	site := fmt.Sprintf("%s:%d", path, line)
	if _, exists := sites[site]; exists {
		return
	}
	sites[site] = strings.TrimSpace(callName(call)) + " used in a branch at line " + strconv.Itoa(line)
}

func firstExistenceCall(root ast.Node) (*ast.CallExpr, bool) {
	if root == nil {
		return nil, false
	}
	var hit *ast.CallExpr
	ast.Inspect(root, func(node ast.Node) bool {
		if hit != nil {
			return false
		}
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if name := callName(call); existencePrimitives[name] != "" {
			hit = call
			return false
		}
		return true
	})
	return hit, hit != nil
}

func callName(call *ast.CallExpr) string {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	receiver, ok := selector.X.(*ast.Ident)
	if !ok {
		return ""
	}
	return receiver.Name + "." + selector.Sel.Name
}
