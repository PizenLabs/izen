package llm

import (
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/core/artifact"
	"github.com/PizenLabs/izen/internal/core/authorization"
	"github.com/PizenLabs/izen/internal/core/budget"
	"github.com/PizenLabs/izen/internal/core/classifier"
)

func TestParseIntentProposal(t *testing.T) {
	ip := ParseIntentProposal("Write a function to calculate fibonacci", "implement")
	if ip == nil {
		t.Fatal("expected non-nil proposal")
	}
	if ip.Prompt != "Write a function to calculate fibonacci" {
		t.Errorf("Prompt = %q", ip.Prompt)
	}
	if ip.Mode != "implement" {
		t.Errorf("Mode = %q", ip.Mode)
	}
}

func TestParseIntentProposalEmpty(t *testing.T) {
	ip := ParseIntentProposal("", "implement")
	if ip != nil {
		t.Error("expected nil for empty output")
	}
}

func TestParseIntentProposalOnlyFences(t *testing.T) {
	ip := ParseIntentProposal("```\n```", "implement")
	if ip != nil {
		t.Errorf("expected nil for fence-only output, got %+v", ip)
	}
}

func TestIntentProposalToArtifact(t *testing.T) {
	ip := &IntentProposal{Prompt: "test", Mode: "ask", Created: time.Now()}
	ia := ip.ToArtifact()
	if ia.Kind() != artifact.ArtifactKindIntent {
		t.Errorf("expected intent kind, got %s", ia.Kind())
	}
	if ia.State() != artifact.StateDraft {
		t.Errorf("expected DRAFT state, got %s", ia.State())
	}
	if ia.Prompt != "test" {
		t.Errorf("Prompt = %q", ia.Prompt)
	}
}

func TestParsePlanProposal(t *testing.T) {
	output := "Strategy: incremental\n- Step one\n- Step two\n- Step three"
	pp := ParsePlanProposal(output)
	if pp == nil {
		t.Fatal("expected non-nil proposal")
	}
	if len(pp.Steps) != 3 {
		t.Errorf("expected 3 steps, got %d", len(pp.Steps))
	}
	if pp.Strategy != "incremental" {
		t.Errorf("Strategy = %q", pp.Strategy)
	}
}

func TestParsePlanProposalWithBulletStars(t *testing.T) {
	output := "* Fix bug A\n* Refactor B\n* Test C"
	pp := ParsePlanProposal(output)
	if pp == nil {
		t.Fatal("expected non-nil proposal")
	}
	if len(pp.Steps) != 3 {
		t.Errorf("expected 3 steps, got %d", len(pp.Steps))
	}
}

func TestParsePlanProposalEmpty(t *testing.T) {
	pp := ParsePlanProposal("")
	if pp != nil {
		t.Error("expected nil for empty output")
	}
}

func TestParsePlanProposalFallback(t *testing.T) {
	output := "do x\ndo y\ndo z"
	pp := ParsePlanProposal(output)
	if pp == nil {
		t.Fatal("expected non-nil proposal")
	}
	if len(pp.Steps) != 3 {
		t.Errorf("expected 3 fallback steps, got %d", len(pp.Steps))
	}
}

func TestPlanProposalToArtifact(t *testing.T) {
	pp := &PlanProposal{
		Steps:    []string{"step1", "step2"},
		Strategy: "direct",
		Delta:    budget.BudgetDelta{Files: 2},
	}
	pa := pp.ToArtifact()
	if pa.Kind() != artifact.ArtifactKindPlan {
		t.Errorf("expected plan kind, got %s", pa.Kind())
	}
	if len(pa.Steps) != 2 {
		t.Errorf("expected 2 steps, got %d", len(pa.Steps))
	}
}

func TestParsePatchProposal(t *testing.T) {
	output := "FILE: main.go\npackage main\n\nfunc main() {}\n"
	patches := ParsePatchProposal(output)
	if len(patches) != 1 {
		t.Fatalf("expected 1 patch, got %d", len(patches))
	}
	if patches[0].File != "main.go" {
		t.Errorf("File = %q", patches[0].File)
	}
	if patches[0].Content == "" {
		t.Error("expected non-empty content")
	}
}

func TestParsePatchProposalMultipleFiles(t *testing.T) {
	output := "FILE: main.go\npackage main\nFILE: utils.go\npackage utils\n"
	patches := ParsePatchProposal(output)
	if len(patches) != 2 {
		t.Fatalf("expected 2 patches, got %d", len(patches))
	}
	if patches[0].File != "main.go" {
		t.Errorf("patch[0].File = %q", patches[0].File)
	}
	if patches[1].File != "utils.go" {
		t.Errorf("patch[1].File = %q", patches[1].File)
	}
}

