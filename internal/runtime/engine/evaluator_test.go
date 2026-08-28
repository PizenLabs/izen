package engine

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/runtime/autonomy"
)

func writeFile(t *testing.T, root, name, content string) error {
	t.Helper()
	return os.WriteFile(filepath.Join(root, name), []byte(content), 0o644)
}

// TestEngine_BlocksInferenceOnFailedPreflight runs the Zero-Token
// EVALUATING_SCOPE step over a structurally corrupt HTML file (an unterminated
// <script> raw-text element). The fail-closed gate MUST:
//   - advance the state machine to AWAITING_HUMAN_PROPOSAL (never DECIDING),
//   - leave the token count at 0 (no inference), and
//   - never invoke the DAG staging callback.
func TestEngine_BlocksInferenceOnFailedPreflight(t *testing.T) {
	corrupt := []byte("<!DOCTYPE html>\n<html>\n<head><title>Broken</title></head>\n<body>\n" +
		"<script>\n  console.log('under construction');\n" +
		"<section><h2>Filler</h2><p>lorem ipsum dolor sit amet</p></section>\n" +
		"</body>\n</html>\n")

	staged := false
	ctx := &ExecutionContext{
		Root:            t.TempDir(),
		Target:          "index.html",
		Content:         corrupt,
		MaxOutputTokens: 1024,
		StateMachine:    autonomy.NewScopeStateMachine(),
		StagePlan: func() error {
			staged = true
			return nil
		},
	}

	// Advance to EVALUATING_SCOPE (the step expects to run from there).
	if _, err := ctx.StateMachine.Observe("context collected"); err != nil {
		t.Fatalf("observe: %v", err)
	}

	err := RunEvaluatingScopeStep(ctx)

	// 1. Fail-closed error returned.
	if !errors.Is(err, ErrEvaluatingScopeBarrier) {
		t.Fatalf("error = %v, want ErrEvaluatingScopeBarrier", err)
	}
	if err == nil || !strings.Contains(err.Error(), "EVALUATING_SCOPE_BARRIER") {
		t.Fatalf("error must carry EVALUATING_SCOPE_BARRIER, got: %v", err)
	}

	// 2. State diverted to AWAITING_HUMAN_PROPOSAL, never DECIDING.
	if ctx.StateMachine.State() != autonomy.StateAwaitingHumanProposal {
		t.Fatalf("state = %s, want awaiting_human_proposal", ctx.StateMachine.State())
	}

	// 3. Zero LLM tokens spent.
	if ctx.TokensSpent != 0 {
		t.Fatalf("TokensSpent = %d, want 0 — the zero-token boundary was violated", ctx.TokensSpent)
	}

	// 4. No DAG staging occurred.
	if staged {
		t.Fatal("DAG staging was invoked on a failed preflight — the fail-closed gate must block staging")
	}
}

// TestEngine_ValidPreflightAdvancesToDeciding runs the Zero-Token
// EVALUATING_SCOPE step over a structurally valid, resolved, in-budget target.
// The gate MUST pass, advance the machine to DECIDING, and spend zero tokens.
func TestEngine_ValidPreflightAdvancesToDeciding(t *testing.T) {
	root := t.TempDir()
	_ = writeFile(t, root, "app.js", "console.log('hi');\n")
	valid := []byte("<!DOCTYPE html>\n<html>\n<head><title>OK</title>\n" +
		"<script src=\"app.js\"></script>\n</head>\n<body>\n<p>hi</p>\n</body>\n</html>\n")

	ctx := &ExecutionContext{
		Root:            root,
		Target:          "index.html",
		Content:         valid,
		MaxOutputTokens: 1024,
		StateMachine:    autonomy.NewScopeStateMachine(),
	}
	if _, err := ctx.StateMachine.Observe("context collected"); err != nil {
		t.Fatalf("observe: %v", err)
	}

	if err := RunEvaluatingScopeStep(ctx); err != nil {
		t.Fatalf("valid target must pass the gate, got error: %v", err)
	}
	if ctx.StateMachine.State() != autonomy.StateDeciding {
		t.Fatalf("state = %s, want deciding", ctx.StateMachine.State())
	}
	if ctx.TokensSpent != 0 {
		t.Fatalf("TokensSpent = %d, want 0", ctx.TokensSpent)
	}
}
