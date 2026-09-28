// PHASE 13 — authorization UX: the action set is DERIVED from runtime state.
//
// The invariant under test is simple and absolute: an action may only be
// rendered when the object it acts on exists. An authorization request is raised
// BEFORE any artifact is generated, so at that moment there is no diff, and
// "Inspect Diff" must not appear. When a candidate/diff object does exist, the
// action must appear and must work.
package ui

import (
	"os"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/execution"
)

// authorizationTestModel returns a model with a staged authorization request and
// a real workspace to mutate.
func authorizationTestModel(t *testing.T) *model {
	t.Helper()
	t.Chdir(t.TempDir())
	m := autonomyTestModel()
	m.pendingAutonomyProposal = &autonomy.Proposal{
		Input:         "redesign a professional personal portfolio page",
		Intent:        autonomy.IntentModification,
		Workspace:     autonomy.WorkspaceBuild,
		Target:        "index.html",
		Risk:          autonomy.RiskLow,
		Scope:         ".",
		Required:      autonomy.CapabilitySet{autonomy.CapRead, autonomy.CapAnalyze, autonomy.CapPropose, autonomy.CapMutate},
		Missing:       autonomy.CapabilitySet{autonomy.CapRead, autonomy.CapAnalyze, autonomy.CapPropose, autonomy.CapMutate},
		AffectedScope: 1,
		Rollback:      true,
	}
	return m
}

// TestAuthorization_NoCandidateNoInspectDiff is the headline repair: with no
// candidate, the card offers exactly Execute and Cancel, states the requested
// capabilities, and says plainly that no mutation has occurred.
func TestAuthorization_NoCandidateNoInspectDiff(t *testing.T) {
	m := authorizationTestModel(t)

	view := stripANSITest(m.renderAutonomyProposalBlock(100))

	if _, ok := m.authorizationCandidate(); ok {
		t.Fatal("no candidate exists in this state")
	}
	if m.hasAuthorizationAction(autonomy.ActionInspect) {
		t.Fatal("Inspect must not be in the derived action set without a candidate")
	}
	if strings.Contains(view, "Inspect") {
		t.Fatalf("no Inspect action may render without a candidate:\n%s", view)
	}
	for _, want := range []string{
		"EXECUTION AUTHORIZATION",
		"Capabilities:", "read", "mutate",
		"No mutation has occurred.",
		"[Enter]", "Execute",
		"[Esc]", "Cancel",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("authorization card missing %q:\n%s", want, view)
		}
	}
}

// TestAuthorization_CandidateEnablesInspectDiff is the converse: a real
// candidate/diff object must surface the action, and activating it must reveal
// that candidate.
func TestAuthorization_CandidateEnablesInspectDiff(t *testing.T) {
	m := authorizationTestModel(t)
	// The runtime holds a real candidate: a staged patch with compiled content.
	m.pendingHotfixPatch = &execution.Patch{File: "index.html", Modified: "--- a/index.html\n+++ b/index.html\n@@ -1 +1 @@\n"}
	m.executorPendingPatchID = "patch-1"

	if !m.hasAuthorizationAction(autonomy.ActionInspect) {
		t.Fatal("a real candidate must put Inspect in the derived action set")
	}
	view := stripANSITest(m.renderAutonomyProposalBlock(100))
	if !strings.Contains(view, "Inspect Diff") {
		t.Fatalf("Inspect Diff must render once a candidate exists:\n%s", view)
	}

	// Navigating to Inspect and activating must reveal the candidate, not a
	// decision-detail-only panel.
	m.autonomyProposalSelect = 1
	if cmd := m.activateAutonomyProposal(); cmd != nil {
		t.Fatal("Inspect must not execute anything")
	}
	expanded := stripANSITest(m.renderAutonomyProposalBlock(100))
	if !strings.Contains(expanded, "Candidate diff:") {
		t.Fatalf("Inspect must reveal the real candidate diff:\n%s", expanded)
	}
	if !strings.Contains(expanded, "Decision detail:") {
		t.Fatalf("Inspect must also carry the decision facts:\n%s", expanded)
	}
}

