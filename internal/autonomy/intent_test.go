package autonomy

import (
	"strings"
	"testing"
)

func TestClassifyConversation(t *testing.T) {
	cases := []string{"hi", "hello", "thanks", "thank you so much", "who are you", "good morning"}
	for _, c := range cases {
		res := Classify(c, nil)
		if res.Intent != IntentConversation {
			t.Errorf("Classify(%q) = %s, want conversation", c, res.Intent)
		}
		if res.Intent.RequiresWorkspace() {
			t.Errorf("Classify(%q) conversation must not require a workspace", c)
		}
	}
}

func TestClassifyConversationDoesNotSwallowTasks(t *testing.T) {
	// A greeting opener with a task tail is a task, never chat.
	res := Classify("hi, can you remove the footer", nil)
	if res.Intent != IntentModification {
		t.Errorf("Classify(greeting+task) = %s, want modification", res.Intent)
	}
}

func TestClassifyInvestigation(t *testing.T) {
	res := Classify("$prompt inspect @file.go", nil)
	if res.Intent != IntentInvestigation {
		t.Errorf("inspect intent = %s, want investigation", res.Intent)
	}
	if len(res.Targets) == 0 || res.Targets[0] != "file.go" {
		t.Errorf("targets = %v, want [file.go]", res.Targets)
	}
	if !res.Required.Has(CapAnalyze) || res.Required.RequiresMutate() {
		t.Errorf("required = %v, want read+analyze only", res.Required)
	}
}

func TestClassifyModification(t *testing.T) {
	res := Classify("$prompt remove unused content from @index.html", nil)
	if res.Intent != IntentModification {
		t.Errorf("modification intent = %s, want modification", res.Intent)
	}
	if !res.RequiresMutation() {
		t.Error("modification intent must require mutation")
	}
	if len(res.Targets) == 0 || res.Targets[0] != "index.html" {
		t.Errorf("targets = %v, want [index.html]", res.Targets)
	}
	if !res.Required.Has(CapMutate) {
		t.Errorf("required = %v, want mutation capability", res.Required)
	}
	if !res.Required.Has(CapRead) || !res.Required.Has(CapAnalyze) || !res.Required.Has(CapPropose) {
		t.Errorf("required = %v, want read+analyze+propose+mutate", res.Required)
	}
}

func TestClassifyCheckThenMutateIsMutation(t *testing.T) {
	// "check X and remove Y" is a mutation request, not a verification request.
	res := Classify("$prompt check @index.html and remove extra contents", nil)
	if res.Intent != IntentModification {
		t.Errorf("check+mutation = %s, want modification", res.Intent)
	}
}

func TestClassifyVerification(t *testing.T) {
	res := Classify("check whether the tests pass", nil)
	if res.Intent != IntentVerification {
		t.Errorf("verification intent = %s, want verification", res.Intent)
	}
}

func TestClassifyMutationVerbDominatesInspection(t *testing.T) {
	// Case F: "$prompt inspect and remove redundant content from @index.html"
	// carries a mutation verb ("remove") — it is a modification, never a
	// read-only investigation.
	res := Classify("inspect and remove redundant content from @index.html", nil)
	if res.Intent != IntentModification {
		t.Errorf("inspect+remove = %s, want modification", res.Intent)
	}
	if !res.RequiresMutation() {
		t.Error("inspect+remove must require mutation")
	}
}

func TestClassifyInspectionWithoutMutationStaysInvestigation(t *testing.T) {
	// Case C: "$prompt inspect @index.html" carries no mutation verb — it stays
	// a read-only investigation.
	res := Classify("inspect @index.html", nil)
	if res.Intent != IntentInvestigation {
		t.Errorf("inspect = %s, want investigation", res.Intent)
	}
	if res.RequiresMutation() {
		t.Error("pure inspect must not require mutation")
	}
}

func TestClassifyDebuggingDominatesMutation(t *testing.T) {
	res := Classify("why is the build failing after the fix", nil)
	if res.Intent != IntentDebugging {
		t.Errorf("debugging intent = %s, want debugging", res.Intent)
	}
}

func TestClassifyRefactoring(t *testing.T) {
	res := Classify("refactor the router package", nil)
	if res.Intent != IntentRefactoring {
		t.Errorf("refactoring intent = %s, want refactoring", res.Intent)
	}
	if !res.RequiresMutation() {
		t.Error("refactoring must require mutation")
	}
}

func TestClassifyPlanningAndExplanation(t *testing.T) {
	if res := Classify("plan the migration to the new event bus", nil); res.Intent != IntentPlanning {
		t.Errorf("planning intent = %s, want planning", res.Intent)
	}
	if res := Classify("explain how the router works", nil); res.Intent != IntentExplanation {
		t.Errorf("explanation intent = %s, want explanation", res.Intent)
	}
}

func TestTargetExtraction(t *testing.T) {
	res := Classify("fix @src/handler.go:42 and @index.html", nil)
	if len(res.Targets) != 2 {
		t.Fatalf("targets = %v, want 2", res.Targets)
	}
	if res.Targets[0] != "src/handler.go" || res.Targets[1] != "index.html" {
		t.Errorf("targets = %v", res.Targets)
	}
}

func TestRequiredCapabilitiesByIntent(t *testing.T) {
	if got := RequiredCapabilities(IntentConversation); len(got) != 0 {
		t.Errorf("conversation required = %v, want empty", got)
	}
	if got := RequiredCapabilities(IntentModification); !got.Has(CapMutate) {
		t.Errorf("modification required = %v, want mutate", got)
	}
	if got := RequiredCapabilities(IntentPlanning); !got.Has(CapPropose) {
		t.Errorf("planning required = %v, want propose", got)
	}
}

func TestCapabilitySetStringOrder(t *testing.T) {
	cs := CapabilitySet{CapMutate, CapRead, CapAnalyze}
	got := cs.String()
	want := "read+analyze+mutate"
	if got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

func TestIsConversationHelper(t *testing.T) {
	if !IsConversation("hi") {
		t.Error("IsConversation(hi) = false, want true")
	}
	if IsConversation("remove the footer") {
		t.Error("IsConversation(remove the footer) = true, want false")
	}
}

func TestParseIntentRoundTrip(t *testing.T) {
	if ParseIntent("modification") != IntentModification {
		t.Error("ParseIntent(modification) failed")
	}
	if ParseIntent("bogus") != IntentUnknown {
		t.Error("ParseIntent(bogus) should be unknown")
	}
	if strings.TrimSpace(IntentUnknown.String()) != "unknown" {
		t.Errorf("unknown intent String() = %q", IntentUnknown.String())
	}
}
