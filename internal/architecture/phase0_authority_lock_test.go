// Phase 0 execution-authority lock suite.
//
// These tests are mechanical locks over the Phase 0 invariants: the
// RuntimeExecutor is the SOLE workspace-mutation authority, the UI layer is a
// pure intent producer and evidence projection, and every dispatch seam fails
// closed when the runtime boundary is missing or errors. The guards are
// structural (go/parser + go/ast over the production sources) or behavioral
// (black-box against the exported internal/execution API) and are deliberately
// resistant to whitespace/comment churn: they assert on syntax nodes, not
// text.
//
// Scoping note for Guard 0.1: "workspace writes" means raw filesystem
// mutation of execution targets. UI-internal bookkeeping persistence (.izen/
// session markers, debug logs, prompt history, audit trail) is frozen to its
// exact current inventory by TestPhase0UIWorkspaceWritesLockedToBookkeeping —
// any NEW raw write site anywhere under internal/ui fails the lock until it is
// consciously reviewed and added there.
package architecture

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/core/authorization"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/execution/strategy"
)

// ── shared AST scan helpers (phase 0 lock suite) ───────────────────────────

// phase0CallRef attributes one call expression to its enclosing top-level
// function. Attribution makes every lock below precise: a violation names the
// exact file AND function that broke the invariant.
type phase0CallRef struct {
	relPath  string // module-relative slash path
	funcName string // enclosing top-level func decl name
	recv     string // rendered receiver expression ("m.executor", "os", "")
	name     string // called identifier / selector method name
	line     int
}

func (r phase0CallRef) String() string {
	return fmt.Sprintf("%s:%d (%s) %s%s", r.relPath, r.line, r.funcName, r.recv, r.name)
}

// uiProductionFilesRecursive walks EVERY non-test .go file under internal/ui,
// including subpackages (internal/ui/status), unlike the flat listing used by
// the earlier authority guards.
func uiProductionFilesRecursive(t *testing.T, root string) []string {
	t.Helper()
	dir := filepath.Join(root, "internal", "ui")
	var files []string
	if err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		files = append(files, path)
		return nil
	}); err != nil {
		t.Fatalf("architecture: walk internal/ui: %v", err)
	}
	return files
}

// scanSelectorCallsInFuncs parses f and returns every call whose selector
// method name is in want, attributed to its enclosing function.
func scanSelectorCallsInFuncs(t *testing.T, root string, relPath string, want map[string]bool) []phase0CallRef {
	t.Helper()
	f, fset := parseFile(t, filepath.Join(root, relPath))
	var refs []phase0CallRef
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if !want[sel.Sel.Name] {
				return true
			}
			pos := fset.Position(call.Pos())
			refs = append(refs, phase0CallRef{
				relPath:  relPath,
				funcName: fn.Name.Name,
				recv:     renderExpr(sel.X),
				name:     sel.Sel.Name,
				line:     pos.Line,
			})
			return true
		})
	}
	return refs
}

// scanIdentCallsInFuncs returns direct identifier calls (pkg-less functions)
// whose name is in want, attributed to their enclosing function.
func scanIdentCallsInFuncs(t *testing.T, root string, relPath string, want map[string]bool) []phase0CallRef {
	t.Helper()
	f, fset := parseFile(t, filepath.Join(root, relPath))
	var refs []phase0CallRef
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			id, ok := call.Fun.(*ast.Ident)
			if !ok || !want[id.Name] {
				return true
			}
			pos := fset.Position(call.Pos())
			refs = append(refs, phase0CallRef{
				relPath:  relPath,
				funcName: fn.Name.Name,
				name:     id.Name,
				line:     pos.Line,
			})
			return true
		})
	}
	return refs
}

// calleeNamesInNode collects every invoked name inside any statement subtree:
// both bare identifiers and selector method names. It is the structural
// inventory a purity lock asserts against.
func calleeNamesInNode(node ast.Node) map[string]int {
	names := make(map[string]int)
	ast.Inspect(node, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fun := call.Fun.(type) {
		case *ast.Ident:
			names[fun.Name]++
		case *ast.SelectorExpr:
			names[fun.Sel.Name]++
		}
		return true
	})
	return names
}

// ── GUARD 0.1 — no UI transaction/mutation ownership ───────────────────────

// transactionLifecycleMethods are the MutationSet/engine transaction lifecycle
// entry points. The UI must never drive them.
var transactionLifecycleMethods = []string{
	"BeginTransaction",
	"CommitTransaction",
	"RollbackTransaction",
}

