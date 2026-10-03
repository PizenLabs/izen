package execution

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/core/authorization"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/llmstep"
)

// ── THE ILLEGAL TRANSITIONS ────────────────────────────────────────────────
//
// One absolute invariant governs this file (spec §14):
//
//	FAILED / EXHAUSTED / INTERRUPTED / SUPERSEDED computation
//	MUST NOT produce an executable MutationCandidate
//
// and the authorization corollary (§7 / §13):
//
//	classifier says mutation, policy says, authorization still required
//	→ no grant → no mutation
//
// Each case below drives the REAL RuntimeExecutor and then asserts BOTH halves:
// no candidate was produced, and the workspace is byte-identical. Asserting only
// the first would let a candidate that was held but not applied pass; asserting
// only the second would let a held candidate pass and be applied later.

// illegalExecutor wires an executor with a real authorization but a controllable
// provider, so the only variable is what the computation produced.
func illegalExecutor(t *testing.T, root string, p ai.Provider) *RuntimeExecutor {
	t.Helper()
	x := NewRuntimeExecutor(root, config.Default(), p, events.NewBus(events.DefaultBufferSize), "")
	x.SetVerifier(trivialVerifier(root))
	x.SetAuthorization(&authorization.MutationAuthorization{
		ID:        authorization.NewAuthorizationID(),
		ExpiresAt: time.Now().Add(time.Hour),
	})
	return x
}

// illegalCase is one illegal transition: a computation condition plus the
// response the provider gives for it.
type illegalCase struct {
	name      string
	condition string
	response  *ai.Response
	err       error
}

func illegalCases() []illegalCase {
	return []illegalCase{
		{
			name:      "OUTPUT_EXHAUSTED — finish_reason=length with delivered bytes",
			condition: "output exhausted",
			// Partial but well-formed-looking bytes: the exact shape that could be
			// mistaken for a usable artifact.
			response: &ai.Response{
				Content: "<<<<<<< SEARCH\n<html><body><p>truncated mid docum",
				Usage: ai.ProviderUsage{
					Known: true, PromptTokens: 40, CompletionTokens: 1024, FinishReason: "length",
				},
			},
		},
		{
			name:      "OUTPUT_EXHAUSTED — repeated exhaustion consumes the request budget",
			condition: "output exhausted repeatedly",
			// Every bounded step is cut off at the ceiling, so the request budget is
			// consumed with no completed artifact.
			response: &ai.Response{
				Content: "<<<<<<< SEARCH\n<html><body><p>one</p><p>tw",
				Usage: ai.ProviderUsage{
					Known: true, PromptTokens: 40, CompletionTokens: 1024, FinishReason: "length",
				},
			},
		},
		{
			name:      "PROVIDER_REFUSAL — the provider declined to generate",
			condition: "provider refused",
			response: &ai.Response{
				Content: "",
				Usage: ai.ProviderUsage{
					Known: true, PromptTokens: 40, CompletionTokens: 0, FinishReason: "content_filter",
				},
			},
		},
		{
			name:      "TRANSPORT_ERROR — the invocation never returned",
			condition: "transport failure",
			err:       errors.New("dial tcp: connection reset by peer"),
		},
		{
			name:      "ARTIFACT_REJECTED — a hallucinated anchor matches nothing",
			condition: "zero-match anchor",
			// A syntactically valid envelope whose SEARCH text is absent from the
			// target: the shape a hallucinating model produces most often.
			response: &ai.Response{
				Content: "<<<<<<< SEARCH\nthis text does not exist in the file at all\n=======\nreplacement\n>>>>>>>",
				Usage: ai.ProviderUsage{
					Known: true, PromptTokens: 40, CompletionTokens: 24, FinishReason: "stop",
				},
			},
		},
	}
}

