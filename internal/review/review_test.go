package review

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ─── Ledger Tests ────────────────────────────────────────────────────────

func TestNewReviewLedger(t *testing.T) {
	l := NewReviewLedger("test-001")
	if l.ReviewID != "test-001" {
		t.Fatalf("expected review_id test-001, got %s", l.ReviewID)
	}
	if l.Status != StatusUnresolved {
		t.Fatalf("expected initial status Unresolved, got %s", l.Status)
	}
}

func TestLedgerAddChange(t *testing.T) {
	l := NewReviewLedger("test-001")
	c := l.AddChange("cmd/api/main.go", "removed signal stop call", "dev")

	if c.File != "cmd/api/main.go" {
		t.Errorf("expected file cmd/api/main.go, got %s", c.File)
	}
	if c.ID != "C-001" {
		t.Errorf("expected ID C-001, got %s", c.ID)
	}
	if c.Actor != "dev" {
		t.Errorf("expected actor dev, got %s", c.Actor)
	}
	if len(l.Changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(l.Changes))
	}
}

func TestLedgerAddRisk(t *testing.T) {
	l := NewReviewLedger("test-001")
	r := l.AddRisk("Behavioral", "cmd/api/main.go", 42, "Signal handling may regress")

	if r.ID != "R-001" {
		t.Errorf("expected ID R-001, got %s", r.ID)
	}
	if r.Category != "Behavioral" {
		t.Errorf("expected category Behavioral, got %s", r.Category)
	}
	if r.Line != 42 {
		t.Errorf("expected line 42, got %d", r.Line)
	}
}

func TestLedgerGraphLinking(t *testing.T) {
	l := NewReviewLedger("test-001")

	c := l.AddChange("cmd/api/main.go", "removed signal stop call", "dev")
	r := l.AddRisk("Behavioral", "cmd/api/main.go", 42, "Repeated interrupt may no longer force shutdown")
	_ = c

	h := l.AddHypothesis(r.ID, "Second interrupt may no longer force shutdown", "SIGINT twice should force-exit within 1s")
	v := l.AddVerification(h.ID, "Signal lifecycle verification")
	e := l.AddEvidence(v.ID, EvTypeExistingTest, EvStatusPassed, ConfVerified, "", "")

	if h.RiskID != r.ID {
		t.Errorf("hypothesis.RiskID = %s, want %s", h.RiskID, r.ID)
	}
	if v.HypothesisID != h.ID {
		t.Errorf("verification.HypothesisID = %s, want %s", v.HypothesisID, h.ID)
	}
	if e.VerificationID != v.ID {
		t.Errorf("evidence.VerificationID = %s, want %s", e.VerificationID, v.ID)
	}

	if len(l.Changes) != 1 {
		t.Errorf("expected 1 change, got %d", len(l.Changes))
	}
	if len(l.Risks) != 1 {
		t.Errorf("expected 1 risk, got %d", len(l.Risks))
	}
	if len(l.Hypotheses) != 1 {
		t.Errorf("expected 1 hypothesis, got %d", len(l.Hypotheses))
	}
	if len(l.Verifications) != 1 {
		t.Errorf("expected 1 verification, got %d", len(l.Verifications))
	}
	if len(l.Evidences) != 1 {
		t.Errorf("expected 1 evidence, got %d", len(l.Evidences))
	}
}

func TestLedgerMultipleRecords(t *testing.T) {
	l := NewReviewLedger("multi-test")
	l.AddChange("a.go", "added handler", "alice")
	l.AddChange("b.go", "removed call", "bob")
	l.AddRisk("Deterministic", "a.go", 10, "panic risk")
	l.AddRisk("Speculative", "b.go", 0, "TODO marker")
	l.AddHypothesis("R-001", "panic on nil input", "nil input returns error not panic")
	l.AddVerification("H-001", "nil input unit test")

	if len(l.Changes) != 2 {
		t.Errorf("expected 2 changes, got %d", len(l.Changes))
	}
	if l.Changes[1].ID != "C-002" {
		t.Errorf("expected second change ID C-002, got %s", l.Changes[1].ID)
	}
	if l.Risks[1].ID != "R-002" {
		t.Errorf("expected second risk ID R-002, got %s", l.Risks[1].ID)
	}
}

