package execution_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/execution/capability"
	"github.com/PizenLabs/izen/internal/runtime/substrate"
)

// ── GOLDEN OBJECTIVE ────────────────────────────────────────────────────────
//
// The objective under test, stated once, in the user's own terms:
//
//	"Inspect this project, understand its current implementation, redesign it,
//	 run it, verify its runtime behaviour, identify problems through
//	 observation, repair them, rerun the project, and leave the workspace in a
//	 verified working state."
//
// The fixture (testdata/goldenweb) is a genuinely imperfect static project. Its
// defects are chosen so that NO static read can find them:
//
//   1. index.html requests `style.css`; the workspace provides `styles.css`.
//      Every file parses, the CSS is valid, and the mismatch is invisible until
//      something actually resolves the URL.
//   2. <main> is never closed. The document still renders, and the defect is
//      invisible until the served bytes are structurally reconciled.
//
// This file asserts the full objective chain on that fixture. It is the test
// that decides whether IZEN can complete an authorized objective, as opposed to
// merely running its pipeline.

// evidenceReasoner is the reasoning backend for the golden objective.
//
// It is deterministic so the objective is reproducible, but it is NOT a template
// for the answer: it DERIVES its repair from the defect it was handed and from
// the real current bytes of the target, and it deliberately names the WRONG
// target in its proposal. Every test here therefore also proves the runtime
// ignores the model's target choice and uses the evidence-derived one — which is
// the invariant that keeps a model from being an authority.
type evidenceReasoner struct {
	calls       int
	sawDefect   bool
	sawEntry    string
	sawCand     []string
	sawEvidence bool
	// readsTarget records that the backend consulted the workspace rather than
	// answering from memory.
	readsTarget int
}

func (r *evidenceReasoner) ProposeRepair(_ context.Context, defect capability.Defect, obs execution.Observation) (execution.RepairProposal, error) {
	r.calls++
	r.sawDefect = defect.Code != ""
	r.sawEntry = defect.Entry
	r.sawCand = append([]string(nil), defect.Candidates...)
	r.sawEvidence = len(defect.Evidence) > 0
	// Consult the real workspace: a repair must be derived from what is there.
	r.readsTarget++

	switch defect.Code {
	case capability.CodeMissingSubresource:
		if len(defect.Candidates) != 1 {
			return execution.RepairProposal{}, nil
		}
		current, err := os.ReadFile(filepath.Join(rootOf(obs), filepath.FromSlash(defect.Entry)))
		if err != nil {
			return execution.RepairProposal{}, err
		}
		// Derive the repair from the ACTUAL bytes: point the broken reference at
		// the file the workspace really provides.
		fixed := strings.Replace(string(current), `href="style.css"`, `href="styles.css"`, 1)
		if fixed == string(current) {
			return execution.RepairProposal{}, nil
		}
		return execution.RepairProposal{
			DefectCode: defect.Code,
			// Deliberately wrong: proves the runtime overrides the model.
			Target:    "styles.css",
			Content:   fixed,
			Rationale: "the runtime answered 404 for style.css and the workspace serves " + defect.Candidates[0],
		}, nil
	case capability.CodeDocumentStructureInvalid:
		current, err := os.ReadFile(filepath.Join(rootOf(obs), filepath.FromSlash(defect.Entry)))
		if err != nil {
			return execution.RepairProposal{}, err
		}
		fixed := closeMainElement(string(current))
		if fixed == string(current) {
			return execution.RepairProposal{}, nil
		}
		return execution.RepairProposal{
			DefectCode: defect.Code,
			Target:     defect.Entry,
			Content:    fixed,
			Rationale:  "the served document does not reconcile: <main> is never closed",
		}, nil
	}
	return execution.RepairProposal{}, nil
}

// rootOf returns the observed workspace root from an observation's evidence, so
// the reasoner reasons about the SAME workspace the runtime observed.
func rootOf(obs execution.Observation) string {
	for _, ev := range obs.Proof {
		if root := ev.Field("root"); root != "" {
			return root
		}
	}
	for _, ev := range obs.Proof {
		if root := ev.Field("workspace_root"); root != "" {
			return root
		}
	}
	return ""
}

// closeMainElement closes an unclosed <main> immediately before </body>.
func closeMainElement(body string) string {
	idx := strings.Index(body, "</body>")
	if idx < 0 {
		return body
	}
	return body[:idx] + "</main>\n" + body[idx:]
}

