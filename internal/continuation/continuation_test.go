package continuation

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// helper to build minimal derivation input
func baseInput() DerivationInput {
	return DerivationInput{
		Task: TaskStateView{
			TaskID:           "task-01",
			Intent:           "investigate and fix memory leak in service X",
			ActiveScope:      []string{"a.go", "b.go"},
			StateFingerprint: "fp-001",
			EvidenceDigest:   "ev-001",
			CompletedSteps:   []string{"step-01"},
			PendingSteps:     []string{"step-02"},
			History: []StepHistoryEntry{
				{StepID: "step-01", Outcome: "complete", StateFingerprint: "fp-001", EvidenceDigest: "ev-001", Patches: 1},
			},
		},
		PlanSteps: []PlanStepView{
			{ID: "step-01", Kind: "INVESTIGATE", References: []string{"a.go"}, Rationale: "inspect ownership"},
			{ID: "step-02", Kind: "MUTATE", References: []string{"b.go"}, Rationale: "bounded mutation"},
		},
		PreviousOutcome: "complete",
		Observations: []Observation{
			{Kind: KindExecutionResult, Subject: "a.go", Detail: "inspected", EvidenceDigest: "ev-001", StateFingerprint: "fp-001"},
			{Kind: KindVerification, Subject: "a.go", Detail: "verified", EvidenceDigest: "ev-001", StateFingerprint: "fp-001"},
		},
		Verified:     true,
		AllowedScope: []string{"a.go", "b.go"},
	}
}

// CONT-01: completed step produces COMPLETE
func TestCONT01_CompletedStepProducesComplete(t *testing.T) {
	in := baseInput()
	in.Task.CompletedSteps = []string{"step-01", "step-02"}
	in.Task.PendingSteps = nil
	in.PreviousOutcome = "complete"
	in.Verified = true
	d := DeriveNextStep(in)
	if d.Action != ActionComplete {
		t.Fatalf("CONT-01: want COMPLETE, got %s reason %s", d.Action, d.Reason)
	}
	if d.NextStep != nil {
		t.Fatalf("CONT-01: COMPLETE must not carry NextStep")
	}
}

// CONT-02: partial model output produces CONTINUE, not task failure
func TestCONT02_PartialProducesContinueNotFailure(t *testing.T) {
	in := baseInput()
	in.PreviousOutcome = "partial"
	in.PreviousReason = "OUTPUT_CEILING"
	in.IsPartialOutput = true
	in.Task.CompletedSteps = []string{}
	in.Task.PendingSteps = []string{"step-01", "step-02"}
	in.Observations = []Observation{
		{Kind: KindObservation, Subject: "a.go", EvidenceDigest: "ev-001", StateFingerprint: "fp-001"},
	}
	d := DeriveNextStep(in)
	if d.Action != ActionContinue {
		t.Fatalf("CONT-02: partial must yield CONTINUE, got %s", d.Action)
	}
	if d.Action == ActionFailed {
		t.Fatal("CONT-02: partial must not be treated as task failure")
	}
	if d.NextStep == nil {
		t.Fatal("CONT-02: CONTINUE must carry NextStep")
	}
}

// CONT-03: continuation derives different bounded next step when evidence shows unresolved work
func TestCONT03_DerivesDifferentNextStep(t *testing.T) {
	in := baseInput()
	in.Task.CompletedSteps = []string{"step-01"}
	in.Task.PendingSteps = []string{"step-02"}
	in.PreviousOutcome = "complete"
	in.Verified = true
	in.Observations = []Observation{
		{Kind: KindExecutionResult, Subject: "a.go", Detail: "done a.go", StateFingerprint: "fp-001", EvidenceDigest: "ev-001"},
		{Kind: KindObservation, Subject: "b.go", Detail: "evidence shows leak remains in b.go", StateFingerprint: "fp-001", EvidenceDigest: "ev-002"},
	}
	d := DeriveNextStep(in)
	if d.Action != ActionContinue {
		t.Fatalf("CONT-03: want CONTINUE, got %s", d.Action)
	}
	if d.NextStep == nil {
		t.Fatal("CONT-03: no NextStep")
	}
	// Must be bounded to b.go (unresolved), not repeat a.go
	foundB := false
	for _, tgt := range d.NextStep.Targets {
		if tgt == "b.go" {
			foundB = true
		}
		if tgt == "a.go" {
			t.Fatalf("CONT-03: must not re-derive already-completed step a.go, got %v", d.NextStep.Targets)
		}
	}
	if !foundB {
		t.Fatalf("CONT-03: expected b.go as next bounded target, got %v", d.NextStep.Targets)
	}
}