// mutationOwnershipConstructors construct mutation machinery. Their presence
// anywhere under internal/ui would create a second mutation authority.
var mutationOwnershipConstructors = []string{
	"NewPatchManager",
	"NewMutationSet",
	"NewTxFS",
}

// patchMutationSurfaceMethods are unambiguous PatchManager/MutationSet apply
// and bookkeeping entry points (no benign homonym exists in the UI's import
// surface today).
var patchMutationSurfaceMethods = []string{
	"ApplyContext",
	"RollbackTo",
	"SetMutationSet",
	"SetLedger",
	"SetContextID",
}

// TestPhase0UIPackageOwnsNoTransactionOrMutationAuthority pins Phase 0 across
// the WHOLE ui package tree (including subpackages): zero transaction
// lifecycle calls except the single composition-root rollback-engine entry,
// zero mutation-machinery constructors, and zero PatchManager/MutationSet
// surface invocations. Transaction and mutation authority live exclusively
// behind the RuntimeExecutor approval boundary.
func TestPhase0UIPackageOwnsNoTransactionOrMutationAuthority(t *testing.T) {
	root := repoRoot(t)
	files := uiProductionFilesRecursive(t, root)
	if len(files) < 40 {
		t.Fatalf("architecture: sanity — expected the full internal/ui file tree, found only %d files; the lock would be vacuous", len(files))
	}

	txWant := make(map[string]bool)
	for _, m := range transactionLifecycleMethods {
		txWant[m] = true
	}
	ctorWant := make(map[string]bool)
	for _, c := range mutationOwnershipConstructors {
		ctorWant[c] = true
	}
	surfaceWant := make(map[string]bool)
	for _, m := range patchMutationSurfaceMethods {
		surfaceWant[m] = true
	}
	execEngExecWant := map[string]bool{"Execute": true, "ExecuteStream": true, "Apply": true}

	txSites := make([]phase0CallRef, 0, len(files))
	ctorSites := make([]phase0CallRef, 0, 2*len(files))
	surfaceSites := make([]phase0CallRef, 0, len(files))
	var engineExecSites []phase0CallRef
	for _, path := range files {
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			t.Fatalf("architecture: rel %s: %v", path, relErr)
		}
		relPath := filepath.ToSlash(rel)
		txSites = append(txSites, scanSelectorCallsInFuncs(t, root, relPath, txWant)...)
		ctorSites = append(ctorSites, scanIdentCallsInFuncs(t, root, relPath, ctorWant)...)
		ctorSites = append(ctorSites, scanSelectorCallsInFuncs(t, root, relPath, ctorWant)...)
		surfaceSites = append(surfaceSites, scanSelectorCallsInFuncs(t, root, relPath, surfaceWant)...)
		for _, ref := range scanSelectorCallsInFuncs(t, root, relPath, execEngExecWant) {
			if strings.HasPrefix(ref.recv, "m.execEng") {
				engineExecSites = append(engineExecSites, ref)
			}
		}
	}

	// (a) Transaction lifecycle: exactly ONE whitelisted reference may exist —
	// RunRollbackEngine's rollback of an in-flight engine transaction through
	// the composition-root-injected facade (app.Execution). It is the `izen
	// --rollback` recovery entry point, wired by compose, not a UI-owned
	// transaction. Anything else fails the lock.
	const (
		allowedTxFile = "internal/ui/program.go"
		allowedTxFn   = "RunRollbackEngine"
		allowedTxRecv = "app.Execution"
		allowedTxName = "RollbackTransaction"
	)
	whitelisted := 0
	for _, ref := range txSites {
		if ref.relPath == allowedTxFile && ref.funcName == allowedTxFn &&
			ref.recv == allowedTxRecv && ref.name == allowedTxName {
			whitelisted++
			continue
		}
		t.Errorf("architecture: UI must not own transaction lifecycle: %s — commit/rollback authority belongs exclusively to the runtime boundary", ref)
	}
	if whitelisted != 1 {
		t.Errorf("architecture: expected exactly one whitelisted %s.%s site (%s::%s), found %d — the lock inventory is stale",
			allowedTxRecv, allowedTxName, allowedTxFile, allowedTxFn, whitelisted)
	}

	// (b) Mutation-machinery constructors: zero tolerance.
	for _, ref := range ctorSites {
		t.Errorf("architecture: UI must not construct mutation machinery: %s — PatchManager/MutationSet/TxFS are runtime-owned", ref)
	}

	// (c) Patch/MutationSet surface: zero tolerance.
	for _, ref := range surfaceSites {
		t.Errorf("architecture: UI must not invoke the mutation surface: %s — applies, rollbacks and ledger wiring belong to the RuntimeExecutor", ref)
	}

	// (d) The legacy UI-owned execution engine must never execute or apply on
	// any path (checkpoints/snapshots bookkeeping remains out of scope).
	for _, ref := range engineExecSites {
		t.Errorf("architecture: UI legacy execution engine must not execute or apply: %s — execution belongs to the RuntimeExecutor", ref)
	}
}

