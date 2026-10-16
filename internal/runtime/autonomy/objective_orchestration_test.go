package autonomy

// ── END-TO-END OBJECTIVE ORCHESTRATION REGRESSIONS ──────────────────────────
//
// These tests pin the objective-orchestration contract end to end through the
// REAL production path: Driver → ExecutorAdapter → RuntimeExecutor over a real
// temporary workspace with a scripted provider. They are the deterministic
// counterparts of the mission's regressions A–J and they reproduce, then refuse,
// the previously observed failure:
//
//	project-level redesign → clarify-by-filename → select index.html →
//	negligible HTML diff → verification skipped → objective PROVEN
//
// Assertions read runtime state, the on-disk filesystem, or the runtime's own
// sealed evidence — never model prose and never a mock that reproduces the
// desired output.

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/core/authorization"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/runtime/durable"
)

// ── fixtures ────────────────────────────────────────────────────────────────

// orchestrationWebWorkspace is a realistic static website: a document, its
// stylesheet, its script and a readme. The readme exists so a derivation that
// simply bound every discovered file, or the first three it saw, is caught.
func orchestrationWebWorkspace(t *testing.T) string {
	t.Helper()
	return writeFiles(t, map[string]string{
		"index.html": "<!DOCTYPE html>\n<html>\n<body>\n  <h1>Old Title</h1>\n  <p class=\"tagline\">stale</p>\n</body>\n</html>\n",
		"styles.css": ":root { --fg: #111 }\nbody { margin: 0; color: var(--fg) }\n",
		"script.js":  "console.log('old-behavior');\n",
		"readme.md":  "# Demo\n\nA tiny static site.\n",
	})
}

func writeFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// webArtifacts maps each workspace file to the anchored SEARCH/REPLACE block a
// model reasoning over that file's real bytes would produce. Keying by TARGET
// (through targetOf) means a mis-dispatch fails loudly rather than mutating the
// wrong file.
var webArtifacts = map[string]scriptResponse{
	"index.html": {Response: ai.Response{
		Content: "<<<<<<< SEARCH\n  <h1>Old Title</h1>\n=======\n  <h1>New Title</h1>\n>>>>>>> REPLACE",
		Usage:   ai.ProviderUsage{Known: true, PromptTokens: 400, CompletionTokens: 40, FinishReason: "stop"},
	}, marker: "<h1>Old Title</h1>"},
	"styles.css": {Response: ai.Response{
		Content: "<<<<<<< SEARCH\nbody { margin: 0; color: var(--fg) }\n=======\nbody { margin: 0; color: var(--fg); font-family: system-ui }\n>>>>>>> REPLACE",
		Usage:   ai.ProviderUsage{Known: true, PromptTokens: 300, CompletionTokens: 30, FinishReason: "stop"},
	}, marker: "body { margin: 0; color: var(--fg) }"},
	"script.js": {Response: ai.Response{
		Content: "<<<<<<< SEARCH\nconsole.log('old-behavior');\n=======\nconsole.log('new-behavior');\n>>>>>>> REPLACE",
		Usage:   ai.ProviderUsage{Known: true, PromptTokens: 300, CompletionTokens: 30, FinishReason: "stop"},
	}, marker: "old-behavior"},
}

// targetProvider answers each invocation from the target the runtime put in
// front of the model. A target it does not describe is a test failure.
type targetProvider struct {
	mu        sync.Mutex
	artifacts map[string]scriptResponse
	requests  []ai.Request
	served    map[string]int
	calls     int
}

func (p *targetProvider) Name() string { return "orchestration-target" }

func (p *targetProvider) Execute(_ context.Context, req ai.Request) (*ai.Response, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	p.requests = append(p.requests, req)
	target := orchestraTargetOf(req, p.artifacts)
	if target == "" {
		return nil, fmt.Errorf("invocation #%d carried no target", p.calls)
	}
	artifact, ok := p.artifacts[target]
	if !ok {
		return nil, fmt.Errorf("invocation #%d targeted %q, which the scenario does not describe", p.calls, target)
	}
	p.served[target]++
	resp := artifact.Response
	return &resp, nil
}