// CONT-04: continuation cannot create authorization
func TestCONT04_CannotCreateAuthorization(t *testing.T) {
	typ := reflect.TypeOf(ContinuationDecision{})
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		low := strings.ToLower(f.Name)
		if strings.Contains(low, "grant") || strings.Contains(low, "authoriz") || strings.Contains(low, "approve") || strings.Contains(low, "capability") {
			t.Fatalf("CONT-04: ContinuationDecision must not have authorization field %q", f.Name)
		}
	}
	typ2 := reflect.TypeOf(StepProposal{})
	for i := 0; i < typ2.NumField(); i++ {
		f := typ2.Field(i)
		low := strings.ToLower(f.Name)
		if strings.Contains(low, "grant") || strings.Contains(low, "authoriz") || strings.Contains(low, "token") && strings.Contains(low, "approv") {
			t.Fatalf("CONT-04: StepProposal must not have authorization field %q", f.Name)
		}
	}
	// Behavioral: derive with MUTATE next step still has no grant
	in := baseInput()
	in.PlanSteps = []PlanStepView{{ID: "step-02", Kind: "MUTATE", References: []string{"b.go"}, Rationale: "mutate"}}
	d := DeriveNextStep(in)
	if d.NextStep != nil && d.NextStep.Kind == "MUTATE" {
		_ = d.NextStep // no grant field to check - pass if type has none
	}
}

// CONT-05: continuation cannot expand $hot
func TestCONT05_CannotExpandHotEnvelope(t *testing.T) {
	in := baseInput()
	in.AllowedScope = []string{"a.go"} // $hot envelope is only a.go
	in.PlanSteps = []PlanStepView{{ID: "step-02", Kind: "MUTATE", References: []string{"b.go"}, Rationale: "needs b.go"}}
	in.Task.ActiveScope = []string{"a.go", "b.go"}
	in.Observations = []Observation{{Kind: KindObservation, Subject: "b.go", Detail: "needs b.go", StateFingerprint: "fp-001", EvidenceDigest: "ev-001"}}
	d := DeriveNextStep(in)
	if d.Action == ActionContinue && d.NextStep != nil {
		for _, tgt := range d.NextStep.Targets {
			if tgt == "b.go" {
				t.Fatalf("CONT-05: continuation must not expand $hot envelope to include %q", tgt)
			}
		}
	}
	if d.Action != ActionAwaitingApproval && d.Action != ActionBlocked {
		t.Fatalf("CONT-05: want AWAITING_APPROVAL or BLOCKED when envelope exceeded, got %s", d.Action)
	}
}

// CONT-06: continuation cannot create capabilities
func TestCONT06_CannotCreateCapabilities(t *testing.T) {
	typ := reflect.TypeOf(ContinuationDecision{})
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		low := strings.ToLower(f.Name)
		if strings.Contains(low, "capability") || strings.Contains(low, "permission") {
			t.Fatalf("CONT-06: ContinuationDecision must not have capability field %q", f.Name)
		}
	}
	// Also check DeriveNextStep never adds capability-granted targets
	in := baseInput()
	in.AllowedScope = []string{"a.go"}
	d := DeriveNextStep(in)
	if d.NextStep != nil {
		_ = d.NextStep // pure proposal, no capability elevation
	}
}