// phase0UIWriteInventory is the FROZEN inventory of every raw filesystem
// mutation call under internal/ui. Each entry pairs "file::function" with the
// exact set of os.* mutating methods that occur inside it. All of them are
// UI-internal bookkeeping (.izen session markers, debug logs, test-run logs,
// prompt history, audit trail, plan-stash cleanup) — none touch execution
// targets. A NEW site anywhere fails the lock; so does silently REMOVING a
// recorded site (the reviewer must consciously update this table).
var phase0UIWriteInventory = map[string]string{
	"internal/ui/commands.go::restorePlan":                "Remove",
	"internal/ui/commands.go::debugLogPlan":               "MkdirAll,OpenFile",
	"internal/ui/commands.go::runTestEngine":              "MkdirAll,WriteFile",
	"internal/ui/debug_completion.go::debugLogCompletion": "MkdirAll,OpenFile",
	"internal/ui/model.go::saveHistory":                   "MkdirAll,OpenFile",
	"internal/ui/stream.go::debugLogPayload":              "MkdirAll,OpenFile",
	"internal/ui/update.go::Update":                       "OpenFile",
	"internal/ui/update_init.go::selfHealWorkspace":       "WriteFile",
	"internal/ui/update_init.go::saveInitState":           "WriteFile",
}

// osMutatingMethods are the stdlib surfaces that create, overwrite, rename or
// delete filesystem state.
var osMutatingMethods = map[string]bool{
	"WriteFile": true, "Create": true, "Rename": true, "Remove": true,
	"RemoveAll": true, "Truncate": true, "OpenFile": true, "MkdirAll": true,
}

// TestPhase0UIWorkspaceWritesLockedToBookkeeping freezes Guard 0.1's write
// surface: every raw os-level mutation call under internal/ui must sit inside
// the frozen bookkeeping inventory — no new workspace-write function can
// appear in the presentation layer without failing this lock.
func TestPhase0UIWorkspaceWritesLockedToBookkeeping(t *testing.T) {
	root := repoRoot(t)

	found := make(map[string][]string) // "file::func" -> methods
	for _, path := range uiProductionFilesRecursive(t, root) {
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			t.Fatalf("architecture: rel %s: %v", path, relErr)
		}
		relPath := filepath.ToSlash(rel)
		for _, ref := range scanSelectorCallsInFuncs(t, root, relPath, osMutatingMethods) {
			// Only raw stdlib-filesystem mutations are locked here; benign
			// same-named methods on other receivers (e.g.
			// m.execEng.Checkpoints.Create, ansi.Truncate) are out of scope.
			if ref.recv != "os" {
				continue
			}
			key := ref.relPath + "::" + ref.funcName
			found[key] = append(found[key], ref.name)
		}
	}

	got := make(map[string]string, len(found))
	for key, methods := range found {
		sort.Strings(methods)
		got[key] = strings.Join(methods, ",")
	}

	// Every discovered site must be in the inventory with the EXACT method set…
	for key, methods := range got {
		want, ok := phase0UIWriteInventory[key]
		if !ok {
			t.Errorf("architecture: NEW raw filesystem write site in UI at %s — the presentation layer must not gain workspace-write functions; review and update the phase 0 write inventory if this is legitimate bookkeeping (found: %s)", key, methods)
			continue
		}
		if methods != want {
			t.Errorf("architecture: write-site drift at %s: got [%s], locked inventory expects [%s]", key, methods, want)
		}
	}
	// …and every inventory entry must still exist (anti-stale, anti-vacuous).
	for key := range phase0UIWriteInventory {
		if _, ok := got[key]; !ok {
			t.Errorf("architecture: locked write site %s disappeared from the UI package — update the phase 0 write inventory consciously (the lock must never rot silently)", key)
		}
	}
}

