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
// So there are six locks here, and each one closes a different way back in:

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
//
//  4. TestKernelLock_MigratedToolCallsOwnNoFilesystemOfTheirOwn — the native
//     tool-call path cannot grow a syscall back. This one is deliberately
//     stronger than the other two per-slice locks: it does not forbid specific
//     calls, it forbids ALL of them, because after slices 2 and 3 this file has
//     no business touching the filesystem at all.
//
//  5. TestKernelLock_CanonicalMutationRoutesThroughKernel — the TUI
//     RuntimeExecutor's final primitive cannot regress to os.WriteFile.
//
//  6. TestKernelLock_PathAMutationRoutesThroughKernel and
//     TestKernelLock_OrchestrateMutationRoutesThroughKernel — the two
//     migrated commit paths (`izen run` and `izen orchestrate`) cannot regress
//     to a direct mutation primitive. Each names the functions that place
//     bytes, so a rollback half cannot drift away from its commit half.

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

// workspacePrimitives are every call that touches the workspace filesystem
// directly: deciding that a path exists, reading its bytes, or putting bytes
// there.
//
// The existence lock above cannot be widened to this set without becoming noise —
// os.WriteFile appears in dozens of places that write runtime bookkeeping, and a
// lock that fires on all of them gets deleted rather than satisfied. That is why
// this vocabulary is enforced against ONE file instead of a package tree.
//
// The file it is enforced against is internal/execution/toolcalls.go, and that is
// not arbitrary. Slices 2 and 3 moved the native tool-call path's read and write
// onto the kernel, which leaves it with no filesystem access of its own: it asks
// the seam for content, and it asks the seam to commit. A file in that state has
// exactly one correct future — keep asking — so any direct syscall appearing there
// is a regression to the old runtime, whatever its intent.
var workspacePrimitives = map[string]string{
	"os.Stat":        "os.Stat",
	"os.Lstat":       "os.Lstat",
	"os.ReadFile":    "os.ReadFile",
	"os.WriteFile":   "os.WriteFile",
	"os.Create":      "os.Create",
	"os.CreateTemp":  "os.CreateTemp",
	"os.OpenFile":    "os.OpenFile",
	"os.Rename":      "os.Rename",
	"os.Remove":      "os.Remove",
	"os.RemoveAll":   "os.RemoveAll",
	"os.MkdirAll":    "os.MkdirAll",
	"os.Mkdir":       "os.Mkdir",
	"os.Open":        "os.Open",
	"io/fs.ReadFile": "fs.ReadFile",
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
	"internal/ui/program.go:668":                     "workspace-probe: is the root a git repository",
	"internal/ui/update_init.go:125":                 "workspace-probe: config file at boot",
	"internal/ui/update_init.go:149":                 "workspace-probe: session slot at boot",
	"internal/ui/update_init.go:169":                 "workspace-probe: git dir at boot",
	"internal/ui/update_init.go:504":                 "workspace-probe: session slot during init",
	"internal/ui/utils.go:63":                        "target-existence: @file composer expansion",
	"internal/ui/utils.go:74":                        "target-existence: @file composer expansion",
	"internal/execution/executor.go:1076":            "target-existence: workspace evidence for context compilation",
	"internal/execution/capability/serve.go:253":     "type-gate: refusing to serve a non-directory",
	"internal/runtime/autonomy/adapter.go:777":       "target-existence: TargetExists/TargetAbsent evidence",
	"internal/runtime/autonomy/adapter.go:800":       "target-existence: TargetExistence pre-dispatch evidence",
	"internal/runtime/autonomy/preflight.go:526":     "target-existence: local dependency feasibility",
	"internal/runtime/executor/file_executor.go:148": "type-gate: preserving the existing file's permission bits",
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

// TestKernelLock_MigratedToolCallsOwnNoFilesystemOfTheirOwn pins slices 2 and 3.
//
// Slices 2 and 3 moved the native LLM tool-call path onto the kernel: buffering a
// `write_file` call now reads its baseline through an adjudicated read execution,
// and approving it now commits through an adjudicated mutation execution. The old
// os.ReadFile / os.WriteFile pair in that file is gone.
//
// The assertions below are what make the deletion permanent. Each one fails when
// violated:
//
//   - a direct workspace syscall in the file, because the whole point of the
//     slice was that the kernel owns that capability. The runtime would still
//     behave identically on a cooperative filesystem, which is exactly why the
//     deletion needs a lock rather than a reviewer;
//   - the file no longer importing the bridge, because that is what a "temporary
//     direct read while we migrate" looks like once the migration is called done;
//   - the removed second write_file implementation coming back, because two
//     implementations of one tool is how a deletion quietly un-deletes itself.
//
// It also asserts the seam the migrated path actually uses is reachable, so this
// lock cannot be satisfied by removing the migration instead of preserving it.
func TestKernelLock_MigratedToolCallsOwnNoFilesystemOfTheirOwn(t *testing.T) {
	root := repoRoot(t)
	const migrated = "internal/execution/toolcalls.go"

	source, err := os.ReadFile(filepath.Join(root, migrated))
	if err != nil {
		t.Fatalf("reading %s: %v", migrated, err)
	}
	text := string(source)

	if sites := scanDirectWorkspacePrimitives(filepath.Join(root, migrated)); len(sites) > 0 {
		t.Errorf("%s reaches the filesystem directly: %s.\n"+
			"Reads and writes on the native tool-call path are kernel executions routed\n"+
			"through %s: a read is an OBSERVE contract adjudicated from file.read\n"+
			"evidence, and the approved write is a CREATE/PATCH execution adjudicated\n"+
			"from file.write evidence and re-read from disk by a verifier that did not\n"+
			"write it. A syscall here has no event, no state, no evidence and no\n"+
			"verification behind it, and it reaches the filesystem with no grant at all.",
			migrated, strings.Join(sites, ", "), kernelBridgePackage)
	}

	if !strings.Contains(text, kernelBridgePackage) {
		t.Errorf("%s no longer imports %s; the tool-call path is not routed through the kernel any more",
			migrated, kernelBridgePackage)
	}

	// The deleted second implementation. DispatchToolCalls wrote straight to disk
	// with no grant, no evidence and no verification, and nothing called it. It was
	// deleted as part of the slice precisely because leaving it would have left two
	// competing write_file implementations in one file, and a future reader would
	// have had no way to know which one the product used.
	for _, dead := range []string{"func DispatchToolCalls", "func dispatchWriteFile", "func dispatchApplyPatch"} {
		if strings.Contains(text, dead) {
			t.Errorf("%s reintroduces %s.\n"+
				"That path wrote to the workspace with no grant, no evidence and no\n"+
				"verification, and had no callers. If a direct write is genuinely needed,\n"+
				"add it to the kernel as a capability instead.", migrated, dead)
		}
	}

	// The seam must still exist. A lock that can be satisfied by deleting the
	// migration is not a lock; it is a way of declaring the old path unreachable
	// by removing the new one.
	for _, required := range []string{
		filepath.Join("internal", "kernelbridge", "apply.go"),
		filepath.Join("internal", "kernelbridge", "read.go"),
	} {
		if _, err := os.Stat(filepath.Join(root, required)); err != nil {
			t.Errorf("the mutation/read seam is missing: %v\n"+
				"%s is the only sanctioned path from the legacy tree to the kernel, so\n"+
				"losing it would leave the migrated call sites unable to compile rather\n"+
				"than restoring a safe path.", err, required)
		}
	}
}

// scanDirectWorkspacePrimitives returns every direct workspace syscall in one
// file, as "file:line" sites, with the call name recorded.
func scanDirectWorkspacePrimitives(path string) []string {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		// An unparseable file cannot be shown to be clean, so it is reported as a
		// site rather than skipped. A lock that passes because it could not read
		// its subject is worse than no lock.
		return []string{fmt.Sprintf("%s (unparseable: %v)", path, err)}
	}

	sites := map[string]struct{}{}
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		name := qualifiedCallName(call)
		if workspacePrimitives[name] != "" {
			line := fset.Position(call.Pos()).Line
			sites[fmt.Sprintf("%s:%d (%s)", path, line, name)] = struct{}{}
		}
		return true
	})

	out := make([]string, 0, len(sites))
	for site := range sites {
		out = append(out, site)
	}
	sort.Strings(out)
	return out
}

