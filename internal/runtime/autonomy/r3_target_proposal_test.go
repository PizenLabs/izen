package autonomy

// ── R3: DISCOVERY → TARGET PROPOSAL → AUTHORITY DECISION ────────────────────
//
// R3 asks whether IZEN can narrow an objective using runtime discovery while
// preserving the separation:
//
//	DISCOVERED   index.html exists            (evidence)
//	PROPOSED     index.html is the sole candidate  (a question, not authority)
//	AUTHORIZED   runtime may inspect/mutate index.html (the gateway accepted)
//
// The invariant under test is the one the whole experiment turns on:
//
//	DISCOVERY EVIDENCE IS NOT EXECUTION AUTHORITY (I13).
//
// A unique candidate can justify a TARGET PROPOSAL. It cannot, by itself,
// justify TARGET AUTHORIZATION. The authority decision stays with the admission
// gate; a proposal that the objective cannot ground in DECLARED evidence reaches
// an explicit, authoritative clarification — never an implicit
// `scan → one file → mutate`.
//
// These tests run the REAL Driver → ExecutorAdapter → RuntimeExecutor path over
// a REAL directory with a scripted provider. Every assertion reads runtime state,
// the on-disk workspace, or a canonical control-plane event — never model prose.

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	coreautonomy "github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
)

const r3Objective = "fix the incorrect greeting in this project"

// r3Files is the single-defect artifact every R3 scenario starts from: the
// greeting is misspelled and the objective names no file.
const r3Index = "<!DOCTYPE html>\n<html><body>\n<h1 id=\"greeting\">Helo</h1>\n</body></html>\n"

// r3Workspace writes the given files into a fresh directory.
func r3Workspace(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// r3FixProvider answers the ONE mutation dispatch the explicit-target arm makes.
// It records its call count so the discovery-only arms can prove zero dispatch.
type r3FixProvider struct {
	mu    sync.Mutex
	calls int
}

func (p *r3FixProvider) Name() string { return "r3-fix" }

func (p *r3FixProvider) Execute(_ context.Context, req ai.Request) (*ai.Response, error) {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	return &ai.Response{
		Content: "<<<<<<< SEARCH\nHelo\n=======\nHello\n>>>>>>> REPLACE",
		Usage:   ai.ProviderUsage{Known: true, PromptTokens: 120, CompletionTokens: 16, FinishReason: "stop"},
	}, nil
}

func (p *r3FixProvider) ExecuteStream(context.Context, ai.Request) (io.ReadCloser, error) {
	return nil, fmt.Errorf("stream not supported in r3FixProvider")
}

func (p *r3FixProvider) Calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// r3EventLog captures the canonical control-plane stream so the assertions can
// read the AUTHORITATIVE record rather than infer it from absence of errors.
type r3EventLog struct {
	mu  sync.Mutex
	all []events.DomainEvent
}

func (l *r3EventLog) Handle(ev events.DomainEvent) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.all = append(l.all, ev)
}

func (l *r3EventLog) snapshot() []events.DomainEvent {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]events.DomainEvent(nil), l.all...)
}

// authorization returns the last authorization record, which is the gate's own
// verdict, or nil if the gate was never reached.
func (l *r3EventLog) authorization() (events.ExecutionAuthorizedPayload, bool) {
	var out events.ExecutionAuthorizedPayload
	found := false
	for _, ev := range l.snapshot() {
		if p, ok := ev.Payload().(events.ExecutionAuthorizedPayload); ok {
			out = p
			found = true
		}
	}
	return out, found
}

func (l *r3EventLog) spec() (events.ExecutionSpecFrozenPayload, bool) {
	var out events.ExecutionSpecFrozenPayload
	found := false
	for _, ev := range l.snapshot() {
		if p, ok := ev.Payload().(events.ExecutionSpecFrozenPayload); ok {
			out = p
			found = true
		}
	}
	return out, found
}

func (l *r3EventLog) sawMutationCompleted() bool {
	for _, ev := range l.snapshot() {
		if _, ok := ev.Payload().(events.MutationCompletedPayload); ok {
			return true
		}
	}
	return false
}

// r3Run is one observed run's authoritative facts.
type r3Run struct {
	Root     string
	Driver   *Driver
	Provider *r3FixProvider
	Events   *r3EventLog
	Before   map[string]string
}