// ── GUARD 0.2 — intent factory seam ────────────────────────────────────────

// legacyExecutionHandlers are retired UI-side execution paths. Their presence
// in the build dispatch chain would resurrect a shadow execution engine.
var legacyExecutionHandlers = []string{
	"streamCmd",
	"proposeBuildPatch",
	"runBuildFastTrack",
}

// executionSurfaceNames are provider/mutation/execution entry points that a
// pure intent factory must never invoke directly.
var executionSurfaceNames = []string{
	"Execute", "ExecuteStream", "Gate", "Admit",
	"Apply", "ApplyContext", "Commit", "RollbackTo",
	"WriteFile", "OpenFile", "Create", "Rename", "Remove", "RemoveAll",
}

func nameSet(names []string) map[string]bool {
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[n] = true
	}
	return set
}

// TestPhase0HandleBuildRunIsPureIntentFactorySeam pins the factory seam
// structurally: handleBuildRun only selects the staged task and hands it to
// dispatchStagedTask; dispatchStagedTask routes FILE_MUTATE/GIT_ACTION
// EXCLUSIVELY through runRuntimeTaskRequest (RuntimeExecutor admission),
// SHELL_EXEC through the interactive gate, and FAILS CLOSED on unknown task
// types with no execution call whatsoever in the default arm.
func TestPhase0HandleBuildRunIsPureIntentFactorySeam(t *testing.T) {
	root := repoRoot(t)
	f, _ := parseFile(t, filepath.Join(root, "internal", "ui", "commands.go"))

	hbr := findFuncDecl(f, "handleBuildRun")
	if hbr == nil {
		t.Fatal("architecture: handleBuildRun must exist as the /build intent factory")
	}

	callees := calleeNamesInNode(hbr)
	required := nameSet([]string{"beginStagedTask", "dispatchStagedTask"})
	banned := nameSet(legacyExecutionHandlers)
	for _, n := range executionSurfaceNames {
		banned[n] = true
	}
	for name := range required {
		if callees[name] == 0 {
			t.Errorf("architecture: handleBuildRun must route through %s (pure intent factory seam)", name)
		}
	}
	for name := range callees {
		if banned[name] {
			t.Errorf("architecture: handleBuildRun invokes execution surface %q — the factory must only produce intents, never execute (handleBuildRun → dispatchStagedTask is the sole seam)", name)
		}
	}

	dispatch := findFuncDecl(f, "dispatchStagedTask")
	if dispatch == nil {
		t.Fatal("architecture: dispatchStagedTask must exist as the single admission-boundary seam")
	}

	// Walk the task-type switch structurally: each case arm's allowed call set
	// is locked, and the default arm must fail closed with zero execution.
	var switchStmt *ast.SwitchStmt
	ast.Inspect(dispatch.Body, func(n ast.Node) bool {
		if sw, ok := n.(*ast.SwitchStmt); ok && switchStmt == nil {
			if renderExpr(sw.Tag) == "task.Type" {
				switchStmt = sw
				return false
			}
		}
		return true
	})
	if switchStmt == nil {
		t.Fatal("architecture: dispatchStagedTask must switch on task.Type to route each staged task across its admitted seam")
	}

	armCalls := make(map[string]map[string]int) // case label -> callee counts
	var defaultCalls map[string]int
	for _, stmt := range switchStmt.Body.List {
		clause, ok := stmt.(*ast.CaseClause)
		if !ok {
			continue
		}
		counts := calleeNamesInNode(clause)
		if clause.List == nil {
			defaultCalls = counts
			continue
		}
		for _, expr := range clause.List {
			lit, ok := expr.(*ast.BasicLit)
			if !ok {
				continue
			}
			label := strings.Trim(lit.Value, `"`)
			armCalls[label] = counts
		}
	}

	mutateArm, ok := armCalls["FILE_MUTATE"]
	if !ok {
		t.Fatal("architecture: dispatchStagedTask must carry a FILE_MUTATE case arm")
	}
	gitArm, ok := armCalls["GIT_ACTION"]
	if !ok {
		t.Fatal("architecture: dispatchStagedTask must carry a GIT_ACTION case arm")
	}
	if mutateArm["runRuntimeTaskRequest"] != 1 || gitArm["runRuntimeTaskRequest"] != 1 {
		t.Error("architecture: FILE_MUTATE and GIT_ACTION arms must each submit through runRuntimeTaskRequest exactly once — RuntimeExecutor admission is exclusive")
	}
	for _, label := range []string{"FILE_MUTATE", "GIT_ACTION"} {
		for name := range armCalls[label] {
			if banned[name] || name == "runStagedShellGate" {
				t.Errorf("architecture: %s arm of dispatchStagedTask must not invoke %q — file mutations cross ONLY runRuntimeTaskRequest", label, name)
			}
		}
	}
	if armCalls["SHELL_EXEC"]["runStagedShellGate"] != 1 {
		t.Error("architecture: SHELL_EXEC arm must cross the interactive shell gate (runStagedShellGate) exactly once")
	}

	if defaultCalls == nil {
		t.Fatal("architecture: dispatchStagedTask must have a default arm that fails closed on unsupported task types")
	}
	for name := range defaultCalls {
		switch name {
		case "push", "Sprintf", "StageTaskList", "Save":
			// Bookkeeping-only: stall the task, persist the ledger, surface
			// the halt message. Execution is forbidden here.
		default:
			t.Errorf("architecture: default arm of dispatchStagedTask invoked %q — unknown task types MUST fail closed with no execution path", name)
		}
	}
	if defaultCalls["push"] == 0 {
		t.Error("architecture: default arm must surface the fail-closed halt to the operator (m.push)")
	}
}