func TestLedgerSetStatus(t *testing.T) {
	l := NewReviewLedger("test-001")
	l.SetStatus(StatusVerified)
	if l.Status != StatusVerified {
		t.Errorf("expected StatusVerified, got %s", l.Status)
	}
	l.SetStatus(StatusConditional)
	if l.Status != StatusConditional {
		t.Errorf("expected StatusConditional, got %s", l.Status)
	}
}

func TestLedgerActiveEvidenceIDs(t *testing.T) {
	l := NewReviewLedger("test-001")
	l.AddEvidence("V-001", EvTypeExistingTest, EvStatusPassed, ConfVerified, "", "")
	l.AddEvidence("V-001", EvTypeEphemeralTest, EvStatusPassed, ConfHigh, "", "")

	ids := l.ActiveEvidenceIDs()
	if !strings.Contains(ids, "E-001") || !strings.Contains(ids, "E-002") {
		t.Errorf("expected both E-001 and E-002 in active IDs, got %s", ids)
	}
}

func TestLedgerFormatCompact(t *testing.T) {
	l := NewReviewLedger("test-001")
	l.AddChange("cmd/api/main.go", "removed signal stop", "dev")
	l.AddRisk("Behavioral", "cmd/api/main.go", 42, "Signal handling may regress")
	l.AddHypothesis("R-001", "Second interrupt may no longer force shutdown", "SIGINT twice should force-exit")
	l.AddVerification("H-001", "Signal lifecycle verification")
	l.AddEvidence("V-001", EvTypeExistingTest, EvStatusPassed, ConfVerified, "", "")

	output := l.FormatCompact()
	if !strings.Contains(output, "Review Ledger") {
		t.Error("expected 'Review Ledger' in output")
	}
	if !strings.Contains(output, "C-001") {
		t.Error("expected C-001 in output")
	}
	if !strings.Contains(output, "R-001") {
		t.Error("expected R-001 in output")
	}
	if !strings.Contains(output, "H-001") {
		t.Error("expected H-001 in output")
	}
	if !strings.Contains(output, "V-001") {
		t.Error("expected V-001 in output")
	}
	if !strings.Contains(output, "E-001") {
		t.Error("expected E-001 in output")
	}
	if !strings.Contains(output, "Review Status: Unresolved") {
		t.Error("expected 'Review Status: Unresolved' in output")
	}
}

// ─── Classifier Tests ────────────────────────────────────────────────────

func TestClassifyDeterministicRisks(t *testing.T) {
	tests := []struct {
		name     string
		risk     InputRisk
		category RiskCategory
		genTest  bool
	}{
		{
			name:     "hardcoded secret",
			risk:     InputRisk{Category: "hardcoded_secret", RuleID: "SEC-SECRET-001"},
			category: RiskDeterministic,
			genTest:  true,
		},
		{
			name:     "sql injection",
			risk:     InputRisk{Category: "sql_injection", RuleID: "SEC-SQL-001"},
			category: RiskDeterministic,
			genTest:  true,
		},
		{
			name:     "panic",
			risk:     InputRisk{Category: "panic", RuleID: "GO-PANIC-001"},
			category: RiskDeterministic,
			genTest:  true,
		},
		{
			name:     "os command",
			risk:     InputRisk{Category: "os_command", RuleID: "SEC-CMD-001"},
			category: RiskDeterministic,
			genTest:  true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			class := ClassifyRisk(tc.risk)
			if class.Category != tc.category {
				t.Errorf("expected category %s, got %s", tc.category, class.Category)
			}
			if got := ShouldGenerateTest(class); got != tc.genTest {
				t.Errorf("ShouldGenerateTest = %v, want %v", got, tc.genTest)
			}
		})
	}
}

func TestClassifyBehavioralRisks(t *testing.T) {
	tests := []struct {
		name    string
		risk    InputRisk
		genTest bool
	}{
		{
			name:    "goroutine",
			risk:    InputRisk{Category: "goroutine", RuleID: "GO-GOROUTINE-001"},
			genTest: false,
		},
		{
			name:    "lock without defer",
			risk:    InputRisk{Category: "lock_without_defer", RuleID: "GO-LOCK-001"},
			genTest: false,
		},
		{
			name:    "HTTP endpoint",
			risk:    InputRisk{Category: "exposed_endpoint", RuleID: "SEC-HTTP-001"},
			genTest: false,
		},
		{
			name:    "serialization",
			risk:    InputRisk{Category: "serialization", RuleID: "SEC-SERIAL-001"},
			genTest: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			class := ClassifyRisk(tc.risk)
			if class.Category != RiskBehavioral {
				t.Errorf("expected category %s, got %s", RiskBehavioral, class.Category)
			}
			if got := ShouldGenerateTest(class); got != tc.genTest {
				t.Errorf("ShouldGenerateTest = %v, want %v", got, tc.genTest)
			}
		})
	}
}