// qualifiedCallName renders a call's callee path, so `os.Stat` and `fs.ReadFile`
// are both nameable. It deliberately walks the full selector chain rather than
// only its last segment: `os.Remove` and `self.Remove` must not be conflated, and
// the receiver name is what makes that distinction.
func qualifiedCallName(call *ast.CallExpr) string {
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

// kernelMutationPrimitives are the direct calls that change the workspace — put
// bytes there, remove them, or create the directory that holds them.
//
// The separate `workspacePrimitives` vocabulary above is deliberately wider (it
// includes reads) and is enforced against a whole file. This one is narrower on
// purpose: it is enforced against ONE method, the canonical mutation seam, where
// reads for patch derivation are legitimate but a write is a bypass.
var kernelMutationPrimitives = map[string]string{
	"os.WriteFile":  "os.WriteFile",
	"os.Create":     "os.Create",
	"os.CreateTemp": "os.CreateTemp",
	"os.OpenFile":   "os.OpenFile",
	"os.Rename":     "os.Rename",
	"os.Remove":     "os.Remove",
	"os.RemoveAll":  "os.RemoveAll",
	"os.MkdirAll":   "os.MkdirAll",
	"os.Mkdir":      "os.Mkdir",
}

// TestKernelLock_CanonicalMutationRoutesThroughKernel pins the RuntimeExecutor's
// final filesystem primitive to the bridge.
//
// `internal/execution/patch.go` resolves what a target should hold and then must
// place it through `internal/kernelbridge`. Before this lock, the method placed
// bytes with a bare os.WriteFile: no grant, no event, no evidence, no
// verification, and a symlink out of the workspace resolved by the kernel was
// followed instead. These assertions fail when that regresses, in either
// direction: a direct mutation primitive reappearing in the method, the bridge
// import being dropped, or the method no longer calling the seam.
func TestKernelLock_CanonicalMutationRoutesThroughKernel(t *testing.T) {
	root := repoRoot(t)
	const migrated = "internal/execution/patch.go"
	abs := filepath.Join(root, migrated)

	source, err := os.ReadFile(abs)
	if err != nil {
		t.Fatalf("reading %s: %v", migrated, err)
	}
	if !strings.Contains(string(source), kernelBridgePackage) {
		t.Errorf("%s no longer imports %s; the canonical mutation seam is not routed through the kernel any more",
			migrated, kernelBridgePackage)
	}

	// The method that places the resolved bytes must call the kernel seam.
	if calls := functionCalls(t, abs, "apply"); !calls["pm.commitThroughKernel"] {
		t.Errorf("%s: PatchManager.apply no longer calls pm.commitThroughKernel; the canonical write does not reach the kernel", migrated)
	}
	// ...and the seam it calls must be the bridge.
	if calls := functionCalls(t, abs, "commitThroughKernel"); !calls["kernelbridge.Apply"] {
		t.Errorf("%s: PatchManager.commitThroughKernel no longer calls kernelbridge.Apply", migrated)
	}

	// The method itself must own no direct workspace mutation. Reads for patch
	// derivation are allowed; a write or a directory creation is not.
	if sites := scanFunctionMutationPrimitives(t, abs, "apply"); len(sites) > 0 {
		t.Errorf("%s: PatchManager.apply reaches the filesystem directly: %s.\n"+
			"The resolved bytes must be placed by %s under an explicit grant, so they\n"+
			"are written by the confined capability and re-read from disk by a verifier\n"+
			"that did not write them. A direct write here has no event, no state, no\n"+
			"evidence and no verification behind it.",
			migrated, strings.Join(sites, ", "), kernelBridgePackage)
	}
}

// functionCalls returns the qualified call names used inside the named top-level
// function or method, so a lock can constrain one method instead of a file.
func functionCalls(t *testing.T, path, name string) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	out := map[string]bool{}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil || fn.Name.Name != name {
			continue
		}
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			if q := qualifiedCallName(call); q != "" {
				out[q] = true
			}
			return true
		})
	}
	return out
}

