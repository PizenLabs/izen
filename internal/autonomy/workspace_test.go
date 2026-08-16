package autonomy

import "testing"

func TestWorkspaceContracts(t *testing.T) {
	// ASK: read only — never mutate, never verify.
	ask := ContractFor(WorkspaceAsk)
	if !ask.Allows(CapRead) {
		t.Error("ask must allow read")
	}
	if ask.Allows(CapMutate) {
		t.Error("ask must forbid mutate")
	}
	if ask.Allows(CapVerify) {
		t.Error("ask must forbid verify")
	}

	// INVESTIGATE: evidence collection, never mutation.
	inv := ContractFor(WorkspaceInvestigate)
	if !inv.Allows(CapAnalyze) {
		t.Error("investigate must allow analyze")
	}
	if inv.Allows(CapMutate) {
		t.Error("investigate must forbid mutate")
	}

	// BUILD: the only domain that may mutate.
	build := ContractFor(WorkspaceBuild)
	if !build.Allows(CapMutate) {
		t.Error("build must allow mutate")
	}
	if !build.Covers(CapabilitySet{CapRead, CapAnalyze, CapPropose, CapMutate, CapVerify}) {
		t.Error("build must cover the full capability vector")
	}

	// REVIEW: audit, never mutation.
	rev := ContractFor(WorkspaceReview)
	if !rev.Allows(CapVerify) {
		t.Error("review must allow verify")
	}
	if rev.Allows(CapMutate) {
		t.Error("review must forbid mutate")
	}

	// PLAN: propose, never mutate.
	plan := ContractFor(WorkspacePlan)
	if !plan.Allows(CapPropose) {
		t.Error("plan must allow propose")
	}
	if plan.Allows(CapMutate) {
		t.Error("plan must forbid mutate")
	}
}

func TestSelectWorkspaceMutationRequiresBuild(t *testing.T) {
	route := SelectWorkspace(IntentModification, RiskLow, RequiredCapabilities(IntentModification))
	if route.Workspace != WorkspaceBuild {
		t.Errorf("mutation workspace = %s, want build", route.Workspace)
	}
	if !route.Covers {
		t.Error("build contract must cover mutation capabilities")
	}
}

func TestSelectWorkspaceInvestigationReadOnly(t *testing.T) {
	route := SelectWorkspace(IntentInvestigation, RiskLow, RequiredCapabilities(IntentInvestigation))
	if route.Workspace != WorkspaceInvestigate {
		t.Errorf("investigation workspace = %s, want investigate", route.Workspace)
	}
	if !route.Covers {
		t.Error("investigate contract must cover read+analyze")
	}
}

func TestSelectWorkspacePlanning(t *testing.T) {
	route := SelectWorkspace(IntentPlanning, RiskLow, RequiredCapabilities(IntentPlanning))
	if route.Workspace != WorkspacePlan {
		t.Errorf("planning workspace = %s, want plan", route.Workspace)
	}
}

func TestSelectWorkspaceVerification(t *testing.T) {
	route := SelectWorkspace(IntentVerification, RiskLow, RequiredCapabilities(IntentVerification))
	if route.Workspace != WorkspaceReview {
		t.Errorf("verification workspace = %s, want review", route.Workspace)
	}
}

func TestSelectWorkspaceConversationNoWorkspace(t *testing.T) {
	route := SelectWorkspace(IntentConversation, RiskLow, nil)
	if route.Workspace != WorkspaceAsk {
		t.Errorf("conversation workspace = %s, want ask (direct response)", route.Workspace)
	}
}

func TestSelectWorkspaceNoCoverageFallsBack(t *testing.T) {
	// A verification intent whose required vector is unsatisfiable outside
	// review falls back deterministically, never invents a contract.
	route := SelectWorkspace(IntentVerification, RiskLow, CapabilitySet{CapRead, CapAnalyze, CapVerify})
	if route.Workspace != WorkspaceReview || !route.Covers {
		t.Errorf("verification fallback = %+v, want review/covers", route)
	}
}

func TestRiskLevelParsing(t *testing.T) {
	cases := map[string]RiskLevel{
		"low": RiskLow, "medium": RiskMedium, "moderate": RiskMedium,
		"high": RiskHigh, "critical": RiskCritical, "bogus": RiskUnknown,
	}
	for in, want := range cases {
		if got := ParseRisk(in); got != want {
			t.Errorf("ParseRisk(%q) = %v, want %v", in, got, want)
		}
	}
}