// ── Guard 0.2 (b): fail-closed when the executor is uninitialized ──────────

// phase0RuntimeEntryPoints are the UI seams that submit work to the runtime.
// Each must guard on the runtime being unwired BEFORE anything else and fall
// back to NOTHING: no legacy handler, no provider, no shadow engine.
var phase0RuntimeEntryPoints = []struct {
	file     string
	funcName string
}{
	{"internal/ui/runtime_cutover.go", "runRuntimeExecuteCmd"},
	{"internal/ui/runtime_cutover.go", "runRuntimeTaskRequest"},
	{"internal/ui/runtime_cutover.go", "runStagedBuildViaRuntime"},
	{"internal/ui/runtime_cutover.go", "runRuntimePrompt"},
}

// findNilGuard locates the if-statement that fail-closes on an unwired
// runtime: a comparison of m.executor / m.gateway against nil. It returns the
// statement and its rendered condition subjects.
func findNilGuard(fn *ast.FuncDecl) (*ast.IfStmt, []string) {
	var guarded []string
	var guardStmt *ast.IfStmt
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		ifStmt, ok := n.(*ast.IfStmt)
		if !ok || guardStmt != nil {
			return true
		}
		found := false
		ast.Inspect(ifStmt.Cond, func(c ast.Node) bool {
			bin, ok := c.(*ast.BinaryExpr)
			if !ok || bin.Op != token.EQL {
				return true
			}
			xNil := renderExpr(bin.X) == "nil"
			yNil := renderExpr(bin.Y) == "nil"
			value := ""
			if xNil {
				value = renderExpr(bin.Y)
			} else if yNil {
				value = renderExpr(bin.X)
			}
			if value == "m.executor" || value == "m.gateway" {
				found = true
				guarded = append(guarded, value)
			}
			return true
		})
		if found {
			guardStmt = ifStmt
		}
		return true
	})
	return guardStmt, guarded
}