func TestIllegalTransitionsProduceNoMutationCandidate(t *testing.T) {
	for _, tc := range illegalCases() {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			const original = "<html><body><p>one</p><p>two</p></body></html>\n"
			seedIllegalTarget(t, root, "index.html", original)

			var provider ai.Provider
			if tc.err != nil {
				provider = &illegalFailingProvider{err: tc.err}
			} else {
				provider = &mockProvider{responses: repeatIllegalResponse(tc.response, 16)}
			}
			counter, _ := provider.(interface{ calls() int })
			x := illegalExecutor(t, root, provider)
			before := 0
			if counter != nil {
				before = counter.calls()
			}

			res, err := x.Execute(context.Background(), ExecuteRequest{
				Mode:   "build",
				Prompt: "shrink index.html",
				Target: "index.html",
			})

			// ── No candidate ───────────────────────────────────────────────
			// A pending patch ID is the ONLY handle the approval boundary accepts,
			// so its absence is the structural guarantee.
			if res != nil && res.PendingPatchID != "" {
				t.Fatalf("%s produced an executable candidate %q", tc.condition, res.PendingPatchID)
			}
			for _, id := range x.PendingPatchIDs() {
				t.Fatalf("%s left candidate %q held at the approval boundary", tc.condition, id)
			}
			// Approval of any candidate the run might have named must fail.
			if res != nil && res.Proof != nil {
				if res.Proof.Outcome == OutcomePendingApproval {
					t.Fatalf("%s sealed a pending_approval outcome", tc.condition)
				}
			}

			// ── No mutation ────────────────────────────────────────────────
			got := readIllegalTarget(t, root, "index.html")
			if got != original {
				t.Fatalf("%s mutated the workspace:\n%s", tc.condition, got)
			}

			// ── And the attempt stayed bounded ─────────────────────────────
			// One ATTEMPT is legitimately several INVOCATIONS: a full-artifact
			// generation continues across the shared bounded-step budget, because
			// finish_reason=length is an invocation outcome, not a task failure
			// (spec §15). What must never happen is an UNBOUNDED spend, so the
			// count is asserted against the shared continuation ceiling rather
			// than against 1.
			if counter != nil {
				maxInvocations := 1 + llmstep.DefaultMaxContinuationSteps
				if got := counter.calls() - before; got > maxInvocations {
					t.Fatalf("%s spent %d provider invocation(s) in ONE attempt; the bounded-step ceiling is %d",
						tc.condition, got, maxInvocations)
				}
			}
			_ = err // the error value itself is the executor's business, not the contract's
		})
	}
}

// INTERRUPTED is its own case: the invocation is cancelled mid-flight, which is
// neither a completed generation nor a clean failure. The runtime must not turn
// a cancelled stream into a candidate.
func TestInterruptedComputationProducesNoMutationCandidate(t *testing.T) {
	root := t.TempDir()
	const original = "<html><body><p>one</p><p>two</p></body></html>\n"
	seedIllegalTarget(t, root, "index.html", original)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the operation is interrupted before it can complete

	mock := &mockProvider{responses: []*ai.Response{{
		Content: "<<<<<<< SEARCH\n" + original + "=======\n<html></html>\n>>>>>>>",
		Usage:   ai.ProviderUsage{Known: true, PromptTokens: 40, CompletionTokens: 24, FinishReason: "stop"},
	}}}
	x := illegalExecutor(t, root, mock)

	res, _ := x.Execute(ctx, ExecuteRequest{
		Mode:   "build",
		Prompt: "shrink index.html",
		Target: "index.html",
	})

	if res != nil && res.PendingPatchID != "" {
		t.Fatalf("an interrupted computation produced an executable candidate %q", res.PendingPatchID)
	}
	if ids := x.PendingPatchIDs(); len(ids) != 0 {
		t.Fatalf("an interrupted computation left candidates held: %v", ids)
	}
	if got := readIllegalTarget(t, root, "index.html"); got != original {
		t.Fatalf("an interrupted computation mutated the workspace:\n%s", got)
	}
}

