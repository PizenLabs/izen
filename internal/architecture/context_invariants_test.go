// Phase 1 (P1) context fidelity & risk scope architectural guards.
//
// These tests pin the admission invariants of the runtime: every intent's
// execution context payload is frozen into an integrity-sealed snapshot at
// intent creation and re-verified fail-closed at the RuntimeExecutor entry,
// and every intent's blast radius is classified by the deterministic Risk
// Scope Evaluator BEFORE any stage that could act on the world.
package architecture

import (
	"go/ast"
	"reflect"
	"testing"

	"github.com/PizenLabs/izen/internal/execution"
)

// TestExecutorAdmitsBeforeAnyActingStage pins the ORDER of the runtime
// pipeline: RuntimeExecutor.Execute must verify context fidelity before
// strategy selection, and must evaluate risk scope after strategy selection
// but BEFORE both the read-only invocation path and the mutation path. A
// rejection at any admission check therefore precedes every provider call and
// every mutation surface.
func TestExecutorAdmitsBeforeAnyActingStage(t *testing.T) {
	root := repoRoot(t)
	const executorFile = "internal/execution/executor.go"
	f, fset := parseFile(t, root+"/"+executorFile)

	exec := findFuncDecl(f, "Execute")
	if exec == nil {
		t.Fatal("architecture: RuntimeExecutor.Execute must exist")
	}

	type landmark struct {
		name string
		line int
	}
	var landmarks []landmark
	foundContextVerify := false
	foundScopeAdmit := false
	ast.Inspect(exec, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fun := call.Fun.(type) {
		case *ast.Ident:
			// The context fidelity admission seam.
			if fun.Name == "verifyIntentContext" {
				foundContextVerify = true
				landmarks = append(landmarks, landmark{"context.verify", fset.Position(call.Pos()).Line})
			}
		case *ast.SelectorExpr:
			switch {
			case fun.Sel.Name == "Admit" && renderExpr(fun.X) == "x.admission":
				foundScopeAdmit = true
				landmarks = append(landmarks, landmark{"riskscope.admit", fset.Position(call.Pos()).Line})
			case fun.Sel.Name == "invokeReadOnly" || fun.Sel.Name == "invokeMutation":
				landmarks = append(landmarks, landmark{fun.Sel.Name, fset.Position(call.Pos()).Line})
			}
		}
		return true
	})

	if !foundContextVerify {
		t.Fatal("architecture: Execute must verify the intent context snapshot (context fidelity admission)")
	}
	if !foundScopeAdmit {
		t.Fatal("architecture: Execute must run the risk scope evaluator via x.admission.Admit")
	}

	lineOf := func(name string) int {
		for _, lm := range landmarks {
			if lm.name == name {
				return lm.line
			}
		}
		return -1
	}
	contextLine := lineOf("context.verify")
	admitLine := lineOf("riskscope.admit")
	readOnlyLine := lineOf("invokeReadOnly")
	mutationLine := lineOf("invokeMutation")

	if contextLine < 0 || admitLine < 0 || readOnlyLine < 0 || mutationLine < 0 {
		t.Fatalf("architecture: expected admission + acting stages in Execute, found %+v", landmarks)
	}
	if contextLine > admitLine {
		t.Errorf("architecture: context fidelity verification (line %d) must precede risk scope evaluation (line %d)", contextLine, admitLine)
	}
	if admitLine > readOnlyLine || admitLine > mutationLine {
		t.Errorf("architecture: risk scope evaluation (line %d) must precede invokeReadOnly (%d) and invokeMutation (%d)",
			admitLine, readOnlyLine, mutationLine)
	}
}

// TestIntentGatewayFreezesContextAtCreation pins that IntentGateway.Gate — the
// point of intent creation — seals the execution context payload for EVERY
// branch, so no user action can reach the runtime without a frozen snapshot.
func TestIntentGatewayFreezesContextAtCreation(t *testing.T) {
	root := repoRoot(t)
	f, _ := parseFile(t, root+"/internal/execution/intent.go")

	gate := findFuncDecl(f, "Gate")
	if gate == nil {
		t.Fatal("architecture: IntentGateway.Gate must exist")
	}
	freezeCalls := 0
	ast.Inspect(gate, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if sel, ok := call.Fun.(*ast.Ident); ok && sel.Name == "freezeGatewayContext" {
				freezeCalls++
			}
		}
		return true
	})
	// Gate has two return branches (directive-only clarification and resolved
	// request); BOTH must freeze.
	if freezeCalls < 2 {
		t.Fatalf("architecture: Gate must freeze the intent context on every branch, found %d freeze calls", freezeCalls)
	}
}