// orchestraTargetOf identifies which workspace file the runtime put in front of
// the model. It reads the explicit multi-file context header first, then falls
// back to the file whose own pre-mutation content appears in the prompt. The
// fallback is what makes a retry (whose prompt may omit the header) still
// attributable, and it is keyed by the ORIGINAL content so a mis-dispatch cannot
// silently answer for the wrong file.
func orchestraTargetOf(req ai.Request, artifacts map[string]scriptResponse) string {
	var blob strings.Builder
	for _, msg := range req.Messages {
		blob.WriteString(msg.Content)
		blob.WriteByte('\n')
	}
	text := blob.String()
	// The longest original-content marker is the most specific signal, so it
	// wins: a file's own bytes appearing in the prompt prove the model was shown
	// that file, whatever header the lane happens to render.
	best, bestLen := "", 0
	for target, artifact := range artifacts {
		if artifact.marker != "" && strings.Contains(text, artifact.marker) && len(artifact.marker) > bestLen {
			best, bestLen = target, len(artifact.marker)
		}
	}
	return best
}

func (p *targetProvider) ExecuteStream(context.Context, ai.Request) (io.ReadCloser, error) {
	return nil, fmt.Errorf("stream not supported")
}

// orchestrationExecutor builds a RuntimeExecutor over the provider. When
// customVerifier is true a trivial always-true step is installed (so the
// verification gate RAN); otherwise the language's own contract applies and an
// HTML/CSS/JS target has no steps, so verification is SKIPPED.
func orchestrationExecutor(t *testing.T, root string, provider ai.Provider, customVerifier bool) *execution.RuntimeExecutor {
	t.Helper()
	x := execution.NewRuntimeExecutor(root, config.Default(), provider, nil, "")
	v := execution.NewVerifier(root)
	if customVerifier {
		v.SetCustomSteps([]execution.VerificationStep{{Name: "noop", Command: "true", Optional: false}})
	}
	x.SetVerifier(v)
	x.SetAuthorization(&authorization.MutationAuthorization{
		ID:        authorization.NewAuthorizationID(),
		ExpiresAt: time.Now().Add(time.Hour),
	})
	return x
}

func orchestrationDriver(t *testing.T, root string, provider ai.Provider, customVerifier bool) *Driver {
	t.Helper()
	bus := events.NewBus(events.DefaultBufferSize)
	t.Cleanup(bus.Close)
	x := orchestrationExecutor(t, root, provider, customVerifier)
	adapter := NewExecutorAdapter(root, execution.NewIntentGateway(root), x)
	d := NewDriver(adapter, bus)
	d.SetScope("$prompt")
	return d
}

func (p *targetProvider) targets() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, 0, len(p.served))
	for k := range p.served {
		out = append(out, k)
	}
	return out
}

// ── TEST B — PROJECT-LEVEL REDESIGN ─────────────────────────────────────────

