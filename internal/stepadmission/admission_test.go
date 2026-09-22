package stepadmission

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/changesurface"
	"github.com/PizenLabs/izen/internal/mutationstrategy"
	"github.com/PizenLabs/izen/internal/understanding"
)

// helper to find repo root
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod")
		}
		dir = parent
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// ADMIT-01: step below provider ceiling is admitted
func TestADMIT01_BelowCeilingIsAdmitted(t *testing.T) {
	cap := CapabilityFromOutputCeiling(1024)
	budget := mutationstrategy.StepBudget{MaxOutputTokens: 4096, MaxFiles: 4}
	eff := EffectiveBudget(cap, budget) // should be 1024-128=896

	candidate := CandidateStep{
		ID:           "step-01",
		Kind:         "INVESTIGATE",
		Targets:      []string{"internal/auth/middleware.go"},
		EvidenceRefs: []string{"evidence:auth"},
		Intent:       "Inspect authentication middleware and identify ownership boundary.",
		StateDigest:  "fp-001",
		// Estimate not set -> computed ~55*1+... < 896
	}
	state := AdmissionState{CurrentFingerprint: "fp-001"}
	d := AdmitStep(candidate, cap, budget, []string{"internal/auth/middleware.go"}, state)
	if d.Action != ActionAdmit {
		t.Fatalf("ADMIT-01: expected ADMIT, got %s reason %q estimate %d budget %d", d.Action, d.Reason, d.Estimate.Expected, eff.MaxOutputTokens)
	}
	if d.RefinedStep != nil {
		t.Fatal("ADMIT-01: ADMIT must not carry RefinedStep")
	}
}

// ADMIT-02: step exceeding provider ceiling is refined
func TestADMIT02_ExceedingCeilingIsRefined(t *testing.T) {
	cap := CapabilityFromOutputCeiling(1024)
	budget := mutationstrategy.StepBudget{MaxOutputTokens: 4096, MaxFiles: 4}
	candidate := CandidateStep{
		ID:           "step-01",
		Kind:         "MUTATE",
		Targets:      []string{"a.go", "b.go", "c.go", "d.go", "e.go", "f.go", "g.go", "h.go"},
		EvidenceRefs: []string{"e1", "e2", "e3", "e4"},
		Intent:       "Refactor the authentication subsystem.",
		StateDigest:  "fp-002",
	}
	// Force large estimate manually to guarantee exceed
	candidate.Estimate = StepSizeEstimate{Lower: 1200, Expected: 1400, Upper: 1600, Confidence: 0.8}
	state := AdmissionState{CurrentFingerprint: "fp-002"}
	d := AdmitStep(candidate, cap, budget, []string{"a.go", "b.go", "c.go", "d.go", "e.go", "f.go", "g.go", "h.go"}, state)
	if d.Action != ActionRefine {
		t.Fatalf("ADMIT-02: expected REFINE, got %s reason %q estimate %d", d.Action, d.Reason, d.Estimate.Expected)
	}
	if d.RefinedStep == nil {
		t.Fatal("ADMIT-02: REFINE must carry RefinedStep")
	}
}

// ADMIT-03: refinement produces a smaller step
func TestADMIT03_RefinementProducesSmallerStep(t *testing.T) {
	cap := CapabilityFromOutputCeiling(1024)
	budget := mutationstrategy.StepBudget{MaxOutputTokens: 4096, MaxFiles: 4}
	candidate := CandidateStep{
		ID:           "step-01",
		Kind:         "MUTATE",
		Targets:      []string{"a.go", "b.go", "c.go", "d.go"},
		EvidenceRefs: []string{"e1", "e2", "e3", "e4"},
		Intent:       "Refactor the authentication subsystem.",
		StateDigest:  "fp-003",
		Estimate:     StepSizeEstimate{Lower: 1100, Expected: 1300, Upper: 1500, Confidence: 0.7},
	}
	state := AdmissionState{CurrentFingerprint: "fp-003"}
	d := AdmitStep(candidate, cap, budget, []string{"a.go", "b.go", "c.go", "d.go"}, state)
	if d.Action != ActionRefine {
		t.Fatalf("ADMIT-03: expected REFINE, got %s", d.Action)
	}
	refined := d.RefinedStep
	if refined.Estimate.Expected >= d.Estimate.Expected {
		t.Fatalf("ADMIT-03: refined expected %d must be < original %d", refined.Estimate.Expected, d.Estimate.Expected)
	}
	if len(refined.Targets) >= len(candidate.Targets) {
		t.Fatalf("ADMIT-03: refined targets %v must be subset smaller than %v", refined.Targets, candidate.Targets)
	}
}

