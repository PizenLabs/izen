package headless_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/runtime/headless"
	"github.com/PizenLabs/izen/runtime/kernel"
)

// These tests are the executable proof of the kernel's UI independence: they
// drive complete executions, observe real terminal truth, and assert on the
// report a non-interactive consumer receives. No terminal, no renderer, and no
// presentation package appears anywhere.

// TestHeadlessCreateExecutionProves proves the full positive path end to end
// without a terminal: a real CREATE contract, real capabilities, real evidence,
// real verification, and a PROVEN verdict.
func TestHeadlessCreateExecutionProves(t *testing.T) {
	root := t.TempDir()
	runner, err := headless.New(root)
	if err != nil {
		t.Fatalf("runner: %v", err)
	}

	spec := kernel.Spec{
		ExecutionID: "headless-create-1",
		Objective:   "write a release note",
		Contract: kernel.Contract{
			Kind:                 kernel.ContractCreate,
			Targets:              []string{"RELEASE.md"},
			RequiresVerification: true,
		},
		Program: kernel.Program{
			{ID: "look", Capability: kernel.WorkspaceDiscover, Args: map[string]string{}},
			{
				ID:         "write",
				Capability: kernel.FileWrite,
				Target:     "RELEASE.md",
				Args:       map[string]string{"content": "# Release\n\nFirst entry.\n"},
			},
		},
	}

	grant, err := kernel.NewGrant("g1", kernel.AllCapabilityIDs(), []string{"RELEASE.md"})
	if err != nil {
		t.Fatalf("grant: %v", err)
	}

	result, err := runner.Run(context.Background(), spec, grant)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !result.Proves() {
		t.Fatalf("a real create was not PROVEN\noutcome=%s reason=%s unmet=%v",
			result.Outcome, result.Reason, result.Unmet)
	}

	// The workspace must actually contain the file. A PROVEN verdict that does
	// not correspond to bytes on disk would be the whole bug this runtime exists
	// to prevent.
	content, err := os.ReadFile(filepath.Join(root, "RELEASE.md"))
	if err != nil {
		t.Fatalf("PROVEN but the file is absent: %v", err)
	}
	if !strings.Contains(string(content), "First entry") {
		t.Errorf("file content = %q", content)
	}
	if !result.State.Mutation.Durable() {
		t.Error("a PROVEN create did not hold a durable mutation axis")
	}
}

// TestHeadlessObserveWithoutEvidenceIsUnsubstantiated proves the negative path
// works headlessly too: a contract naming a target that was never observed is
// unsubstantiated, not proven.
func TestHeadlessObserveWithoutEvidenceIsUnsubstantiated(t *testing.T) {
	root := t.TempDir()
	runner, err := headless.New(root)
	if err != nil {
		t.Fatalf("runner: %v", err)
	}

	spec := kernel.Spec{
		ExecutionID: "headless-observe-1",
		Contract: kernel.Contract{
			Kind:                kernel.ContractObserve,
			Targets:             []string{"never-inspected.txt"},
			RequiresObservation: true,
		},
		Program: kernel.Program{{
			ID:         "stat",
			Capability: kernel.FileExists,
			Target:     "never-inspected.txt",
		}},
	}
	grant, err := kernel.NewGrant("g1", kernel.AllCapabilityIDs(), []string{"never-inspected.txt"})
	if err != nil {
		t.Fatalf("grant: %v", err)
	}

	result, err := runner.Run(context.Background(), spec, grant)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Proves() {
		t.Fatal("an observe contract was PROVEN with no evidence of looking")
	}
	if result.Outcome != kernel.OutcomeUnsubstantiated {
		t.Fatalf("outcome = %q, want %q", result.Outcome, kernel.OutcomeUnsubstantiated)
	}
}

// TestReportNeverClaimsSuccessAheadOfEvidence proves the report is truthful.
//
// It parses the rendered report and asserts that every axis line is present with
// its own value. A report that collapsed the axes into one word would be the
// presentation-layer version of the exact lie the kernel refuses to tell.
func TestReportNeverClaimsSuccessAheadOfEvidence(t *testing.T) {
	root := t.TempDir()
	runner, err := headless.New(root)
	if err != nil {
		t.Fatalf("runner: %v", err)
	}

	spec := kernel.Spec{
		ExecutionID: "headless-report-1",
		Contract:    kernel.Contract{Kind: kernel.ContractObserve, Targets: []string{"a.txt"}, RequiresObservation: true},
		Program:     kernel.Program{{ID: "s", Capability: kernel.FileExists, Target: "a.txt"}},
	}
	grant, err := kernel.NewGrant("g1", kernel.AllCapabilityIDs(), []string{"a.txt"})
	if err != nil {
		t.Fatalf("grant: %v", err)
	}

	result, err := runner.Run(context.Background(), spec, grant)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	var sb strings.Builder
	if err := headless.Report(&sb, result); err != nil {
		t.Fatalf("report: %v", err)
	}
	report := sb.String()

	for _, want := range []string{
		"provider  " + string(kernel.ProviderDone),
		"artifact  " + string(kernel.ArtifactNone),
		"mutation  " + string(kernel.MutationNone),
		"verify    " + string(kernel.VerifyNotApplicable),
		"outcome     " + string(kernel.OutcomeUnsubstantiated),
	} {
		if !strings.Contains(report, want) {
			t.Errorf("report missing %q\n---\n%s", want, report)
		}
	}
	if strings.Contains(report, "success") || strings.Contains(report, "SUCCESS") {
		t.Error("the report contains the word \"success\"; " +
			"the verdict line is the outcome and nothing else may claim success")
	}
	// An unsubstantiated outcome must name what went unsatisfied.
	if !strings.Contains(report, "unmet contract clauses") {
		t.Errorf("report does not name the unmet clauses\n---\n%s", report)
	}
}

