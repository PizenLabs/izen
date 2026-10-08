package execution

// ── CONSTRAINED MODEL + CREATION ─────────────────────────────────────────────
//
// The constrained-output invariant (max_output <= 1024 / ":free") forces the
// bounded SEARCH/REPLACE contract for PATCH-SHAPED artifacts. That contract is
// only sound when there is existing content to anchor a SEARCH block against.
// A creation target does not exist yet, so forcing it into search_replace asks
// the model for a patch against nothing — an impossible artifact that
// guarantees failure and burns the constrained budget proving it.
//
// These tests pin the boundary: a constrained model still creates a new file
// through the FULL-ARTIFACT contract (not SEARCH/REPLACE), and hidden reasoning
// is disabled so the small shared budget is not consumed by chain-of-thought.

import (
	"context"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution/strategy"
)

const constrainedCreatePrompt = "Create a file named testfile.md with the content 'Hello World'."

func TestConstrainedModelCreateKeepsFullArtifactContract(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "README.md", "# Sample Project\n\nA small fixture.\n")

	bus := events.NewBus(events.DefaultBufferSize)
	mock := &mockProvider{responses: []*ai.Response{{
		Content: "Hello World\n",
		Usage:   ai.ProviderUsage{Known: true, PromptTokens: 40, CompletionTokens: 4, FinishReason: "stop"},
	}}}
	cfg := config.Default()
	cfg.Models.SessionModel = "cohere/north-mini-code:free"
	x := NewRuntimeExecutor(root, cfg, mock, bus, "")
	x.SetVerifier(trivialVerifier(root))
	x.SetAuthorization(testAuthorization())

	profile := NewIntentGateway(root).SelectStrategy(constrainedCreatePrompt)
	if profile.Artifact.Kind != "create_file" {
		t.Fatalf("production strategy selected artifact %q, want create_file", profile.Artifact.Kind)
	}

	res, err := x.Execute(context.Background(), ExecuteRequest{
		RequestID:       "constrained-create",
		Mode:            "autonomy",
		Prompt:          constrainedCreatePrompt,
		Target:          "testfile.md",
		Targets:         []string{"testfile.md"},
		Strategy:        &profile,
		MaxOutputTokens: profile.MaxOutputTokens,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res == nil {
		t.Fatal("nil execution result")
	}
	if len(mock.requests) != 1 {
		t.Fatalf("provider requests = %d, want 1", len(mock.requests))
	}
	sent := mock.requests[0]
	if strings.Contains(sent.System, "You are the bounded patch engine") {
		t.Fatalf("a constrained CREATION was dispatched the bounded-patch engine prompt:\n%s", sent.System)
	}
	if strings.Contains(sent.System, "MUST NOT rewrite or re-emit the whole file") {
		t.Fatalf("a constrained CREATION was forbidden from emitting the new file:\n%s", sent.System)
	}
	if !strings.Contains(sent.System, "You are the bounded mutation engine") {
		t.Fatalf("a constrained creation did not use the full-artifact mutation lane:\n%s", sent.System)
	}
	if !strings.Contains(sent.System, "FILE_CREATE") {
		t.Fatalf("a constrained creation was not asked for the create envelope:\n%s", sent.System)
	}
	if sent.Reasoning == nil || !sent.Reasoning.Disabled {
		t.Fatalf("constrained creation did not disable hidden reasoning: %+v", sent.Reasoning)
	}
	if sent.MaxTokens <= 0 || sent.MaxTokens > ConstrainedMaxTokens {
		t.Fatalf("constrained creation budget = %d, want within (0, %d]", sent.MaxTokens, ConstrainedMaxTokens)
	}

	// The creation is staged, not applied; approval must materialize the bytes.
	if res.PendingPatchID == "" {
		t.Fatalf("no candidate staged for approval: %+v", res)
	}
	if _, err := x.Approve(context.Background(), res.PendingPatchID); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if got := readFileAt(t, root+"/testfile.md"); strings.TrimSpace(got) != "Hello World" {
		t.Fatalf("created content = %q, want %q", got, "Hello World")
	}
}

// TestConstrainedModelModifyStillForcesBoundedPatch is the negative half: the
// fix must NOT weaken the existing-content case. A constrained model editing an
// EXISTING file still runs the bounded SEARCH/REPLACE contract.
func TestConstrainedModelModifyStillForcesBoundedPatch(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "note.txt", sampleOriginal)

	bus := events.NewBus(events.DefaultBufferSize)
	mock := &mockProvider{responses: []*ai.Response{{
		Content: sampleReplace,
		Usage:   ai.ProviderUsage{Known: true, FinishReason: "stop"},
	}}}
	cfg := config.Default()
	cfg.Models.SessionModel = "cohere/north-mini-code:free"
	x := NewRuntimeExecutor(root, cfg, mock, bus, "")
	x.SetVerifier(trivialVerifier(root))
	x.SetAuthorization(testAuthorization())

	profile := NewIntentGateway(root).SelectStrategy("change bar to qux in note.txt")
	if profile.Artifact.Kind != "replace_block" {
		t.Fatalf("production strategy selected artifact %q, want replace_block", profile.Artifact.Kind)
	}
	if _, err := x.Execute(context.Background(), ExecuteRequest{
		RequestID:       "constrained-modify",
		Mode:            "autonomy",
		Prompt:          "change bar to qux in note.txt",
		Target:          "note.txt",
		Targets:         []string{"note.txt"},
		Strategy:        &profile,
		MaxOutputTokens: profile.MaxOutputTokens,
	}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(mock.requests) != 1 {
		t.Fatalf("provider requests = %d, want 1", len(mock.requests))
	}
	if sent := mock.requests[0]; !strings.Contains(sent.System, "<<<<<<< SEARCH") {
		t.Fatalf("a constrained MODIFY of an existing file was not asked for the bounded SEARCH/REPLACE contract:\n%s", sent.System)
	}
}

var _ = strategy.OperationCreate