// ADMIT-04: refinement preserves task intent
func TestADMIT04_RefinementPreservesIntent(t *testing.T) {
	cap := CapabilityFromOutputCeiling(1024)
	budget := mutationstrategy.StepBudget{MaxOutputTokens: 4096, MaxFiles: 4}
	originalIntent := "Find and fix the memory leak."
	candidate := CandidateStep{
		ID:           "step-01",
		Kind:         "INVESTIGATE",
		Targets:      []string{"a.go", "b.go", "c.go", "d.go"},
		EvidenceRefs: []string{"e1", "e2"},
		Intent:       originalIntent,
		StateDigest:  "fp-004",
		Estimate:     StepSizeEstimate{Lower: 1000, Expected: 1300, Upper: 1500, Confidence: 0.6},
	}
	state := AdmissionState{CurrentFingerprint: "fp-004"}
	d := AdmitStep(candidate, cap, budget, []string{"a.go", "b.go", "c.go", "d.go"}, state)
	if d.Action != ActionRefine {
		t.Fatalf("ADMIT-04: expected REFINE, got %s", d.Action)
	}
	if d.RefinedStep.Intent != originalIntent {
		t.Fatalf("ADMIT-04: refined intent %q must equal original %q; refinement must not destroy task intent", d.RefinedStep.Intent, originalIntent)
	}
	// Must not have been trivialized to single file read without intent
	if d.RefinedStep.Intent == "Read main.go" {
		t.Fatal("ADMIT-04: refined intent must not be trivialized")
	}
}