func r3Snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		out[filepath.ToSlash(rel)] = string(body)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func r3Diff(before, after map[string]string) []string {
	var changed []string
	for name, prior := range before {
		if now, ok := after[name]; !ok || now != prior {
			changed = append(changed, name)
		}
	}
	for name := range after {
		if _, ok := before[name]; !ok {
			changed = append(changed, name)
		}
	}
	sort.Strings(changed)
	return changed
}

// r3Observe runs one objective over a fresh workspace and captures the full
// authoritative chain.
func r3Observe(t *testing.T, files map[string]string, objective string) *r3Run {
	t.Helper()
	root := r3Workspace(t, files)
	before := r3Snapshot(t, root)

	bus := events.NewBus(events.DefaultBufferSize)
	log := &r3EventLog{}
	bus.SubscribeAll(log.Handle)

	provider := &r3FixProvider{}
	x := testExecutor(t, root, provider, bus)
	adapter := NewExecutorAdapter(root, execution.NewIntentGateway(root), x)
	d := NewDriver(adapter, bus)

	if _, err := d.Run(context.Background(), objective); err != nil {
		t.Fatalf("driver.Run(%q): %v", objective, err)
	}
	return &r3Run{
		Root:     root,
		Driver:   d,
		Provider: provider,
		Events:   log,
		Before:   before,
	}
}

// r3Log prints the whole chain for one run so the evidence is READ rather than
// inferred. It is called by the proof test only.
func r3Log(t *testing.T, label string, r *r3Run, objective string) {
	t.Helper()
	d := r.Driver
	log := func(k string, v any) { t.Logf("  %-24s %v", k, v) }
	t.Logf("===== %s =====", label)
	t.Logf("objective: %q", objective)
	log("classification", coreautonomy.Classify(objective, nil).Intent)
	log("derivation status", d.scopeDerivation.StatusOrUnresolved())
	log("derivation kinds", d.scopeDerivation.Kinds)
	log("derivation candidates", d.scopeDerivation.Candidates)
	log("scope state", d.scopeResolution.State)
	log("scope targets (proposed)", d.scopeResolution.Targets)
	log("scope candidates", d.scopeResolution.Candidates)
	log("scope authorizes mutation", d.scopeResolution.AuthorizesMutation())
	log("bound targets", d.resolved.Targets)
	op := EvaluatePreflightAdmission(d.preflightExecutionSpec(context.Background()))
	log("authority verdict", op.Verdict)
	log("authority candidates", op.Candidates)
	log("authority reason", op.Reason)
	log("provider calls", r.Provider.Calls())
	log("state", d.State())
	if b := d.Boundary(); b != nil {
		log("boundary action", b.Action)
		log("boundary options", b.Options)
		log("boundary targets", b.Targets)
		log("boundary patch", b.PatchID)
	} else {
		log("boundary", "<nil>")
	}
	if a, ok := r.Events.authorization(); ok {
		log("event verdict", a.Verdict)
		log("event scope", a.Scope)
		log("event candidates", a.Candidates)
		log("event proposed", a.ProposedTargets)
	}
	log("filesystem delta", r3Diff(r.Before, r3Snapshot(t, r.Root)))
	t.Log("")
}

// TestR3_Proof records the three R3 chains for inspection. It asserts only the
// shape that all three share: nothing unauthorized was dispatched.
func TestR3_Proof(t *testing.T) {
	a := r3Observe(t, map[string]string{"index.html": r3Index}, r3Objective)
	r3Log(t, "R3-A unique candidate, no declared kind", a, r3Objective)

	b := r3Observe(t, map[string]string{
		"index.html":   r3Index,
		"landing.html": r3Index,
		"about.html":   r3Index,
	}, r3Objective)
	r3Log(t, "R3-B ambiguous candidates, no declared kind", b, r3Objective)

	c := r3Observe(t, map[string]string{"index.html": r3Index}, "fix the greeting in index.html")
	r3Log(t, "R3-C explicit target", c, "fix the greeting in index.html")

	for _, run := range []*r3Run{a, b} {
		if run.Provider.Calls() != 0 {
			t.Fatalf("a discovery-only objective dispatched %d provider call(s)", run.Provider.Calls())
		}
	}
}