// TestOrchestration_B_ProjectRedesignDerivesMultiFileScope is the regression
// that directly reproduces the previously observed behavior. Before the fix,
// "redesign this project help me" (a project-level objective that declares no
// artifact kind) parked at a clarify boundary offering every workspace file and
// required the human to nominate index.html; selecting it reduced the whole
// project redesign to a single-file mutation.
//
// The corrected runtime must:
//   - NOT park at a filename clarification;
//   - resolve the scope from workspace evidence to the implementation artifacts
//     (index.html, styles.css, script.js) and exclude the readme;
//   - dispatch a bounded invocation per bound target;
//   - hold the approval gate over exactly that scope;
//   - apply every target's change on approval and leave out-of-scope files
//     byte-identical.
func TestOrchestration_B_ProjectRedesignDerivesMultiFileScope(t *testing.T) {
	root := orchestrationWebWorkspace(t)
	p := &targetProvider{artifacts: webArtifacts, served: map[string]int{}}
	d := orchestrationDriver(t, root, p, true)

	term, err := d.Run(context.Background(), "redesign this project help me")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if d.State() != autonomy.RuntimeAwaitingHuman {
		t.Fatalf("state = %s (term=%+v), want awaiting_human", d.State(), term)
	}
	b := d.Boundary()
	if b == nil {
		t.Fatal("no boundary at all")
	}
	// The previously observed defect: a clarify-by-filename boundary.
	if b.Action == autonomy.HumanBoundaryClarify {
		t.Fatalf("project-level objective parked at a filename clarification: %q options=%v", b.Reason, b.Options)
	}
	if b.Action != autonomy.HumanBoundaryApproval || b.PatchID == "" {
		t.Fatalf("boundary = %+v, want an approval gate over a real held candidate", b)
	}

	// The evidence-bound scope is exactly the implementation surface.
	if got := d.scopeResolution.State; got != ScopeResolved {
		t.Fatalf("scope resolution = %s, want RESOLVED", got)
	}
	for _, want := range []string{"index.html", "styles.css", "script.js"} {
		if !contains(d.resolved.Targets, want) {
			t.Errorf("target %q not bound; got %v", want, d.resolved.Targets)
		}
		if !contains(b.Targets, want) {
			t.Errorf("approval gate does not cover %q; covers %v", want, b.Targets)
		}
	}
	if contains(d.resolved.Targets, "readme.md") {
		t.Errorf("derivation dragged a non-implementation file into scope: %v", d.resolved.Targets)
	}
	if contains(b.Targets, "readme.md") {
		t.Errorf("approval gate covers the readme: %v", b.Targets)
	}
	// A real bounded invocation must have run per bound target (not one file).
	if p.calls < 3 {
		t.Fatalf("provider calls = %d, want one per bound target (%v)", p.calls, d.resolved.Targets)
	}

	// Nothing is written before approval.
	before := readTarget(t, root, "index.html")
	if before != "<!DOCTYPE html>\n<html>\n<body>\n  <h1>Old Title</h1>\n  <p class=\"tagline\">stale</p>\n</body>\n</html>\n" {
		t.Fatalf("workspace mutated before approval: %q", before)
	}

	if _, err := d.ResumeApprove(context.Background()); err != nil {
		t.Fatalf("ResumeApprove: %v", err)
	}

	// The model's bytes reached the correct files.
	if html := readTarget(t, root, "index.html"); !strings.Contains(html, "New Title") {
		t.Errorf("index.html was not redesigned: %q", html)
	}
	if css := readTarget(t, root, "styles.css"); !strings.Contains(css, "font-family: system-ui") {
		t.Errorf("styles.css was not redesigned: %q", css)
	}
	if js := readTarget(t, root, "script.js"); !strings.Contains(js, "new-behavior") {
		t.Errorf("script.js was not redesigned: %q", js)
	}
	// Scope discipline: an out-of-scope file is byte-identical.
	if md := readTarget(t, root, "readme.md"); md != "# Demo\n\nA tiny static site.\n" {
		t.Errorf("mutation escaped the bound scope: readme.md = %q", md)
	}
	// The run is NOT restricted to index.html.
	if len(p.served) < 3 {
		t.Fatalf("only %v were dispatched; the redesign was reduced to one file", p.targets())
	}
}