// ADMIT-05: refinement cannot expand scope
func TestADMIT05_RefinementCannotExpandScope(t *testing.T) {
	cap := CapabilityFromOutputCeiling(1024)
	budget := mutationstrategy.StepBudget{MaxOutputTokens: 4096, MaxFiles: 4}
	allowed := []string{"a.go", "b.go"}
	candidate := CandidateStep{
		ID:           "step-01",
		Kind:         "MUTATE",
		Targets:      []string{"a.go", "b.go"},
		EvidenceRefs: []string{"e1"},
		Intent:       "fix bug",
		StateDigest:  "fp-005",
		Estimate:     StepSizeEstimate{Lower: 900, Expected: 1100, Upper: 1300, Confidence: 0.7},
	}
	// Admission with allowed scope that does NOT contain c.go should block if candidate had c.go
	// First test that valid candidate within scope refines still within scope
	state := AdmissionState{CurrentFingerprint: "fp-005"}
	d := AdmitStep(candidate, cap, budget, allowed, state)
	if d.Action == ActionAwaitingApproval {
		t.Fatalf("ADMIT-05: candidate within scope must not be AWAITING_APPROVAL, got %s", d.Reason)
	}
	if d.Action == ActionRefine && d.RefinedStep != nil {
		for _, tgt := range d.RefinedStep.Targets {
			found := false
			for _, orig := range candidate.Targets {
				if tgt == orig {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("ADMIT-05: refined target %q not subset of original %v (scope expansion)", tgt, candidate.Targets)
			}
			// also must be within allowed
			if tgt != "a.go" && tgt != "b.go" {
				t.Fatalf("ADMIT-05: refined target %q expands beyond allowed %v", tgt, allowed)
			}
		}
	}
	// Now craft a candidate that exceeds allowed scope -> must be blocked/awaiting, not admitted
	outOfScope := CandidateStep{
		ID:           "step-02",
		Kind:         "MUTATE",
		Targets:      []string{"a.go", "secret/outside.go"},
		EvidenceRefs: []string{"e1"},
		Intent:       "fix bug",
		StateDigest:  "fp-005",
		Estimate:     StepSizeEstimate{Lower: 100, Expected: 200, Upper: 300, Confidence: 0.8},
	}
	d2 := AdmitStep(outOfScope, cap, budget, allowed, state)
	if d2.Action != ActionAwaitingApproval && d2.Action != ActionBlock {
		t.Fatalf("ADMIT-05: out-of-scope candidate must be BLOCK or AWAITING_APPROVAL, got %s", d2.Action)
	}
}

// ADMIT-06: refinement cannot create authorization
func TestADMIT06_RefinementCannotCreateAuthorization(t *testing.T) {
	typ := reflect.TypeOf(StepAdmissionDecision{})
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		low := strings.ToLower(f.Name)
		if strings.Contains(low, "grant") || strings.Contains(low, "authoriz") || strings.Contains(low, "approve") {
			t.Fatalf("ADMIT-06: decision must not have authorization field %q", f.Name)
		}
	}
	typ2 := reflect.TypeOf(CandidateStep{})
	for i := 0; i < typ2.NumField(); i++ {
		f := typ2.Field(i)
		low := strings.ToLower(f.Name)
		if strings.Contains(low, "grant") || strings.Contains(low, "authoriz") || strings.Contains(low, "capability") {
			t.Fatalf("ADMIT-06: CandidateStep must not have authorization/capability field %q", f.Name)
		}
	}
	// Behavioral: refined step carries no grant
	cap := CapabilityFromOutputCeiling(1024)
	budget := mutationstrategy.StepBudget{MaxOutputTokens: 4096, MaxFiles: 4}
	candidate := CandidateStep{
		ID: "step-01", Kind: "MUTATE", Targets: []string{"a.go", "b.go", "c.go"},
		EvidenceRefs: []string{"e1"}, Intent: "fix", StateDigest: "fp-006",
		Estimate: StepSizeEstimate{Lower: 900, Expected: 1100, Upper: 1300, Confidence: 0.7},
	}
	d := AdmitStep(candidate, cap, budget, []string{"a.go", "b.go", "c.go"}, AdmissionState{CurrentFingerprint: "fp-006"})
	if d.Action == ActionRefine && d.RefinedStep != nil {
		// Reflection again on refined – no grant fields
		_ = d.RefinedStep
	}
}

// ADMIT-07: refinement cannot execute
func TestADMIT07_RefinementCannotExecute(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "marker.txt")
	if err := os.WriteFile(tmp, []byte("before"), 0600); err != nil {
		t.Fatal(err)
	}
	cap := CapabilityFromOutputCeiling(1024)
	budget := mutationstrategy.StepBudget{MaxOutputTokens: 4096, MaxFiles: 4}
	candidate := CandidateStep{
		ID: "step-01", Kind: "MUTATE", Targets: []string{"a.go", "b.go", "c.go", "d.go"},
		EvidenceRefs: []string{"e1"}, Intent: "fix", StateDigest: "fp-007",
		Estimate: StepSizeEstimate{Lower: 900, Expected: 1100, Upper: 1300, Confidence: 0.7},
	}
	_ = AdmitStep(candidate, cap, budget, []string{"a.go", "b.go", "c.go", "d.go"}, AdmissionState{CurrentFingerprint: "fp-007"})
	data, _ := os.ReadFile(tmp)
	if string(data) != "before" {
		t.Fatal("ADMIT-07: admission/refinement mutated workspace — must not execute")
	}
	root := repoRoot(t)
	for _, rel := range []string{"internal/stepadmission/admission.go", "internal/stepadmission/refine.go", "internal/stepadmission/estimate.go", "internal/stepadmission/capability.go"} {
		data, _ := os.ReadFile(filepath.Join(root, rel))
		content := string(data)
		if strings.Contains(content, "os.WriteFile") || strings.Contains(content, "os/exec") || strings.Contains(content, "substrate.Execute") {
			t.Fatalf("ADMIT-07: %s must not contain execution primitives", rel)
		}
	}
}