// TestR3_UniqueCandidateIsProposedNotAuthorized is R3-A. A workspace with one
// file and an objective that declares no artifact kind:
//
//   - DISCOVERED: the candidate is observed and recorded as evidence;
//   - PROPOSED: the sole candidate is proposed, and the proposal binds NOTHING;
//   - AUTHORITY: the admission gate refuses to authorize it without
//     objective-declared evidence and parks for an explicit clarification.
func TestR3_UniqueCandidateIsProposedNotAuthorized(t *testing.T) {
	r := r3Observe(t, map[string]string{"index.html": r3Index}, r3Objective)
	d := r.Driver

	if got := coreautonomy.Classify(r3Objective, nil); !got.RequiresMutation() {
		t.Fatalf("classification = %s, want a mutating intent", got.Intent)
	}

	// DISCOVERED: the candidate was observed, with no target derived.
	if got := d.scopeDerivation.StatusOrUnresolved(); got != execution.DerivationUnresolved {
		t.Fatalf("derivation = %s, want UNRESOLVED (the objective declares no artifact kind)", got)
	}
	if len(d.scopeDerivation.Kinds) != 0 {
		t.Fatalf("derivation declared kinds %v; the objective names none", d.scopeDerivation.Kinds)
	}
	if !contains(d.scopeDerivation.Candidates, "index.html") {
		t.Fatalf("derivation candidates = %v, want the observed index.html", d.scopeDerivation.Candidates)
	}

	// PROPOSED: a unique candidate is proposed — and the proposal is NOT a scope.
	if d.scopeResolution.State != ScopeProposed {
		t.Fatalf("scope state = %s, want PROPOSED", d.scopeResolution.State)
	}
	if !contains(d.scopeResolution.Targets, "index.html") {
		t.Fatalf("proposal = %v, want index.html", d.scopeResolution.Targets)
	}
	if d.scopeResolution.AuthorizesMutation() {
		t.Fatal("a PROPOSED scope claimed mutation authority; proposal must not be authorization")
	}
	// The proposal never binds: the authoritative target set stays empty.
	if len(d.resolved.Targets) != 0 {
		t.Fatalf("the proposal bound %v as an execution target", d.resolved.Targets)
	}

	// AUTHORITY: the admission gate refuses to admit and asks explicitly.
	op := EvaluatePreflightAdmission(d.preflightExecutionSpec(context.Background()))
	if op.Verdict != AdmissionDisambiguate {
		t.Fatalf("authority verdict = %s, want DISAMBIGUATE (reason=%q)", op.Verdict, op.Reason)
	}
	if !contains(op.Candidates, "index.html") {
		t.Fatalf("disambiguation candidates = %v, want the discovered index.html", op.Candidates)
	}

	// The park is a clarification that offers the candidate, not an empty fallback.
	b := d.Boundary()
	if b == nil || b.Action != coreautonomy.HumanBoundaryClarify {
		t.Fatalf("boundary = %+v, want a clarification", b)
	}
	if !contains(b.Options, "index.html") {
		t.Fatalf("clarification options = %v, want index.html", b.Options)
	}
	if d.State() != coreautonomy.RuntimeAwaitingHuman {
		t.Fatalf("state = %s, want awaiting_human", d.State())
	}

	// The refusal is published as explicitly as a grant would be: a run that
	// records DISCOVERED+PROPOSED and a DISAMBIGUATE verdict is not an
	// accidental zero-scope fallback.
	auth, ok := r.Events.authorization()
	if !ok {
		t.Fatal("no authorization record; the gate's refusal is indistinguishable from never reaching it")
	}
	if auth.Verdict != "disambiguate" || auth.Granted || !auth.Blocked {
		t.Fatalf("authorization record = verdict=%q granted=%t blocked=%t, want a blocked disambiguate",
			auth.Verdict, auth.Granted, auth.Blocked)
	}
	if auth.Scope != string(ScopeProposed) {
		t.Fatalf("authorization scope = %q, want PROPOSED", auth.Scope)
	}
	if !contains(auth.Candidates, "index.html") {
		t.Fatalf("authorization candidates = %v, want the discovered candidate", auth.Candidates)
	}
	if !contains(auth.ProposedTargets, "index.html") {
		t.Fatalf("authorization proposed targets = %v, want the proposal", auth.ProposedTargets)
	}
}