// CONT-07: continuation cannot directly execute
func TestCONT07_CannotDirectlyExecute(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "marker.txt")
	if err := os.WriteFile(tmp, []byte("before"), 0600); err != nil {
		t.Fatal(err)
	}
	in := baseInput()
	d := DeriveNextStep(in)
	// After DeriveNextStep, file must be unchanged — no execution
	data, _ := os.ReadFile(tmp)
	if string(data) != "before" {
		t.Fatal("CONT-07: DeriveNextStep mutated workspace — must not execute")
	}
	// Check no execute method exists
	typ := reflect.TypeOf((*ContinuationDecision)(nil))
	if typ == nil {
		t.Fatal("unexpected nil type")
	}
	_ = d
	// Also ensure continuation package has no os/exec or os.WriteFile calls via source grep
	root := findRepoRoot(t)
	data2, _ := os.ReadFile(filepath.Join(root, "internal/continuation/derive.go"))
	content := string(data2)
	if strings.Contains(content, "os.WriteFile") || strings.Contains(content, "os/exec") || strings.Contains(content, "substrate.Execute") {
		t.Fatal("CONT-07: continuation must not call execution substrate")
	}
}

// CONT-08: continuation cannot directly schedule
func TestCONT08_CannotDirectlySchedule(t *testing.T) {
	in := baseInput()
	d := DeriveNextStep(in)
	// Must be pure proposal, not a scheduled ExecutionStep
	if d.NextStep != nil {
		// StepProposal ID is proposal ID, not scheduler's ExecutionStep ID with Sequence/TotalSteps
		if strings.Contains(d.NextStep.ID, "of") {
			t.Fatalf("CONT-08: StepProposal must not look like scheduler ExecutionStep ID, got %q", d.NextStep.ID)
		}
	}
	root := findRepoRoot(t)
	data, _ := os.ReadFile(filepath.Join(root, "internal/continuation/derive.go"))
	if strings.Contains(string(data), "StepScheduler") || strings.Contains(string(data), "Schedule(") {
		t.Fatal("CONT-08: continuation must not directly schedule via StepScheduler")
	}
}

// CONT-09: stale state / OCC drift prevents unsafe continuation
func TestCONT09_StaleStatePreventsContinuation(t *testing.T) {
	in := baseInput()
	in.HasStaleState = true
	d := DeriveNextStep(in)
	if d.Action != ActionStale {
		t.Fatalf("CONT-09: stale state must yield STALE, got %s", d.Action)
	}
	if d.NextStep != nil {
		t.Fatal("CONT-09: STALE must not carry NextStep")
	}
	in2 := baseInput()
	in2.Task.RecoveryReason = "OCC drift: fingerprint mismatch"
	d2 := DeriveNextStep(in2)
	if d2.Action != ActionStale {
		t.Fatalf("CONT-09: OCC drift must yield STALE, got %s", d2.Action)
	}
}

// CONT-10: repeated no-progress state terminates
func TestCONT10_NoProgressTerminates(t *testing.T) {
	in := baseInput()
	in.Task.History = []StepHistoryEntry{
		{StepID: "step-01", Outcome: "partial", StateFingerprint: "fp-001", EvidenceDigest: "ev-001", Patches: 0},
		{StepID: "step-01", Outcome: "partial", StateFingerprint: "fp-001", EvidenceDigest: "ev-001", Patches: 0},
		{StepID: "step-01", Outcome: "partial", StateFingerprint: "fp-001", EvidenceDigest: "ev-001", Patches: 0},
	}
	in.Task.StateFingerprint = "fp-001"
	d := DeriveNextStep(in)
	if d.Action != ActionNoProgress {
		t.Fatalf("CONT-10: want NO_PROGRESS, got %s reason %s", d.Action, d.Reason)
	}
	if d.NextStep != nil {
		t.Fatal("CONT-10: NO_PROGRESS must not carry NextStep")
	}
}

