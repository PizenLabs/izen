package autonomy

// ── TARGETLESS INVESTIGATION ACCEPTANCE ─────────────────────────────────────
//
// The directive's central distinction is KNOWN TARGET vs TARGET DISCOVERY. A
// read-only investigation must be able to BEGIN with an unknown target, observe
// the repository as authoritative EVIDENCE, answer from that evidence, and reach
// a truthful terminal state — without ever turning a discovered candidate into
// mutation authority.
//
// These tests drive the real Driver + RuntimeExecutor over a real workspace.

import (
	"context"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/execution"
)

// TestTargetlessInvestigation_ProvesFromRepositoryEvidence is the primary
// investigation capability acceptance test. It proves:
//
//  1. the objective begins with NO known target;
//  2. the runtime observes the repository as authoritative EVIDENCE;
//  3. the model is given that observed material (it does not answer from priors);
//  4. the objective reaches PROVEN with ZERO mutation;
//  5. the repository files are never bound as mutation targets.
func TestTargetlessInvestigation_ProvesFromRepositoryEvidence(t *testing.T) {
	root, mock, a, _ := testHarness(t, []*ai.Response{
		{Content: "The user endpoint fails because userHandler passes an empty id to findUser. No files were modified."},
		{Content: "The user endpoint fails because userHandler passes an empty id to findUser. No files were modified."},
		{Content: "The user endpoint fails because userHandler passes an empty id to findUser. No files were modified."},
	})
	d := NewDriver(a, nil)

	term, err := d.Run(context.Background(),
		"investigate why this application returns an error from the user endpoint. Do not modify anything.")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if term == nil || term.State != autonomy.RuntimeCompleted {
		t.Fatalf("targetless investigation did not complete: term=%+v state=%s contract=%s reason=%s",
			term, d.State(), d.lastContract.Kind, d.objectiveEvaluation().Reason)
	}
	if got := d.objectiveEvaluation().Outcome; got != execution.ObjectiveProven {
		t.Fatalf("objective outcome = %s (%s), want PROVEN", got, d.objectiveEvaluation().Reason)
	}
	// The contract must be a read-only one; no mutation was requested.
	if d.lastContract.Kind != execution.TaskRead && d.lastContract.Kind != execution.TaskReview {
		t.Fatalf("contract kind = %s, want READ or REVIEW", d.lastContract.Kind)
	}
	// The observation requirement is satisfied by REAL repository evidence.
	if obs := d.obs.Objective.RepositoryObservations; obs < 1 {
		t.Fatalf("repository observations = %d; a targetless investigation must observe the repository", obs)
	}
	// Mutation authority is untouched: no boundary, no changed bytes.
	if b := d.Boundary(); b != nil {
		t.Fatalf("a read-only investigation parked at a mutation boundary: %+v", b)
	}
	if got := readTarget(t, root, "note.txt"); got != sampleOriginal {
		t.Fatalf("a read-only investigation mutated the workspace: %q", got)
	}

	// The model was actually shown observed repository material, not asked to
	// answer from priors.
	if !requestsMention(mock, "note.txt") {
		t.Fatalf("no provider request carried the observed repository file; investigation was not evidence-grounded")
	}
}

// TestTargetlessInvestigation_NegatedConstraintStillProves is the regression
// for the dispatch contract guard: a targetless investigation whose text
// contains a NEGATED change verb ("Do not modify anything") must not be forced
// into a mutation contract by a substring scan. It must run read-only, observe
// the repository, and reach PROVEN without mutating anything.
func TestTargetlessInvestigation_NegatedConstraintStillProves(t *testing.T) {
	root, _, a, _ := testHarness(t, []*ai.Response{
		{Content: "The endpoint fails because findUser is called with an empty id."},
		{Content: "The endpoint fails because findUser is called with an empty id."},
		{Content: "The endpoint fails because findUser is called with an empty id."},
	})
	d := NewDriver(a, nil)

	term, err := d.Run(context.Background(),
		"investigate why the user endpoint is failing. Do not modify anything.")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if term == nil || term.State != autonomy.RuntimeCompleted {
		t.Fatalf("negated read-only investigation did not complete: term=%+v state=%s reason=%s",
			term, d.State(), d.objectiveEvaluation().Reason)
	}
	if d.lastContract.RequiresMutation() {
		t.Fatalf("a read-only investigation derived a mutation contract: %s", d.lastContract.Kind)
	}
	if got := readTarget(t, root, "note.txt"); got != sampleOriginal {
		t.Fatalf("discovery produced a workspace mutation: %q", got)
	}
}

// requestsMention reports whether any recorded provider request's messages
// mention needle.
func requestsMention(m *mockProvider, needle string) bool {
	for _, req := range m.recordedRequests() {
		if strings.Contains(req.System, needle) {
			return true
		}
		for _, msg := range req.Messages {
			if strings.Contains(msg.Content, needle) {
				return true
			}
		}
	}
	return false
}