// TestR3_AmbiguousCandidatesParkWithCandidateSet is R3-B. Several plausible
// candidates and no declared kind: the runtime must not silently choose one. It
// records the DISCOVERED set, forms no proposal, and asks the human.
func TestR3_AmbiguousCandidatesParkWithCandidateSet(t *testing.T) {
	r := r3Observe(t, map[string]string{
		"index.html":   r3Index,
		"landing.html": r3Index,
		"about.html":   r3Index,
	}, r3Objective)
	d := r.Driver

	if d.scopeResolution.State != ScopeDiscovered {
		t.Fatalf("scope state = %s, want DISCOVERED (candidates observed, no proposal)", d.scopeResolution.State)
	}
	if len(d.scopeResolution.Targets) != 0 {
		t.Fatalf("a DISCOVERED scope bound targets %v", d.scopeResolution.Targets)
	}
	if d.scopeResolution.AuthorizesMutation() {
		t.Fatal("a DISCOVERED scope claimed mutation authority")
	}
	for _, want := range []string{"about.html", "index.html", "landing.html"} {
		if !contains(d.scopeResolution.Candidates, want) {
			t.Errorf("DISCOVERED candidate %q missing from %v", want, d.scopeResolution.Candidates)
		}
	}
	// No proposal was formed: a non-empty candidate list is a question.
	if len(d.scopeResolution.Targets) != 0 {
		t.Fatalf("an ambiguous discovery produced a proposal %v", d.scopeResolution.Targets)
	}

	op := EvaluatePreflightAdmission(d.preflightExecutionSpec(context.Background()))
	if op.Verdict != AdmissionDisambiguate {
		t.Fatalf("authority verdict = %s, want DISAMBIGUATE", op.Verdict)
	}
	b := d.Boundary()
	if b == nil || b.Action != coreautonomy.HumanBoundaryClarify {
		t.Fatalf("boundary = %+v, want a clarification", b)
	}
	for _, want := range []string{"about.html", "index.html", "landing.html"} {
		if !contains(b.Options, want) {
			t.Errorf("clarification option %q missing from %v", want, b.Options)
		}
	}
	if d.State() != coreautonomy.RuntimeAwaitingHuman {
		t.Fatalf("state = %s, want awaiting_human", d.State())
	}
}

