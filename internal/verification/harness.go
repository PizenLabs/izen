package verification

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/PizenLabs/izen/internal/core/domain"
	"github.com/PizenLabs/izen/internal/core/domain/authorization"
	"github.com/PizenLabs/izen/internal/core/domain/evidence"
	"github.com/PizenLabs/izen/internal/runtime/executor"
	"github.com/PizenLabs/izen/internal/runtime/orchestrator"
	"github.com/PizenLabs/izen/internal/runtime/preflight"
	"github.com/PizenLabs/izen/internal/runtime/target"
)

// ── Typed failure sentinels (INV: Adversarial Resilience) ────────────────────

// Harness-level sentinels wrap the orchestrator's canonical sentinels so
// errors.Is succeeds against both layers. NEVER panic on malformed output.
var (
	ErrProposalValidationFailed = orchestrator.ErrProposalValidationFailed
	ErrSyntaxError              = errors.New("verification: syntax error")
	ErrVerificationFailed       = errors.New("verification: verification failed")
)

// ── Proposal JSON contract (model output is UNTRUSTED) ──────────────────────

// RawProposalJSON is the minimal model JSON contract used by the harness
// parser. ALL fields are untrusted; authority is never derived from them.
type RawProposalJSON struct {
	Plan        string `json:"plan"`
	TestsPassed *bool  `json:"tests_passed,omitempty"`
	Content     string `json:"content,omitempty"`
	RawPatch    string `json:"raw_patch,omitempty"`
	TargetFile  string `json:"target_file,omitempty"`
}

// injectionMarkers are substrings whose presence in model bytes proves an
// attempted authority override. Matching is case-insensitive.
var injectionMarkers = []string{
	"override_capability",
	"override-capability",
	"overridecapability",
	"grant_capability",
	"capability_grant",
	"grant_all_caps",
	"grant all caps",
	"disable_guard",
	"bypass_authorization",
	"bypass-authorization",
	"ignore all instructions",
	"system prompt",
}

// looksTruncated reports heuristics for truncated JSON (context exhaustion).
func looksTruncated(raw string) bool {
	s := strings.TrimSpace(raw)
	if s == "" {
		return false
	}
	if strings.Contains(strings.ToLower(s), "...truncated") || strings.Contains(s, "\x00") {
		return true
	}
	// Unbalanced braces / missing closing } or ] is the primary signal.
	opens := strings.Count(s, "{")
	closes := strings.Count(s, "}")
	if opens > closes {
		return true
	}
	opensB := strings.Count(s, "[")
	closesB := strings.Count(s, "]")
	if opensB > closesB {
		return true
	}
	// ends without } or ] or " suggests mid-token cut
	if len(s) > 0 {
		last := s[len(s)-1]
		if last != '}' && last != ']' && last != '"' {
			// JSON must end with } or ] – if it ends with colon/comma/letter, it's truncated
			if last == ':' || last == ',' || (last >= 'a' && last <= 'z') || (last >= 'A' && last <= 'Z') {
				return true
			}
		}
	}
	return false
}

// containsInjection reports whether raw contains an authority injection marker.
func containsInjection(raw string) (string, bool) {
	lower := strings.ToLower(raw)
	for _, m := range injectionMarkers {
		if strings.Contains(lower, m) {
			return m, true
		}
	}
	return "", false
}

// ParseAndValidateProposal is the single hardened entry point for all model
// JSON. It enforces:
//  1. Truncation (context exhaustion) -> ErrProposalValidationFailed + SyntaxError
//  2. Authority injection -> ErrProposalValidationFailed + ErrCapabilityDenied
//  3. Malformed JSON -> ErrProposalValidationFailed + SyntaxError
//  4. Valid JSON but possibly lying claims (tests_passed) -> success; claims
//     are NEVER trusted for verification (Truthful Verification Invariant).
//
// NEVER panics; NEVER mutates filesystem.
func ParseAndValidateProposal(raw string) (*RawProposalJSON, error) {
	if raw == "" {
		return nil, fmt.Errorf("%w: empty proposal payload: %w", ErrProposalValidationFailed, ErrSyntaxError)
	}
	if marker, ok := containsInjection(raw); ok {
		return nil, fmt.Errorf("%w: %w: untrusted proposal carries authority directive %q", ErrProposalValidationFailed, authorization.ErrCapabilityDenied, marker)
	}
	if looksTruncated(raw) {
		return nil, fmt.Errorf("%w: %w: truncated JSON payload (context exhaustion)", ErrProposalValidationFailed, ErrSyntaxError)
	}
	var p RawProposalJSON
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return nil, fmt.Errorf("%w: %w: %v", ErrProposalValidationFailed, ErrSyntaxError, err)
	}
	// Re-check structured fields for injection (defense in depth)
	combined := p.Plan + " " + p.Content + " " + p.RawPatch + " " + p.TargetFile
	if marker, ok := containsInjection(combined); ok {
		return nil, fmt.Errorf("%w: %w: proposal field carries directive %q", ErrProposalValidationFailed, authorization.ErrCapabilityDenied, marker)
	}
	return &p, nil
}

