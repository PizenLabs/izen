package ui

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/core/authorization"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/modes"
)

// blockingGateProvider blocks on the context until cancelled — it mimics a
// slow/stalled provider call so the gated-execution cancellation path is
// provable end to end.
type blockingGateProvider struct {
	started   chan struct{}
	cancelled chan struct{}
}

func (b *blockingGateProvider) Name() string { return "blocking" }

func (b *blockingGateProvider) Execute(ctx context.Context, _ ai.Request) (*ai.Response, error) {
	if b.started != nil {
		close(b.started)
	}
	select {
	case <-ctx.Done():
		if b.cancelled != nil {
			close(b.cancelled)
		}
		return nil, ctx.Err()
	case <-time.After(30 * time.Second):
		return nil, errors.New("provider was never cancelled")
	}
}

func (b *blockingGateProvider) ExecuteStream(context.Context, ai.Request) (io.ReadCloser, error) {
	return nil, errors.New("stream not supported")
}

// TestGatedExecutionCtrlCCancelsProviderCall pins P0 #1: a gated execution is
// bound to the ACTIVE OPERATION context, so Ctrl+C (handleEmergencyInterrupt →
// activeOp.Cancel) cancels the in-flight provider call immediately instead of
// leaving a detached background call running silently — and the terminal
// outcome is a CLEAN cancellation, never a fabricated failure.
func TestGatedExecutionCtrlCCancelsProviderCall(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html></html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	prov := &blockingGateProvider{started: make(chan struct{}), cancelled: make(chan struct{})}
	m := newTestModel()
	m.workspaceRoot = dir
	m.state = StateChat
	m.gateway = execution.NewIntentGateway(dir)
	m.executor = execution.NewRuntimeExecutor(dir, m.cfg, prov, nil, "")
	trivial := execution.NewVerifier(dir)
	trivial.SetCustomSteps([]execution.VerificationStep{{Name: "noop", Command: "true", Optional: false}})
	m.executor.SetVerifier(trivial)
	m.executor.SetAuthorization(&authorization.MutationAuthorization{
		ID:        authorization.NewAuthorizationID(),
		ExpiresAt: time.Now().Add(time.Hour),
	})
	m.resolver.Set(modes.ModeBuild)

	cmd := m.runGatedLine("$hot fix index.html")
	if cmd == nil {
		t.Fatal("gated dispatch returned nil command")
	}
	// The gated execution must own a real foreground operation (cancellable
	// context, watchdog, telemetry) — never a detached context.
	if m.activeOp == nil {
		t.Fatal("gated execution must bind an active operation")
	}

	done := make(chan gatedExecutionMsg, 1)
	go func() {
		msgs := drainCmds(t, cmd)
		for _, m := range msgs {
			if gem, ok := m.(gatedExecutionMsg); ok {
				done <- gem
				return
			}
		}
		t.Fatalf("no gatedExecutionMsg found in command result")
	}()
	<-prov.started

	// Ctrl+C: the universal emergency interrupt cancels the active operation
	// context, which the provider call inherits.
	m.handleEmergencyInterrupt("ctrl-c test")
	<-prov.cancelled

	msg := <-done
	if msg.res == nil {
		t.Fatal("nil execution result after cancellation")
	}
	if msg.res.Err != nil {
		t.Fatalf("cancelled execution returned err = %v, want nil (clean cancellation)", msg.res.Err)
	}
	if msg.res.Proof == nil || msg.res.Proof.Outcome != execution.OutcomeCancelled {
		t.Fatalf("proof outcome = %+v, want %s", msg.res.Proof, execution.OutcomeCancelled)
	}
	if msg.res.PendingPatchID != "" {
		t.Fatal("cancelled execution must not reach the approval gate")
	}
	// The operation was released: no active operation survives the cancel.
	if m.activeOp != nil {
		t.Fatal("active operation survived the Ctrl+C cancellation")
	}
}
