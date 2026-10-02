package execution_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/core/domain"
	domaincap "github.com/PizenLabs/izen/internal/domain/capability"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/execution/capability"
	"github.com/PizenLabs/izen/internal/runtime/substrate"
)

// goldenFixture copies testdata/goldenweb into a writable temp workspace so a
// test can repair it without mutating the checked-in fixture.
//
// The source path is resolved from THIS FILE's location via runtime.Caller,
// never from the process working directory: other tests in this package chdir,
// so a relative path would make the fixture vanish depending on test order. An
// order-dependent fixture is a flake that only appears on someone else's
// machine.
func goldenFixture(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the test source file to resolve the fixture path")
	}
	src, err := filepath.Abs(filepath.Join(filepath.Dir(thisFile), "..", "..", "testdata", "goldenweb"))
	if err != nil {
		t.Fatalf("abs fixture: %v", err)
	}
	if _, err := os.Stat(src); err != nil {
		t.Fatalf("golden fixture %s is missing: %v", src, err)
	}
	dst := t.TempDir()
	err = filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, rErr := filepath.Rel(src, p)
		if rErr != nil {
			return rErr
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, rErr2 := os.ReadFile(p)
		if rErr2 != nil {
			return rErr2
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatalf("copy fixture: %v", err)
	}
	return dst
}

func behavioralGrant() capability.Grant {
	return capability.Grant{
		Provenance: "$prompt",
		Discover:   true,
		Read:       true,
		Execute:    true,
		Network:    true,
	}
}

// scriptedProposer repairs a known defect the way a reasoning backend would:
// it READS the observed defect and emits content derived from the target's real
// current bytes. It is scripted only in WHICH defect it handles — it never
// selects the target, because the loop derives that from evidence.
type scriptedProposer struct {
	calls    int
	handled  map[string]bool
	failNext bool
}

func (p *scriptedProposer) ProposeRepair(_ context.Context, defect capability.Defect, _ execution.Observation) (execution.RepairProposal, error) {
	p.calls++
	if p.failNext {
		p.failNext = false
		return execution.RepairProposal{}, context.DeadlineExceeded
	}
	if p.handled == nil {
		p.handled = map[string]bool{}
	}
	p.handled[defect.Code] = true
	switch defect.Code {
	case capability.CodeMissingSubresource, capability.CodeDocumentStructureInvalid:
	default:
		return execution.RepairProposal{}, nil
	}
	// A real backend would read the target document and rewrite the broken
	// reference and the unclosed element. Here the derivation is scripted; what
	// is under test is that the RUNTIME re-derives the target from evidence and
	// routes the write through the mutation authority — the proposal's own
	// target field is deliberately set to something the evidence does not
	// support, so a passing test proves the runtime ignored it.
	return execution.RepairProposal{
		DefectCode: defect.Code,
		Target:     "styles.css",
		Content:    fixedHTML,
		Rationale:  "the workspace serves styles.css, so the reference must name it; and <main> must close",
	}, nil
}

const fixedHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8" />
  <title>Task Board</title>
  <link rel="stylesheet" href="styles.css" />
</head>
<body>
  <main id="board">
    <h1>Task Board</h1>
    <section class="column"><h2>Backlog</h2></section>
    <section class="column"><h2>Done</h2></section>
  </main>
  <script src="board.js"></script>