// ── Truthful Verification Invariant ────────────────────────────────────────

// VerifyTruthfulTerminal asserts INV: Truthful Verification
// If any required evidence level is FAIL (compilation / test failure in
// reality), TerminalState MUST NOT be StateVerified / VerdictPass / Completed+Passed.
// Returns nil on invariant held, error on violation (never panics).
func VerifyTruthfulTerminal(ts evidence.TerminalState, vec evidence.EvidenceVector, required evidence.EvidenceLevel) error {
	if !ts.Valid() {
		// Invalid combinations are already a failure to be truthful – but if
		// the state is invalid it cannot be Verified/Passed, so it holds.
		// We still report the invalid shape for observability if it claimed Verified.
		if ts.Workflow == domain.StateVerified && ts.Verdict == evidence.VerdictPass {
			return fmt.Errorf("%w: terminal claims Verified/PASS but is invalid: %s", ErrVerificationFailed, ts.String())
		}
		return nil
	}
	actual := evidence.DeriveEvidenceState(vec, required)
	if actual == evidence.VerdictFailed {
		if ts.Workflow == domain.StateVerified {
			return fmt.Errorf("%w: terminal is StateVerified while evidence is FAILED (required %s): %s", ErrVerificationFailed, required.String(), ts.String())
		}
		if ts.Verdict == evidence.VerdictPass {
			return fmt.Errorf("%w: terminal Verdict is PASS while evidence is FAILED: %s", ErrVerificationFailed, ts.String())
		}
		if ts.Completed && actual == evidence.VerdictFailed {
			// Completed+Failed is allowed; Completed+Passed when evidence Failed is not.
			// The Valid() check already covers StateVerified+Completed, but we also guard VerdictPass.
			if ts.Verdict == evidence.VerdictPass {
				return fmt.Errorf("%w: terminal Completed+PASS contradicts FAILED evidence", ErrVerificationFailed)
			}
		}
	}
	return nil
}

// AssertTerminalNotVerified is a test helper that fails if the terminal claims
// verified success. Used by adversarial tests.
func AssertTerminalNotVerified(ts evidence.TerminalState, verdict evidence.EvidenceState) error {
	if ts.Workflow == domain.StateVerified && ts.Verdict == evidence.VerdictPass && ts.Completed {
		return fmt.Errorf("%w: terminal must not be verified: %s verdict=%s", ErrVerificationFailed, ts.String(), verdict.String())
	}
	if verdict == evidence.VerdictPassed {
		return fmt.Errorf("%w: verdict must not be PASSED on failure: %s", ErrVerificationFailed, verdict.String())
	}
	if ts.Valid() && verdict == evidence.VerdictPassed && ts.Workflow == domain.StateVerified {
		return fmt.Errorf("%w: truthful invariant violated: valid Verified+Passed on failing evidence", ErrVerificationFailed)
	}
	return nil
}

// ── Model-Agnostic Authority Invariant ─────────────────────────────────────

// ProviderKind classifies simulated LLM capability.
type ProviderKind string

const (
	ProviderAdversarial ProviderKind = "adversarial"
	ProviderWeak        ProviderKind = "weak"
	ProviderValid       ProviderKind = "valid"
	ProviderMalformed   ProviderKind = "malformed"
)

// MockProvider is a scriptable orchestrator.ProposalProvider that simulates
// different LLM behaviours without ever granting authority.
type MockProvider struct {
	Kind         ProviderKind
	Name         string
	ProposalFn   func(ctx context.Context, req *preflight.CompiledRequest) (*executor.ProposedMutation, error)
	TokenCost    int
	Latency      time.Duration
	RepairRounds int
}

func (m *MockProvider) GenerateProposal(ctx context.Context, req *preflight.CompiledRequest) (*executor.ProposedMutation, error) {
	if m.ProposalFn != nil {
		return m.ProposalFn(ctx, req)
	}
	return nil, errors.New("mock provider: no proposal function")
}