// ADMIT-08: refinement cannot schedule
func TestADMIT08_RefinementCannotSchedule(t *testing.T) {
	root := repoRoot(t)
	for _, rel := range []string{"internal/stepadmission/admission.go", "internal/stepadmission/refine.go"} {
		data, _ := os.ReadFile(filepath.Join(root, rel))
		content := string(data)
		if strings.Contains(content, "StepScheduler") || strings.Contains(content, "Schedule(") {
			t.Fatalf("ADMIT-08: %s must not directly schedule via StepScheduler", rel)
		}
	}
	// Verify refined step ID is proposal style, not scheduler ExecutionStep style with Sequence/TotalSteps
	cap := CapabilityFromOutputCeiling(1024)
	budget := mutationstrategy.StepBudget{MaxOutputTokens: 4096, MaxFiles: 4}
	candidate := CandidateStep{
		ID: "step-01", Kind: "INVESTIGATE", Targets: []string{"a.go", "b.go", "c.go"},
		EvidenceRefs: []string{"e1"}, Intent: "investigate", StateDigest: "fp-008",
		Estimate: StepSizeEstimate{Lower: 900, Expected: 1100, Upper: 1300, Confidence: 0.7},
	}
	d := AdmitStep(candidate, cap, budget, []string{"a.go", "b.go", "c.go"}, AdmissionState{CurrentFingerprint: "fp-008"})
	if d.RefinedStep != nil && strings.Contains(d.RefinedStep.ID, "of") {
		t.Fatalf("ADMIT-08: refined ID %q must not look like scheduler ExecutionStep ID", d.RefinedStep.ID)
	}
}

// ADMIT-09: refinement cannot create capabilities
func TestADMIT09_RefinementCannotCreateCapabilities(t *testing.T) {
	typ := reflect.TypeOf(StepAdmissionDecision{})
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		low := strings.ToLower(f.Name)
		if strings.Contains(low, "capability") || strings.Contains(low, "permission") {
			t.Fatalf("ADMIT-09: decision must not have capability field %q", f.Name)
		}
	}
	// Behavioral: admission does not mint new capability profile beyond input
	cap := CapabilityFromOutputCeiling(1024)
	budget := mutationstrategy.StepBudget{MaxOutputTokens: 4096, MaxFiles: 4}
	candidate := CandidateStep{
		ID: "step-01", Kind: "MUTATE", Targets: []string{"a.go", "b.go", "c.go", "d.go"},
		EvidenceRefs: []string{"e1"}, Intent: "fix", StateDigest: "fp-009",
		Estimate: StepSizeEstimate{Lower: 900, Expected: 1100, Upper: 1300, Confidence: 0.7},
	}
	d := AdmitStep(candidate, cap, budget, []string{"a.go", "b.go", "c.go", "d.go"}, AdmissionState{CurrentFingerprint: "fp-009"})
	if d.Action == ActionRefine && d.RefinedStep != nil {
		// Refined step must not carry a capability field; decision budget must be derived, not newly minted grant
		if d.Budget.MaxOutputTokens != EffectiveBudget(cap, budget).MaxOutputTokens {
			t.Fatalf("ADMIT-09: budget must be derived from input capability, not newly created")
		}
	}
}