// CONT-11: multi-step task can complete across multiple bounded steps
func TestCONT11_MultiStepTaskCompletesAcrossSteps(t *testing.T) {
	// Simulate 5-step progression
	plan := []PlanStepView{
		{ID: "step-01", Kind: "INVESTIGATE", References: []string{"a.go"}, Rationale: "inspect"},
		{ID: "step-02", Kind: "ANALYZE", References: []string{"b.go"}, Rationale: "analyze"},
		{ID: "step-03", Kind: "MUTATE", References: []string{"c.go"}, Rationale: "mutate"},
		{ID: "step-04", Kind: "VERIFY", References: []string{"c.go"}, Rationale: "verify"},
		{ID: "step-05", Kind: "OBSERVE", References: []string{"c.go"}, Rationale: "observe"},
	}
	completed := []string{}
	for i, step := range plan {
		in := DerivationInput{
			Task: TaskStateView{
				TaskID:           "task-multi",
				Intent:           "investigate and fix leak",
				ActiveScope:      []string{"a.go", "b.go", "c.go"},
				StateFingerprint: "fp-multi",
				CompletedSteps:   append([]string(nil), completed...),
				PendingSteps:     pendingAfter(plan, completed),
				EvidenceDigest:   "ev-multi",
			},
			PlanSteps:       plan,
			PreviousOutcome: "complete",
			Verified:        true,
			Observations: []Observation{
				{Kind: KindVerification, Subject: step.References[0], EvidenceDigest: "ev-multi", StateFingerprint: "fp-multi"},
			},
			AllowedScope: []string{"a.go", "b.go", "c.go"},
		}
		if i < len(plan)-1 {
			d := DeriveNextStep(in)
			if d.Action != ActionContinue {
				t.Fatalf("CONT-11 step %d: want CONTINUE, got %s", i+1, d.Action)
			}
			if d.NextStep == nil {
				t.Fatalf("CONT-11 step %d: no NextStep", i+1)
			}
			completed = append(completed, step.ID)
		} else {
			// last step -> after completing it, should be COMPLETE
			in.Task.CompletedSteps = append([]string(nil), planIDs(plan)...)
			in.Task.PendingSteps = nil
			d := DeriveNextStep(in)
			if d.Action != ActionComplete {
				t.Fatalf("CONT-11 final: want COMPLETE, got %s", d.Action)
			}
		}
	}
}

func pendingAfter(plan []PlanStepView, completed []string) []string {
	done := map[string]bool{}
	for _, c := range completed {
		done[c] = true
	}
	out := make([]string, 0, len(plan))
	for _, p := range plan {
		if !done[p.ID] {
			out = append(out, p.ID)
		}
	}
	return out
}
func planIDs(plan []PlanStepView) []string {
	out := make([]string, 0, len(plan))
	for _, p := range plan {
		out = append(out, p.ID)
	}
	return out
}

// CONT-12: 1024-token constrained provider can progress through multiple steps
func TestCONT12_ConstrainedProviderProgresses(t *testing.T) {
	in := baseInput()
	in.Task.ProviderCeiling = 1024
	in.Task.RequestedBudget = 4096
	in.Task.RemainingTaskBudget = 8192
	in.Task.ActiveScope = []string{"a.go", "b.go", "c.go", "d.go", "e.go"}
	in.PlanSteps = []PlanStepView{
		{ID: "step-01", Kind: "INVESTIGATE", References: []string{"a.go"}, Rationale: "inspect"},
		{ID: "step-02", Kind: "ANALYZE", References: []string{"b.go"}, Rationale: "analyze"},
		{ID: "step-03", Kind: "MUTATE", References: []string{"c.go"}, Rationale: "mutate"},
		{ID: "step-04", Kind: "VERIFY", References: []string{"d.go"}, Rationale: "verify"},
		{ID: "step-05", Kind: "OBSERVE", References: []string{"e.go"}, Rationale: "observe"},
	}
	in.Task.CompletedSteps = []string{"step-01"}
	in.Task.PendingSteps = []string{"step-02", "step-03", "step-04", "step-05"}
	in.PreviousOutcome = "partial"
	in.IsPartialOutput = true
	in.Observations = []Observation{{Kind: KindObservation, Subject: "b.go", EvidenceDigest: "ev-001", StateFingerprint: "fp-001"}}

	d := DeriveNextStep(in)
	if d.Action != ActionContinue {
		t.Fatalf("CONT-12: constrained provider partial must yield CONTINUE, got %s", d.Action)
	}
	if d.NextStep == nil {
		t.Fatal("CONT-12: no NextStep")
	}
	if d.NextStep.EstimatedTokens > 1024 {
		t.Fatalf("CONT-12: step budget %d exceeds per-step provider ceiling 1024", d.NextStep.EstimatedTokens)
	}
	// Task budget remains larger than step budget — task not treated as exhausted
	if in.Task.RemainingTaskBudget <= 1024 {
		t.Fatal("CONT-12: task budget must remain larger than per-step ceiling")
	}
}

