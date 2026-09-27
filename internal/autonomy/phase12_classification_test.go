package autonomy

// PHASE 12 — a change verb decides the intent.
//
// The production `$prompt` failure this file locks was: a request that
// unambiguously requires new files ("redesign a professional personal
// portfolio page … using HTML, CSS, and JS") classified as read-only
// `IntentPlanning` because `planningPatterns` carries the bare substring
// "design" and the planning match was NOT gated on a change verb — unlike
// investigation and verification, which both were. The objective then routed
// to the read-only `WorkspacePlan`, the Driver was never entered, and the
// human had to re-issue the request as `/build`.
//
// These tests pin the corrected precedence: a change verb wins, pure design
// advice stays read-only, and nothing about the fix grants authority — the
// capability table still decides which workspace may mutate.

import (
	"reflect"
	"testing"
)

// TestPhase12_ChangeVerbIsNotVetoedByDesignWord is the primary regression
// test: the reported portfolio objective must classify as a mutation and route
// to the BUILD capability domain without a manual /build.
func TestPhase12_ChangeVerbIsNotVetoedByDesignWord(t *testing.T) {
	const portfolio = "Please review this project and redesign a professional " +
		"personal portfolio page for me using HTML, CSS, and JS; " +
		"the author's name is Tom Hunter, an AI Engineer."

	res := Classify(portfolio, nil)
	if res.Intent != IntentModification {
		t.Fatalf("portfolio redesign intent = %s (%s), want modification",
			res.Intent, res.Explanation)
	}
	if !res.RequiresMutation() {
		t.Fatal("a redesign that produces new files must require mutation")
	}
	route := SelectWorkspace(res.Intent, RiskMedium, RequiredCapabilities(res.Intent))
	if route.Workspace != WorkspaceBuild {
		t.Fatalf("workspace = %q (%s), want build — $prompt must not require a manual /build",
			route.Workspace, route.Reason)
	}
}

// TestPhase12_ChangeVerbTable pins the compound change verbs that were missing
// from modificationPatterns entirely.
func TestPhase12_ChangeVerbTable(t *testing.T) {
	cases := []string{
		"redesign the landing page with a hero section",
		"restyle every card in @index.html",
		"recreate the settings panel",
		"rework the navigation",
		"overhaul the settings screen",
		"revamp the homepage",
	}
	for _, c := range cases {
		res := Classify(c, nil)
		if res.Intent != IntentModification && res.Intent != IntentRefactoring {
			t.Errorf("Classify(%q) = %s (%s), want a change intent",
				c, res.Intent, res.Explanation)
		}
		if !res.RequiresMutation() {
			t.Errorf("Classify(%q) must require mutation", c)
		}
	}
}

// TestPhase12_DesignAdviceStaysReadOnly is the negative half: pure design
// phrasing with NO change verb must remain read-only planning. A repair that
// simply appended "design" to modificationPatterns would fail here.
func TestPhase12_DesignAdviceStaysReadOnly(t *testing.T) {
	cases := []string{
		"plan the migration to the new event bus",
		"design the migration architecture for the billing service",
		"how should we design the ingestion pipeline",
		"what is the best way to architect the storage layer",
		"draft a roadmap for the next quarter",
	}
	for _, c := range cases {
		res := Classify(c, nil)
		if res.Intent != IntentPlanning {
			t.Errorf("Classify(%q) = %s (%s), want planning", c, res.Intent, res.Explanation)
		}
		if res.RequiresMutation() {
			t.Errorf("Classify(%q) must not require mutation", c)
		}
		route := SelectWorkspace(res.Intent, RiskLow, RequiredCapabilities(res.Intent))
		if route.Workspace == WorkspaceBuild {
			t.Errorf("Classify(%q) routed to BUILD — read-only advice must stay read-only", c)
		}
	}
}

// TestPhase12_DebuggingStillDominates guards the original reason planning was
// ordered before modification: a failure question must never route to mutation.
func TestPhase12_DebuggingStillDominates(t *testing.T) {
	cases := []string{
		"why is the build failing after the fix",
		"why is the design system breaking the build",
		"why does the redesign crash on startup",
	}
	for _, c := range cases {
		res := Classify(c, nil)
		if res.Intent == IntentModification || res.Intent == IntentRefactoring {
			t.Errorf("Classify(%q) = %s, want a read-only diagnostic intent", c, res.Intent)
		}
	}
}

// TestPhase12_ClassificationGrantsNoAuthority is the negative architecture
// assertion for this change: a BUILD route is a capability-domain decision,
// never an authorization. Only WorkspaceBuild's contract may carry CapMutate,
// and the mutation still has to cross AuthorizationEngine.
func TestPhase12_ClassificationGrantsNoAuthority(t *testing.T) {
	res := Classify("redesign the whole project", nil)
	if res.Intent != IntentModification {
		t.Fatalf("intent = %s, want modification", res.Intent)
	}
	if !RequiredCapabilities(res.Intent).RequiresMutate() {
		t.Fatal("modification must require CapMutate")
	}
	for ws, c := range WorkspaceContracts {
		if ws == WorkspaceBuild {
			continue
		}
		if c.Allows(CapMutate) {
			t.Errorf("workspace %q grants CapMutate — BUILD must remain the only mutation domain", ws)
		}
	}
	if !ContractFor(WorkspaceBuild).Allows(CapMutate) {
		t.Fatal("BUILD must remain the only domain permitting CapMutate")
	}
	// A classified mutation still routes through the gateway, which mints
	// ScopeDynamic. The classifier itself mints nothing: IntentResult has no
	// scope/authorization/capability-grant field at all.
	typ := reflect.TypeOf(res)
	for _, forbidden := range []string{"ScopeProvenance", "Scope", "Authorization", "Grant", "Capabilities"} {
		if _, ok := typ.FieldByName(forbidden); ok {
			t.Errorf("IntentResult gained the authority-bearing field %q — classification must grant nothing",
				forbidden)
		}
	}
}
