package ui

import (
	"context"
	"os"
	"strings"
	"testing"

	intentdomain "github.com/PizenLabs/izen/internal/core/domain"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/execution/strategy"
	"github.com/PizenLabs/izen/internal/modes"
	"github.com/PizenLabs/izen/internal/modes/plan"
)

func TestControl_ScopeNoneBuildRejection(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile("target.go", []byte("package target\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := newTestModel()
	m.resolver.Set(modes.ModeAsk)
	m.gateway = execution.NewIntentGateway(".")
	plain := "refactor @target.go"
	ast, err := m.intentFromInput(plain)
	if err != nil {
		t.Fatal(err)
	}
	if ast.ScopeProvenance != intentdomain.ScopeNone {
		t.Fatalf("plain prompt authorization = %v", ast.ScopeProvenance)
	}
	req, _, err := m.gateway.Gate(context.Background(), plain)
	if err != nil {
		t.Fatal(err)
	}
	if req.Strategy.Strategy != strategy.TargetedReasoning {
		t.Fatalf("plain mutation request strategy = %s, want read-only reasoning", req.Strategy.Strategy)
	}
	if m.resolver.Current() != modes.ModeAsk {
		t.Fatalf("plain prompt changed mode to %v", m.resolver.Current())
	}
	// Even an executable task synthesized from a read-only plan cannot be
	// promoted to mutation by a later /build command.
	m.sess.ScopeProvenance = ast.ScopeProvenance
	m.sess.StageTaskList(&[]plan.Task{{StepNum: 1, Type: "FILE_MUTATE", Target: "target.go", Status: "idle"}})
	if m.sess.StagedScopeProvenance != intentdomain.ScopeNone {
		t.Fatal("staging invented scope authorization")
	}
	if cmd := m.handleBuildRun(0); cmd != nil {
		t.Fatal("unauthorized per-task build dispatched work")
	}
	if cmd := m.runStagedBuildViaRuntime(); cmd != nil {
		t.Fatal("unauthorized batch build dispatched work")
	}
	found := false
	for _, record := range m.records {
		if strings.Contains(record.text, intentdomain.ScopeAuthorizationError) {
			found = true
		}
	}
	if !found {
		t.Fatal("build did not report the explicit scope authorization error")
	}
	if m.sess.CurrentTasks[0].Status != "idle" {
		t.Fatal("rejected build modified task status")
	}
	if data, err := os.ReadFile("target.go"); err != nil || string(data) != "package target\n" {
		t.Fatalf("rejected build changed workspace: %q, %v", data, err)
	}
}

func TestControl_ExplicitScopeDirectives(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile("target.go", []byte("package target\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gateway := execution.NewIntentGateway(".")
	for _, tc := range []struct {
		input string
		want  intentdomain.ScopeProvenance
	}{
		{"$prompt refactor @target.go", intentdomain.ScopeDynamic},
		{"/build$hot refactor @target.go", intentdomain.ScopeDeclared},
		{"explain the word hot in @target.go", intentdomain.ScopeNone},
	} {
		t.Run(tc.input, func(t *testing.T) {
			req, res, err := gateway.Gate(context.Background(), tc.input)
			if err != nil {
				t.Fatal(err)
			}
			if req.ScopeProvenance != tc.want || res.ScopeProvenance != tc.want {
				t.Fatalf("provenance = %v/%v, want %v", req.ScopeProvenance, res.ScopeProvenance, tc.want)
			}
			if tc.want.AllowsMutation() && req.Strategy.Strategy != strategy.TargetedMutation {
				t.Fatalf("authorized mutation strategy = %s", req.Strategy.Strategy)
			}
		})
	}
	if _, _, err := gateway.Gate(context.Background(), "/build refactor @target.go"); err == nil || err.Error() != intentdomain.ScopeAuthorizationError {
		t.Fatalf("bare build error = %v", err)
	}
	if req, _, err := gateway.Gate(context.Background(), "$promptly refactor @target.go"); err == nil && req.ScopeProvenance.AllowsMutation() {
		t.Fatal("directive substring authorized mutation")
	}
}