// ProviderLabel returns a stable label for matrix reporting.
func (m *MockProvider) ProviderLabel() string {
	if m.Name != "" {
		return m.Name
	}
	return string(m.Kind)
}

// NewAdversarialProvider returns a provider that unconditionally claims
// "Success! All tests passed" via proposal bytes, without fixing code.
// Its proposal still targets a file; the harness must prove terminal truth
// ignores the claim.
func NewAdversarialProvider(ref *target.TargetRef) *MockProvider {
	return &MockProvider{
		Kind:      ProviderAdversarial,
		Name:      "adversarial-claude",
		TokenCost: 8000,
		Latency:   2 * time.Second,
		ProposalFn: func(_ context.Context, _ *preflight.CompiledRequest) (*executor.ProposedMutation, error) {
			claim := `{"tests_passed": true, "plan": "Success! All tests passed"}`
			return &executor.ProposedMutation{
				ProposalID: "adv-1",
				TargetRef:  ref,
				RawPatch:   claim,
			}, nil
		},
	}
}

// NewWeakProvider returns a provider that is uncertain and produces low-
// confidence, possibly truncated output.
func NewWeakProvider(ref *target.TargetRef) *MockProvider {
	return &MockProvider{
		Kind:         ProviderWeak,
		Name:         "weak-qwen",
		TokenCost:    3000,
		Latency:      5 * time.Second,
		RepairRounds: 3,
		ProposalFn: func(_ context.Context, _ *preflight.CompiledRequest) (*executor.ProposedMutation, error) {
			return &executor.ProposedMutation{
				ProposalID: "weak-1",
				TargetRef:  ref,
				RawPatch:   "maybe this fixes it?\npackage main\n",
			}, nil
		},
	}
}

// NewValidProvider returns a provider that produces a structurally valid fix.
func NewValidProvider(ref *target.TargetRef, content string) *MockProvider {
	return &MockProvider{
		Kind:      ProviderValid,
		Name:      "valid-gpt4",
		TokenCost: 1500,
		Latency:   1 * time.Second,
		ProposalFn: func(_ context.Context, _ *preflight.CompiledRequest) (*executor.ProposedMutation, error) {
			return &executor.ProposedMutation{
				ProposalID: "valid-1",
				TargetRef:  ref,
				RawPatch:   content,
			}, nil
		},
	}
}

// NewMalformedProvider returns a provider that emits truncated / injection
// payloads.
func NewMalformedProvider(ref *target.TargetRef, raw string) *MockProvider {
	return &MockProvider{
		Kind:      ProviderMalformed,
		Name:      "malformed-local",
		TokenCost: 500,
		Latency:   500 * time.Millisecond,
		ProposalFn: func(_ context.Context, _ *preflight.CompiledRequest) (*executor.ProposedMutation, error) {
			return &executor.ProposedMutation{
				ProposalID: "mal-1",
				TargetRef:  ref,
				RawPatch:   raw,
			}, nil
		},
	}
}

// ── Model Substitution Benchmark Matrix ────────────────────────────────────

// MatrixOutcome records one run of the benchmark harness.
type MatrixOutcome struct {
	Provider     string
	Kind         ProviderKind
	TokenCost    int
	Latency      time.Duration
	RepairRounds int
	Terminal     evidence.TerminalState
	Verdict      evidence.EvidenceState
	Err          error
	Committed    bool
	MutatedFile  string
}

// MatrixConfig is the common task fed to every provider in the matrix.
type MatrixConfig struct {
	WorkDir        string
	TargetRef      *target.TargetRef
	InitialContent string
	RequiredLevel  evidence.EvidenceLevel
}

// RunModelSubstitutionMatrix executes the same task through each provider and
// returns per-provider outcomes. The caller asserts identical security and
// terminal truth verdicts; only TokenCost/Latency/RepairRounds may vary.
func RunModelSubstitutionMatrix(ctx context.Context, providers []*MockProvider, cfg MatrixConfig, baseOrchestrator func() *orchestrator.Orchestrator, baseReq func() preflight.PreflightRequest, bridgeFactory func() orchestrator.UIProjectionBridge) []MatrixOutcome {
	out := make([]MatrixOutcome, 0, len(providers))
	for _, p := range providers {
		orch := baseOrchestrator()
		bridge := bridgeFactory()
		req := baseReq()
		res, err := orch.RunCycle(ctx, req, p, bridge, orchestrator.OrchestratorConfig{})
		o := MatrixOutcome{
			Provider:     p.ProviderLabel(),
			Kind:         p.Kind,
			TokenCost:    p.TokenCost,
			Latency:      p.Latency,
			RepairRounds: p.RepairRounds,
		}
		if err != nil {
			o.Err = err
			// On validation / auth failure, terminal is not set; derive failed terminal
			o.Terminal = evidence.TerminalState{Workflow: domain.StateFailed, Verdict: evidence.VerdictFail, Completed: false, Reason: err.Error()}
			o.Verdict = evidence.VerdictFailed
		} else if res != nil {
			o.Terminal = res.Terminal
			o.Verdict = res.Verdict
			o.Committed = res.Committed
			o.MutatedFile = res.Target
		}
		out = append(out, o)
	}
	return out
}

