package execution

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/core/authorization"
	"github.com/PizenLabs/izen/internal/events"
)

// validationMock implements ai.Provider for the end-to-end validation flow.
type validationMock struct {
	mu        sync.Mutex
	callCount int
}

func (m *validationMock) Name() string { return "mock" }

func (m *validationMock) Execute(_ context.Context, _ ai.Request) (*ai.Response, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callCount++
	return &ai.Response{
		Content: "<<<<<<< SEARCH\n<p>one</p>\n=======\n<p>one</p><p>two</p>\n>>>>>>>",
		Usage:   ai.ProviderUsage{Known: true, PromptTokens: 15, CompletionTokens: 8},
	}, nil
}

func (m *validationMock) ExecuteStream(_ context.Context, _ ai.Request) (io.ReadCloser, error) {
	return nil, fmt.Errorf("stream not supported")
}

// TestValidationPromptTargetedMutation is the acceptance contract:
//
// Given: "$prompt check index.html and remove extra contents"
// Expected event sequence (from the runtime, in order):
//
//	execution.started
//	strategy.selected
//	target.resolved
//	context.prepared
//	model.invoked (only if needed)
//	artifact.produced
//	approval.required
//	mutation.completed
//	verification.completed
//	execution.finished
//
// No: phase ask -> build, fake stage completed, UI-owned execution, hidden
// /build invocation.
func TestValidationPromptTargetedMutation(t *testing.T) {
	root := t.TempDir()
	writeValidationTarget(t, root, "index.html", "<html><body><p>one</p><p>two</p></body></html>\n")

	bus := events.NewBus(events.DefaultBufferSize)
	order := &sequence{mu: sync.Mutex{}, types: []string{}, done: make(chan struct{}, 1)}
	for _, typ := range []string{
		events.EventExecutionStarted,
		events.EventStrategySelected,
		events.EventTargetResolved,
		events.EventContextPrepared,
		events.EventModelInvoked,
		events.EventArtifactProduced,
		events.EventApprovalRequired,
		events.EventMutationStarted,
		events.EventMutationCompleted,
		events.EventVerificationCompleted,
		events.EventExecutionFinished,
		// Forbidden event types (must NEVER appear):
		events.EventStageCompleted,
		events.EventPhaseChanged,
	} {
		bus.Subscribe(typ, func(ev events.DomainEvent) {
			order.mu.Lock()
			order.types = append(order.types, ev.Type())
			order.mu.Unlock()
		})
	}
	bus.Subscribe(events.EventExecutionFinished, func(events.DomainEvent) {
		select {
		case order.done <- struct{}{}:
		default:
		}
	})

	cfg := config.Default()
	mock := &validationMock{}
	x := NewRuntimeExecutor(root, cfg, mock, bus, "")
	trivial := NewVerifier(root)
	trivial.SetCustomSteps([]VerificationStep{{Name: "noop", Command: "true", Optional: false}})
	x.SetVerifier(trivial)
	x.SetAuthorization(&authorization.MutationAuthorization{
		ID:        authorization.NewAuthorizationID(),
		ExpiresAt: time.Now().Add(time.Hour),
	})

	// ── 1. Intent Gateway: "$prompt ..." produces an ExecuteRequest ──
	g := NewIntentGateway(root)
	req, det, err := g.Gate(context.Background(), "$prompt check index.html and remove extra contents")
	if err != nil {
		t.Fatalf("gate: %v", err)
	}
	if det.Directive != "prompt" {
		t.Fatalf("directive = %q, want prompt", det.Directive)
	}
	if !strings.Contains(string(det.Profile.Strategy), "mutation") && det.Profile.Strategy != "targeted_mutation" {
		t.Fatalf("strategy = %s, want targeted_mutation", det.Profile.Strategy)
	}
	if len(req.Targets) == 0 || req.Targets[0] != "index.html" {
		t.Fatalf("targets = %v, want [index.html]", req.Targets)
	}

	// ── 2. RuntimeExecutor.Execute (runtime owns the model + context) ──
	res, err := x.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.PendingPatchID == "" {
		t.Fatal("expected a pending patch id (approval gate)")
	}

	// ── 3. Approve (runtime owns mutation + verification) ──────────────
	apr, err := x.Approve(context.Background(), res.PendingPatchID)
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if got := mustReadValidation(t, root, "index.html"); got == "<html><body><p>one</p><p>two</p></body></html>\n" {
		t.Fatal("approve did not mutate the file")
	}
	if apr.Proof == nil || !apr.Proof.Outcome.MutationSucceeded() {
		t.Fatalf("proof outcome = %v, want a succeeded mutation", apr.Proof)
	}

	// ── 4. The runtime emitted the canonical event sequence ────────────
	<-order.done
	time.Sleep(50 * time.Millisecond) // drain any trailing events
	order.mu.Lock()
	got := append([]string{}, order.types...)
	order.mu.Unlock()

	want := []string{
		events.EventExecutionStarted,
		events.EventStrategySelected,
		events.EventTargetResolved,
		events.EventContextPrepared,
		events.EventModelInvoked,
		events.EventArtifactProduced,
		events.EventApprovalRequired,
		events.EventMutationStarted,
		events.EventMutationCompleted,
		events.EventVerificationCompleted,
		events.EventExecutionFinished,
	}
	// Order-insensitive membership + exact presence.
	for _, w := range want {
		found := false
		for _, g := range got {
			if g == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing canonical event %q; got %v", w, got)
		}
	}
	// No fake stage completions, no phase transitions.
	for _, g := range got {
		if g == events.EventStageCompleted {
			t.Error("fake stage.completed emitted — the runtime must not fabricate stages")
		}
		if g == events.EventPhaseChanged {
			t.Error("phase.changed emitted — execution must not trigger mode transitions")
		}
	}

	// ── 5. Evidence: real verifier result + authoritative provider usage ──
	if !apr.Verification.Passed {
		t.Fatalf("verification evidence missing or failed: %+v", apr.Verification)
	}
	if len(apr.Proof.ModelInvocations) != 1 || apr.Proof.ModelInvocations[0].TokenOutput != 8 {
		t.Fatalf("token accounting must come only from provider usage: %+v", apr.Proof.ModelInvocations)
	}
	if mock.callCount != 1 {
		t.Fatalf("provider invoked %d times, want exactly 1", mock.callCount)
	}
}

type sequence struct {
	mu    sync.Mutex
	types []string
	done  chan struct{}
}

func writeValidationTarget(t *testing.T, root, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustReadValidation(t *testing.T, root, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