// ── AUTHORIZATION ──────────────────────────────────────────────────────────
//
// Intent is not authorization (§7). A request that DECLARES a mutation strategy
// and names an explicit target is still refused without a grant, and the refusal
// happens before the provider is ever called — so a mis-labelled classifier
// verdict cannot manufacture a mutation out of nothing.
func TestNoAuthorizationMeansNoMutationEvenForAMutationShapedRequest(t *testing.T) {
	root := t.TempDir()
	const original = "<html><body><p>one</p><p>two</p></body></html>\n"
	seedIllegalTarget(t, root, "index.html", original)

	mock := &mockProvider{responses: []*ai.Response{{
		Content: "<<<<<<< SEARCH\n" + original + "=======\n<html></html>\n>>>>>>>",
		Usage:   ai.ProviderUsage{Known: true, PromptTokens: 40, CompletionTokens: 24, FinishReason: "stop"},
	}}}
	// NO SetAuthorization: the request carries full mutation intent and an
	// explicit target, and the runtime still refuses.
	x := NewRuntimeExecutor(root, config.Default(), mock, events.NewBus(events.DefaultBufferSize), "")
	x.SetVerifier(trivialVerifier(root))

	res, err := x.Execute(context.Background(), ExecuteRequest{
		Mode:   "build",
		Prompt: "shrink index.html",
		Target: "index.html",
	})
	if err != nil {
		t.Fatalf("the computation itself must still run: %v", err)
	}
	if res.PendingPatchID == "" {
		t.Fatal("precondition: the authorized computation must hold a candidate")
	}
	// The candidate exists; AUTHORIZING it must not. Authorization is evaluated
	// at the apply gate, which is where "may this operation occur?" is decided.
	if _, err := x.Approve(context.Background(), res.PendingPatchID); err == nil {
		t.Fatal("Approve succeeded without any authorization")
	}
	if ids := x.PendingPatchIDs(); len(ids) != 0 {
		t.Fatalf("the refused approval left the candidate held: %v", ids)
	}
	if got := readIllegalTarget(t, root, "index.html"); got != original {
		t.Fatalf("an unauthorized approval mutated the workspace:\n%s", got)
	}
}

// An EXPIRED grant is not a grant. Authority is time-bounded, and the boundary
// must honour the bound rather than the fact that a grant once existed.
func TestExpiredAuthorizationMeansNoMutation(t *testing.T) {
	root := t.TempDir()
	const original = "<html><body><p>one</p><p>two</p></body></html>\n"
	seedIllegalTarget(t, root, "index.html", original)

	mock := &mockProvider{responses: []*ai.Response{{
		Content: "<<<<<<< SEARCH\n" + original + "=======\n<html></html>\n>>>>>>>",
		Usage:   ai.ProviderUsage{Known: true, PromptTokens: 40, CompletionTokens: 24, FinishReason: "stop"},
	}}}
	x := NewRuntimeExecutor(root, config.Default(), mock, events.NewBus(events.DefaultBufferSize), "")
	x.SetVerifier(trivialVerifier(root))
	x.SetAuthorization(&authorization.MutationAuthorization{
		ID:        authorization.NewAuthorizationID(),
		ExpiresAt: time.Now().Add(-time.Minute),
	})

	res, err := x.Execute(context.Background(), ExecuteRequest{
		Mode:   "build",
		Prompt: "shrink index.html",
		Target: "index.html",
	})
	if err != nil {
		t.Fatalf("the computation itself must still run: %v", err)
	}
	if res.PendingPatchID == "" {
		t.Fatal("precondition: the computation must hold a candidate")
	}
	if _, err := x.Approve(context.Background(), res.PendingPatchID); err == nil {
		t.Fatal("Approve accepted an EXPIRED grant — authority is time-bounded")
	}
	if got := readIllegalTarget(t, root, "index.html"); got != original {
		t.Fatalf("an expired grant mutated the workspace:\n%s", got)
	}
}

