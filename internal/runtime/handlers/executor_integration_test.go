package handlers

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/core/authorization"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/runtime"
)

// executorMockProvider implements ai.Provider for the runtime-handler executor
// integration test.
type executorMockProvider struct {
	mu        sync.Mutex
	responses []*ai.Response
	callCount int
}

func (m *executorMockProvider) Name() string { return "mock" }

func (m *executorMockProvider) Execute(_ context.Context, req ai.Request) (*ai.Response, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.callCount >= len(m.responses) {
		return nil, fmt.Errorf("unexpected call #%d", m.callCount+1)
	}
	resp := m.responses[m.callCount]
	m.callCount++
	return resp, nil
}

func (m *executorMockProvider) ExecuteStream(_ context.Context, _ ai.Request) (io.ReadCloser, error) {
	return nil, fmt.Errorf("stream not supported")
}

// TestApprovePatchHandler_RoutesThroughExecutor proves the approval command
// drives a REAL mutation through the RuntimeExecutor (Rule 3: no fake states):
// the patch is applied to disk, the MutationSet is committed, and the canonical
// lifecycle events fire — instead of the old fabricated +1/-0 record.
func TestApprovePatchHandler_RoutesThroughExecutor(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "note.txt")
	original := "foo\nbar\nbaz\n"
	if err := os.WriteFile(target, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	bus := events.NewBus(events.DefaultBufferSize)
	c := subscribeCollect(bus,
		events.EventMutationStarted,
		events.EventMutationCompleted,
		events.EventVerificationCompleted,
		events.EventExecutionFinished,
		events.EventPatchApplied,
	)

	cfg := config.Default()
	mock := &executorMockProvider{responses: []*ai.Response{{
		Content: "<<<<<<< SEARCH\nbar\n=======\nqux\n>>>>>>>",
		Usage:   ai.ProviderUsage{Known: true, PromptTokens: 12, CompletionTokens: 7},
	}}}
	x := execution.NewRuntimeExecutor(root, cfg, mock, bus, "")
	trivial := execution.NewVerifier(root)
	trivial.SetCustomSteps([]execution.VerificationStep{{Name: "noop", Command: "true", Optional: false}})
	x.SetVerifier(trivial)
	x.SetAuthorization(&authorization.MutationAuthorization{
		ID:        authorization.NewAuthorizationID(),
		ExpiresAt: time.Now().Add(time.Hour),
	})

	deps := HandlerDeps{Bus: bus, Executor: x}
	h := New(deps).Approve()

	ctx := context.Background()
	// The presentation layer stages a pending mutation the same way the UI
	// does (submit request to the executor boundary).
	res, err := x.Execute(ctx, execution.ExecuteRequest{
		RequestID: "r1",
		Mode:      "build",
		Prompt:    "change bar to qux",
		Target:    "note.txt",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.PendingPatchID == "" {
		t.Fatal("expected pending patch id")
	}

	// Dispatch the canonical approval command through the runtime handler.
	if err := h.Handle(ctx, runtime.ApprovePatchCmd{PatchID: res.PendingPatchID}); err != nil {
		t.Fatalf("ApprovePatch: %v", err)
	}

	// The runtime applied the mutation to disk.
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) == original {
		t.Fatal("approve through the runtime did not mutate the file")
	}

	// The canonical lifecycle events fired (no fake PatchApplied).
	deadline := time.Now().Add(2 * time.Second)
	for !c.has(events.EventPatchApplied) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	for _, typ := range []string{
		events.EventMutationStarted,
		events.EventMutationCompleted,
		events.EventVerificationCompleted,
		events.EventExecutionFinished,
		events.EventPatchApplied,
	} {
		if !c.has(typ) {
			t.Errorf("missing event %q", typ)
		}
	}

	// The proof carries real evidence.
	apr, err := x.Approve(ctx, res.PendingPatchID)
	if err == nil {
		// Double-approve of the consumed patch must fail — no fake re-apply.
		t.Fatalf("double approve should fail, got nil (proof outcome %q)", apr.Proof.Outcome)
	}
}

// TestRejectPatchHandler_RoutesThroughExecutor proves rejection terminates the
// held mutation with zero disk mutation.
func TestRejectPatchHandler_RoutesThroughExecutor(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "note.txt")
	original := "foo\nbar\nbaz\n"
	if err := os.WriteFile(target, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	bus := events.NewBus(events.DefaultBufferSize)
	cfg := config.Default()
	mock := &executorMockProvider{responses: []*ai.Response{{
		Content: "<<<<<<< SEARCH\nbar\n=======\nqux\n>>>>>>>",
	}}}
	x := execution.NewRuntimeExecutor(root, cfg, mock, bus, "")
	trivial := execution.NewVerifier(root)
	trivial.SetCustomSteps([]execution.VerificationStep{{Name: "noop", Command: "true", Optional: false}})
	x.SetVerifier(trivial)
	x.SetAuthorization(&authorization.MutationAuthorization{
		ID:        authorization.NewAuthorizationID(),
		ExpiresAt: time.Now().Add(time.Hour),
	})

	deps := HandlerDeps{Bus: bus, Executor: x}
	h := New(deps).Reject()

	res, err := x.Execute(context.Background(), execution.ExecuteRequest{
		RequestID: "r2",
		Mode:      "build",
		Prompt:    "change bar to qux",
		Target:    "note.txt",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if err := h.Handle(context.Background(), runtime.RejectPatchCmd{PatchID: res.PendingPatchID, Reason: "nope"}); err != nil {
		t.Fatalf("RejectPatch: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Fatalf("reject mutated the file: %q", got)
	}
}