// scanFunctionMutationPrimitives returns every direct workspace-mutation call in
// one named function, as "file:line (call)" sites.
func scanFunctionMutationPrimitives(t *testing.T, path, name string) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return []string{fmt.Sprintf("%s (unparseable: %v)", path, err)}
	}
	var out []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil || fn.Name.Name != name {
			continue
		}
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			callerName := qualifiedCallName(call)
			if kernelMutationPrimitives[callerName] == "" {
				return true
			}
			out = append(out, fmt.Sprintf("%s:%d (%s)", path, fset.Position(call.Pos()).Line, callerName))
			return true
		})
	}
	sort.Strings(out)
	return out
}

// pathAExecutionFiles are the files that make up Path A: the workspace mutation
// route `izen run` takes through the app pipeline into the substrate's proposal
// executor. The lock is scoped to exactly these files, because that is the
// architectural execution path being pinned — not the package, not the
// repository, and not the rest of the substrate.
var pathAExecutionFiles = []string{
	"internal/runtime/substrate/engine.go",
	"internal/runtime/substrate/kernelcommit.go",
	"internal/runtime/substrate/snapshot.go",
}

// pathARecoverySites are the only functions in Path A permitted to write to the
// workspace outside the kernel, with the reason each one is allowed to.
//
// This is the classification the migration was built on, made executable. The
// substrate package still contains os.WriteFile, and it always will: it owns the
// recovery path and the proof artifact, neither of which is an execution
// authority. What must never happen is one of them growing a second way to place
// bytes a proposal asked for — so the list is exact in both directions. A new
// direct writer fails as an unregistered site; deleting one of these fails as a
// missing recovery path, which would mean rollback had quietly stopped working.
var pathARecoverySites = map[string]string{
	"writeEvidenceProof": "bookkeeping: records what the transaction did under .izen",
	"writeForRecovery":   "recovery: restores an original the transaction overwrote",
	"removeForRecovery":  "recovery: deletes a file the transaction itself created",
}