// ── INVALID TARGET ─────────────────────────────────────────────────────────
//
// §9: "If IZEN cannot establish what it is acting on, it must not act." A
// target that cannot be bound by evidence is not a destination, so no request
// naming one may reach the mutation boundary.
func TestUnboundTargetMeansNoMutation(t *testing.T) {
	// A stated file that does not exist is NOT in this table: naming a
	// non-existent file is a legitimate CREATION target. What is unbindable is a
	// target the runtime cannot establish from evidence at all.
	cases := []struct {
		name   string
		prompt string
		target string
	}{
		{name: "a directory is not a mutation target", prompt: "fix @subdir", target: "subdir"},
		{name: "a bare language word is not a target", prompt: "improve the HTML", target: ""},
		{name: "a path that escapes the workspace is not a target", prompt: "fix @../outside.txt", target: "../outside.txt"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.MkdirAll(filepath.Join(root, "subdir"), 0o755); err != nil {
				t.Fatal(err)
			}
			seedIllegalTarget(t, root, "index.html", "<html></html>\n")

			mock := &mockProvider{responses: []*ai.Response{{
				Content: "irrelevant",
				Usage:   ai.ProviderUsage{Known: true, PromptTokens: 10, CompletionTokens: 4, FinishReason: "stop"},
			}}}
			x := illegalExecutor(t, root, mock)

			res, _ := x.Execute(context.Background(), ExecuteRequest{
				Mode:   "build",
				Prompt: tc.prompt,
				Target: tc.target,
			})
			if res != nil && res.PendingPatchID != "" {
				t.Fatalf("an unbound target produced a candidate %q", res.PendingPatchID)
			}
			if ids := x.PendingPatchIDs(); len(ids) != 0 {
				t.Fatalf("an unbound target left candidates held: %v", ids)
			}
			entries, _ := os.ReadDir(root)
			for _, e := range entries {
				if e.Name() == "index.html" {
					got := readIllegalTarget(t, root, "index.html")
					if got != "<html></html>\n" {
						t.Fatalf("an unbound target mutated the workspace:\n%s", got)
					}
				}
			}
		})
	}
}

// ── helpers ────────────────────────────────────────────────────────────────

func seedIllegalTarget(t *testing.T, root, rel, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, rel), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readIllegalTarget(t *testing.T, root, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(data)
}

// illegalFailingProvider fails every invocation at the transport layer, which is
// the condition under test for the TRANSPORT_ERROR case.
type illegalFailingProvider struct {
	err  error
	seen int
}

func (p *illegalFailingProvider) Name() string { return "failing" }

func (p *illegalFailingProvider) calls() int { return p.seen }

func (p *illegalFailingProvider) Execute(context.Context, ai.Request) (*ai.Response, error) {
	p.seen++
	return nil, p.err
}

func (p *illegalFailingProvider) ExecuteStream(context.Context, ai.Request) (io.ReadCloser, error) {
	p.seen++
	return nil, p.err
}

func outcomeOf(res *ExecutionResult) string {
	if res == nil || res.Proof == nil {
		return "<nil result>"
	}
	return string(res.Proof.Outcome)
}

// The outcome vocabulary must never claim a mutation for a failed computation.
// This is the cheap, total version of the assertion above and it fails loudly if
// a new outcome is introduced that means "an artifact exists".
func TestParseMutationOutcomeNeverMapsAnUnknownStatusToSuccess(t *testing.T) {
	for _, s := range []string{
		"", "  ", "done-ish", "success", "ok", "committed!", "changed-ish", "mutated",
	} {
		got := ParseMutationOutcome(s)
		if got.MutationSucceeded() {
			t.Errorf("ParseMutationOutcome(%q) = %q, which claims a mutation happened", s, got)
		}
		if got == OutcomeNoChange {
			t.Errorf("ParseMutationOutcome(%q) = nochange, which claims an artifact was compared and matched", s)
		}
	}
}

// ParseMutationOutcome must keep recognising every canonical outcome the runtime
// can seal, so a projection can never lose a real verdict to the unknown default.
func TestParseMutationOutcomeRecognisesEveryCanonicalOutcome(t *testing.T) {
	canonical := []MutationOutcome{
		OutcomeChanged, OutcomeCreated, OutcomeNoChange, OutcomeNoArtifact,
		OutcomePatchGenerationFailed, OutcomeArtifactRejected,
		OutcomeArtifactRetryableRejected, OutcomeTruncated, OutcomeApplyFailed,
		OutcomeVerifyFailed, OutcomeOCCAborted, OutcomeSkipped, OutcomeCancelled,
		OutcomePendingApproval, OutcomeRejected, OutcomeFailed,
		OutcomePreflightInfeasible, OutcomeCompleted,
		OutcomeNoOpObjectiveSatisfied, OutcomeNoOpNoSafeMutation, OutcomeNoOpObjectiveUnresolved,
	}
	for _, want := range canonical {
		if got := ParseMutationOutcome(string(want)); got != want {
			t.Errorf("ParseMutationOutcome(%q) = %q, want the same outcome", want, got)
		}
	}
}