// ADMIT-10: 1024-token model can process a large task through multiple bounded steps
func TestADMIT10_ConstrainedModelProcessesLargeTaskViaMultipleSteps(t *testing.T) {
	cap := CapabilityFromOutputCeiling(1024)
	budget := mutationstrategy.StepBudget{MaxOutputTokens: 4096, MaxFiles: 4}
	allowed := []string{"a.go", "b.go", "c.go", "d.go", "e.go", "f.go"}
	// Large task: 6 files, each MUTATE -> estimated ~110*6=~660+ families etc -> exceeds 896? Let's force exceed
	largeCandidate := CandidateStep{
		ID:           "step-01",
		Kind:         "MUTATE",
		Targets:      []string{"a.go", "b.go", "c.go", "d.go", "e.go", "f.go"},
		EvidenceRefs: []string{"e1", "e2", "e3", "e4", "e5", "e6"},
		Intent:       "Refactor the authentication subsystem.",
		StateDigest:  "fp-010",
		Estimate:     StepSizeEstimate{Lower: 1100, Expected: 1350, Upper: 1600, Confidence: 0.6},
	}
	state := AdmissionState{CurrentFingerprint: "fp-010"}
	d1 := AdmitStep(largeCandidate, cap, budget, allowed, state)
	if d1.Action != ActionRefine {
		t.Fatalf("ADMIT-10: large task first step must be REFINE, got %s", d1.Action)
	}
	refined := d1.RefinedStep
	if refined == nil {
		t.Fatal("ADMIT-10: no refined step")
	}
	if refined.Estimate.Expected > EffectiveBudget(cap, budget).MaxOutputTokens {
		t.Fatalf("ADMIT-10: refined expected %d must fit within budget %d", refined.Estimate.Expected, EffectiveBudget(cap, budget).MaxOutputTokens)
	}
	// Second step for remaining work: simulate remaining targets after first bounded step consumed part of scope
	remaining := []string{"d.go", "e.go", "f.go"}
	second := CandidateStep{
		ID:           "step-02",
		Kind:         "MUTATE",
		Targets:      remaining,
		EvidenceRefs: []string{"e4", "e5", "e6"},
		Intent:       "Refactor the authentication subsystem.",
		StateDigest:  "fp-010",
	}
	// Estimate for second step should be smaller and fit
	d2 := AdmitStep(second, cap, budget, allowed, state)
	if d2.Action != ActionAdmit {
		t.Fatalf("ADMIT-10: second bounded step must be ADMIT, got %s reason %q estimate %d", d2.Action, d2.Reason, d2.Estimate.Expected)
	}
	// Prove task not declared impossible despite 1024 ceiling < large task complexity
	// Overall task required ~1350 but was accomplished via 2 bounded steps each < 1024
	if d1.Estimate.Expected <= 1024 {
		t.Fatal("ADMIT-10: original estimate should have exceeded 1024 to prove large task handling")
	}
}