// TestKernelLock_PathAMutationRoutesThroughKernel pins Path A to the kernel and
// forbids it from regressing to a direct mutation primitive.
//
// Path A used to place a committed FILE_WRITE and FILE_DELETE through the
// substrate's FilePort adapter, which was a bare os.WriteFile and os.Remove: no
// grant, no event, no state, no evidence and no verification. It behaves
// identically to a kernel execution on a cooperative filesystem, which is exactly
// why the deletion needs a lock rather than a reviewer — nothing in the product
// would look different if it came back.
//
// The assertions fail in both directions. A direct primitive or a FilePort write
// reappearing in the operation loop or in the seam fails as a bypass. The
// operation loop stopping to call the seam, or the seam stopping to call the
// bridge, fails as a migration that was deleted rather than preserved — because a
// lock satisfiable by removing the new path is just a declaration that the old
// path is unreachable.
func TestKernelLock_PathAMutationRoutesThroughKernel(t *testing.T) {
	root := repoRoot(t)
	const engine = "internal/runtime/substrate/engine.go"
	const seam = "internal/runtime/substrate/kernelcommit.go"
	engineAbs := filepath.Join(root, engine)
	seamAbs := filepath.Join(root, seam)

	// ── The seam exists, and the loop still routes through it ─────────────
	if _, err := os.Stat(seamAbs); err != nil {
		t.Fatalf("%s is missing: %v\n"+
			"That file is where Path A crosses the kernel. Without it the committed\n"+
			"write and delete have no sanctioned route back to %s.", seam, err, kernelBridgePackage)
	}

	loop := functionCalls(t, engineAbs, "Execute")
	if !loop["s.commitWrite"] {
		t.Errorf("%s: ConcreteSubstrate.Execute no longer calls s.commitWrite; the\n"+
			"committed write does not reach the kernel", engine)
	}
	if !loop["s.commitDelete"] {
		t.Errorf("%s: ConcreteSubstrate.Execute no longer calls s.commitDelete; the\n"+
			"committed delete does not reach the kernel", engine)
	}

	// ── The seam reaches the bridge ───────────────────────────────────────
	if calls := functionCalls(t, seamAbs, "commitWrite"); !calls["kernelbridge.Apply"] {
		t.Errorf("%s: commitWrite no longer calls kernelbridge.Apply", seam)
	}
	if calls := functionCalls(t, seamAbs, "commitDelete"); !calls["kernelbridge.Delete"] {
		t.Errorf("%s: commitDelete no longer calls kernelbridge.Delete", seam)
	}

	// ── The execution path owns no direct mutation ───────────────────────
	//
	// These four functions are Path A's whole execution surface: the operation
	// loop and the two commit helpers. None of them may reach the filesystem.
	// Snapshot reads and rollback writes live elsewhere on purpose, so this
	// assertion is about the commit path rather than about the package.
	for _, site := range []struct{ file, fn string }{
		{engine, "Execute"},
		{seam, "commitWrite"},
		{seam, "commitDelete"},
	} {
		if offenders := scanFunctionDirectMutation(t, root, site.file, site.fn); len(offenders) > 0 {
			t.Errorf("%s: %s places bytes on the workspace directly: %s.\n"+
				"Path A's final filesystem primitive is the Runtime Kernel. A write or a\n"+
				"removal here carries no grant, no event, no state, no evidence and no\n"+
				"verification, and a destination a symlink redirects out of the workspace\n"+
				"is followed by the syscall rather than refused. Reach %s instead:\n"+
				"commitWrite / commitDelete for an effect a proposal asked for,\n"+
				"writeForRecovery / removeForRecovery for undoing one, and\n"+
				"writeEvidenceProof for the .izen proof artifact.",
				site.file, site.fn, strings.Join(offenders, ", "), kernelBridgePackage)
		}
	}

	// ── Recovery and bookkeeping sites are exact, in both directions ─────
	//
	// Without this the assertion above could be satisfied by deleting the
	// recovery path entirely, which would leave the transaction unable to undo a
	// partial batch while looking perfectly clean to every other check.
	found := map[string]string{}
	for _, rel := range pathAExecutionFiles {
		abs := filepath.Join(root, rel)
		if _, err := os.Stat(abs); err != nil {
			t.Fatalf("%s is missing: %v; the lock is silently covering nothing", rel, err)
		}
		for _, fn := range functionsUsingPortMutation(t, abs) {
			found[fn] = rel
		}
	}
	for fn := range found {
		if _, ok := pathARecoverySites[fn]; !ok {
			t.Errorf("%s: %s writes to the workspace through the substrate FilePort and is\n"+
				"not a registered recovery or bookkeeping site.\n"+
				"If it places a byte a proposal asked for, it is a second execution\n"+
				"authority and must go through kernelbridge. If it genuinely undoes or\n"+
				"records one, add it to pathARecoverySites with that reason.",
				found[fn], fn)
		}
	}
	for fn, reason := range pathARecoverySites {
		if _, ok := found[fn]; !ok {
			t.Errorf("%s no longer writes through the FilePort (%s).\n"+
				"That site is registered as recovery or bookkeeping. If it was removed\n"+
				"deliberately, delete its entry here too; if not, the rollback or the\n"+
				"proof artifact has stopped working and the lock above would not notice.",
				fn, reason)
		}
	}
}