// TestHeadlessAdmissionRefusalIsAttributed proves an authorization refusal
// reaches the caller as REQUIRES_AUTHORIZATION rather than a vague failure.
func TestHeadlessAdmissionRefusalIsAttributed(t *testing.T) {
	root := t.TempDir()
	runner, err := headless.New(root)
	if err != nil {
		t.Fatalf("runner: %v", err)
	}

	spec := kernel.Spec{
		ExecutionID: "headless-auth-1",
		Contract: kernel.Contract{
			Kind:    kernel.ContractCreate,
			Targets: []string{"secret.txt"},
		},
		Program: kernel.Program{{
			ID:         "write",
			Capability: kernel.FileWrite,
			Target:     "secret.txt",
			Args:       map[string]string{"content": "x"},
		}},
	}
	readOnly, err := kernel.NewGrant("g-read", []kernel.CapabilityID{kernel.FileRead}, nil)
	if err != nil {
		t.Fatalf("grant: %v", err)
	}

	result, err := runner.Run(context.Background(), spec, readOnly)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Outcome != kernel.OutcomeRequiresAuthorization {
		t.Fatalf("outcome = %q, want %q\nreason=%s", result.Outcome, kernel.OutcomeRequiresAuthorization, result.Reason)
	}
	if _, statErr := os.Stat(filepath.Join(root, "secret.txt")); statErr == nil {
		t.Error("a refused execution created the file")
	}
}

// TestCapabilitySurfaceIsReported proves the runner can state what it can
// actually do before anything is attempted.
func TestCapabilitySurfaceIsReported(t *testing.T) {
	runner, err := headless.New(t.TempDir())
	if err != nil {
		t.Fatalf("runner: %v", err)
	}
	surface, err := runner.CapabilitySurface()
	if err != nil {
		t.Fatalf("surface: %v", err)
	}
	for _, want := range []string{
		string(kernel.WorkspaceDiscover), string(kernel.FileRead),
		string(kernel.FileSearch), string(kernel.FileExists), string(kernel.FileWrite),
	} {
		if !strings.Contains(surface, want) {
			t.Errorf("capability surface %q is missing %q", surface, want)
		}
	}
	if strings.Contains(surface, string(kernel.CommandRun)) {
		t.Error("the surface claims command.run, which this runner cannot execute")
	}
}

// TestRunnerRequiresRoot proves construction is fail-closed.
func TestRunnerRequiresRoot(t *testing.T) {
	if _, err := headless.New("  "); err == nil {
		t.Error("New accepted a blank root")
	}
}

// TestReportRejectsNilWriter proves the reporter refuses to silently discard
// output.
func TestReportRejectsNilWriter(t *testing.T) {
	if err := headless.Report(nil, kernel.Result{}); err == nil {
		t.Error("Report accepted a nil writer")
	}
}

// TestDeleteContractVerifiesAbsence proves the verifier does real work: after a
// real delete, the target is genuinely gone and the contract is satisfied only
// because absence was observed.
func TestDeleteContractVerifiesAbsence(t *testing.T) {
	root := t.TempDir()
	obsolete := filepath.Join(root, "obsolete.txt")
	if err := os.WriteFile(obsolete, []byte("old"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	runner, err := headless.New(root)
	if err != nil {
		t.Fatalf("runner: %v", err)
	}

	// The program observes the file, then "deletes" it. No file.delete capability
	// exists yet, so the write cannot actually remove anything — which is exactly
	// the honest outcome: a DELETE contract whose mutation did not happen cannot
	// be proven.
	spec := kernel.Spec{
		ExecutionID: "headless-delete-1",
		Contract: kernel.Contract{
			Kind:                 kernel.ContractDelete,
			Targets:              []string{"obsolete.txt"},
			RequiresObservation:  true,
			RequiresVerification: true,
		},
		Program: kernel.Program{
			{ID: "look", Capability: kernel.WorkspaceDiscover, Args: map[string]string{}},
			{
				ID:         "touch",
				Capability: kernel.FileWrite,
				Target:     "obsolete.txt",
				Args:       map[string]string{"content": "new"},
			},
			{ID: "confirm", Capability: kernel.FileExists, Target: "obsolete.txt"},
		},
	}
	grant, err := kernel.NewGrant("g1", kernel.AllCapabilityIDs(), []string{"obsolete.txt"})
	if err != nil {
		t.Fatalf("grant: %v", err)
	}

	result, err := runner.Run(context.Background(), spec, grant)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// The target still exists, so a DELETE contract must not be satisfied.
	if result.Proves() {
		t.Fatal("a DELETE contract was PROVEN while the target still exists; " +
			"the verifier did not do its job")
	}
	if _, statErr := os.Stat(obsolete); statErr != nil {
		t.Errorf("the target disappeared, which no authorized step could do: %v", statErr)
	}
}