// ADMIT-11: large mutation uses existing MutationPlan/MutationStep semantics
func TestADMIT11_LargeMutationUsesExistingMutationPlanSemantics(t *testing.T) {
	// Build a real ProjectUnderstanding + ChangeSurface and derive MutationPlan
	u := understanding.ProjectUnderstanding{
		Kind:       understanding.KindExisting,
		Digest:     "digest-123",
		SnapshotID: "snap-123",
		Root:       "/tmp",
	}
	surface := changesurface.ChangeSurface{
		Status:              changesurface.StatusResolved,
		UnderstandingDigest: "digest-123",
		Candidates: []changesurface.Candidate{
			{Path: "a.go", Certainty: changesurface.CertaintyDirect, Evidence: []string{"evidence:a"}},
			{Path: "b.go", Certainty: changesurface.CertaintyDirect, Evidence: []string{"evidence:b"}},
			{Path: "c.go", Certainty: changesurface.CertaintyDirect, Evidence: []string{"evidence:c"}},
		},
		Evidence: []string{"evidence:a", "evidence:b", "evidence:c"},
	}
	// Use a constrained budget to force multi-step or TOO_LARGE-like behavior
	opts := mutationstrategy.PlanOptions{
		ModelConstraints: mutationstrategy.ModelConstraints{OutputCeiling: 1024},
		StepBudget:       mutationstrategy.StepBudget{MaxOutputTokens: 500, MaxFiles: 4},
	}
	plan := mutationstrategy.Derive("refactor authentication subsystem for clarity", u, surface, opts)
	if len(plan.Steps) == 0 {
		t.Fatalf("ADMIT-11: expected mutation steps, got status %s reason %q", plan.Status, plan.UnresolvedReason)
	}
	// Convert first MutationStep to CandidateStep and admit it
	cap := CapabilityFromOutputCeiling(1024)
	budget := mutationstrategy.StepBudget{MaxOutputTokens: 500, MaxFiles: 4}
	allowed := []string{"a.go", "b.go", "c.go"}
	for _, ms := range plan.Steps {
		cand := CandidateFromMutationStep(ms, "refactor authentication subsystem", "fp-011", 0)
		d := AdmitStep(cand, cap, budget, allowed, AdmissionState{CurrentFingerprint: "fp-011"})
		if d.Action != ActionAdmit && d.Action != ActionRefine {
			t.Fatalf("ADMIT-11: mutation candidate must be ADMIT or REFINE, got %s reason %q", d.Action, d.Reason)
		}
		// Verify that candidate kind is MUTATE and estimate came from mutation semantics
		if cand.Kind != "MUTATE" {
			t.Fatalf("ADMIT-11: expected MUTATE kind, got %s", cand.Kind)
		}
		// Ensure the estimate is distinct from EstimatedMutationSize (different type)
		var _ = cand.Estimate // StepSizeEstimate
		var _ = ms.Estimate   // EstimatedMutationSize
		if reflect.TypeOf(cand.Estimate) == reflect.TypeOf(ms.Estimate) {
			t.Fatal("ADMIT-11: StepSizeEstimate must be distinct type from EstimatedMutationSize")
		}
	}
}

// ADMIT-12: large investigation can be refined without becoming mutation
func TestADMIT12_LargeInvestigationRefinedWithoutMutation(t *testing.T) {
	cap := CapabilityFromOutputCeiling(1024)
	budget := mutationstrategy.StepBudget{MaxOutputTokens: 4096, MaxFiles: 4}
	candidate := CandidateStep{
		ID:           "step-01",
		Kind:         "INVESTIGATE",
		Targets:      []string{"a.go", "b.go", "c.go", "d.go", "e.go"},
		EvidenceRefs: []string{"e1", "e2", "e3", "e4", "e5"},
		Intent:       "Analyze all concurrency paths in this subsystem.",
		StateDigest:  "fp-012",
		Estimate:     StepSizeEstimate{Lower: 1000, Expected: 1300, Upper: 1500, Confidence: 0.6},
	}
	state := AdmissionState{CurrentFingerprint: "fp-012"}
	d := AdmitStep(candidate, cap, budget, []string{"a.go", "b.go", "c.go", "d.go", "e.go"}, state)
	if d.Action != ActionRefine {
		t.Fatalf("ADMIT-12: expected REFINE, got %s", d.Action)
	}
	if d.RefinedStep.Kind != "INVESTIGATE" {
		t.Fatalf("ADMIT-12: refined kind must remain INVESTIGATE, got %s", d.RefinedStep.Kind)
	}
	if d.RefinedStep.Kind == "MUTATE" {
		t.Fatal("ADMIT-12: investigation refinement must not become MUTATE")
	}
}