// selectorChain renders a call's full callee path, so `s.delegate.file.Write` is
// nameable as `s.delegate.file.Write` rather than as a bare `Write`.
//
// qualifiedCallName deliberately reads only the last selector, which is right
// for distinguishing `os.Remove` from `self.Remove`. It is not enough here: a
// bypass reintroduced through the substrate's own port field is three selectors
// deep, and a check that stopped at the last segment would see an unqualified
// `Write` and match nothing.
func selectorChain(call *ast.CallExpr) string {
	var parts []string
	expr := call.Fun
	for {
		sel, ok := expr.(*ast.SelectorExpr)
		if !ok {
			break
		}
		parts = append([]string{sel.Sel.Name}, parts...)
		expr = sel.X
	}
	ident, ok := expr.(*ast.Ident)
	if !ok {
		return ""
	}
	return ident.Name + "." + strings.Join(parts, ".")
}

// portMutationCall reports whether a callee path is a mutating method on the
// substrate's FilePort.
func portMutationCall(chain string) bool {
	return strings.HasSuffix(chain, ".file.Write") || strings.HasSuffix(chain, ".file.Remove")
}

// scanFunctionDirectMutation returns every call in one named function that would
// place or remove bytes on the workspace without going through the kernel —
// either a direct os primitive or a mutating FilePort method.
func scanFunctionDirectMutation(t *testing.T, root, rel, name string) []string {
	t.Helper()
	abs := filepath.Join(root, rel)
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, abs, nil, 0)
	if err != nil {
		return []string{fmt.Sprintf("%s (unparseable: %v)", rel, err)}
	}
	var out []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil || fn.Name.Name != name {
			continue
		}
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			chain := selectorChain(call)
			if chain == "" {
				return true
			}
			if kernelMutationPrimitives[chain] == "" && !portMutationCall(chain) {
				return true
			}
			out = append(out, fmt.Sprintf("%s:%d (%s)", rel, fset.Position(call.Pos()).Line, chain))
			return true
		})
	}
	sort.Strings(out)
	return out
}

// functionsUsingPortMutation names every function in one file that calls a
// mutating FilePort method.
func functionsUsingPortMutation(t *testing.T, abs string) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, abs, nil, 0)
	if err != nil {
		// A file this lock cannot read is a file it cannot clear. Failing is the
		// only honest answer: a silent nil would report "no direct writers" for a
		// file it never parsed.
		t.Fatalf("parsing %s: %v", abs, err)
	}
	var out []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		hits := false
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			if call, ok := node.(*ast.CallExpr); ok && portMutationCall(selectorChain(call)) {
				hits = true
			}
			return true
		})
		if hits {
			out = append(out, fn.Name.Name)
		}
	}
	sort.Strings(out)
	return out
}

// orchestrateExecutionFiles are the files that make up the orchestrate path:
// the workspace mutation route `izen orchestrate` and `izen prompt` take
// through FileExecutor — cmd/izen/orchestrate.go → cli.Wire → cli.Stack.Run →
// orchestrator.RunCycle → FileExecutor.Commit / FileExecutor.Rollback.
//
// It is named for its entry point rather than for a letter because the
// repository already uses "stack B" for something else entirely (the LEA
// layered engine, which places no bytes). The lock is scoped to exactly these
// two files, because that is the architectural execution path being pinned —
// not the package, which still legitimately reads the filesystem to take a
// snapshot.
var orchestrateExecutionFiles = []string{
	"internal/runtime/executor/file_executor.go",
	"internal/runtime/executor/kernelcommit.go",
}

// orchestrateMutationSites is every function on the orchestrate path that
// places or removes bytes on the workspace, paired with the seam it is
// required to call instead.
//
// It is a map so each entry can only fail in one direction per problem. A
// function that places bytes without its recorded call is a bypass. A
// function that IS recorded here but no longer exists is a migration that was
// quietly deleted rather than preserved — and a lock satisfiable by removing
// the new path is just a declaration that the old path is unreachable.
var orchestrateMutationSites = map[string]string{
	"Commit":              "e.commitThroughKernel",
	"Rollback":            "e.restoreThroughKernel",
	"placeThroughKernel":  "kernelbridge.Apply",
	"removeThroughKernel": "kernelbridge.Delete",
}