// TestOrchestration_B_ProjectRedesignUnverifiedIsNotProven is the truthful
// completion regression. With no verifier configured for HTML/CSS/JS, a broad
// project-level redesign must NOT be reported PROVEN merely because a patch was
// applied: the mutation lands, but the objective's acceptance criteria remain
// unverified.
func TestOrchestration_B_ProjectRedesignUnverifiedIsNotProven(t *testing.T) {
	root := orchestrationWebWorkspace(t)
	p := &targetProvider{artifacts: webArtifacts, served: map[string]int{}}
	d := orchestrationDriver(t, root, p, false) // no custom verifier → skipped

	if _, err := d.Run(context.Background(), "redesign this project help me"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if d.State() != autonomy.RuntimeAwaitingHuman {
		t.Fatalf("state = %s, want awaiting_human (approval gate)", d.State())
	}
	if _, err := d.ResumeApprove(context.Background()); err != nil {
		t.Fatalf("ResumeApprove: %v", err)
	}

	// Drain any continuation attempts the authority may make; the terminal
	// verdict must not be COMPLETED.
	for i := 0; i < 12 && d.State() == autonomy.RuntimeAwaitingHuman; i++ {
		if b := d.Boundary(); b != nil && b.Action == autonomy.HumanBoundaryApproval && b.PatchID != "" {
			if _, err := d.ResumeApprove(context.Background()); err != nil {
				t.Fatalf("ResumeApprove: %v", err)
			}
			continue
		}
		break
	}

	// The mutation DID land (truthful observation)…
	if html := readTarget(t, root, "index.html"); !strings.Contains(html, "New Title") {
		t.Fatalf("the redesign never reached disk: %q", html)
	}
	// …but the objective is not proven, and the integrity condition records
	// GATE_UNVERIFIED rather than a pass.
	if d.State() == autonomy.RuntimeCompleted {
		t.Fatalf("an unverified broad objective terminated COMPLETED:\n%+v", d.ObjectiveReport())
	}
	conditions := d.ObjectiveConditions()
	var integrity *execution.CompletionCondition
	for i := range conditions {
		if conditions[i].Obligation == execution.ObligationIntegrityHeld {
			integrity = &conditions[i]
		}
	}
	if integrity == nil {
		t.Fatal("no integrity condition was evaluated")
	}
	if integrity.Status == execution.ConditionSatisfied {
		t.Fatalf("integrity held as %s for an unverified broad objective", integrity.VerificationState)
	}
	if integrity.VerificationState != execution.VerifyGateUnverified {
		t.Fatalf("integrity state = %s, want %s", integrity.VerificationState, execution.VerifyGateUnverified)
	}
}

// ── TEST A — EXPLICIT SINGLE-FILE CREATION ──────────────────────────────────

// TestOrchestration_A_ExplicitCreateExactContent pins the short path: an
// objective that names its target and desired content is resolved directly with
// no project-wide clarification, applied under authorization, and the resulting
// file content is verified by re-reading it.
func TestOrchestration_A_ExplicitCreateExactContent(t *testing.T) {
	root := writeFiles(t, map[string]string{"README.md": "# Sample\n"})
	p := &mockProvider{responses: []*ai.Response{{
		Content: "Hello everyone\n",
		Usage:   ai.ProviderUsage{Known: true, PromptTokens: 100, CompletionTokens: 5, FinishReason: "stop"},
	}}}
	d := orchestrationDriver(t, root, p, true)

	term, err := d.Run(context.Background(), `create a new file named zuru.md with content "Hello everyone"`)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if d.State() != autonomy.RuntimeAwaitingHuman {
		t.Fatalf("state = %s (term=%+v), want approval gate", d.State(), term)
	}
	b := d.Boundary()
	if b == nil || b.Action != autonomy.HumanBoundaryApproval {
		t.Fatalf("boundary = %+v, want approval", b)
	}
	if b.CandidateOperation != "" && !strings.EqualFold(b.CandidateOperation, "create") {
		t.Errorf("candidate operation = %q, want create", b.CandidateOperation)
	}
	if _, err := d.ResumeApprove(context.Background()); err != nil {
		t.Fatalf("ResumeApprove: %v", err)
	}
	if got := readTarget(t, root, "zuru.md"); got != "Hello everyone\n" && got != "Hello everyone" {
		t.Fatalf("zuru.md = %q, want the requested content", got)
	}
	// A targeted create must not have parked at a clarify boundary.
	if d.State() == autonomy.RuntimeAwaitingHuman && d.Boundary() != nil && d.Boundary().Action == autonomy.HumanBoundaryClarify {
		t.Fatal("explicit create required clarification")
	}
}

// ── TEST C — EXISTING TARGET CONFLICT ───────────────────────────────────────

// TestOrchestration_C_ExistingTargetNeverSilentlyOverwritten pins the
// create/modification distinction: when an objective asks to CREATE a file that
// already exists with sentinel content, the runtime must not overwrite it
// without the operator's authorization. A refusal preserves the sentinel.
func TestOrchestration_C_ExistingTargetNeverSilentlyOverwritten(t *testing.T) {
	root := writeFiles(t, map[string]string{
		"zuru.md":   "SENTINEL — do not overwrite\n",
		"README.md": "# Sample\n",
	})
	p := &mockProvider{responses: []*ai.Response{{
		Content: "<<<<<<< SEARCH\nSENTINEL — do not overwrite\n=======\nREPLACED BY MODEL\n>>>>>>> REPLACE",
		Usage:   ai.ProviderUsage{Known: true, PromptTokens: 100, CompletionTokens: 20, FinishReason: "stop"},
	}}}
	d := orchestrationDriver(t, root, p, true)

	term, err := d.Run(context.Background(), `create a new file named zuru.md with content "Hello everyone"`)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if d.State() != autonomy.RuntimeAwaitingHuman {
		t.Fatalf("state = %s (term=%+v), want a boundary requiring authorization", d.State(), term)
	}
	b := d.Boundary()
	if b == nil {
		t.Fatal("no boundary")
	}
	// The sentinel must be intact while the decision is pending.
	if got := readTarget(t, root, "zuru.md"); got != "SENTINEL — do not overwrite\n" {
		t.Fatalf("existing content changed before authorization: %q", got)
	}
	// If the boundary is an approval gate, reject it and confirm preservation.
	if b.Action == autonomy.HumanBoundaryApproval {
		if _, err := d.ResumeReject(context.Background(), "existing file must not be overwritten"); err != nil {
			t.Fatalf("ResumeReject: %v", err)
		}
	}
	if got := readTarget(t, root, "zuru.md"); got != "SENTINEL — do not overwrite\n" {
		t.Fatalf("rejection did not preserve the sentinel: %q", got)
	}
}

// ── TEST E — NO UNNECESSARY CLARIFICATION ───────────────────────────────────

// TestOrchestration_E_ClearLowRiskAndDiscoverableProceed pins that neither a
// clear low-risk target NOR a project-level objective whose scope is
// discoverable requires the human to repeat information the runtime already
// has.
func TestOrchestration_E_ClearLowRiskAndDiscoverableProceed(t *testing.T) {
	t.Run("discoverable-project", func(t *testing.T) {
		root := orchestrationWebWorkspace(t)
		p := &targetProvider{artifacts: webArtifacts, served: map[string]int{}}
		d := orchestrationDriver(t, root, p, true)
		if _, err := d.Run(context.Background(), "redesign this project help me"); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if b := d.Boundary(); b != nil && b.Action == autonomy.HumanBoundaryClarify {
			t.Fatalf("needless clarification: %q %v", b.Reason, b.Options)
		}
	})
	t.Run("low-risk-create", func(t *testing.T) {
		root := writeFiles(t, map[string]string{"README.md": "# Sample\n"})
		p := &mockProvider{responses: []*ai.Response{{Content: "note body\n"}}}
		d := orchestrationDriver(t, root, p, true)
		if _, err := d.Run(context.Background(), "create a new file named note.md with the content note body"); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if b := d.Boundary(); b != nil && b.Action == autonomy.HumanBoundaryClarify {
			t.Fatalf("needless clarification for a clear create: %q", b.Reason)
		}
	})
}

// ── TEST J — SURFACES PRESERVE STATIC AUTHORITY ─────────────────────────────

// TestOrchestration_J_HotSurfaceNeverWidensToProjectScope pins the static
// authority boundary: $hot is a strict local target boundary, so a project-level
// objective under $hot must NOT auto-derive the workspace's implementation
// surface. The capability to resolve a deferred project target from evidence is
// a $prompt/$build/$run affordance, not a $hot one.
func TestOrchestration_J_HotSurfaceNeverWidensToProjectScope(t *testing.T) {
	root := orchestrationWebWorkspace(t)
	p := &targetProvider{artifacts: webArtifacts, served: map[string]int{}}
	d := orchestrationDriver(t, root, p, true)
	d.SetScope("$hot")

	if _, err := d.Run(context.Background(), "redesign this project help me"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(d.resolved.Targets) != 0 {
		t.Fatalf("$hot auto-bound a project scope %v; $hot must not widen authority", d.resolved.Targets)
	}
	if p.calls != 0 {
		t.Fatalf("$hot dispatched %d provider call(s) for a targetless project objective", p.calls)
	}
}

// ── DURABLE HUMAN DECISION (mission item 4) ─────────────────────────────────

// TestOrchestration_DurableHumanDecisionRecorded pins that an approval is a
// durable fact of its own, not merely inferable from the mutation. A resumed
// run must be able to read WHAT the human authorized (action + candidate
// identity + content digest), which is what makes a stale approval detectable.
func TestOrchestration_DurableHumanDecisionRecorded(t *testing.T) {
	root, _, a, _ := testHarness(t, []*ai.Response{{Content: sampleReplace}})
	store := openLedger(t, root)
	d := NewDriver(a, nil, WithLedger(store, "session-durable"))

	if _, err := d.Run(context.Background(), "change bar to qux @note.txt"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if d.State() != autonomy.RuntimeAwaitingHuman {
		t.Fatalf("state = %s, want awaiting_human", d.State())
	}
	if _, err := d.ResumeApprove(context.Background()); err != nil {
		t.Fatalf("ResumeApprove: %v", err)
	}

	events := ledgerEvents(t, store.LedgerPath())
	if !hasEvent(events, durable.EventHumanDecision) {
		t.Fatalf("no HUMAN_DECISION recorded: %v", eventTypes(events))
	}
	var payload map[string]any
	for _, ev := range events {
		if ev.EventType == durable.EventHumanDecision {
			payload = ev.Payload
		}
	}
	if payload["action"] != "approve" {
		t.Fatalf("human decision action = %v, want approve", payload["action"])
	}
	if digest, _ := payload["digest"].(string); digest == "" {
		t.Fatalf("approval was not bound to a candidate digest: %+v", payload)
	}
}

// ── TEST H — VERIFICATION SKIPPED / FAILED ──────────────────────────────────

// TestOrchestration_H_SkippedVerifierIsDistinctFromPass pins the completion
// authority's vocabulary directly: a not-applicable verifier on a
// content-determined objective is still not a pass, and a broad objective's
// skipped verifier is UNVERIFIED.
func TestOrchestration_H_SkippedVerifierIsDistinctFromPass(t *testing.T) {
	base := execution.ObjectiveEvidence{
		Provider:             execution.ProviderDone,
		FinishReason:         "stop",
		Artifact:             execution.ArtifactProduced,
		ArtifactsParsed:      1,
		Mutation:             execution.FilesystemApplied,
		MutatedFiles:         1,
		ObservedDeltaTargets: []string{"index.html"},
		TargetExists:         map[string]bool{"index.html": true},
		VerificationSkipped:  true,
	}
	contract := execution.TaskContract{Kind: execution.TaskPatch, Targets: []string{"index.html"}, RequiresVerifier: true}

	// A content-determined patch: a provably not-applicable gate is not a
	// failure, and the objective may hold.
	if got := execution.NewObjectiveCompletionAuthority().Evaluate(contract, base); !got.Proves() {
		t.Fatalf("content-determined skipped verifier = %s, want PROVEN", got)
	}
	// The same evidence on a broad project-level objective is UNVERIFIED.
	broad := base
	broad.BroadObjective = true
	if got := execution.NewObjectiveCompletionAuthority().Evaluate(contract, broad); got.Proves() {
		t.Fatalf("broad objective with skipped verification proved: %s", got)
	}
	conditions := execution.ReduceConditions(
		[]execution.CompletionCondition{{ID: "c", Obligation: execution.ObligationIntegrityHeld}},
		broad)
	if conditions[0].Status != execution.ConditionUnsatisfied ||
		conditions[0].VerificationState != execution.VerifyGateUnverified {
		t.Fatalf("broad skipped verifier condition = %s/%s, want UNSATISFIED/%s",
			conditions[0].Status, conditions[0].VerificationState, execution.VerifyGateUnverified)
	}
	// A gate that RAN and FAILED remains a failure, never a pass.
	failed := base
	failed.VerificationSkipped = false
	failed.VerificationRan = true
	failed.VerificationPassed = false
	if got := execution.NewObjectiveCompletionAuthority().Evaluate(contract, failed); got.Proves() {
		t.Fatal("a failed verifier proved the objective")
	}
}