// TestContextSnapshotDigestIsUnexported pins the forgery resistance of the
// seal mechanism: the digest field of execution.ContextSnapshot must be
// unexported so a snapshot assembled from decoded JSON or a zero value is
// UNSEALED and rejected by every Verify call site.
func TestContextSnapshotDigestIsUnexported(t *testing.T) {
	typ := reflect.TypeOf(execution.ContextSnapshot{})
	digestField, ok := typ.FieldByName("digest")
	if !ok {
		t.Fatal("architecture: ContextSnapshot must carry a digest field")
	}
	if digestField.IsExported() {
		t.Fatal("architecture: ContextSnapshot.digest must be unexported — an exported seal can be forged by decoding JSON")
	}
	channelsField, ok := typ.FieldByName("Channels")
	if !ok || channelsField.Type.Kind().String() != "slice" {
		t.Fatal("architecture: ContextSnapshot must expose its frozen Channels payload for binding checks")
	}
}

// TestRiskScopeEvaluatorIsPure pins forbidden change #4 structurally: the
// deterministic Risk Scope Evaluator lives inside the core execution package
// and performs no I/O at classification time — EvaluateRiskScope takes no
// context, no configuration store and no external policy source; it is a pure
// function of declared intent facts.
func TestRiskScopeEvaluatorIsPure(t *testing.T) {
	root := repoRoot(t)
	f, _ := parseFile(t, root+"/internal/execution/admission.go")

	fn := findFuncDecl(f, "EvaluateRiskScope")
	if fn == nil {
		t.Fatal("architecture: EvaluateRiskScope must exist as the deterministic classifier")
	}
	if fn.Type.Results == nil || len(fn.Type.Results.List) != 1 {
		t.Fatal("architecture: EvaluateRiskScope must return exactly the verdict value")
	}
	bannedIO := map[string]bool{
		"ReadFile": true, "WriteFile": true, "Stat": true, "Getenv": true,
		"Dial": true, "Open": true, "Create": true, "Do": true,
	}
	ast.Inspect(fn, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fun := call.Fun.(type) {
		case *ast.Ident:
			if bannedIO[fun.Name] {
				t.Errorf("architecture: EvaluateRiskScope must stay pure — no %s call inside the classifier", fun.Name)
			}
		case *ast.SelectorExpr:
			if bannedIO[fun.Sel.Name] {
				t.Errorf("architecture: EvaluateRiskScope must stay pure — no .%s call inside the classifier", fun.Sel.Name)
			}
		}
		return true
	})

	// The evaluator must classify into the bounded tier set and nothing else.
	scopeType := reflect.TypeOf(execution.ScopeReadOnly)
	if scopeType.Kind() != reflect.Int {
		t.Fatal("architecture: RiskScope must be a closed ordered integer tier set")
	}
}

// TestExecutionPackageOwnsNoDynamicPolicyStore pins forbidden changes #4/#5 at
// the dependency level: the admission layer must not import configuration
// servers, external policy stores or dynamic rule engines. Its only inputs are
// the declared intent facts and the admitted capability surface.
func TestExecutionPackageOwnsNoDynamicPolicyStore(t *testing.T) {
	root := repoRoot(t)
	banned := map[string]string{
		"net/http":     "no HTTP policy fetches in the deterministic admission layer",
		"net/rpc":      "no RPC policy stores in the deterministic admission layer",
		"database/sql": "no external policy database in the deterministic admission layer",
		"os/exec":      "admission classifies intents; it never executes them",
	}
	for _, file := range []string{"internal/execution/admission.go", "internal/execution/context.go"} {
		f, _ := parseFile(t, root+"/"+file)
		for path, why := range banned {
			if imports(f)[path] {
				t.Errorf("architecture: %s imports %s — %s", file, path, why)
			}
		}
	}
}