// TestPhase0RuntimeDispatchFailsClosedWithoutExecutor proves structurally that
// every runtime entry point checks for the unwired executor/gateway FIRST and
// degrades to a surfaced error — never to a legacy execution handler, a
// provider call, or any other shadow execution path. This is the negative
// half of the intent-factory contract: with the RuntimeExecutor absent or
// erroring, nothing mutates.
func TestPhase0RuntimeDispatchFailsClosedWithoutExecutor(t *testing.T) {
	root := repoRoot(t)
	parsed := make(map[string]*ast.File)
	for _, ep := range phase0RuntimeEntryPoints {
		f, ok := parsed[ep.file]
		if !ok {
			f, _ = parseFile(t, filepath.Join(root, ep.file))
			parsed[ep.file] = f
		}
		fn := findFuncDecl(f, ep.funcName)
		if fn == nil {
			t.Fatalf("architecture: %s must exist as a runtime dispatch seam", ep.funcName)
		}

		guard, subjects := findNilGuard(fn)
		if guard == nil {
			t.Fatalf("architecture: %s must fail closed with a nil-guard on m.executor/m.gateway before any dispatch", ep.funcName)
		}
		if len(subjects) == 0 {
			t.Fatalf("architecture: %s nil-guard lost its executor/gateway subject", ep.funcName)
		}

		// The failure branch itself must be inert: no runtime submission, no
		// gateway resolution, no legacy handler — surfacing the error is all.
		bannedInGuard := nameSet(legacyExecutionHandlers)
		bannedInGuard["Execute"] = true
		bannedInGuard["ExecuteStream"] = true
		bannedInGuard["Gate"] = true
		bannedInGuard["SelectStrategy"] = true
		guardCallees := calleeNamesInNode(guard.Body)
		for name := range guardCallees {
			if bannedInGuard[name] {
				t.Errorf("architecture: %s fail-closed branch invoked %q — an unwired runtime must NEVER fall back to another execution path", ep.funcName, name)
			}
		}

		// Every runtime submission inside the seam must sit AFTER the guard:
		// no m.executor/m.gateway method call may precede the nil check.
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			recv := renderExpr(sel.X)
			if recv != "m.executor" && recv != "m.gateway" {
				return true
			}
			if guard.Pos() <= call.Pos() && call.Pos() <= guard.End() {
				return true // references inside the guard's own condition/body
			}
			if call.Pos() < guard.End() {
				t.Errorf("architecture: %s touches %s.%s before the nil-guard completes — the fail-closed check must be first", ep.funcName, recv, sel.Sel.Name)
			}
			return true
		})

		// And the seam as a whole must stay free of legacy fallbacks.
		for name := range calleeNamesInNode(fn) {
			if nameSet(legacyExecutionHandlers)[name] {
				t.Errorf("architecture: %s falls back to legacy execution handler %q — no shadow engine may exist behind the runtime boundary", ep.funcName, name)
			}
		}
	}
}

// ── behavioral fixtures (exported-API only) ────────────────────────────────

const (
	lockTargetFile     = "note.txt"
	lockOriginal       = "foo\nbar\nbaz\n"
	lockSearchReplace  = "<<<<<<< SEARCH\nbar\n=======\nqux\n>>>>>>>"
	lockMutated        = "foo\nqux\nbaz\n"
	lockMutationPrompt = "change bar to qux in @" + lockTargetFile
)

// lockScriptedProvider is a race-safe ai.Provider fake that either serves a
// fixed SEARCH/REPLACE artifact or fails deterministically, counting every
// invocation so side-effect locks can prove ZERO provider crossings.
type lockScriptedProvider struct {
	mu        sync.Mutex
	err       error
	responses int // how many successful responses to serve before failing
	callCount int
}

func (p *lockScriptedProvider) Name() string { return "mock" }

func (p *lockScriptedProvider) Execute(_ context.Context, _ ai.Request) (*ai.Response, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.callCount++
	if p.err != nil || p.callCount > p.responses {
		return nil, fmt.Errorf("lockScriptedProvider: deterministic failure")
	}
	return &ai.Response{Content: lockSearchReplace}, nil
}

func (p *lockScriptedProvider) ExecuteStream(_ context.Context, _ ai.Request) (io.ReadCloser, error) {
	return nil, errors.New("streaming not supported by lockScriptedProvider")
}

func (p *lockScriptedProvider) calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.callCount
}

func lockWriteTarget(t *testing.T, root, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, lockTargetFile), []byte(content), 0o644); err != nil {
		t.Fatalf("write target fixture: %v", err)
	}
}

func lockReadTarget(t *testing.T, root string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, lockTargetFile))
	if err != nil {
		t.Fatalf("read target fixture: %v", err)
	}
	return string(data)
}

// lockFrozenMutationIntent builds an integrity-sealed mutation request the way
// IntentGateway.Gate would: prompt + referenced-file channels sealed at intent
// creation, targeted_mutation profile attached.
func lockFrozenMutationIntent(root string) execution.ExecuteRequest {
	profile := strategy.ExecutionStrategyProfile{
		Strategy:       strategy.TargetedMutation,
		ModelRequired:  true,
		StrategyReason: "phase 0/1 architecture lock: bounded targeted mutation",
	}
	snapshot := execution.FreezeContext("", []execution.ContextChannel{
		{Kind: execution.ContextKindUserPrompt, Name: "prompt", Content: lockMutationPrompt},
		{Kind: execution.ContextKindEnvironment, Name: "workspace", Content: root},
		{Kind: execution.ContextKindReferencedFile, Name: lockTargetFile},
	})
	return execution.ExecuteRequest{
		RequestID: "arch-lock",
		Mode:      "build",
		Prompt:    lockMutationPrompt,
		Targets:   []string{lockTargetFile},
		Strategy:  &profile,
		Context:   snapshot,
	}
}