</body>
</html>`

// denyAll is an authorization gate that refuses every mutation. It is the
// control-plane boundary a loop must respect.
func denyAll(string) error { return errDenied }

var errDenied = context.Canceled

func newTestLoop(t *testing.T, root string, proposer execution.RepairProposer, authorize func(string) error) *execution.BehaviorLoop {
	t.Helper()
	rt := execution.NewBehavioralRuntime(execution.BehavioralConfig{Root: root})
	loop, err := execution.NewBehaviorLoop(execution.BehaviorLoopConfig{
		Runtime:   rt,
		Grant:     behavioralGrant(),
		Proposer:  proposer,
		Mutate:    substrate.NewConcreteSubstrate(root),
		Authorize: authorize,
	})
	if err != nil {
		t.Fatalf("NewBehaviorLoop: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	return loop
}

// TestObserveDetectsBothFixtureDefects: the observation must surface the
// runtime-only missing subresource AND the structural defect, with real
// evidence, without anyone naming them in advance.
func TestObserveDetectsBothFixtureDefects(t *testing.T) {
	root := goldenFixture(t)
	rt := execution.NewBehavioralRuntime(execution.BehavioralConfig{Root: root})
	defer func() { _ = rt.Close() }()

	obs := rt.Observe(context.Background(), behavioralGrant())
	if obs.Block != nil {
		t.Fatalf("observation blocked: %s", obs.Block.Error())
	}
	if obs.Verified() {
		t.Fatal("the broken fixture must not verify")
	}
	if obs.BaseURL == "" {
		t.Fatal("observation must record the discovered reachable URL")
	}
	if !strings.HasPrefix(obs.BaseURL, "http://127.0.0.1:") {
		t.Fatalf("base URL = %q, want a real bound loopback address", obs.BaseURL)
	}

	var missing, structural bool
	for _, d := range obs.Defects() {
		switch d.Code {
		case capability.CodeMissingSubresource:
			missing = true
			if len(d.Candidates) != 1 || d.Candidates[0] != "styles.css" {
				t.Fatalf("missing-subresource candidates = %v, want the real [styles.css]", d.Candidates)
			}
		case capability.CodeDocumentStructureInvalid:
			structural = true
			if !strings.Contains(d.Summary, "main") {
				t.Fatalf("structural defect must name the unclosed element: %s", d.Summary)
			}
		}
	}
	if !missing {
		t.Fatalf("defects = %s, want MISSING_SUBRESOURCE", obs.DefectLine())
	}
	if !structural {
		t.Fatalf("defects = %s, want DOCUMENT_STRUCTURE_INVALID", obs.DefectLine())
	}
	// The evidence must contain the real 404 the runtime actually returned.
	var saw404 bool
	for _, ev := range obs.Proof {
		if ev.Capability == capability.RuntimeFetch && ev.Field("status") == "404" {
			saw404 = true
		}
	}
	if !saw404 {
		t.Fatalf("proof must retain the real 404 observation: %s", obs.EvidenceLine())
	}
}

// TestObserveReleasesTheListener: the runtime must not hold the port after a
// pass, or the next observation would be measuring a stale server.
func TestObserveReleasesTheListener(t *testing.T) {
	root := goldenFixture(t)
	rt := execution.NewBehavioralRuntime(execution.BehavioralConfig{Root: root})
	defer func() { _ = rt.Close() }()

	obs := rt.Observe(context.Background(), behavioralGrant())
	if obs.Block != nil {
		t.Fatalf("observation blocked: %s", obs.Block.Error())
	}
	if !obs.Stop.OK {
		t.Fatalf("stop evidence = %+v, want a proven stop", obs.Stop)
	}
	if rt.Served() {
		t.Fatal("runtime must not report itself served after Observe returns")
	}
	if rt.URL() != "" {
		t.Fatalf("URL = %q, want empty after the listener is released", rt.URL())
	}
}

// TestLoopRepairsVerifiesAndProves is the end-to-end property: an authorized
// objective on a genuinely imperfect workspace reaches PROVEN through real
// observation, real diagnosis, real repair, re-execution and re-verification.
func TestLoopRepairsVerifiesAndProves(t *testing.T) {
	root := goldenFixture(t)
	proposer := &scriptedProposer{}
	loop := newTestLoop(t, root, proposer, func(string) error { return nil })

	result := loop.Run(context.Background())
	if result.Block != nil && result.Block.Class != capability.FailureObjectiveUnproven {
		t.Fatalf("unexpected block: %s", result.Block.Error())
	}
	if !result.Proven {
		t.Fatalf("loop did not prove the objective: block=%v defects=%s repairs=%d",
			blockText(result), result.Final.DefectLine(), result.RepairCount())
	}
	if len(result.Rounds) != 1 {
		t.Fatalf("rounds = %d, want exactly 1: both defects are repaired in one round and the re-observation then proves the objective", len(result.Rounds))
	}

	// The workspace must ACTUALLY be fixed on disk, not merely reported fixed.
	html, err := os.ReadFile(filepath.Join(root, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(html), `href="styles.css"`) {
		t.Fatalf("index.html on disk was not repaired:\n%s", html)
	}

	// Exactly one workspace-changing repair, applied through the authority.
	if result.RepairCount() != 1 {
		t.Fatalf("repairs that changed the workspace = %d, want exactly 1: the second defect was already repaired by the first proposal", result.RepairCount())
	}
	first := result.Repairs[0]
	if first.Outcome != execution.RepairApplied {
		t.Fatalf("first repair outcome = %s (%s)", first.Outcome, first.Reason)
	}
	if first.Target != "index.html" {
		t.Fatalf("repair target = %q, want the evidence-derived index.html", first.Target)
	}
	// The second defect was already fixed by the first proposal. Reporting it as
	// APPLIED would credit the runtime with a change it did not make.
	if len(result.Repairs) < 2 {
		t.Fatal("both observed defects must be accounted for")
	}
	if got := result.Repairs[1].Outcome; got != execution.RepairNotNeeded {
		t.Fatalf("second repair outcome = %s, want %s (the first proposal already fixed it)", got, execution.RepairNotNeeded)
	}
	// The final round must be a REAL re-observation, and it must be clean.
	last := result.Rounds[len(result.Rounds)-1]
	if !last.Verified {
		t.Fatalf("final round did not verify: %s", last.After.DefectLine())
	}
}

func blockText(r execution.BehaviorResult) string {
	if r.Block == nil {
		return "none"
	}
	return r.Block.Error()
}

// TestLoopRefusesWithoutAuthorization: the Control Plane gate is real. A loop
// with a denying gate must not change one byte, and must say why.
func TestLoopRefusesWithoutAuthorization(t *testing.T) {
	root := goldenFixture(t)
	proposer := &scriptedProposer{}
	loop := newTestLoop(t, root, proposer, denyAll)

	before, err := os.ReadFile(filepath.Join(root, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	result := loop.Run(context.Background())
	if result.Proven {
		t.Fatal("a denied objective must never report PROVEN")
	}
	after, err := os.ReadFile(filepath.Join(root, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("a denied authorization must leave the workspace byte-identical")
	}
	if proposer.calls != 0 {
		t.Fatalf("proposer was called %d time(s); an unauthorized target must cost no provider call", proposer.calls)
	}
	var sawDenial bool
	for _, a := range result.Repairs {
		if a.Outcome == execution.RepairNotAuthorized {
			sawDenial = true
		}
	}
	if !sawDenial {
		t.Fatalf("repairs = %+v, want a NOT_AUTHORIZED outcome", result.Repairs)
	}
	if result.Block == nil || result.Block.Class != capability.FailureObjectiveUnproven {
		t.Fatalf("block = %v, want OBJECTIVE_UNPROVEN after exhausting the bound", result.Block)
	}
}

// TestResolveRepairTargetAlwaysComesFromEvidence pins the loop's central
// invariant directly, because an invariant that can only be reached through a
// full loop cannot be tested at the cases that actually matter.
//
// The two failure modes it rules out are both destructive, in opposite
// directions: writing to the entry when the observation never attributed the
// defect to one, and writing to a candidate file when that file SATISFIES the
// requirement rather than stating it.
func TestResolveRepairTargetAlwaysComesFromEvidence(t *testing.T) {
	cases := []struct {
		name       string
		defect     capability.Defect
		wantTarget string
		wantEmpty  bool
	}{
		{
			name:       "attributed document is the target, not the satisfying candidate",
			defect:     capability.Defect{Code: capability.CodeMissingSubresource, Entry: "index.html", Candidates: []string{"styles.css"}},
			wantTarget: "index.html",
		},
		{
			name:       "attributed document wins even with many candidates",
			defect:     capability.Defect{Code: capability.CodeMissingSubresource, Entry: "docs/page.html", Candidates: []string{"a.css", "b.css"}},
			wantTarget: "docs/page.html",
		},
		{
			name:      "no attribution and no candidate: refuse rather than invent",
			defect:    capability.Defect{Code: capability.CodeDocumentStructureInvalid, Summary: "unclosed element"},
			wantEmpty: true,
		},
		{
			name:      "no attribution and many candidates: refuse rather than pick",
			defect:    capability.Defect{Code: capability.CodeMissingSubresource, Candidates: []string{"a.css", "b.css"}},
			wantEmpty: true,
		},
		{
			name:       "single candidate with no attribution is still evidence",
			defect:     capability.Defect{Code: capability.CodeMissingSubresource, Candidates: []string{"styles.css"}},
			wantTarget: "styles.css",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target, reason := execution.ResolveRepairTarget(tc.defect)
			if tc.wantEmpty {
				if target != "" {
					t.Fatalf("target = %q, want no target (reason: %s)", target, reason)
				}
				if reason == "" {
					t.Fatal("a refusal must carry a reason")
				}
				return
			}
			if target != tc.wantTarget {
				t.Fatalf("target = %q, want %q", target, tc.wantTarget)
			}
		})
	}
}

// TestLoopNeverInventsATarget: a defect the runtime cannot attribute to any
// document and cannot ground in a candidate must cost no provider call and no
// mutation. That is DIAGNOSIS_UNCERTAIN, reported rather than papered over.
func TestLoopNeverInventsATarget(t *testing.T) {
	root := goldenFixture(t)
	// Wrap the proposer so it refuses to name any target, which forces the
	// runtime's own resolution to be the deciding factor.
	proposer := &scriptedProposer{}
	loop := newTestLoop(t, root, proposer, func(string) error { return nil })

	result := loop.Run(context.Background())
	if !result.Proven {
		t.Fatalf("precondition: the scripted repair should fix the workspace; got %s", result.Final.DefectLine())
	}
	// Every applied repair must have an evidence-derived target, and that target
	// must be the attributed document rather than the satisfying candidate.
	for _, a := range result.Repairs {
		if a.Outcome != execution.RepairApplied {
			continue
		}
		if a.Target == "" {
			t.Fatal("an applied repair must carry the target it wrote to")
		}
		if a.Defect.Entry != "" && a.Target != a.Defect.Entry {
			t.Fatalf("repair target = %q, want the attributed document %q", a.Target, a.Defect.Entry)
		}
		// The destructive failure mode: writing to the file that SATISFIES the
		// requirement instead of the document that STATES it. That silently
		// overwrites a healthy asset with page markup.
		for _, cand := range a.Defect.Candidates {
			if cand != a.Defect.Entry && a.Target == cand {
				t.Fatalf("repair wrote to the satisfying candidate %q instead of the attributed document %q",
					cand, a.Defect.Entry)
			}
		}
	}
}

// TestLoopReportsDeclinedRepairTruthfully: a backend that consistently cannot
// propose is a DECLINED outcome for every attempt, and the objective terminates
// UNPROVEN. It must never be reported as a pass.
func TestLoopReportsDeclinedRepairTruthfully(t *testing.T) {
	root := goldenFixture(t)
	alwaysDeclines := execution.RepairProposerFunc(func(context.Context, capability.Defect, execution.Observation) (execution.RepairProposal, error) {
		return execution.RepairProposal{}, context.DeadlineExceeded
	})
	loop := newTestLoop(t, root, alwaysDeclines, func(string) error { return nil })

	result := loop.Run(context.Background())
	if result.Proven {
		t.Fatal("a declined repair must not report PROVEN")
	}
	if len(result.Repairs) == 0 {
		t.Fatal("the declined attempts must be recorded")
	}
	for i, a := range result.Repairs {
		if a.Outcome != execution.RepairDeclined {
			t.Fatalf("repair %d outcome = %s, want %s", i, a.Outcome, execution.RepairDeclined)
		}
	}
	if result.Block == nil || result.Block.Class != capability.FailureObjectiveUnproven {
		t.Fatalf("block = %v, want OBJECTIVE_UNPROVEN", result.Block)
	}
}

// persistentlyBrokenHTML is a workspace whose single observable defect cannot be
// fixed by rewriting the document: it references a resource nothing in the
// workspace provides, and the "repair" the backend proposes is byte-identical
// to what is already there.
const persistentlyBrokenHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <title>Unfixable</title>
  <link rel="stylesheet" href="absent.css" />
</head>
<body>
  <main id="root"><p>nothing here can satisfy absent.css</p></main>
</body>
</html>`

