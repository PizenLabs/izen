package cli

// ── EXECUTION PARITY: legacy CLI path vs authoritative execution plane ───────
//
// The migration moves `izen prompt` / `izen orchestrate` off the CLI's private
// proposal lifecycle and onto the authoritative runtime. The behavioral
// contract that must not change is the MUTATION OUTCOME for a given operator
// decision: an explicit yes applies exactly the reviewed bytes; anything else
// leaves the workspace untouched.
//
// This test runs the SAME fixture, the SAME model content and the SAME operator
// decision through both owners:
//
//   legacy        : cli.Stack.Run  → orchestrator.RunCycle → FileExecutor
//   authoritative : execution.RuntimeExecutor (the plane the autonomy Driver
//                   drives through ExecutorAdapter; the Driver adds the loop,
//                   not the bytes)
//
// and asserts the resulting file bytes are identical. It is a real execution
// test, not an interface-shape check.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/core/authorization"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/runtime/orchestrator"
)

// parityProvider is a minimal ai.Provider: streaming is refused so the executor
// uses its non-stream path, exactly as the executor's own tests do.
type parityProvider struct {
	content string
}

func (p *parityProvider) Name() string { return "parity-mock" }

func (p *parityProvider) Execute(_ context.Context, _ ai.Request) (*ai.Response, error) {
	return &ai.Response{
		Content: p.content,
		Usage:   ai.ProviderUsage{Known: true, FinishReason: "stop"},
	}, nil
}

func (p *parityProvider) ExecuteStream(_ context.Context, _ ai.Request) (io.ReadCloser, error) {
	return nil, errors.New("stream not supported")
}

func syntheticAuthorization() *authorization.MutationAuthorization {
	return &authorization.MutationAuthorization{
		ID:        authorization.NewAuthorizationID(),
		ExpiresAt: time.Now().Add(time.Hour),
	}
}

// runLegacyCLI drives the historical CLI proposal lifecycle for one decision.
func runLegacyCLI(t *testing.T, content, decision string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stack := Wire(&stubLLM{content: content}, dir, strings.NewReader(decision), &bytes.Buffer{})
	if _, err := stack.Run(context.Background(), dir, "note.txt"); err != nil {
		// A rejection is a legitimate terminal here; only unexpected errors fail.
		if !errors.Is(err, orchestrator.ErrExecutionRejected) {
			t.Fatalf("legacy Run: %v", err)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// runAuthoritativePlane drives the authoritative RuntimeExecutor — the exact
// execution plane the autonomy Driver wraps — for one decision.
func runAuthoritativePlane(t *testing.T, content, decision string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	bus := events.NewBus(events.DefaultBufferSize)
	exec := execution.NewRuntimeExecutor(dir, config.Default(), &parityProvider{content: content}, bus, "")
	exec.SetAuthorization(syntheticAuthorization())

	gateway := execution.NewIntentGateway(dir)
	profile := gateway.SelectStrategy("note.txt")
	res, err := exec.Execute(context.Background(), execution.ExecuteRequest{
		RequestID: "parity-authoritative",
		Mode:      "build",
		Prompt:    "note.txt",
		Target:    "note.txt",
		Targets:   []string{"note.txt"},
		Strategy:  &profile,
	})
	if err != nil {
		t.Fatalf("authoritative Execute: %v", err)
	}
	if strings.EqualFold(strings.TrimSpace(decision), "y") {
		if res.PendingPatchID == "" {
			t.Fatalf("approved decision but no candidate staged: %+v", res)
		}
		if _, err := exec.Approve(context.Background(), res.PendingPatchID); err != nil {
			t.Fatalf("authoritative Approve: %v", err)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestParity_ApproveAppliesIdenticalBytes(t *testing.T) {
	t.Parallel()
	const content = "new content\n"
	legacy := runLegacyCLI(t, content, "y\n")
	authoritative := runAuthoritativePlane(t, content, "y\n")
	if legacy != authoritative {
		t.Fatalf("approve parity: legacy=%q authoritative=%q", legacy, authoritative)
	}
	if strings.TrimSpace(legacy) != "new content" {
		t.Fatalf("approved bytes = %q, want the reviewed content", legacy)
	}
}

func TestParity_RejectLeavesWorkspaceUntouched(t *testing.T) {
	t.Parallel()
	const content = "new content\n"
	legacy := runLegacyCLI(t, content, "n\n")
	authoritative := runAuthoritativePlane(t, content, "n\n")
	if legacy != authoritative {
		t.Fatalf("reject parity: legacy=%q authoritative=%q", legacy, authoritative)
	}
	if legacy != "old\n" {
		t.Fatalf("rejected workspace = %q, want untouched %q", legacy, "old\n")
	}
}