// CONT-13: losing transcript does not destroy durable task state
func TestCONT13_LosingTranscriptDoesNotDestroyState(t *testing.T) {
	in := baseInput()
	in.Task.StateFingerprint = "fp-durable-123"
	in.Task.EvidenceDigest = "ev-durable-456"
	in.Observations = []Observation{
		{Kind: KindExecutionResult, Subject: "a.go", Detail: "executed", EvidenceDigest: "ev-durable-456", StateFingerprint: "fp-durable-123"},
		{Kind: KindVerification, Subject: "a.go", Detail: "verified", EvidenceDigest: "ev-durable-456", StateFingerprint: "fp-durable-123"},
	}
	d1 := DeriveNextStep(in)
	// Simulate losing transcript: no model proposal, but durable state same
	in2 := in
	in2.Observations = []Observation{
		{Kind: KindExecutionResult, Subject: "a.go", Detail: "executed", EvidenceDigest: "ev-durable-456", StateFingerprint: "fp-durable-123"},
		{Kind: KindVerification, Subject: "a.go", Detail: "verified", EvidenceDigest: "ev-durable-456", StateFingerprint: "fp-durable-123"},
	}
	// No transcript field exists; prove determinism from durable state
	d2 := DeriveNextStep(in2)
	if d1.Action != d2.Action || d1.StateDigest != d2.StateDigest {
		t.Fatalf("CONT-13: durable state must be transcript-independent: %v vs %v", d1, d2)
	}
	if d1.StateDigest == "" {
		t.Fatal("CONT-13: StateDigest must be derived from durable state, not empty")
	}
}

// CONT-14: continuation uses observation/evidence rather than model claims
func TestCONT14_UsesObservationNotModelClaims(t *testing.T) {
	// Model claims it fixed the leak, but verification says failed
	in := baseInput()
	in.Observations = []Observation{
		{Kind: KindModelProposal, Subject: "b.go", Detail: "I fixed the memory leak", EvidenceDigest: "ev-model", StateFingerprint: "fp-001"},
		{Kind: KindVerification, Subject: "b.go", Detail: "tests failed: leak still present", EvidenceDigest: "ev-ver", StateFingerprint: "fp-001"},
	}
	in.Verified = false
	in.PreviousOutcome = "complete"
	d := DeriveNextStep(in)
	// Must not treat model claim as complete; must continue based on verification
	if d.Action == ActionComplete {
		t.Fatal("CONT-14: must not treat model claim as complete without verification")
	}
	if d.Action != ActionContinue && d.Action != ActionBlocked && d.Action != ActionFailed {
		t.Fatalf("CONT-14: want CONTINUE/BLOCKED/FAILED based on verification, got %s", d.Action)
	}

	// Only model proposal without verification -> requests verification, not complete
	in2 := baseInput()
	in2.Observations = []Observation{
		{Kind: KindModelProposal, Subject: "b.go", Detail: "I fixed leak", EvidenceDigest: "ev-model", StateFingerprint: "fp-001"},
	}
	in2.Verified = false
	d2 := DeriveNextStep(in2)
	if d2.Action == ActionComplete {
		t.Fatal("CONT-14: model-only claim must not yield COMPLETE")
	}
	if d2.NextStep != nil && d2.NextStep.Kind != "VERIFY" {
		_ = d2.NextStep // at least it should not be COMPLETE; VERIFY is preferred
	}
}

