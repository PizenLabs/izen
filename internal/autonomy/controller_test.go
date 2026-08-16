package autonomy

import "testing"

func controller() *AutonomyController {
	return NewAutonomyController()
}

func baseMutationInput() DecisionInput {
	return DecisionInput{
		Intent:            IntentModification,
		IntentConfidence:  0.9,
		TargetConfidence:  0.95,
		Target:            "index.html",
		MutationRisk:      MutationRiskInput{Level: RiskLow},
		AffectedScope:     1,
		RollbackAvailable: true,
		Granted:           CapabilitySet{CapRead, CapAnalyze, CapPropose, CapMutate, CapVerify},
	}
}

func TestDecideConversationDirectResponse(t *testing.T) {
	out := controller().Decide(DecisionInput{Intent: IntentConversation})
	if out.Decision != DecisionDirectResponse {
		t.Errorf("conversation decision = %s, want direct_response", out.Decision)
	}
	if !out.Decision.Continues() {
		t.Error("direct_response must count as continuing")
	}
}

func TestDecideUngrantedMutationAsksOnce(t *testing.T) {
	in := baseMutationInput()
	in.Granted = CapabilitySet{CapRead} // mutation capability not granted
	out := controller().Decide(in)
	if out.Decision != DecisionAskUser {
		t.Errorf("ungranted mutation = %s, want ask_user", out.Decision)
	}
	if !out.Missing.Has(CapMutate) {
		t.Errorf("missing = %v, want mutate", out.Missing)
	}
}

func TestDecideGrantedLowRiskAutoContinues(t *testing.T) {
	out := controller().Decide(baseMutationInput())
	if out.Decision != DecisionAutoContinue {
		t.Errorf("granted low-risk mutation = %s, want auto_continue", out.Decision)
	}
}

func TestDecideHighRiskAsks(t *testing.T) {
	in := baseMutationInput()
	in.MutationRisk = MutationRiskInput{Level: RiskHigh}
	if out := controller().Decide(in); out.Decision != DecisionAskUser {
		t.Errorf("high risk = %s, want ask_user", out.Decision)
	}

	// Critical risk with rollback asks; without rollback it blocks.
	in.MutationRisk = MutationRiskInput{Level: RiskCritical}
	if out := controller().Decide(in); out.Decision != DecisionAskUser {
		t.Errorf("critical+rollback = %s, want ask_user", out.Decision)
	}
	in.RollbackAvailable = false
	if out := controller().Decide(in); out.Decision != DecisionBlock {
		t.Errorf("critical without rollback = %s, want block", out.Decision)
	}
}

func TestDecideAmbiguousTargetAsks(t *testing.T) {
	in := baseMutationInput()
	in.Target = ""
	in.TargetConfidence = 0.5
	if out := controller().Decide(in); out.Decision != DecisionAskUser {
		t.Errorf("missing target = %s, want ask_user", out.Decision)
	}
}

func TestDecideLargeScopeAsks(t *testing.T) {
	in := baseMutationInput()
	in.AffectedScope = MaxAutonomousScope + 1
	if out := controller().Decide(in); out.Decision != DecisionAskUser {
		t.Errorf("large scope = %s, want ask_user", out.Decision)
	}
}

func TestDecideNoRollbackAsks(t *testing.T) {
	in := baseMutationInput()
	in.RollbackAvailable = false
	if out := controller().Decide(in); out.Decision != DecisionAskUser {
		t.Errorf("no rollback = %s, want ask_user", out.Decision)
	}
}

func TestDecideReadOnlyAutoContinues(t *testing.T) {
	in := DecisionInput{
		Intent:           IntentInvestigation,
		IntentConfidence: 0.9,
		Target:           "file.go",
		TargetConfidence: 0.95,
		Granted:          CapabilitySet{CapRead, CapAnalyze},
	}
	if out := controller().Decide(in); out.Decision != DecisionAutoContinue {
		t.Errorf("read-only = %s, want auto_continue", out.Decision)
	}
}

func TestDecideReadOnlyAmbiguousTargetAsks(t *testing.T) {
	in := DecisionInput{
		Intent:           IntentInvestigation,
		IntentConfidence: 0.9,
		Target:           "file.go",
		TargetConfidence: 0.4,
		Granted:          CapabilitySet{CapRead, CapAnalyze},
	}
	if out := controller().Decide(in); out.Decision != DecisionAskUser {
		t.Errorf("read-only ambiguous = %s, want ask_user", out.Decision)
	}
}

func TestDecideLowIntentConfidenceAsks(t *testing.T) {
	in := baseMutationInput()
	in.IntentConfidence = 0.4
	if out := controller().Decide(in); out.Decision != DecisionAskUser {
		t.Errorf("low intent confidence = %s, want ask_user", out.Decision)
	}
}

func TestDecideUnknownIntentAsks(t *testing.T) {
	out := controller().Decide(DecisionInput{Intent: IntentUnknown})
	if out.Decision != DecisionAskUser {
		t.Errorf("unknown intent = %s, want ask_user", out.Decision)
	}
}

func TestDecideDeterministic(t *testing.T) {
	in := baseMutationInput()
	if a, b := controller().Decide(in), controller().Decide(in); a.Decision != b.Decision {
		t.Error("decide must be a pure function of its input")
	}
}

func TestGrantRequest(t *testing.T) {
	req := NewGrantRequest("repository", CapabilitySet{CapMutate}, IntentModification, "index.html", RiskLow, 1)
	if req.Scope != "repository" || !req.Required.Has(CapMutate) {
		t.Errorf("unexpected grant request: %+v", req)
	}
}