// TestR3_ExplicitTargetKeepsTheProvenPath is R3-C: an objective that NAMES the
// file must keep the existing proven path — resolve, admit, mutate, verify,
// PROVEN. The discovery work must not regress it.
func TestR3_ExplicitTargetKeepsTheProvenPath(t *testing.T) {
	r := r3Observe(t, map[string]string{"index.html": r3Index}, "fix the greeting in index.html")
	d := r.Driver

	if d.scopeResolution.State != ScopeResolved || !d.scopeResolution.AuthorizesMutation() {
		t.Fatalf("scope = %s authorizes=%t, want RESOLVED/true for an explicitly named target",
			d.scopeResolution.State, d.scopeResolution.AuthorizesMutation())
	}
	if !contains(d.resolved.Targets, "index.html") {
		t.Fatalf("resolved targets = %v, want index.html", d.resolved.Targets)
	}
	if op := EvaluatePreflightAdmission(d.preflightExecutionSpec(context.Background())); op.Verdict != AdmissionAdmit {
		t.Fatalf("authority verdict = %s, want ADMIT", op.Verdict)
	}
	b := d.Boundary()
	if b == nil || b.Action != coreautonomy.HumanBoundaryApproval || b.PatchID == "" {
		t.Fatalf("boundary = %+v, want an approval gate holding a candidate", b)
	}

	if _, err := d.ResumeApprove(context.Background()); err != nil {
		t.Fatalf("approve: %v", err)
	}
	obs := d.LastObservation()
	if obs.Objective.Mutation != execution.FilesystemApplied || obs.Objective.MutatedFiles == 0 {
		t.Fatalf("mutation = %s files=%d, want an applied filesystem mutation",
			obs.Objective.Mutation, obs.Objective.MutatedFiles)
	}
	if !obs.Objective.VerificationRan {
		t.Fatal("verification never ran over the approved mutation")
	}
	if got := d.objectiveEvaluation().Outcome; got != execution.ObjectiveProven {
		t.Fatalf("objective outcome = %s (%s), want PROVEN", got, d.objectiveEvaluation().Reason)
	}
	body, err := os.ReadFile(filepath.Join(r.Root, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "Hello") || strings.Contains(string(body), "Helo") {
		t.Fatalf("workspace does not hold the repair: %q", body)
	}
}

// TestR3_DiscoveryEvidenceCannotDirectlyAuthorizeMutation is the invariant, at
// the pure layers. A candidate set is never authority: only a RESOLVED scope
// authorizes, and an UNRESOLVED derivation with candidates cannot satisfy the
// admission gate on its own.
func TestR3_DiscoveryEvidenceCannotDirectlyAuthorizeMutation(t *testing.T) {
	// 1. Exactly one scope position authorizes mutation.
	authorizers := 0
	for _, s := range AllScopeResolutionStates() {
		if s.AuthorizesMutation() {
			authorizers++
			if s != ScopeResolved {
				t.Errorf("%s claims mutation authority; only RESOLVED may", s)
			}
		}
	}
	if authorizers != 1 {
		t.Fatalf("%d scope positions authorize mutation, want exactly 1", authorizers)
	}

	// 2. A proposal carrying a real target still does not authorize.
	proposed := ScopeResolution{State: ScopeProposed, Targets: []string{"index.html"}}
	if proposed.AuthorizesMutation() {
		t.Fatal("a PROPOSED scope carrying a target reports itself as authority")
	}
	discovered := ScopeResolution{State: ScopeDiscovered, Candidates: []string{"index.html"}}
	if discovered.AuthorizesMutation() {
		t.Fatal("a DISCOVERED scope reports itself as authority")
	}

	// 3. An UNRESOLVED derivation with observed candidates is evidence, not a
	// resolution: the admission gate may not admit on it.
	spec := ExecutionSpec{
		Intent:          IntentMutate,
		ExplicitTargets: nil,
		Derivation: execution.Derivation{
			Status:     execution.DerivationUnresolved,
			Candidates: []string{"index.html"},
			Reason:     "no artifact kind declared",
		},
		TargetBinding: &execution.TargetBindingResult{
			State:  execution.TargetStateAmbiguous,
			Phase:  execution.PhaseAwaitingDisambiguation,
			Status: execution.BindingUnresolved,
		},
	}
	outcome := EvaluatePreflightAdmission(spec)
	if outcome.Verdict == AdmissionAdmit {
		t.Fatal("the admission gate ADMITTED a mutation on discovery evidence alone")
	}
	if !outcome.Blocked() {
		t.Fatalf("outcome = %s, want a blocked verdict", outcome.Verdict)
	}
	if spec.ProviderCalls != 0 || spec.ProviderTokens != 0 {
		t.Fatal("a blocked admission carried provider facts")
	}
}

// TestR3_UnauthorizedTargetCannotReachRuntimeExecutor is the end-to-end
// negative: neither the unique-candidate nor the ambiguous discovery objective
// may dispatch a provider call or touch a byte, and no mutation may be recorded.
func TestR3_UnauthorizedTargetCannotReachRuntimeExecutor(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
	}{
		{"unique", map[string]string{"index.html": r3Index}},
		{"ambiguous", map[string]string{
			"index.html":   r3Index,
			"landing.html": r3Index,
			"about.html":   r3Index,
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := r3Observe(t, tc.files, r3Objective)
			d := r.Driver

			if r.Provider.Calls() != 0 {
				t.Fatalf("the runtime dispatched %d provider call(s) for an unauthorized target", r.Provider.Calls())
			}
			if changed := r3Diff(r.Before, r3Snapshot(t, r.Root)); len(changed) != 0 {
				t.Fatalf("the runtime mutated %v without authorization", changed)
			}
			if r.Events.sawMutationCompleted() {
				t.Fatal("a mutation.completed event was published for an unauthorized target")
			}
			if b := d.Boundary(); b != nil && b.PatchID != "" {
				t.Fatalf("an unauthorized run staged a mutation candidate: %+v", b)
			}
			if got := d.LastObservation().Objective.MutatedFiles; got != 0 {
				t.Fatalf("mutated files = %d, want 0", got)
			}
		})
	}
}