func TestParsePatchProposalLowercaseFile(t *testing.T) {
	output := "file: main.go\npackage main\n"
	patches := ParsePatchProposal(output)
	if len(patches) != 1 {
		t.Fatalf("expected 1 patch, got %d", len(patches))
	}
	if patches[0].File != "main.go" {
		t.Errorf("File = %q", patches[0].File)
	}
}

func TestParsePatchProposalEmpty(t *testing.T) {
	patches := ParsePatchProposal("")
	if patches != nil {
		t.Error("expected nil for empty output")
	}
}

func TestPatchProposalToArtifact(t *testing.T) {
	pp := &PatchProposal{File: "main.go", Content: "package main"}
	pa := pp.ToArtifact()
	if pa.Kind() != artifact.ArtifactKindPatch {
		t.Errorf("expected patch kind, got %s", pa.Kind())
	}
	if len(pa.Changes) != 1 || pa.Changes[0] != "main.go" {
		t.Errorf("Changes = %v", pa.Changes)
	}
	if pa.PatchContent != "package main" {
		t.Errorf("PatchContent = %q", pa.PatchContent)
	}
}

func TestFailureClassification(t *testing.T) {
	fc := FailureClassification{
		Class:  classifier.FailureCodeClass,
		Reason: "syntax error",
	}
	if fc.IsUnknown() {
		t.Error("code class should not be unknown")
	}
	if fc.String() != "FailureClassification{class=code reason=\"syntax error\"}" {
		t.Errorf("String() = %q", fc.String())
	}
}

func TestFailureClassificationUnknown(t *testing.T) {
	fc := FailureClassification{
		Class:  classifier.FailureUnknownClass,
		Reason: "unknown error",
	}
	if !fc.IsUnknown() {
		t.Error("unknown class should be IsUnknown")
	}
}

func TestClassifyFailureWithClassifier(t *testing.T) {
	classify := classifier.NewFailureClassifier()
	pg := &ProposalGenerator{classify: classify}

	fc := pg.ClassifyFailure("syntax error: unexpected EOF", 2)
	if fc.Class != classifier.FailureCodeClass {
		t.Errorf("expected code class, got %s", fc.Class)
	}
}

func TestClassifyFailureUnknownPreservesReason(t *testing.T) {
	classify := classifier.NewFailureClassifier()
	pg := &ProposalGenerator{classify: classify}

	fc := pg.ClassifyFailure("something went wrong but I don't know what", 0)
	if fc.Class != classifier.FailureUnknownClass {
		t.Errorf("expected unknown class, got %s", fc.Class)
	}
	if fc.Reason == "" {
		t.Error("expected non-empty reason")
	}
}

func TestSubmitIntent(t *testing.T) {
	store := artifact.NewStore("/tmp")
	pg := &ProposalGenerator{store: store}

	ia, err := pg.SubmitIntent("do the thing", "implement")
	if err != nil {
		t.Fatalf("SubmitIntent: %v", err)
	}
	if ia == nil {
		t.Fatal("expected non-nil artifact")
	}
	if ia.Prompt != "do the thing" {
		t.Errorf("Prompt = %q", ia.Prompt)
	}
}

func TestSubmitPlan(t *testing.T) {
	store := artifact.NewStore("/tmp")
	pg := &ProposalGenerator{store: store}

	pa, err := pg.SubmitPlan("- step one\n- step two")
	if err != nil {
		t.Fatalf("SubmitPlan: %v", err)
	}
	if pa == nil {
		t.Fatal("expected non-nil artifact")
	}
	if len(pa.Steps) != 2 {
		t.Errorf("expected 2 steps, got %d", len(pa.Steps))
	}
}

func TestSubmitPatch(t *testing.T) {
	store := artifact.NewStore("/tmp")
	pg := &ProposalGenerator{store: store}

	patches, err := pg.SubmitPatch("FILE: main.go\npackage main\n")
	if err != nil {
		t.Fatalf("SubmitPatch: %v", err)
	}
	if len(patches) != 1 {
		t.Fatalf("expected 1 patch, got %d", len(patches))
	}
}

func TestNewProposalGenerator(t *testing.T) {
	store := artifact.NewStore("/tmp")
	authEng := authorization.NewAuthorizationEngine(nil, nil, nil)
	classify := classifier.NewFailureClassifier()

	pg := NewProposalGenerator(store, authEng, classify, nil)
	if pg.Store() != store {
		t.Error("Store mismatch")
	}
	if pg.AuthEngine() != authEng {
		t.Error("AuthEngine mismatch")
	}
	if pg.Classifier() != classify {
		t.Error("Classifier mismatch")
	}
}