func TestClassifySpeculativeRisks(t *testing.T) {
	tests := []struct {
		name string
		risk InputRisk
	}{
		{
			name: "debug output",
			risk: InputRisk{Category: "debug_output", RuleID: "CQ-PRINT-001"},
		},
		{
			name: "unused result",
			risk: InputRisk{Category: "unused_result", RuleID: "CQ-BLANK-001"},
		},
		{
			name: "TODO marker",
			risk: InputRisk{Category: "code_quality", RuleID: "CQ-TODO-001"},
		},
		{
			name: "info severity",
			risk: InputRisk{Category: "some_other", Severity: "info"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			class := ClassifyRisk(tc.risk)
			if class.Category != RiskSpeculative {
				t.Errorf("expected category %s, got %s", RiskSpeculative, class.Category)
			}
			if ShouldGenerateTest(class) {
				t.Error("ShouldGenerateTest should be false for speculative risks")
			}
		})
	}
}

func TestClassifyStructuralRisks(t *testing.T) {
	risk := InputRisk{Category: "no_error_return", RuleID: "GO-FUNC-001"}
	class := ClassifyRisk(risk)
	if class.Category != RiskStructural {
		t.Errorf("expected category %s, got %s", RiskStructural, class.Category)
	}
	if ShouldGenerateTest(class) {
		t.Error("ShouldGenerateTest should be false for structural risks")
	}
}

func TestClassifyEnvironmentalRisks(t *testing.T) {
	risk := InputRisk{Category: "side_effect", RuleID: "SEC-EXEC-001"}
	class := ClassifyRisk(risk)
	if class.Category != RiskEnvironmental {
		t.Errorf("expected category %s, got %s", RiskEnvironmental, class.Category)
	}
	if ShouldGenerateTest(class) {
		t.Error("ShouldGenerateTest should be false for environmental risks")
	}
}

func TestClassifyUncategorizedDefaultsToSpeculative(t *testing.T) {
	risk := InputRisk{Category: "unknown_risk", RuleID: "UNKNOWN-001", Severity: "high"}
	class := ClassifyRisk(risk)
	if class.Category != RiskSpeculative {
		t.Errorf("expected default category %s for unknown risk, got %s", RiskSpeculative, class.Category)
	}
	if ShouldGenerateTest(class) {
		t.Error("ShouldGenerateTest should be false for uncategorized risks")
	}
}

// ─── Sandbox Tests ───────────────────────────────────────────────────────

func TestNewSandbox(t *testing.T) {
	sb := NewSandbox("review-001", "/tmp/fake-project")
	if sb.ReviewID != "review-001" {
		t.Errorf("expected review-001, got %s", sb.ReviewID)
	}
	if sb.Workspace != "/tmp/izen/review/review-001" {
		t.Errorf("expected /tmp/izen/review/review-001, got %s", sb.Workspace)
	}
	if sb.ProjectRoot != "/tmp/fake-project" {
		t.Errorf("expected /tmp/fake-project, got %s", sb.ProjectRoot)
	}
}

func TestSanitizeID(t *testing.T) {
	tests := []struct {
		input, expected string
	}{
		{"simple", "simple"},
		{"with-dashes", "with-dashes"},
		{"with_underscores", "with_underscores"},
		{"special!@#$", "special____"},
		{"spaces and stuff", "spaces_and_stuff"},
	}
	for _, tc := range tests {
		got := sanitizeID(tc.input)
		if got != tc.expected {
			t.Errorf("sanitizeID(%q) = %q, want %q", tc.input, got, tc.expected)
		}
	}
}

func TestSandboxCreateAndCleanup(t *testing.T) {
	reviewID := "test-sandbox-" + strings.ReplaceAll(t.Name(), "/", "_")
	sb := NewSandbox(reviewID, t.TempDir())

	err := sb.Create()
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if !sb.created {
		t.Fatal("expected created flag true after Create")
	}

	if _, err := os.Stat(sb.Workspace); os.IsNotExist(err) {
		t.Fatal("expected sandbox directory to exist after Create")
	}

	err = sb.Cleanup()
	if err != nil {
		t.Fatalf("Cleanup: %v", err)
	}

	if sb.created {
		t.Fatal("expected created flag false after Cleanup")
	}

	if _, err := os.Stat(sb.Workspace); !os.IsNotExist(err) {
		t.Fatal("expected sandbox directory to be removed after Cleanup")
	}
}

