package executor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/PizenLabs/izen/internal/core/domain"
	"github.com/PizenLabs/izen/internal/core/domain/authorization"
	"github.com/PizenLabs/izen/internal/core/domain/occ"
	"github.com/PizenLabs/izen/internal/modes"
	"github.com/PizenLabs/izen/internal/runtime/scope"
	"github.com/PizenLabs/izen/internal/runtime/substrate"
)

// permittedGuard is an always-permit guard so the test isolates the
// confinement gate (a real deployment uses SimpleCapabilityGuard).
type permittedGuard struct{}

func (permittedGuard) Evaluate(_ context.Context, _ authorization.AuthorizationInput) authorization.AuthorizationDecision {
	return authorization.AuthorizationDecision{Permitted: true}
}

// TestRuntimeExecutorTOCTOUSymlinkSwapFailsClosed is the adversarial race
// test at the execution boundary: a valid target foo.txt is resolved,
// then swapped on disk with a symlink pointing to /tmp/evil immediately
// before execution. RuntimeExecutor must halt with ErrWorkspaceEscape
// and write no bytes outside the boundary.
func TestRuntimeExecutorTOCTOUSymlinkSwapFailsClosed(t *testing.T) {
	ws := t.TempDir()
	outside := t.TempDir()
	evil := filepath.Join(outside, "evil.txt")
	if err := os.WriteFile(evil, []byte("untouched"), 0o644); err != nil {
		t.Fatalf("write evil: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ws, "foo.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("write foo: %v", err)
	}

	scopeRoot, err := scope.Open(ws)
	if err != nil {
		t.Fatalf("scope.Open: %v", err)
	}
	defer func() { _ = scopeRoot.Close() }()

	sub := substrate.NewSubstrate(ws, nil, nil).WithScopeRoot(scopeRoot)
	exec := NewRuntimeExecutor(permittedGuard{}, sub, &occ.OCCGate{}, &mockCheckpoint{id: "chk-1"},
		domain.ResourceBudget{MaxFiles: 10, MaxDiffLines: 100}).WithScopeRoot(scopeRoot)

	intent := domain.ExecutionIntent{
		Objective:       validObjective(),
		Unit:            domain.ExecutionUnit{UnitID: "unit-toctou", FrameID: "frame-1", ObjectiveSlice: domain.ObjectiveSlice{Targets: []string{"foo.txt"}}},
		Budget:          domain.ResourceBudget{MaxFiles: 10, MaxDiffLines: 100},
		Capabilities:    domain.DomainCapabilitySet(domain.CapWrite | domain.CapPatch),
		SourceState:     domain.SourceState{FileHashes: map[string]string{"foo.txt": "hash1"}},
		Artifact:        validArtifact(),
		CheckpointID:    "chk-1",
		HasCheckpoint:   true,
		HumanApproved:   true,
		ExpectedVersion: 0,
		WorkflowState:   domain.StateBuilding,
	}

	// Adversary swaps the resolved file with an absolute symlink escape
	// immediately before execution.
	if err := os.Remove(filepath.Join(ws, "foo.txt")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := os.Symlink(evil, filepath.Join(ws, "foo.txt")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	if _, err := exec.Execute(context.Background(), intent); !errors.Is(err, ErrWorkspaceEscape) {
		t.Fatalf("Execute = %v, want ErrWorkspaceEscape", err)
	}

	data, err := os.ReadFile(evil)
	if err != nil {
		t.Fatalf("read evil: %v", err)
	}
	if string(data) != "untouched" {
		t.Fatalf("outside file mutated: %q", data)
	}
}

// TestBuildModeWithoutCapabilityTokenDenied is the mode-elevation test:
// an execution cycle under /build mode with an empty capability set must
// be dropped at the authorization gate with ErrCapabilityDenied, and the
// mode policy surface must not elevate the empty grant. Zero filesystem
// mutations must occur.
func TestBuildModeWithoutCapabilityTokenDenied(t *testing.T) {
	// The mode policy surface never elevates an explicit grant: /build
	// with no MutationCapability token yields an empty effective set.
	if got := modes.EffectiveCapabilities(modes.ModeBuild, 0); got != 0 {
		t.Fatalf("EffectiveCapabilities(build, empty) = %v, want empty: mode must not grant authority", got)
	}
	if modes.EffectiveCapabilitiesGrants(modes.ModeBuild, 0, modes.CapWrite) {
		t.Fatal("empty grant must not satisfy CapWrite under /build")
	}

	ws := t.TempDir()
	sub := substrate.NewSubstrate(ws, nil, nil)
	exec := NewRuntimeExecutor(&authorization.SimpleCapabilityGuard{}, sub, &occ.OCCGate{},
		&mockCheckpoint{id: "chk-1"}, domain.ResourceBudget{MaxFiles: 10, MaxDiffLines: 100})

	intent := domain.ExecutionIntent{
		Objective:       validObjective(),
		Unit:            domain.ExecutionUnit{UnitID: "unit-elev", FrameID: "frame-1", ObjectiveSlice: domain.ObjectiveSlice{Targets: []string{"main.go"}}},
		Budget:          domain.ResourceBudget{MaxFiles: 10, MaxDiffLines: 100},
		Capabilities:    domain.DomainCapabilitySet(0), // empty: no MutationCapability token
		SourceState:     domain.SourceState{FileHashes: map[string]string{"main.go": "hash1"}},
		Artifact:        validArtifact(),
		CheckpointID:    "chk-1",
		HasCheckpoint:   true,
		HumanApproved:   true,
		ExpectedVersion: 0,
		WorkflowState:   domain.StateBuilding,
	}
	if _, err := exec.Execute(context.Background(), intent); !errors.Is(err, authorization.ErrCapabilityDenied) {
		t.Fatalf("Execute = %v, want ErrCapabilityDenied", err)
	}

	// Zero filesystem mutations: the denied target must not exist.
	if _, err := os.Stat(filepath.Join(ws, "main.go")); !os.IsNotExist(err) {
		t.Fatalf("denied execution mutated the workspace: main.go exists (stat err = %v)", err)
	}
}