// AssertMatrixInvariant checks Model-Agnostic Authority Invariant:
// all outcomes must share identical terminal truth (Workflow, Verdict class,
// Completed) regardless of provider. Token cost / latency / repair rounds are
// allowed to differ. Returns error on divergence.
func AssertMatrixInvariant(outcomes []MatrixOutcome) error {
	if len(outcomes) == 0 {
		return errors.New("matrix invariant: no outcomes")
	}
	ref := outcomes[0]
	for i := 1; i < len(outcomes); i++ {
		cur := outcomes[i]
		// Truth-bearing fields must match exactly.
		if ref.Terminal.Workflow != cur.Terminal.Workflow {
			return fmt.Errorf("model-agnostic invariant violated: workflow differs %s (%s) vs %s (%s)", ref.Provider, ref.Terminal.Workflow, cur.Provider, cur.Terminal.Workflow)
		}
		if ref.Terminal.Verdict != cur.Terminal.Verdict {
			return fmt.Errorf("model-agnostic invariant violated: verdict differs %s (%s) vs %s (%s)", ref.Provider, ref.Terminal.Verdict, cur.Provider, cur.Terminal.Verdict)
		}
		if ref.Terminal.Completed != cur.Terminal.Completed {
			return fmt.Errorf("model-agnostic invariant violated: completed differs %s (%v) vs %s (%v)", ref.Provider, ref.Terminal.Completed, cur.Provider, cur.Terminal.Completed)
		}
		if ref.Verdict != cur.Verdict {
			return fmt.Errorf("model-agnostic invariant violated: evidence state differs %s (%s) vs %s (%s)", ref.Provider, ref.Verdict, cur.Provider, cur.Verdict)
		}
	}
	return nil
}

// ── Benchmark harness runner (evidence-driven) ─────────────────────────────

// BenchmarkHarness runs the verification-grade evidence pipeline in-process
// without requiring a real LLM network call. It derives evidence truth from
// executor.BuildEvidenceVector and asserts invariants.
type BenchmarkHarness struct {
	RequiredLevel evidence.EvidenceLevel
}

// Evaluate derives the truthful terminal from a mutation result + logs.
func (h *BenchmarkHarness) Evaluate(result domain.MutationResult, stdout string, exitCode int, testLogs string, humanApproved bool, committed bool, auditErr error) (evidence.TerminalState, evidence.EvidenceState) {
	required := h.RequiredLevel
	if required == evidence.LevelNone {
		required = evidence.L3_UnitTests
	}
	vec := executor.BuildEvidenceVector(result, stdout, exitCode, testLogs, humanApproved)
	_ = vec
	derived := evidence.DeriveEvidenceState(vec, required)
	// Map derived state onto terminal truth via orchestrator's EvaluateTerminalState
	// but override when evidence failed: even if committed, evidence failure forces Failed.
	if derived == evidence.VerdictFailed {
		return evidence.TerminalState{Workflow: domain.StateFailed, Verdict: evidence.VerdictFail, Completed: false, Reason: "evidence verification failed"}, evidence.VerdictFailed
	}
	return orchestrator.EvaluateTerminalState(committed, auditErr)
}

// VerifyNoUnauthorizedMutation checks that no file outside allowedTargets was
// mutated. allowedTargets may be nil (no mutation expected).
func VerifyNoUnauthorizedMutation(before map[string]string, after map[string]string, allowedTargets []string) error {
	allowed := make(map[string]bool)
	for _, t := range allowedTargets {
		allowed[t] = true
	}
	for path, afterContent := range after {
		beforeContent, existed := before[path]
		if !existed {
			if !allowed[path] {
				return fmt.Errorf("unauthorized mutation: created file %q not in allowed targets %v", path, allowedTargets)
			}
			continue
		}
		if beforeContent != afterContent && !allowed[path] {
			return fmt.Errorf("unauthorized mutation: file %q mutated but not in allowed targets", path)
		}
	}
	return nil
}