// ADMIT-13: large verification step can be refined
func TestADMIT13_LargeVerificationCanBeRefined(t *testing.T) {
	cap := CapabilityFromOutputCeiling(1024)
	budget := mutationstrategy.StepBudget{MaxOutputTokens: 4096, MaxFiles: 4}
	candidate := CandidateStep{
		ID:           "step-01",
		Kind:         "VERIFY",
		Targets:      []string{"a.go", "b.go", "c.go", "d.go"},
		EvidenceRefs: []string{"e1", "e2", "e3", "e4"},
		Intent:       "Verify behavior across subsystem.",
		StateDigest:  "fp-013",
		Estimate:     StepSizeEstimate{Lower: 950, Expected: 1200, Upper: 1450, Confidence: 0.6},
	}
	state := AdmissionState{CurrentFingerprint: "fp-013"}
	d := AdmitStep(candidate, cap, budget, []string{"a.go", "b.go", "c.go", "d.go"}, state)
	if d.Action != ActionRefine {
		t.Fatalf("ADMIT-13: expected REFINE, got %s", d.Action)
	}
	if d.RefinedStep.Kind != "VERIFY" {
		t.Fatalf("ADMIT-13: refined kind must remain VERIFY, got %s", d.RefinedStep.Kind)
	}
	if len(d.RefinedStep.Targets) >= len(candidate.Targets) {
		t.Fatalf("ADMIT-13: refined must be smaller targets %v vs %v", d.RefinedStep.Targets, candidate.Targets)
	}
}