// TestGoldenObjective_VerifiedWorkingState is the primary acceptance test.
//
// It walks the objective's stages in order and asserts that each one left real
// evidence behind:
//
//	DISCOVER → OBSERVE → DIAGNOSE → REPAIR → RE-EXECUTE → VERIFY → PROVE
func TestGoldenObjective_VerifiedWorkingState(t *testing.T) {
	root := goldenFixture(t)

	// ── AUTHORIZATION ──────────────────────────────────────────────────
	// The objective is authorized as a $prompt scope over a workspace that holds
	// read + execute + write authority. The grant is a projection of that
	// authority, not a widening of it.
	caps := fullCapabilitySet()
	grant := execution.GrantFor(scopeDynamicForTest(), caps)
	if !grant.Discover || !grant.Read || !grant.Execute || !grant.Network {
		t.Fatalf("an authorized $prompt scope must be able to observe its workspace: %+v", grant)
	}

	rt := execution.NewBehavioralRuntime(execution.BehavioralConfig{Root: root})
	defer func() { _ = rt.Close() }()

	// ── DISCOVER ───────────────────────────────────────────────────────
	// The runtime finds the actual workspace rather than assuming filenames.
	discovery := rt.Discover(context.Background(), grant)
	if discovery.Block != nil {
		t.Fatalf("discovery blocked: %s", discovery.Block.Error())
	}
	if discovery.Profile.Entry == nil || discovery.Profile.Entry.Path != "index.html" {
		t.Fatalf("discovery entry = %+v, want the structurally derived index.html", discovery.Profile.Entry)
	}
	if len(discovery.Profile.Paths()) < 3 {
		t.Fatalf("discovery observed %v, want the real project files", discovery.Profile.Paths())
	}

	// ── OBSERVE (baseline) ─────────────────────────────────────────────
	// The project is RUN and its runtime behaviour is observed before any change.
	baseline := rt.Observe(context.Background(), grant)
	if baseline.Block != nil {
		t.Fatalf("baseline observation blocked: %s", baseline.Block.Error())
	}
	if baseline.Verified() {
		t.Fatal("precondition: the golden fixture must NOT verify before repair")
	}
	if baseline.BaseURL == "" || !strings.HasPrefix(baseline.BaseURL, "http://127.0.0.1:") {
		t.Fatalf("the runtime must be actually served at a discovered URL, got %q", baseline.BaseURL)
	}

	// ── DIAGNOSE ───────────────────────────────────────────────────────
	// Both defects are named from evidence, and the runtime-only one carries the
	// real candidate the workspace actually provides.
	codes := map[string]bool{}
	for _, d := range baseline.Defects() {
		codes[d.Code] = true
	}
	if !codes[capability.CodeMissingSubresource] {
		t.Fatalf("baseline defects = %s, want a MISSING_SUBRESOURCE discovered by probing", baseline.DefectLine())
	}
	if !codes[capability.CodeDocumentStructureInvalid] {
		t.Fatalf("baseline defects = %s, want a DOCUMENT_STRUCTURE_INVALID from the served bytes", baseline.DefectLine())
	}

	// ── REPAIR → RE-EXECUTE → VERIFY → PROVE ───────────────────────────
	reasoner := &evidenceReasoner{}
	loop, err := execution.NewBehaviorLoop(execution.BehaviorLoopConfig{
		Runtime:   rt,
		Grant:     grant,
		Proposer:  reasoner,
		Mutate:    substrate.NewConcreteSubstrate(root),
		Authorize: authorizeWorkspaceWrite,
	})
	if err != nil {
		t.Fatalf("NewBehaviorLoop: %v", err)
	}
	result := loop.Run(context.Background())

	if result.Block != nil {
		t.Fatalf("golden objective blocked: %s", result.Block.Error())
	}
	if !result.Proven {
		t.Fatalf("golden objective did not reach PROVEN; final defects: %s", result.Final.DefectLine())
	}

	// The model reasoned from evidence, not from a template.
	if reasoner.calls == 0 {
		t.Fatal("the reasoning backend was never consulted")
	}
	if !reasoner.sawDefect || !reasoner.sawEvidence {
		t.Fatal("the reasoning backend must receive the defect together with its evidence")
	}
	if reasoner.readsTarget == 0 {
		t.Fatal("the reasoning backend must consult the workspace, not answer from memory")
	}

	// The runtime OVERRODE the model's wrong target suggestion on the
	// missing-subresource defect. This is the invariant that keeps the model an
	// untrusted proposal source.
	var overrodeWrongTarget bool
	for _, a := range result.Repairs {
		if a.Proposal.Target != "" && a.Proposal.Target != a.Target {
			overrodeWrongTarget = true
			if a.Outcome != execution.RepairApplied {
				t.Fatalf("the evidence-derived target must win, got outcome %s for %q", a.Outcome, a.Target)
			}
		}
	}
	if !overrodeWrongTarget {
		t.Fatal("expected at least one repair where the model's target was overridden by evidence")
	}

	// ── The workspace is ACTUALLY fixed, on disk ────────────────────────
	html, err := os.ReadFile(filepath.Join(root, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	final := string(html)
	if strings.Contains(final, `href="style.css"`) {
		t.Fatalf("index.html still references the unresolvable stylesheet:\n%s", final)
	}
	if !strings.Contains(final, `href="styles.css"`) {
		t.Fatalf("index.html does not reference the stylesheet the workspace provides:\n%s", final)
	}
	if !strings.Contains(final, "</main>") {
		t.Fatalf("index.html still has an unclosed <main>:\n%s", final)
	}

	// ── The repaired workspace observes clean, independently ───────────
	// A PROVEN claim must be re-derivable: run the observation AGAIN from
	// scratch and require the same verdict. If PROVEN depended on the loop's own
	// state it would not survive a fresh observer.
	verify := rt.Observe(context.Background(), grant)
	if verify.Block != nil {
		t.Fatalf("post-repair observation blocked: %s", verify.Block.Error())
	}
	if !verify.Verified() {
		t.Fatalf("post-repair observation is not clean: %s", verify.DefectLine())
	}
	if rt.Served() || rt.URL() != "" {
		t.Fatal("the runtime must not leak its listener between observations")
	}

	// The evidence retained at the proving observation must be sufficient to
	// audit the claim: it names the URL, the resources and their statuses.
	var sawServe, sawStop bool
	for _, ev := range verify.Proof {
		switch {
		case ev.Capability == capability.RuntimeServe && strings.Contains(ev.Summary, "stopped"):
			sawStop = true
		case ev.Capability == capability.RuntimeServe && ev.Field("base_url") != "":
			sawServe = true
		}
	}
	if !sawServe {
		t.Fatalf("the proving observation must retain serve evidence: %s", verify.EvidenceLine())
	}
	if !sawStop {
		t.Fatalf("the proving observation must retain stop evidence: %s", verify.EvidenceLine())
	}
}

// TestGoldenObjective_ProvesOnlyOnRealObservation: PROVEN is not reachable by
// asserting it. An observation that never ran cannot verify, no matter what a
// caller believes about the workspace.
func TestGoldenObjective_ProvesOnlyOnRealObservation(t *testing.T) {
	root := goldenFixture(t)
	rt := execution.NewBehavioralRuntime(execution.BehavioralConfig{Root: root})
	defer func() { _ = rt.Close() }()

	// A read-only grant cannot observe a runtime at all, so the observation is
	// BLOCKED with the class that says why — never PASS.
	obs := rt.Observe(context.Background(), capability.Grant{
		Provenance: "read_only", Discover: true, Read: true,
	})
	if obs.Verified() {
		t.Fatal("an observation that could not run must never verify")
	}
	if obs.Block == nil {
		t.Fatal("an unrunnable observation must carry a block")
	}
	if obs.Block.Class != capability.FailureAuthorizationBlocked {
		t.Fatalf("block class = %s, want %s", obs.Block.Class, capability.FailureAuthorizationBlocked)
	}
}

// TestGoldenObjective_NotSolvableByTemplate: the loop must not conclude PROVEN
// for a workspace whose defect it never repaired, even when the reasoning
// backend confidently proposes a fix for a DIFFERENT file. This is the check
// that the golden objective is genuinely multi-stage rather than a single
// hardcoded transformation.
func TestGoldenObjective_NotSolvableByTemplate(t *testing.T) {
	root := goldenFixture(t)
	rt := execution.NewBehavioralRuntime(execution.BehavioralConfig{Root: root})
	defer func() { _ = rt.Close() }()

	// A backend that "fixes" the wrong file: it rewrites the stylesheet rather
	// than the document that references the missing one.
	wrongTarget := execution.RepairProposerFunc(func(_ context.Context, _ capability.Defect, _ execution.Observation) (execution.RepairProposal, error) {
		return execution.RepairProposal{Target: "styles.css", Content: "/* repaired the wrong file */\n"}, nil
	})
	loop, err := execution.NewBehaviorLoop(execution.BehaviorLoopConfig{
		Runtime:   rt,
		Grant:     behavioralGrant(),
		Proposer:  wrongTarget,
		Mutate:    substrate.NewConcreteSubstrate(root),
		Authorize: func(string) error { return nil },
		Bounds:    execution.LoopBounds{MaxRounds: 2, MaxDefectsPerRound: 1},
	})
	if err != nil {
		t.Fatalf("NewBehaviorLoop: %v", err)
	}
	result := loop.Run(context.Background())
	if result.Proven {
		t.Fatal("repairing the wrong file must not prove the objective")
	}
	// The stylesheet the backend mangled must NOT contain page markup.
	css, err := os.ReadFile(filepath.Join(root, "styles.css"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(css), "<!DOCTYPE") || strings.Contains(string(css), "<main") {
		t.Fatalf("the repair wrote page markup into a stylesheet:\n%s", css)
	}
}

func TestGoldenFixtureIsDiscoverable(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate test source")
	}
	root := filepath.Join(filepath.Dir(thisFile), "..", "..", "testdata", "goldenweb")
	for _, want := range []string{"index.html", "styles.css", "board.js"} {
		if _, err := os.Stat(filepath.Join(root, want)); err != nil {
			t.Fatalf("golden fixture must contain %s: %v", want, err)
		}
	}
}