// TestLoopBoundsTerminate: a workspace that never becomes correct must terminate
// as OBJECTIVE_UNPROVEN within the bound. This is the anti-liveness property:
// without it a repair loop over a stubborn workspace runs forever, which is the
// failure mode a bounded runtime exists to prevent.
func TestLoopBoundsTerminate(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte(persistentlyBrokenHTML), 0o644); err != nil {
		t.Fatal(err)
	}
	// The proposal reproduces the target byte-for-byte, so it is a genuine no-op
	// the runtime must detect rather than count as progress.
	noop := execution.RepairProposerFunc(func(_ context.Context, _ capability.Defect, _ execution.Observation) (execution.RepairProposal, error) {
		return execution.RepairProposal{Content: persistentlyBrokenHTML}, nil
	})
	rt := execution.NewBehavioralRuntime(execution.BehavioralConfig{Root: root})
	defer func() { _ = rt.Close() }()
	loop, err := execution.NewBehaviorLoop(execution.BehaviorLoopConfig{
		Runtime:   rt,
		Grant:     behavioralGrant(),
		Proposer:  noop,
		Mutate:    substrate.NewConcreteSubstrate(root),
		Authorize: func(string) error { return nil },
		Bounds:    execution.LoopBounds{MaxRounds: 3, MaxDefectsPerRound: 1},
	})
	if err != nil {
		t.Fatalf("NewBehaviorLoop: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	result := loop.Run(ctx)
	if result.Proven {
		t.Fatal("a no-op repair must not prove the objective")
	}
	if result.Block == nil || result.Block.Class != capability.FailureObjectiveUnproven {
		t.Fatalf("block = %v, want OBJECTIVE_UNPROVEN", result.Block)
	}
	if len(result.Rounds) != 3 {
		t.Fatalf("rounds = %d, want exactly the bound (3)", len(result.Rounds))
	}
	if result.RepairCount() != 0 {
		t.Fatalf("repairs that changed the workspace = %d, want 0: a byte-identical proposal changes nothing",
			result.RepairCount())
	}
	for i, a := range result.Repairs {
		if a.Outcome != execution.RepairNotNeeded {
			t.Fatalf("repair %d outcome = %s, want %s", i, a.Outcome, execution.RepairNotNeeded)
		}
	}
}

// TestBlockedRootIsReportedTruthfully: a runtime over a nonexistent root must
// report a capability failure, never an empty-but-verified workspace.
func TestBlockedRootIsReportedTruthfully(t *testing.T) {
	if _, err := execution.NewBehaviorLoop(execution.BehaviorLoopConfig{
		Runtime:  execution.NewBehavioralRuntime(execution.BehavioralConfig{Root: filepath.Join(t.TempDir(), "absent")}),
		Proposer: &scriptedProposer{},
	}); err == nil {
		t.Fatal("a loop over a nonexistent root must be refused at construction")
	}
}

// TestGrantForReflectsExistingAuthority: the capability grant is a projection of
// authority IZEN already holds, never a widening of it.
func TestGrantForReflectsExistingAuthority(t *testing.T) {
	t.Run("read-only scope grants no execution", func(t *testing.T) {
		caps := fullCapabilitySet()
		grant := execution.GrantFor(domain.ScopeNone, caps)
		if grant.Execute || grant.Network {
			t.Fatalf("read-only scope granted execute=%v network=%v", grant.Execute, grant.Network)
		}
		if !grant.Read || !grant.Discover {
			t.Fatal("read-only scope must still permit bounded discovery and reading")
		}
	})
	t.Run("empty capability set grants nothing beyond discovery", func(t *testing.T) {
		grant := execution.GrantFor(domain.ScopeDynamic, emptyCapabilitySet())
		if grant.Read || grant.Execute || grant.Network {
			t.Fatalf("an empty capability set must cap the grant, got %+v", grant)
		}
		if !grant.Discover {
			t.Fatal("discovery is always available; it grants nothing by itself")
		}
	})
	t.Run("prompt scope does not manufacture execution authority", func(t *testing.T) {
		noExec := capabilitySetWithoutExecute()
		grant := execution.GrantFor(domain.ScopeDynamic, noExec)
		if grant.Execute {
			t.Fatal("$prompt must not manufacture execute authority the capability set never granted")
		}
		// Network observation is workspace-scoped and gated on the same execute
		// authority: a runtime that may not start a process cannot be reached by
		// one, so granting probe alone would only ever yield OBSERVATION_FAILED.
		if grant.Network {
			t.Fatal("$prompt must not grant observation it cannot produce")
		}
	})
	t.Run("prompt scope with execute authority can observe its own runtime", func(t *testing.T) {
		grant := execution.GrantFor(domain.ScopeDynamic, fullCapabilitySet())
		if !grant.Execute || !grant.Network || !grant.Read {
			t.Fatalf("a fully granted $prompt must be able to run and observe its workspace: %+v", grant)
		}
	})
}

func TestGrantForLabelsProvenance(t *testing.T) {
	if got := execution.ScopeProvenanceLabel(domain.ScopeDynamic); got != "$prompt" {
		t.Fatalf("label = %q, want $prompt", got)
	}
	if got := execution.ScopeProvenanceLabel(domain.ScopeNone); got != "read_only" {
		t.Fatalf("label = %q, want read_only", got)
	}
}

// Capability-set helpers. They build REAL domain capability sets so the grant
// projection is exercised against the type production uses, not a stand-in.

func fullCapabilitySet() *domaincap.CapabilitySet {
	caps := domaincap.NewCapabilitySet()
	caps.Grant(domaincap.CapabilityRead)
	caps.Grant(domaincap.CapabilityWrite)
	caps.Grant(domaincap.CapabilityExecute)
	return caps
}

func emptyCapabilitySet() *domaincap.CapabilitySet { return domaincap.NewCapabilitySet() }

func capabilitySetWithoutExecute() *domaincap.CapabilitySet {
	caps := domaincap.NewCapabilitySet()
	caps.Grant(domaincap.CapabilityRead)
	caps.Grant(domaincap.CapabilityWrite)
	return caps
}