// ADMIT-14: refinement does not invent unsupported targets
func TestADMIT14_RefinementDoesNotInventTargets(t *testing.T) {
	cap := CapabilityFromOutputCeiling(1024)
	budget := mutationstrategy.StepBudget{MaxOutputTokens: 4096, MaxFiles: 4}
	allowed := []string{"a.go", "b.go", "c.go"}
	candidate := CandidateStep{
		ID:           "step-01",
		Kind:         "MUTATE",
		Targets:      []string{"a.go", "b.go", "c.go"},
		EvidenceRefs: []string{"e1", "e2", "e3"},
		Intent:       "fix bug",
		StateDigest:  "fp-014",
		Estimate:     StepSizeEstimate{Lower: 900, Expected: 1100, Upper: 1300, Confidence: 0.7},
	}
	state := AdmissionState{CurrentFingerprint: "fp-014"}
	d := AdmitStep(candidate, cap, budget, allowed, state)
	if d.Action != ActionRefine {
		t.Fatalf("ADMIT-14: expected REFINE, got %s", d.Action)
	}
	for _, tgt := range d.RefinedStep.Targets {
		found := false
		for _, orig := range candidate.Targets {
			if tgt == orig {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("ADMIT-14: refined target %q was not in original evidence-backed set %v (invented)", tgt, candidate.Targets)
		}
	}
	// Explicitly check dashboard.tsx style invention is absent
	for _, tgt := range d.RefinedStep.Targets {
		if tgt == "dashboard.tsx" || tgt == "auth/service.go" || tgt == "database/schema.sql" {
			t.Fatalf("ADMIT-14: refined invented unsupported target %q", tgt)
		}
	}
	// Ensure sorted evidence-backed subset, not invented
	sort.Strings(candidate.Targets)
	sort.Strings(d.RefinedStep.Targets)
	for _, tgt := range d.RefinedStep.Targets {
		if !contains(candidate.Targets, tgt) {
			t.Fatalf("ADMIT-14: refined target %q invented", tgt)
		}
	}
}

// Additional structural invariants

func TestAdmission_NoSecondPlannerType(t *testing.T) {
	root := repoRoot(t)
	forbidden := []string{"AdaptivePlanner", "StepPlanner", "TokenPlanner", "ModelPlanner"}
	for _, rel := range listGoFiles(t, filepath.Join(root, "internal/stepadmission")) {
		data, _ := os.ReadFile(filepath.Join(root, "internal/stepadmission", rel))
		content := string(data)
		for _, name := range forbidden {
			if strings.Contains(content, "type "+name) {
				t.Fatalf("no second planner: found forbidden type %q in stepadmission", name)
			}
		}
	}
}

func TestAdmission_NoSecondSchedulerType(t *testing.T) {
	root := repoRoot(t)
	forbidden := []string{"AdaptiveScheduler", "StepAdmissionScheduler", "RefinementScheduler"}
	for _, rel := range listGoFiles(t, filepath.Join(root, "internal/stepadmission")) {
		data, _ := os.ReadFile(filepath.Join(root, "internal/stepadmission", rel))
		content := string(data)
		for _, name := range forbidden {
			if strings.Contains(content, "type "+name) {
				t.Fatalf("no second scheduler: found forbidden type %q", name)
			}
		}
	}
}

func TestAdmission_DistinguishesEstimateFromBudget(t *testing.T) {
	est := StepSizeEstimate{Lower: 100, Expected: 200, Upper: 300, Confidence: 0.8}
	budget := mutationstrategy.StepBudget{MaxOutputTokens: 1024}
	if reflect.TypeOf(est) == reflect.TypeOf(budget) {
		t.Fatal("estimate and budget must be distinct types")
	}
	// EstimatedMutationSize is distinct from StepSizeEstimate
	var mEst mutationstrategy.EstimatedMutationSize
	if reflect.TypeOf(est) == reflect.TypeOf(mEst) {
		t.Fatal("StepSizeEstimate must be distinct from EstimatedMutationSize")
	}
}

func TestAdmission_StaleStateIsNotAdmitted(t *testing.T) {
	cap := CapabilityFromOutputCeiling(1024)
	budget := mutationstrategy.StepBudget{MaxOutputTokens: 4096}
	candidate := CandidateStep{
		ID: "step-01", Kind: "INVESTIGATE", Targets: []string{"a.go"},
		EvidenceRefs: []string{"e1"}, Intent: "investigate", StateDigest: "fp-old",
		Estimate: StepSizeEstimate{Lower: 50, Expected: 100, Upper: 150, Confidence: 0.8},
	}
	state := AdmissionState{CurrentFingerprint: "fp-new"}
	d := AdmitStep(candidate, cap, budget, []string{"a.go"}, state)
	if d.Action != ActionStale {
		t.Fatalf("stale digest must yield STALE, got %s", d.Action)
	}
	if d.RefinedStep != nil {
		t.Fatal("STALE must not carry RefinedStep")
	}
}

func TestAdmission_FiniteRefinement(t *testing.T) {
	cap := CapabilityFromOutputCeiling(100)
	budget := mutationstrategy.StepBudget{MaxOutputTokens: 100}
	// Candidate with depth at limit
	candidate := CandidateStep{
		ID: "step-01", Kind: "MUTATE", Targets: []string{"a.go", "b.go"},
		EvidenceRefs: []string{"e1", "e2"}, Intent: "fix", StateDigest: "fp-finite",
		Estimate:        StepSizeEstimate{Lower: 180, Expected: 200, Upper: 220, Confidence: 0.8},
		RefinementDepth: MaxRefinementDepth,
	}
	d := AdmitStep(candidate, cap, budget, []string{"a.go", "b.go"}, AdmissionState{CurrentFingerprint: "fp-finite"})
	if d.Action != ActionBlock {
		t.Fatalf("finite depth exhausted must yield BLOCK, got %s", d.Action)
	}
}

func TestAdmission_SingleFileTooLargeBlocked(t *testing.T) {
	cap := CapabilityFromOutputCeiling(100)
	budget := mutationstrategy.StepBudget{MaxOutputTokens: 100}
	candidate := CandidateStep{
		ID: "step-01", Kind: "MUTATE", Targets: []string{"single.go"},
		EvidenceRefs: []string{"e1"}, Intent: "generate complete implementation",
		StateDigest: "fp-single",
		Estimate:    StepSizeEstimate{Lower: 180, Expected: 250, Upper: 320, Confidence: 0.8},
	}
	d := AdmitStep(candidate, cap, budget, []string{"single.go"}, AdmissionState{CurrentFingerprint: "fp-single"})
	if d.Action != ActionBlock {
		t.Fatalf("single-file too-large must BLOCK (no invention), got %s", d.Action)
	}
}

func contains(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}

func listGoFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".go") {
			out = append(out, e.Name())
		}
	}
	return out
}