// TestKernelLock_OrchestrateMutationRoutesThroughKernel pins the orchestrate
// path to the kernel and forbids it from regressing to a direct mutation
// primitive.
//
// FileExecutor used to place a commit with a hand-rolled temp-file-and-rename
// protocol — os.MkdirAll, os.CreateTemp, os.Chmod, os.Rename — and undo it
// with os.WriteFile, os.Chmod and os.Remove. No grant, no event, no state, no
// evidence, no verification, and a symlink out of the workspace followed by
// the syscall rather than refused. It behaves identically to a kernel
// execution on a cooperative filesystem, which is exactly why the deletion
// needs a lock rather than a reviewer — nothing in the product would look
// different if it came back.
//
// It is also the one path whose transaction the migration had to leave intact,
// so the lock covers both directions of it. Commit and Rollback stay Core's:
// they own the snapshot, the rollback policy, and the decision that a failure
// rolls back at all. Only the final filesystem effect moved. A lock that
// pinned the files but not the functions would let either half drift, and a
// half-migrated transaction is the failure that is hardest to see — the commit
// still reports an error whether or not anything was undone.
func TestKernelLock_OrchestrateMutationRoutesThroughKernel(t *testing.T) {
	root := repoRoot(t)
	const (
		core = "internal/runtime/executor/file_executor.go"
		seam = "internal/runtime/executor/kernelcommit.go"
	)

	for _, rel := range orchestrateExecutionFiles {
		abs := filepath.Join(root, rel)
		if _, err := os.Stat(abs); err != nil {
			t.Fatalf("%s is missing: %v\n"+
				"That file is where the orchestrate path crosses the kernel. Without it\n"+
				"the committed write and the rollback have no sanctioned route back to %s.", rel, err, kernelBridgePackage)
		}
		source, err := os.ReadFile(abs)
		if err != nil {
			t.Fatalf("reading %s: %v", rel, err)
		}
		if !strings.Contains(string(source), kernelBridgePackage) {
			t.Errorf("%s no longer imports %s; the orchestrate path's mutation is not routed through the kernel any more", rel, kernelBridgePackage)
		}
	}

	// ── The execution path still calls its seams ────────────────────────
	//
	// Both directions are checked. A Commit that stopped asking the kernel to
	// write would be a bypass; a Rollback that stopped asking it to restore
	// would leave a failed commit with nothing to undo it, which reads as
	// working right up until a write fails.
	coreAbs := filepath.Join(root, core)
	if calls := functionCalls(t, coreAbs, "Commit"); !calls["e.commitThroughKernel"] {
		t.Errorf("%s: FileExecutor.Commit no longer calls e.commitThroughKernel; the\n"+
			"committed write does not reach the kernel", core)
	}
	rollbackCalls := functionCalls(t, coreAbs, "Rollback")
	if !rollbackCalls["e.restoreThroughKernel"] {
		t.Errorf("%s: FileExecutor.Rollback no longer calls e.restoreThroughKernel;\n"+
			"a failed commit would have no way to put the original bytes back", core)
	}
	if !rollbackCalls["e.removeThroughKernel"] {
		t.Errorf("%s: FileExecutor.Rollback no longer calls e.removeThroughKernel;\n"+
			"a commit that created a file could no longer be undone", core)
	}

	seamAbs := filepath.Join(root, seam)
	if calls := functionCalls(t, seamAbs, "placeThroughKernel"); !calls["kernelbridge.Apply"] {
		t.Errorf("%s: placeThroughKernel no longer calls kernelbridge.Apply", seam)
	}
	if calls := functionCalls(t, seamAbs, "removeThroughKernel"); !calls["kernelbridge.Delete"] {
		t.Errorf("%s: removeThroughKernel no longer calls kernelbridge.Delete", seam)
	}

	// ── The execution path owns no direct mutation ───────────────────────
	//
	// Snapshot reads are legitimate and are not in this vocabulary:
	// PrepareSnapshot has to read the target to have something to restore.
	// What is forbidden is placing or removing bytes without crossing the
	// kernel, and that is exactly what these functions used to do.
	for fn, required := range orchestrateMutationSites {
		file := core
		if fn == "placeThroughKernel" || fn == "removeThroughKernel" {
			file = seam
		}
		if offenders := scanFunctionDirectMutation(t, root, file, fn); len(offenders) > 0 {
			t.Errorf("%s: %s places bytes on the workspace directly: %s.\n"+
				"The orchestrate path's final filesystem primitive is the Runtime\n"+
				"Kernel. A direct write or removal here carries no grant, no event,\n"+
				"no state, no evidence and no verification, and a destination a\n"+
				"symlink redirects out of the workspace is followed by the syscall\n"+
				"rather than refused. It must reach %s through %s instead.", file, fn, strings.Join(offenders, ", "),
				kernelBridgePackage, required)
		}
	}

	// ── Every recorded seam still exists ─────────────────────────────────
	//
	// Without this the assertions above could be satisfied by deleting the
	// migration outright, which would leave FileExecutor with no way to place
	// a byte while looking perfectly clean to every other check.
	seamFns := map[string]bool{}
	for _, rel := range orchestrateExecutionFiles {
		for _, fn := range declaredFuncNames(filepath.Join(root, rel)) {
			seamFns[fn] = true
		}
	}
	for fn := range orchestrateMutationSites {
		if !seamFns[fn] {
			t.Errorf("the orchestrate path no longer defines %s.\n"+
				"That function is registered as its only route to the kernel. If it was\n"+
				"removed deliberately, delete its entry from orchestrateMutationSites\n"+
				"too; if not, the migration has been deleted rather than preserved.",
				fn)
		}
	}
}