func TestSandboxWriteTestFile(t *testing.T) {
	reviewID := "test-write-" + strings.ReplaceAll(t.Name(), "/", "_")
	sb := NewSandbox(reviewID, t.TempDir())

	if err := sb.Create(); err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer func() {
		if err := sb.Cleanup(); err != nil {
			t.Logf("Cleanup: %v", err)
		}
	}()

	content := `package main

func TestNothing(t *testing.T) {
	t.Log("test ran")
}
`
	err := sb.WriteTestFile("main_test.go", content)
	if err != nil {
		t.Fatalf("WriteTestFile: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(sb.Workspace, "main_test.go"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != content {
		t.Errorf("written content mismatch:\ngot:\n%s\nwant:\n%s", string(data), content)
	}
}

func TestSandboxWriteTestFileNestedDir(t *testing.T) {
	reviewID := "test-nested-" + strings.ReplaceAll(t.Name(), "/", "_")
	sb := NewSandbox(reviewID, t.TempDir())

	if err := sb.Create(); err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer func() {
		if err := sb.Cleanup(); err != nil {
			t.Logf("Cleanup: %v", err)
		}
	}()

	err := sb.WriteTestFile("pkg/subpkg/thing_test.go", "package subpkg\n")
	if err != nil {
		t.Fatalf("WriteTestFile nested: %v", err)
	}

	if _, err := os.Stat(filepath.Join(sb.Workspace, "pkg/subpkg/thing_test.go")); os.IsNotExist(err) {
		t.Fatal("expected nested test file to exist")
	}
}

func TestSandboxCleanupRemovesAllFiles(t *testing.T) {
	reviewID := "test-cleanup-" + strings.ReplaceAll(t.Name(), "/", "_")
	sb := NewSandbox(reviewID, t.TempDir())

	if err := sb.Create(); err != nil {
		t.Fatalf("Create: %v", err)
	}

	_ = sb.WriteTestFile("a_test.go", "package main\n")
	_ = sb.WriteTestFile("pkg/b_test.go", "package pkg\n")

	entries, _ := os.ReadDir(sb.Workspace)
	if len(entries) == 0 {
		t.Fatal("expected files in sandbox before cleanup")
	}

	if err := sb.Cleanup(); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}

	if _, err := os.Stat(sb.Workspace); !os.IsNotExist(err) {
		t.Fatal("expected sandbox directory to be fully removed after cleanup")
	}
}

func TestSandboxRunTestWithoutCreate(t *testing.T) {
	sb := NewSandbox("no-create", t.TempDir())
	result := sb.RunTest("main_test.go")
	if result.Passed {
		t.Error("expected test to fail when sandbox not created")
	}
	if result.Output != "sandbox not created" {
		t.Errorf("expected 'sandbox not created', got %s", result.Output)
	}
}

func TestRunWithSandboxCleanupOnSuccess(t *testing.T) {
	projectRoot := t.TempDir()
	reviewID := "test-runwith-" + strings.ReplaceAll(t.Name(), "/", "_")

	sandboxPath := filepath.Join("/tmp/izen/review", reviewID)

	rec, err := RunWithSandbox(reviewID, projectRoot, func(sb *Sandbox) (EvidenceStatus, EvidenceConfidence, string, string) {
		_ = sb.WriteTestFile("test_cleanup.go", "package main\n")
		return EvStatusPassed, ConfHigh, "", "test passed"
	})
	if err != nil {
		t.Fatalf("RunWithSandbox: %v", err)
	}
	if rec.Status != EvStatusPassed {
		t.Errorf("expected EvStatusPassed, got %s", rec.Status)
	}
	if rec.Confidence != ConfHigh {
		t.Errorf("expected ConfHigh, got %s", rec.Confidence)
	}

	if _, err := os.Stat(sandboxPath); !os.IsNotExist(err) {
		t.Fatal("expected sandbox directory to be cleaned up after RunWithSandbox")
	}
}

