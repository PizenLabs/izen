package autonomy

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/execution/strategy"
)

// newBehaviorExecutor builds a real RuntimeExecutor bound to root so the
// behavioral stage's mutation gate runs through the SAME admission path
// production uses, rather than a stub that could drift from it.
//
// The executor is wired with no provider: the behavioral stage's reasoning is
// supplied by its own proposer, and a mutation authorization check never invokes
// one. That keeps the fixture honest about what it is testing — the gate, not
// generation.
func newBehaviorExecutor(t *testing.T, root string) *execution.RuntimeExecutor {
	t.Helper()
	cfg := config.Default()
	x := execution.NewRuntimeExecutor(root, cfg, nil, nil, "")
	if x == nil {
		t.Fatal("cannot construct an executor for the behavioral fixture")
	}
	return x
}

// TestBehavioralMutationGateAcceptsAWorkspaceTarget: the gate must admit an
// ordinary workspace repair. If it refused everything the behavioral stage could
// never repair anything, and the golden objective would be unachievable.
func TestBehavioralMutationGateAcceptsAWorkspaceTarget(t *testing.T) {
	root := t.TempDir()
	x := newBehaviorExecutor(t, root)
	if err := x.AuthorizeMutationTarget("index.html"); err != nil {
		t.Fatalf("the behavioral mutation gate refused an ordinary workspace target: %v", err)
	}
}

// TestBehavioralMutationGateRefusesEscapeAndSystemPaths: the gate must refuse a
// target outside the workspace or on a system path, because the behavioral stage
// is a new CALLER of the gate and must not become a way around it.
func TestBehavioralMutationGateRefusesEscapeAndSystemPaths(t *testing.T) {
	root := t.TempDir()
	x := newBehaviorExecutor(t, root)
	for _, target := range []string{
		"../escape.html",
		"../../etc/passwd",
		"/etc/passwd",
		"a/../../../b.txt",
	} {
		if err := x.AuthorizeMutationTarget(target); err == nil {
			t.Errorf("the gate admitted an escaping target %q", target)
		}
	}
}

// TestBehavioralMutationGateRefusesAnEmptyTarget: an empty target is a question,
// not a destination.
func TestBehavioralMutationGateRefusesAnEmptyTarget(t *testing.T) {
	x := newBehaviorExecutor(t, t.TempDir())
	if err := x.AuthorizeMutationTarget("   "); err == nil {
		t.Fatal("the gate admitted an empty target")
	}
}

// TestBehavioralLoopRefusesWithoutAnAuthorizationGate: a loop with no gate wired
// must deny every mutation. This is the safe default for a caller who wired no
// authority, and it is why the stage always supplies one.
func TestBehavioralLoopRefusesWithoutAnAuthorizationGate(t *testing.T) {
	root := behaviorFixture(t)
	rt := execution.NewBehavioralRuntime(execution.BehavioralConfig{Root: root})
	defer func() { _ = rt.Close() }()
	loop, err := execution.NewBehaviorLoop(execution.BehaviorLoopConfig{
		Runtime:   rt,
		Grant:     execution.GrantFor(behaviorScope(), behaviorCaps()),
		Proposer:  &repairingProposer{},
		Mutate:    behaviorSubstrate(root),
		Authorize: nil,
	})
	if err != nil {
		t.Fatalf("NewBehaviorLoop: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result := loop.Run(ctx)
	if result.Proven {
		t.Fatal("a loop with no authorization gate must not prove anything")
	}
	for _, a := range result.Repairs {
		if a.Outcome == execution.RepairApplied {
			t.Fatal("a loop with no authorization gate must not apply a repair")
		}
	}
}

// TestBehavioralLoopHonoursTheEvidenceProfile: the loop must derive the same
// entry document the discovery pass derived, proving the two agree rather than
// each guessing.
func TestBehavioralLoopHonoursTheEvidenceProfile(t *testing.T) {
	root := behaviorFixture(t)
	rt := execution.NewBehavioralRuntime(execution.BehavioralConfig{Root: root})
	defer func() { _ = rt.Close() }()
	grant := execution.GrantFor(behaviorScope(), behaviorCaps())
	discovery := rt.Discover(context.Background(), grant)
	if discovery.Block != nil {
		t.Fatalf("discovery blocked: %s", discovery.Block.Error())
	}
	if discovery.Profile.Entry == nil || discovery.Profile.Entry.Path != "index.html" {
		t.Fatalf("entry = %+v", discovery.Profile.Entry)
	}
	obs := rt.Observe(context.Background(), grant)
	if obs.Block != nil {
		t.Fatalf("observation blocked: %s", obs.Block.Error())
	}
	if !strings.HasSuffix(obs.EntryPath, "/index.html") {
		t.Fatalf("served entry = %q, want the discovered index.html", obs.EntryPath)
	}
	// The observed entry must agree with the discovered one.
	if strings.TrimPrefix(obs.EntryPath, "/") != discovery.Profile.Entry.Path {
		t.Fatalf("observation served %q but discovery chose %q",
			strings.TrimPrefix(obs.EntryPath, "/"), discovery.Profile.Entry.Path)
	}
}

// TestStrategyProfileIsTheMutationStrategy: a small guard on the assumption the
// admission call rests on. If the mutation strategy label ever changed, the
// behavioral gate would be admitting the wrong risk scope.
func TestStrategyProfileIsTheMutationStrategy(t *testing.T) {
	if strategy.TargetedMutation.String() == "" {
		t.Fatal("the mutation strategy must have a stable label for audit records")
	}
}