// TestAuthorization_DiffBackedCandidateAlsoEnablesInspect proves the second
// real diff source: a staged proposal carrying a compiled diff. Both are objects
// the runtime actually produced, so both legitimately enable the action.
func TestAuthorization_DiffBackedCandidateAlsoEnablesInspect(t *testing.T) {
	m := authorizationTestModel(t)
	m.pendingProposals = []SemanticProposal{{
		ID:     "p1",
		Diff:   "--- a/index.html\n+++ b/index.html\n",
		Target: SemanticTarget{QualifiedName: "index.html"},
	}}

	if !m.hasAuthorizationAction(autonomy.ActionInspect) {
		t.Fatal("a staged proposal with a compiled diff must enable Inspect")
	}
	diff, ok := m.authorizationCandidate()
	if !ok || diff == "" {
		t.Fatal("the candidate must resolve to the actual diff text")
	}
}

// TestAuthorization_EmptyProposalDiffDoesNotEnableInspect proves an EMPTY diff
// string is not a candidate. A staged proposal whose diff is blank has nothing
// to inspect, and must not advertise an Inspect action.
func TestAuthorization_EmptyProposalDiffDoesNotEnableInspect(t *testing.T) {
	m := authorizationTestModel(t)
	m.pendingProposals = []SemanticProposal{{ID: "p1", Diff: ""}}

	if m.hasAuthorizationAction(autonomy.ActionInspect) {
		t.Fatal("a blank diff is not a candidate")
	}
}

// TestAuthorization_NavigationNeverLandsOnAHiddenAction proves the derived list
// is the single source for BOTH the rendered hints and the ↑/↓ cursor: a hidden
// action can never be highlighted, and therefore can never be activated.
func TestAuthorization_NavigationNeverLandsOnAHiddenAction(t *testing.T) {
	m := authorizationTestModel(t)
	actions := m.authorizationActions()
	if len(actions) != 2 {
		t.Fatalf("derived action set = %v, want exactly [Execute Cancel]", actions)
	}
	seen := map[autonomy.ProposalAction]bool{}
	for i := 0; i < 8; i++ {
		m.navigateAutonomyProposal(1)
		if m.autonomyProposalSelect < 0 || m.autonomyProposalSelect >= len(actions) {
			t.Fatalf("selection %d escaped the derived set %v", m.autonomyProposalSelect, actions)
		}
		seen[actions[m.autonomyProposalSelect]] = true
	}
	if seen[autonomy.ActionInspect] {
		t.Error("navigation must never land on an action that is not rendered")
	}
	if !seen[autonomy.ActionExecute] || !seen[autonomy.ActionCancel] {
		t.Errorf("every rendered action must be reachable, saw %v", seen)
	}
}

// TestAuthorization_CancelIssuesNoGrantAndNoMutation is the authority
// invariant: the UI can refuse, but refusing authorizes nothing and changes
// nothing.
func TestAuthorization_CancelIssuesNoGrantAndNoMutation(t *testing.T) {
	m := authorizationTestModel(t)
	orig := "<html><body><p>original</p></body></html>\n"
	if err := os.WriteFile("index.html", []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}

	if cmd := m.cancelAutonomyProposal(); cmd != nil {
		t.Fatal("cancel must not start any execution")
	}
	if m.pendingAutonomyProposal != nil {
		t.Fatal("cancel must clear the authorization request")
	}
	if m.autonomy.Grants().Count() != 0 {
		t.Fatal("cancel must not issue a capability grant")
	}
	if gotBytes, err := os.ReadFile("index.html"); err != nil {
		t.Fatal(err)
	} else if string(gotBytes) != orig {
		t.Fatalf("cancel mutated the workspace:\n%s", gotBytes)
	}
}

// TestAuthorization_ExecuteIssuesTheGrant is the positive authority invariant:
// the grant is issued by the runtime on the human's decision, and only then.
func TestAuthorization_ExecuteIssuesTheGrant(t *testing.T) {
	m := authorizationTestModel(t)
	// Execute continues into the workspace; a nil cmd is acceptable for an
	// engine that cannot proceed headless, but the GRANT must be issued.
	_ = m.executeAutonomyProposal()

	if m.pendingAutonomyProposal != nil {
		t.Error("Execute must consume the authorization request")
	}
	if !m.autonomy.Authority(autonomy.RequiredCapabilities(autonomy.IntentModification)) {
		t.Error("Execute must issue the internal capability grant")
	}
}