func TestRunWithSandboxCleanupOnError(t *testing.T) {
	projectRoot := t.TempDir()
	reviewID := "test-runwith-fail-" + strings.ReplaceAll(t.Name(), "/", "_")

	sandboxPath := filepath.Join("/tmp/izen/review", reviewID)
	defer func() { _ = os.RemoveAll(sandboxPath) }()

	rec, err := RunWithSandbox(reviewID, projectRoot, func(sb *Sandbox) (EvidenceStatus, EvidenceConfidence, string, string) {
		return EvStatusFailed, ConfLow, "", "simulated failure"
	})
	if err != nil {
		t.Fatalf("RunWithSandbox: %v", err)
	}
	if rec.Status != EvStatusFailed {
		t.Errorf("expected EvStatusFailed, got %s", rec.Status)
	}

	if _, err := os.Stat(sandboxPath); !os.IsNotExist(err) {
		t.Fatal("expected sandbox directory to be cleaned up even after test failure")
	}
}

func TestTruncateOutput(t *testing.T) {
	short := "hello world"
	if got := truncateOutput(short, 100); got != short {
		t.Errorf("expected no truncation for short string, got %s", got)
	}

	long := ""
	for i := 0; i < 100; i++ {
		long += "a"
	}
	truncated := truncateOutput(long, 10)
	if len(truncated) >= len(long) {
		t.Error("expected truncation for long string")
	}
	if !strings.Contains(truncated, "[truncated") {
		t.Error("expected truncation notice")
	}
}

// ─── Provenance Renderer Tests ───────────────────────────────────────────

func TestProvenanceRendererBasic(t *testing.T) {
	l := NewReviewLedger("test-001")
	l.AddChange("cmd/api/main.go", "removed signal stop call", "dev")
	l.AddRisk("Behavioral", "cmd/api/main.go", 42, "Repeated interrupt may no longer force shutdown")
	l.AddHypothesis("R-001", "Second interrupt may no longer force shutdown", "SIGINT twice should force-exit within 1s")
	l.AddVerification("H-001", "Signal lifecycle verification")
	l.AddEvidence("V-001", EvTypeExistingTest, EvStatusPassed, ConfVerified, "", "")
	l.AddEvidence("V-002", EvTypeEphemeralTest, EvStatusPassed, ConfHigh, "", "")
	l.SetStatus(StatusConditional)

	pr := NewProvenanceRenderer(l, 80)
	output := pr.Render()

	if !strings.Contains(output, "Review Ledger") {
		t.Error("expected 'Review Ledger' header")
	}
	if !strings.Contains(output, "C-001") {
		t.Error("expected C-001")
	}
	if !strings.Contains(output, "R-001") {
		t.Error("expected R-001")
	}
	if !strings.Contains(output, "H-001") {
		t.Error("expected H-001")
	}
	if !strings.Contains(output, "V-001") {
		t.Error("expected V-001")
	}
	if !strings.Contains(output, "E-001") {
		t.Error("expected E-001")
	}
	if !strings.Contains(output, "E-002") {
		t.Error("expected E-002")
	}
	if !strings.Contains(output, "Review Status: Conditional") {
		t.Errorf("expected review status Conditional in output:\n%s", output)
	}
	if !strings.Contains(output, "┌─") || !strings.Contains(output, "└") || !strings.Contains(output, "│") {
		t.Error("expected box drawing characters")
	}
}

func TestProvenanceRendererEmpty(t *testing.T) {
	l := NewReviewLedger("empty-test")
	pr := NewProvenanceRenderer(l, 60)
	output := pr.Render()

	if !strings.Contains(output, "Review Ledger") {
		t.Error("expected 'Review Ledger' header")
	}
	if !strings.Contains(output, "Review Status: Unresolved") {
		t.Error("expected 'Review Status: Unresolved'")
	}
}

func TestProvenanceRendererWidthBounds(t *testing.T) {
	l := NewReviewLedger("width-test")
	pr := NewProvenanceRenderer(l, 10)
	output := pr.Render()
	if !strings.Contains(output, "Review Ledger") {
		t.Error("expected output even with narrow width")
	}

	pr2 := NewProvenanceRenderer(l, 200)
	output2 := pr2.Render()
	if !strings.Contains(output2, "Review Ledger") {
		t.Error("expected output with wide width")
	}
}

func TestProvenanceRendererCompact(t *testing.T) {
	l := NewReviewLedger("compact-test")
	l.AddChange("a.go", "change", "dev")
	l.AddRisk("Deterministic", "a.go", 5, "panic risk")
	compact := NewProvenanceRenderer(l, 60).RenderCompact()
	if !strings.Contains(compact, "Review Ledger") {
		t.Error("expected 'Review Ledger' in compact output")
	}
}
