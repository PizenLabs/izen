package plan

import (
	"strings"
	"testing"
)

func validateOf(t *testing.T, steps ...Step) *ValidatedPlan {
	t.Helper()
	np := normalizedFrom(t, steps...)
	vp, err := NewPlanValidator().Validate(np)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !vp.Valid() {
		t.Fatalf("fixture plan invalid: %v", vp.FailedResults())
	}
	return vp
}

func policyFixture() []Policy {
	return []Policy{
		{
			ID:             "sandbox",
			Description:    "sandbox constraints",
			AllowedRoots:   []string{"/workspace"},
			AllowedActions: []StepKind{StepCreate, StepModify, StepRead, StepDelete, StepVerify},
			DeniedGlobs:    []string{".env", "*.pem"},
			DeniedPatterns: []string{`(^|/)\.ssh/`, `secret`},
			MaxSteps:       8,
			MaxTargets:     4,
			ForbidShell:    true,
		},
	}
}

func TestPolicyEngineApprovesCleanPlan(t *testing.T) {
	vp := validateOf(t,
		NewStep(StepCreate, "cmd/main.go", WithID("s1")),
		NewStep(StepModify, "internal/app.go", WithID("s2")),
	)
	d := NewPolicyEngine(policyFixture()...).Evaluate(vp)
	if !d.Approved() {
		t.Fatalf("plan should be approved: %v", d.Summary())
	}
}

func TestPolicyEngineRejectsOutOfBoundsPath(t *testing.T) {
	vp := validateOf(t, NewStep(StepCreate, "../../etc/cron", WithID("s1")))
	d := NewPolicyEngine(policyFixture()...).Evaluate(vp)
	if d.Approved() {
		t.Fatal("out-of-bounds target must be denied")
	}
	if !hasViolation(d, "permitted_path") {
		t.Fatalf("expected permitted_path violation, got %v", d.Violations())
	}
}

func TestPolicyEngineRejectsForbiddenAction(t *testing.T) {
	vp := validateOf(t, NewStep(StepRun, "rm -rf /", WithID("s1")))
	d := NewPolicyEngine(policyFixture()...).Evaluate(vp)
	if d.Approved() {
		t.Fatal("run step must be denied by allowed actions")
	}
	if !hasViolation(d, "action_permission") {
		t.Fatalf("expected action_permission violation, got %v", d.Violations())
	}
}

func TestPolicyEngineRejectsForbiddenGlob(t *testing.T) {
	vp := validateOf(t, NewStep(StepModify, ".env", WithID("s1")))
	d := NewPolicyEngine(policyFixture()...).Evaluate(vp)
	if d.Approved() {
		t.Fatal("forbidden glob must be denied")
	}
	if !hasViolation(d, "forbidden_glob") {
		t.Fatalf("expected forbidden_glob violation, got %v", d.Violations())
	}
}

func TestPolicyEngineRejectsForbiddenPattern(t *testing.T) {
	vp := validateOf(t, NewStep(StepRead, ".ssh/config", WithID("s1")))
	d := NewPolicyEngine(policyFixture()...).Evaluate(vp)
	if d.Approved() {
		t.Fatal("forbidden pattern must be denied")
	}
	if !hasViolation(d, "forbidden_pattern") {
		t.Fatalf("expected forbidden_pattern violation, got %v", d.Violations())
	}
}

func TestPolicyEngineRejectsShellWhenForbidden(t *testing.T) {
	vp := validateOf(t,
		NewStep(StepCreate, "a.go", WithID("s1")),
		NewStep(StepVerify, "go test ./...", WithID("s2")),
	)
	d := NewPolicyEngine(policyFixture()...).Evaluate(vp)
	if d.Approved() {
		t.Fatal("shell step must be denied when ForbidShell")
	}
	if !hasViolation(d, "forbid_shell") {
		t.Fatalf("expected forbid_shell violation, got %v", d.Violations())
	}
}

func TestPolicyEngineRejectsTooManySteps(t *testing.T) {
	var steps []Step
	for i := 0; i < 9; i++ {
		steps = append(steps, NewStep(StepCreate, strings.Repeat("x", i+1), WithID(string(rune('a'+i)))))
	}
	vp := validateOf(t, steps...)
	d := NewPolicyEngine(policyFixture()...).Evaluate(vp)
	if d.Approved() {
		t.Fatal("too many steps must be denied")
	}
	if !hasViolation(d, "max_steps") {
		t.Fatalf("expected max_steps violation, got %v", d.Violations())
	}
}

func TestPolicyEngineRejectsTooManyTargets(t *testing.T) {
	vp := validateOf(t,
		NewStep(StepCreate, "a.go", WithID("s1")),
		NewStep(StepCreate, "b.go", WithID("s2")),
		NewStep(StepCreate, "c.go", WithID("s3")),
		NewStep(StepCreate, "d.go", WithID("s4")),
		NewStep(StepCreate, "e.go", WithID("s5")),
	)
	d := NewPolicyEngine(policyFixture()...).Evaluate(vp)
	if d.Approved() {
		t.Fatal("too many targets must be denied")
	}
	if !hasViolation(d, "max_targets") {
		t.Fatalf("expected max_targets violation, got %v", d.Violations())
	}
}

func TestPolicyEngineRejectsInvalidPlan(t *testing.T) {
	vp, _ := NewPlanValidator().Validate(normalizedFrom(t,
		NewStep(StepKind("explode"), "a.go", WithID("s1")),
	))
	d := NewPolicyEngine(policyFixture()...).Evaluate(vp)
	if d.Approved() {
		t.Fatal("invalid plan must not be approved")
	}
	if d.Violations()[0].PolicyID != "engine" {
		t.Fatalf("expected engine-level violation, got %+v", d.Violations())
	}
}

func TestPolicyEngineNilPlan(t *testing.T) {
	d := NewPolicyEngine(policyFixture()...).Evaluate(nil)
	if d.Approved() {
		t.Fatal("nil plan must not be approved")
	}
}

func TestPolicyEngineIgnoresUnboundedRoots(t *testing.T) {
	vp := validateOf(t, NewStep(StepCreate, "anywhere.go", WithID("s1")))
	engine := NewPolicyEngine(Policy{ID: "open", Description: "no roots"})
	d := engine.Evaluate(vp)
	if !d.Approved() {
		t.Fatalf("unbounded policy should approve: %v", d.Violations())
	}
}

func TestPolicyDecisionImmutability(t *testing.T) {
	vp := validateOf(t, NewStep(StepCreate, "a.go", WithID("s1")))
	d := NewPolicyEngine(policyFixture()...).Evaluate(vp)
	// Appending to a returned copy must not leak back into the decision or
	// into slices returned by later accessor calls.
	a := d.Violations()
	b := d.Violations()
	a = append(a, Violation{PolicyID: "x"})
	if len(a) != 1 {
		t.Fatal("append failed to grow the copy")
	}
	if len(b) != 0 {
		t.Fatal("Violations() returned an aliased slice")
	}
	if len(d.Violations()) != 0 {
		t.Fatal("decision leaked via Violations()")
	}
}

func hasViolation(d PolicyDecision, rule string) bool {
	for _, v := range d.Violations() {
		if v.Rule == rule {
			return true
		}
	}
	return false
}