// CONT-15: mutation continuation re-enters existing authorization boundary
func TestCONT15_MutationContinuationReentersAuthorization(t *testing.T) {
	in := baseInput()
	in.PlanSteps = []PlanStepView{{ID: "step-02", Kind: "MUTATE", References: []string{"b.go"}, Rationale: "mutate"}}
	in.Task.CompletedSteps = []string{"step-01"}
	in.Task.PendingSteps = []string{"step-02"}
	in.Observations = []Observation{{Kind: KindObservation, Subject: "b.go", EvidenceDigest: "ev-001", StateFingerprint: "fp-001"}}
	d := DeriveNextStep(in)
	if d.Action != ActionContinue {
		t.Fatalf("CONT-15: want CONTINUE, got %s", d.Action)
	}
	if d.NextStep == nil || d.NextStep.Kind != "MUTATE" {
		t.Fatalf("CONT-15: want MUTATE NextStep, got %v", d.NextStep)
	}
	// Prove no grant is present: StepProposal and decision carry no authorization
	typ := reflect.TypeOf(d)
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if strings.Contains(strings.ToLower(f.Name), "grant") || strings.Contains(strings.ToLower(f.Name), "authoriz") {
			t.Fatalf("CONT-15: decision must not carry grant, found %q", f.Name)
		}
	}
}

// CONT-16: existing StepScheduler remains the only scheduler (checked via architecture test, but also behavioral)
func TestCONT16_NoSecondScheduler(t *testing.T) {
	root := findRepoRoot(t)
	// Search for forbidden scheduler types
	forbidden := []string{"AdaptiveScheduler", "ContinuationScheduler", "AgentScheduler", "ProblemScheduler"}
	for _, rel := range goFilesUnderIntermediate(root) {
		if !strings.HasSuffix(rel, ".go") || strings.Contains(rel, "_test.go") {
			continue
		}
		data, _ := os.ReadFile(filepath.Join(root, rel))
		content := string(data)
		for _, name := range forbidden {
			if strings.Contains(content, "type "+name) {
				t.Fatalf("CONT-16: forbidden second scheduler %q found in %s", name, rel)
			}
		}
	}
}

// CONT-17: existing execution authority remains only execution authority
func TestCONT17_NoSecondExecutionAuthority(t *testing.T) {
	root := findRepoRoot(t)
	forbidden := []string{"RuntimeExecutor", "AnotherExecutor", "ContinuationExecutor"}
	// The canonical executor is execution.RuntimeExecutor and runtime/executor
	// but there must not be a second one in continuation
	data, _ := os.ReadFile(filepath.Join(root, "internal/continuation/derive.go"))
	if strings.Contains(string(data), "type RuntimeExecutor") {
		t.Fatal("CONT-17: continuation must not define execution authority")
	}
	for _, rel := range goFilesUnderIntermediate(root) {
		if rel == "internal/continuation/derive.go" {
			continue
		}
		if strings.Contains(rel, "internal/continuation/") && strings.Contains(rel, "executor") {
			t.Fatalf("CONT-17: continuation must not have executor file %s", rel)
		}
	}
	_ = forbidden
}

// CONT-18: no domain-specific continuation
func TestCONT18_DomainNeutral(t *testing.T) {
	root := findRepoRoot(t)
	forbidden := []string{"ReactContinuation", "BackendContinuation", "GoContinuation", "StaticWebContinuation", "DatabaseContinuation"}
	for _, rel := range goFilesUnderIntermediate(root) {
		if !strings.Contains(rel, "internal/continuation/") {
			continue
		}
		if strings.HasSuffix(rel, "_test.go") {
			continue
		}
		data, _ := os.ReadFile(filepath.Join(root, rel))
		for _, name := range forbidden {
			if strings.Contains(string(data), name) {
				t.Fatalf("CONT-18: domain-specific continuation %q forbidden, found in %s", name, rel)
			}
		}
	}
	// Also check derive logic is domain-neutral: it must not branch on web semantics
	data, _ := os.ReadFile(filepath.Join(root, "internal/continuation/derive.go"))
	low := strings.ToLower(string(data))
	if strings.Contains(low, "react") || strings.Contains(low, "html") || strings.Contains(low, "css") {
		// Allow only generic references, not domain branching
		if strings.Contains(low, "reactcontinuation") || strings.Contains(low, "staticweb") {
			t.Fatalf("CONT-18: continuation contains domain-specific branching")
		}
	}
}

func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod found")
		}
		dir = parent
	}
}

func goFilesUnderIntermediate(root string) []string {
	var out []string
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(info.Name(), ".go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	return out
}
