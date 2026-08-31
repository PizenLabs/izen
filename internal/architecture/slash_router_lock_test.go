// Slash Command Fallthrough Guard enforcement (Phase 3 repair). The strict
// input router rule: slash-prefixed input is owned EXCLUSIVELY by the Slash
// Command surface and must never degrade into a submit_prompt / IntentGateway
// submission. These locks pin the two enforcement points — the registry
// registration of /new and the Enter-handler guard around runtimeSubmitCmd.
package architecture

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSlashRouterGuardPinnedStructurally verifies the two non-bypassable
// enforcement points of the Slash Command Fallthrough Guard:
//
//  1. The Command Registry registers /new as a top-level global route (so the
//     parser resolves it and the AST dispatch reaches handleCommand — never a
//     "parser: unknown command" error).
//  2. The Enter handler (keys.go) gates the Application-layer SubmitPromptCmd
//     on isSlashInput, so slash input can never degrade into a prompt
//     submission.
func TestSlashRouterGuardPinnedStructurally(t *testing.T) {
	root := repoRoot(t)

	// 1. /new is a registered top-level command route in the registry.
	regSrc := readFileOrSkip(t, filepath.Join(root, "pkg/domain/command/registry.go"))
	if !strings.Contains(regSrc, `Name: "new"`) {
		t.Error("registry.go must register /new as a top-level command route")
	}
	if !strings.Contains(regSrc, `Name: "session"`) {
		t.Error("registry.go must register /session as a top-level command route")
	}

	// 2. The Enter handler gates runtimeSubmitCmd on isSlashInput.
	keysSrc := readFileOrSkip(t, filepath.Join(root, "internal/ui/keys.go"))
	if !strings.Contains(keysSrc, "if !isSlashInput(userInput)") {
		t.Error("keys.go must gate runtimeSubmitCmd on isSlashInput (Slash Command Fallthrough Guard)")
	}
	if !strings.Contains(keysSrc, "isSlashInput(userInput)") {
		t.Error("keys.go must reference isSlashInput on the Enter path")
	}
}

// TestDispatchASTIntentPreservesSingleSlashCommandArgs pins that a single
// global slash command is dispatched with its FULL original line so subcommand
// arguments survive (/session resume A reaches the resume handler, not the
// list handler).
func TestDispatchASTIntentPreservesSingleSlashCommandArgs(t *testing.T) {
	root := repoRoot(t)
	fset := token.NewFileSet()
	src, err := os.ReadFile(filepath.Join(root, "internal/ui/intent_dispatch.go"))
	if err != nil {
		t.Fatal(err)
	}
	f, err := parser.ParseFile(fset, "intent_dispatch.go", src, parser.AllErrors)
	if err != nil {
		t.Fatal(err)
	}

	// Inside the GlobalCommands loop there must be a full-line dispatch path
	// gated on a single global command + isSlashInput.
	var sawFullLineDispatch bool
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "handleCommand" {
			return true
		}
		// The full-line dispatch passes the ORIGINAL input line (possibly via
		// strings.TrimSpace) as the sole argument — never a reconstructed
		// "/"+name that would strip subcommand arguments.
		if len(call.Args) == 1 {
			switch a := call.Args[0].(type) {
			case *ast.Ident:
				if a.Name == "line" {
					sawFullLineDispatch = true
				}
			case *ast.CallExpr:
				if sel, ok := a.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "TrimSpace" {
					sawFullLineDispatch = true
				}
			}
		}
		return true
	})
	if !sawFullLineDispatch {
		t.Error("dispatchASTIntent must route a single global slash command with its full original line (m.handleCommand(line))")
	}
	if !strings.Contains(string(src), "isSlashInput(line)") {
		t.Error("the full-line dispatch must be gated on isSlashInput")
	}
}