// TestPhase0ExecutorErrorYieldsZeroSideEffects is the behavioral half of
// Guard 0.2: when the RuntimeExecutor's provider fails, there is NO shadow
// retry loop, NO fallback engine, NO partial mutation — the workspace stays
// byte-identical and no approval surface appears.
func TestPhase0ExecutorErrorYieldsZeroSideEffects(t *testing.T) {
	root := t.TempDir()
	lockWriteTarget(t, root, lockOriginal)
	provider := &lockScriptedProvider{err: errors.New("provider down")}
	x := execution.NewRuntimeExecutor(root, config.Default(), provider, nil, "")

	req := lockFrozenMutationIntent(root)
	res, execErr := x.Execute(context.Background(), req)
	if execErr == nil {
		t.Fatal("executor must surface the provider failure (no silent swallow, no fallback path)")
	}
	if res == nil || res.Err == nil {
		t.Fatal("the terminal result must carry the failure")
	}
	if provider.calls() > 1 {
		t.Fatalf("provider invoked %d times after failure — a shadow retry/fallback loop exists", provider.calls())
	}
	if got := lockReadTarget(t, root); got != lockOriginal {
		t.Fatalf("workspace mutated despite executor failure: %q", got)
	}
	if pending := x.PendingPatchIDs(); len(pending) != 0 {
		t.Fatalf("failed execution leaked approval surface: %v", pending)
	}
}

// TestPhase0UninitializedProviderFailsClosed pins the uninitialized-boundary
// case of Guard 0.2: a model-required mutation with NO provider bound fails
// deterministically before any mutation surface.
func TestPhase0UninitializedProviderFailsClosed(t *testing.T) {
	root := t.TempDir()
	lockWriteTarget(t, root, lockOriginal)
	x := execution.NewRuntimeExecutor(root, config.Default(), nil, nil, "")

	req := lockFrozenMutationIntent(root)
	_, execErr := x.Execute(context.Background(), req)
	if execErr == nil {
		t.Fatal("model-required strategy without a provider must fail deterministically")
	}
	if got := lockReadTarget(t, root); got != lockOriginal {
		t.Fatalf("workspace mutated with an uninitialized provider: %q", got)
	}
	if pending := x.PendingPatchIDs(); len(pending) != 0 {
		t.Fatalf("uninitialized-provider run leaked approval surface: %v", pending)
	}
}

// ── GUARD 0.3 — execution seam authority ───────────────────────────────────

// TestPhase0MutationDispatchExclusivelyThroughRuntimeExecutor sweeps the whole
// UI package for executor submissions: m.executor.Execute may appear ONLY in
// the two admitted seams (the gated line handler and the runtime cutover
// dispatcher); Approve/Reject only in the approval bridge. Any new submission
// site would be a second execution path.
func TestPhase0MutationDispatchExclusivelyThroughRuntimeExecutor(t *testing.T) {
	root := repoRoot(t)

	executeWant := map[string]bool{"Execute": true}
	var executeSites []phase0CallRef
	approveRejectWant := map[string]bool{"Approve": true, "Reject": true}
	var approveRejectSites []phase0CallRef
	for _, path := range uiProductionFilesRecursive(t, root) {
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			t.Fatalf("architecture: rel %s: %v", path, relErr)
		}
		relPath := filepath.ToSlash(rel)
		for _, ref := range scanSelectorCallsInFuncs(t, root, relPath, executeWant) {
			if ref.recv == "m.executor" {
				executeSites = append(executeSites, ref)
			}
		}
		for _, ref := range scanSelectorCallsInFuncs(t, root, relPath, approveRejectWant) {
			if ref.recv == "m.executor" || ref.recv == "x" {
				approveRejectSites = append(approveRejectSites, ref)
			}
		}
	}

	type seamQuota struct {
		file, fn string
		quota    int
	}
	seams := []seamQuota{
		{"internal/ui/runtime_cutover.go", "runRuntimeExecuteCmd", 1},
		{"internal/ui/gateway.go", "runGatedLine", 2},
	}
	counts := make(map[string]int)
	for _, ref := range executeSites {
		key := ref.relPath + "::" + ref.funcName
		counts[key]++
	}
	matchedSeams := make(map[string]bool)
	for _, seam := range seams {
		key := seam.file + "::" + seam.fn
		if counts[key] != seam.quota {
			t.Errorf("architecture: executor submission seam %s has %d m.executor.Execute sites, want exactly %d", key, counts[key], seam.quota)
		}
		matchedSeams[key] = true
	}
	for _, ref := range executeSites {
		key := ref.relPath + "::" + ref.funcName
		if !matchedSeams[key] {
			t.Errorf("architecture: m.executor.Execute outside the admitted seams at %s — every execution must cross runGatedLine or runRuntimeExecuteCmd", ref)
		}
	}

	approveBridge, rejectBridge := 0, 0
	for _, ref := range approveRejectSites {
		if ref.relPath != "internal/ui/runtime_executor.go" {
			t.Errorf("architecture: executor %s submitted outside the approval bridge at %s — approvals resolve ONLY via the RuntimeExecutor bridge", ref.name, ref)
			continue
		}
		switch ref.name {
		case "Approve":
			approveBridge++
		case "Reject":
			rejectBridge++
		}
	}
	if approveBridge == 0 || rejectBridge == 0 {
		t.Errorf("architecture: approval bridge lost its resolve surface (Approve=%d Reject=%d) — the lock inventory is stale", approveBridge, rejectBridge)
	}
}