// declaredFuncNames returns every top-level function and method name defined
// in one file.
func declaredFuncNames(path string) []string {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return nil
	}
	var out []string
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok {
			out = append(out, fn.Name.Name)
		}
	}
	return out
}

// functionChainCalls returns the full selector chains of every call inside the
// named top-level function or method. Unlike functionCalls it uses
// selectorChain, so a call through a field (`t.exec.Execute`) is nameable
// rather than reduced to a bare selector that matches nothing.
func functionChainCalls(t *testing.T, path, name string) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	out := map[string]bool{}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil || fn.Name.Name != name {
			continue
		}
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			if chain := selectorChain(call); chain != "" {
				out[chain] = true
			}
			return true
		})
	}
	return out
}

// brownfieldExecutionFiles are the files that make up the brownfield execution
// path: `izen run`'s brownfield branch through the planner into the Core
// authority. The lock is scoped to exactly these files (plus the shared graph
// dispatch below), because that is the architectural execution path being
// pinned — not the package, not the repository, and not the resource
// infrastructure that legitimately reads and snapshots.
var brownfieldExecutionFiles = []string{
	"internal/planner/brownfield/brownfield.go",
	"internal/planner/brownfield/coremutation.go",
}

// TestKernelLock_BrownfieldMutationRoutesThroughKernel pins the brownfield
// mutation path to the Core authority and forbids it from regressing to a
// direct mutation primitive.
//
// Before this lock, every brownfield write node called
// file.FileResource.Write, a bare os.WriteFile: no Core authorization, no
// transaction owner, no snapshot, no rollback, no kernel grant, no primitive
// evidence and no verification. The workspace mutation came straight out of
// the graph/resource layer on any `izen run` against an existing workspace.
//
// The assertions fail in both directions. A direct primitive or a FilePort
// write reappearing in the execution files fails as a bypass. The graph
// dispatch stopping to prefer the context-aware contract, the planner
// stopping to route through coreMutationTarget, the pipeline dropping the
// authority binding, or the seam stopping to call Execute all fail as a
// migration that was deleted rather than preserved — because a lock
// satisfiable by removing the new path is just a declaration that the old path
// is unreachable.
func TestKernelLock_BrownfieldMutationRoutesThroughKernel(t *testing.T) {
	root := repoRoot(t)
	const planner = "internal/planner/brownfield/brownfield.go"
	const seam = "internal/planner/brownfield/coremutation.go"
	const pipeline = "internal/app/plan.go"
	const dispatch = "internal/graph/node.go"

	// ── The seam exists and reaches the Core authority ───────────────────
	seamAbs := filepath.Join(root, seam)
	if _, err := os.Stat(seamAbs); err != nil {
		t.Fatalf("%s is missing: %v\n"+
			"That file is where the brownfield graph hands its intent to the Core\n"+
			"execution authority. Without it the write has no sanctioned route to %s.",
			seam, err, kernelBridgePackage)
	}
	source, err := os.ReadFile(seamAbs)
	if err != nil {
		t.Fatalf("reading %s: %v", seam, err)
	}
	if !strings.Contains(string(source), "internal/runtime/substrate") {
		t.Errorf("%s no longer binds the brownfield write to the Core substrate authority", seam)
	}
	// The chain WriteContext → submit → exec.Execute is what makes the effect
	// Core's rather than the graph's.
	if calls := functionChainCalls(t, seamAbs, "WriteContext"); !calls["t.submit"] {
		t.Errorf("%s: coreMutationTarget.WriteContext no longer calls t.submit; the write does not reach Core", seam)
	}
	if calls := functionChainCalls(t, seamAbs, "DeleteContext"); !calls["t.submit"] {
		t.Errorf("%s: coreMutationTarget.DeleteContext no longer calls t.submit; the delete does not reach Core", seam)
	}
	if calls := functionChainCalls(t, seamAbs, "submit"); !calls["t.exec.Execute"] {
		t.Errorf("%s: coreMutationTarget.submit no longer calls the Core authority's Execute", seam)
	}

	// The seam must still exist in both directions. A missing method means the
	// migration was removed rather than preserved.
	seamFuncs := map[string]bool{}
	for _, fn := range declaredFuncNames(seamAbs) {
		seamFuncs[fn] = true
	}
	for _, fn := range []string{"WriteContext", "DeleteContext", "submit"} {
		if !seamFuncs[fn] {
			t.Errorf("%s no longer defines %s.\n"+
				"That method is the brownfield write's only route to the Core authority.\n"+
				"If it was removed deliberately, the mutation has no authority at all.", seam, fn)
		}
	}

	// ── The planner routes every file target through the seam ────────────
	plannerAbs := filepath.Join(root, planner)
	planCalls := functionCalls(t, plannerAbs, "Plan")
	if !planCalls["p.fileTarget"] {
		t.Errorf("%s: BrownfieldPlanner.Plan no longer lowers artifacts through p.fileTarget;\n"+
			"it would bind write nodes to a raw resource again", planner)
	}
	if calls := functionCalls(t, plannerAbs, "defaultRepair"); !calls["p.fileTarget"] {
		t.Errorf("%s: BrownfieldPlanner.defaultRepair no longer routes repairs through p.fileTarget", planner)
	}
	plannerSource, err := os.ReadFile(plannerAbs)
	if err != nil {
		t.Fatalf("reading %s: %v", planner, err)
	}
	if !strings.Contains(string(plannerSource), "coreMutationTarget{") {
		t.Errorf("%s: fileTarget no longer wraps the file resource in coreMutationTarget", planner)
	}
	if !strings.Contains(string(plannerSource), "ErrNoMutationAuthority") {
		t.Errorf("%s: fileTarget no longer fails closed without a Core authority", planner)
	}

	// ── The pipeline binds the authority ─────────────────────────────────
	pipelineAbs := filepath.Join(root, pipeline)
	if calls := functionCalls(t, pipelineAbs, "plan"); !calls["brownfield.WithMutationExecutor"] {
		t.Errorf("%s: Pipeline.plan no longer binds the Core substrate to the brownfield planner;\n"+
			"brownfield writes would fail closed (or, worse, if the check were removed, bypass Core)",
			pipeline)
	}

	// ── The graph dispatch prefers the context-aware Core contract ───────
	dispatchAbs := filepath.Join(root, dispatch)
	if calls := functionCalls(t, dispatchAbs, "executeWriteFile"); !calls["w.WriteContext"] {
		t.Errorf("%s: OpNode.executeWriteFile no longer prefers the context-aware Core contract;\n"+
			"a Core-routed target would be written through the contextless fileWriter fallback",
			dispatch)
	}
	if calls := functionCalls(t, dispatchAbs, "executeDeleteFile"); !calls["d.DeleteContext"] {
		t.Errorf("%s: OpNode.executeDeleteFile no longer prefers the context-aware Core contract", dispatch)
	}

	// ── The execution path owns no direct mutation ───────────────────────
	//
	// None of these files may reach the workspace filesystem directly. Reads
	// for derivation and snapshots are not in this vocabulary; what is
	// forbidden is placing bytes without crossing Core.
	for _, rel := range brownfieldExecutionFiles {
		abs := filepath.Join(root, rel)
		if _, err := os.Stat(abs); err != nil {
			t.Fatalf("%s is missing: %v; the lock is silently covering nothing", rel, err)
		}
		if sites := scanDirectWorkspacePrimitives(abs); len(sites) > 0 {
			t.Errorf("%s reaches the filesystem directly: %s.\n"+
				"The brownfield path's final filesystem primitive is the Core authority.\n"+
				"A direct write or removal here carries no grant, no event, no state, no\n"+
				"evidence and no verification, and a destination a symlink redirects out of\n"+
				"the workspace is followed by the syscall rather than refused. Reach %s\n"+
				"through %s instead.", rel, strings.Join(sites, ", "), kernelBridgePackage, seam)
		}
	}

	// Restoring through the adapter would call file.FileResource.Restore, which
	// writes with a bare os.WriteFile. Assert the adapter refuses to restore
	// instead of delegating, so the seam file owns no mutation capable call.
	if strings.Contains(string(source), ".base.Restore(") {
		t.Errorf("%s delegates Restore to the base file resource, reintroducing a direct writer", seam)
	}
	if strings.Contains(string(source), ".base.Write(") {
		t.Errorf("%s delegates Write to the base file resource, reintroducing the original bypass", seam)
	}
}