// The exact chain the specification spells out, asserted end to end:
//
//	OUTPUT_EXHAUSTED → FAILED COMPUTATION → NO ARTIFACT PROPOSAL
//	→ NO MUTATION CANDIDATE → NO MUTATION APPROVAL → NO MUTATION
func TestOutputExhaustedChainHasNoArtifactAtAnyStage(t *testing.T) {
	root := t.TempDir()
	const original = "<html><body><p>one</p><p>two</p></body></html>\n"
	seedIllegalTarget(t, root, "index.html", original)

	bus := events.NewBus(events.DefaultBufferSize)
	collector := newPhase4Collector(bus)

	mock := &mockProvider{responses: repeatIllegalResponse(&ai.Response{
		Content: "<<<<<<< SEARCH\n<html><body><p>truncated",
		Usage:   ai.ProviderUsage{Known: true, PromptTokens: 40, CompletionTokens: 1024, FinishReason: "length"},
	}, 8)}
	x := illegalExecutor(t, root, mock)

	res, err := x.Execute(context.Background(), ExecuteRequest{
		Mode:   "build",
		Prompt: "shrink index.html",
		Target: "index.html",
	})
	if err == nil {
		t.Fatalf("the exhausted computation reported success: %s", outcomeOf(res))
	}

	// No artifact was proposed: no candidate record claims a committed artifact.
	if res != nil {
		for _, c := range res.ArtifactCandidates {
			if c.Committed {
				t.Fatalf("an exhausted computation reported a committed candidate: %+v", c)
			}
			if c.Status == CandidateComplete {
				t.Fatalf("an exhausted computation reported a complete candidate: %+v", c)
			}
		}
	}
	// No mutation approval: no approval surface was ever opened.
	waitForEvents(collector, 4)
	for _, typ := range collector.types() {
		if typ == events.EventApprovalRequired {
			t.Fatal("an exhausted computation opened an approval surface")
		}
		if typ == events.EventMutationStarted {
			t.Fatal("an exhausted computation entered the mutation boundary")
		}
	}
	// No mutation.
	if got := readIllegalTarget(t, root, "index.html"); got != original {
		t.Fatalf("an exhausted computation mutated the workspace:\n%s", got)
	}
	// And the diagnostic is truthful about WHY.
	if res != nil && res.Err != nil && !strings.Contains(strings.ToLower(res.Err.Error()), "length") &&
		!strings.Contains(strings.ToLower(res.Err.Error()), "exhaust") &&
		!strings.Contains(strings.ToLower(res.Err.Error()), "truncat") {
		t.Errorf("the exhaustion diagnostic does not name the boundary condition: %v", res.Err)
	}
}

// waitForEvents gives the bus's asynchronous dispatch goroutine a bounded window
// to deliver, so an assertion about ABSENT events is not a race against delivery.
func waitForEvents(c *phase4Collector, n int) {
	deadline := time.Now().Add(400 * time.Millisecond)
	for time.Now().Before(deadline) {
		if len(c.types()) >= n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// repeatIllegalResponse returns n independent copies of one provider response.
// The recovery matrix may legitimately re-dispatch after a failed computation;
// the invariant under test is that NONE of those attempts yields a candidate,
// so the provider must be able to answer each of them.
func repeatIllegalResponse(r *ai.Response, n int) []*ai.Response {
	out := make([]*ai.Response, 0, n)
	for i := 0; i < n; i++ {
		clone := *r
		out = append(out, &clone)
	}
	return out
}