// TestPhase0MutationsHeldBehindRuntimeApprovalBoundary is the behavioral proof
// of Guard 0.3 from outside the package: a fully-admitted mutation intent does
// NOT touch disk at Execute time — the change is HELD at the approval gate;
// Reject leaves the workspace untouched forever; Approve is the ONLY act that
// writes. 100% of workspace mutations therefore flow through the
// RuntimeExecutor transaction boundary.
func TestPhase0MutationsHeldBehindRuntimeApprovalBoundary(t *testing.T) {
	newExecutor := func(t *testing.T, root string) (*execution.RuntimeExecutor, *lockScriptedProvider) {
		t.Helper()
		provider := &lockScriptedProvider{responses: 1}
		x := execution.NewRuntimeExecutor(root, config.Default(), provider, nil, "")
		x.SetAuthorization(&authorization.MutationAuthorization{
			ID:       authorization.NewAuthorizationID(),
			IssuedAt: rightNow(),
		})
		return x, provider
	}

	// Stage 1: admitted intent stops at the gate with the workspace pristine.
	root := t.TempDir()
	lockWriteTarget(t, root, lockOriginal)
	x, provider := newExecutor(t, root)
	req := lockFrozenMutationIntent(root)
	res, err := x.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("admitted mutation must reach the approval gate: %v", err)
	}
	if res.PendingPatchID == "" {
		t.Fatal("mutation execution must HOLD at the approval gate (PendingPatchID)")
	}
	if got := lockReadTarget(t, root); got != lockOriginal {
		t.Fatalf("pre-approval workspace mutation detected: %q", got)
	}
	if len(res.Mutations) != 0 {
		t.Fatalf("mutation evidence exists before approval: %+v", res.Mutations)
	}

	// Stage 2: rejection resolves the gate WITHOUT ever writing.
	rejRes, err := x.Reject(context.Background(), res.PendingPatchID, "rejected by architecture lock")
	if err != nil {
		t.Fatalf("reject: %v", err)
	}
	if rejRes.Proof == nil || rejRes.Proof.Outcome != execution.OutcomeRejected {
		t.Fatalf("rejection outcome mismatch: %+v", rejRes.Proof)
	}
	if got := lockReadTarget(t, root); got != lockOriginal {
		t.Fatalf("post-rejection workspace mutation detected: %q", got)
	}

	// Stage 3: a fresh identical intent crosses again and Approve — the sole
	// writing act — applies the held change.
	x2, _ := newExecutor(t, root)
	req2 := lockFrozenMutationIntent(root)
	res2, err := x2.Execute(context.Background(), req2)
	if err != nil {
		t.Fatalf("second admission: %v", err)
	}
	if _, err := x2.Approve(context.Background(), res2.PendingPatchID); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if got := lockReadTarget(t, root); got != lockMutated {
		t.Fatalf("approved mutation not applied by the runtime boundary: %q", got)
	}
	if provider.calls() > 2 {
		t.Fatalf("provider crossed %d times for two intents — unexpected extra invocations", provider.calls())
	}
}

// rightNow isolates the wall-clock read for the authorization fixture.
func rightNow() time.Time { return time.Now() }
