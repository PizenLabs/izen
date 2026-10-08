package autonomy

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/core/domain"
	domaincap "github.com/PizenLabs/izen/internal/domain/capability"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/execution/capability"
)

// ── Behavioral stage tests ──────────────────────────────────────────────────
//
// These pin the properties that make the behavioral gate trustworthy: it runs
// only for objectives that demand it, it can only ever REMOVE a completion, and
// it reports a truthful block instead of proving anything it did not observe.

// behaviorFixture copies testdata/goldenweb into a temp workspace. The path is
// resolved from this file, not the working directory, because other tests in
// this package chdir.
func behaviorFixture(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the test source file")
	}
	src, err := filepath.Abs(filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "testdata", "goldenweb"))
	if err != nil {
		t.Fatal(err)
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

func behaviorCaps() *domaincap.CapabilitySet {
	caps := domaincap.NewCapabilitySet()
	caps.Grant(domaincap.CapabilityRead)
	caps.Grant(domaincap.CapabilityWrite)
	caps.Grant(domaincap.CapabilityExecute)
	return caps
}

// repairingProposer derives its repair from the observed defect and the real
// bytes of the attributed document. It deliberately declares the wrong target, so
// a passing assertion proves the RUNTIME chose the target.
type repairingProposer struct {
	calls int
	// decline makes the proposer refuse, so a test can prove the stage reports a
	// truthful unproven rather than proving on a declined repair.
	decline bool
}

func (p *repairingProposer) ProposeRepair(_ context.Context, defect capability.Defect, obs execution.Observation) (execution.RepairProposal, error) {
	p.calls++
	if p.decline {
		return execution.RepairProposal{}, execution.ErrNoProposalReturned
	}
	body, err := os.ReadFile(filepath.Join(rootFrom(obs), filepath.FromSlash(defect.Entry)))
	if err != nil {
		return execution.RepairProposal{}, err
	}
	fixed := string(body)
	switch defect.Code {
	case capability.CodeMissingSubresource:
		fixed = strings.Replace(fixed, `href="style.css"`, `href="styles.css"`, 1)
	case capability.CodeDocumentStructureInvalid:
		fixed = strings.Replace(fixed, "  <script", "  </main>\n  <script", 1)
	default:
		return execution.RepairProposal{}, nil
	}
	if fixed == string(body) {
		return execution.RepairProposal{}, execution.ErrNoProposalReturned
	}
	return execution.RepairProposal{
		DefectCode: defect.Code,
		Target:     "styles.css",
		Content:    fixed,
	}, nil
}

func rootFrom(obs execution.Observation) string {
	for _, ev := range obs.Proof {
		if root := ev.Field("root"); root != "" {
			return root
		}
	}
	return ""
}

// ── BehaviorRequired: which objectives demand behavioral proof ───────────────

// TestBehaviorRequiredTargetsVerifiableObjectives: the predicate must engage
// for objectives that ask for a verifiable result and stay out of the way of
// objectives that only ask for a document.
func TestBehaviorRequiredTargetsVerifiableObjectives(t *testing.T) {
	behavioral := []string{
		"inspect this project, redesign it, run it and verify its runtime behavior, then repair it",
		"fix the broken page so it works",
		"make the app run correctly",
		"run the tests and fix what fails",
		"repair the layout so it renders properly",
		"debug why the server does not start",
	}
	for _, objective := range behavioral {
		if !BehaviorRequired(objective) {
			t.Errorf("BehaviorRequired(%q) = false, want true: this objective demands observable proof", objective)
		}
	}
	readOnly := []string{
		"",
		"explain the architecture of this project",
		"describe how the router works",
		"what is the purpose of this module",
		"list the files under internal/execution",
		"review the changes in this branch",
		"find where the token budget is enforced",
		"where is the retry logic defined",
		"summarize the test strategy",
	}
	for _, objective := range readOnly {
		if BehaviorRequired(objective) {
			t.Errorf("BehaviorRequired(%q) = true, want false: this objective has no runtime to observe", objective)
		}
	}
}

// TestBehaviorRequiredIgnoresFilenameTokens is the regression for the
// real-world CREATE defect: the objective "create new file named testing.md"
// was read as behavioral because the filename contains "test", so the
// behavioral gate tried to serve the workspace, found no entry document, and
// downgraded a PROVEN CREATE to an unsubstantiated terminal.
//
// A target filename is not a verb. The objective's actual verbs still decide.
func TestBehaviorRequiredIgnoresFilenameTokens(t *testing.T) {
	// The reproduced defect: no behavioral verb exists outside the filename.
	for _, objective := range []string{
		"create new file named testing.md",
		"create index.html",
		"write the report to report_test.go",
	} {
		if BehaviorRequired(objective) {
			t.Errorf("BehaviorRequired(%q) = true, want false: only the filename carries a keyword", objective)
		}
	}
	// A real behavioral verb outside the filename must still engage the gate.
	for _, objective := range []string{
		"create testing.md and verify it renders",
		"fix the greeting in @main.go",
		"run the tests in test_utils.py",
	} {
		if !BehaviorRequired(objective) {
			t.Errorf("BehaviorRequired(%q) = false, want true: a real behavioral verb is present", objective)
		}
	}
}

// ── the gate's authority properties ─────────────────────────────────────────

// TestBehaviorGateCanOnlyRemoveACompletion is the single most important property
// of the integration: the behavioral gate is a MUTATION of a proposed completion,
// never a source of one.
//
// The three cases are:
//   - no stage wired: the decision is untouched;
//   - objective does not require behavioral proof: untouched;
//   - stage proves: the reason gains real evidence;
//   - stage cannot prove: the completion is REMOVED.
func TestBehaviorGateCanOnlyRemoveACompletion(t *testing.T) {
	t.Run("no stage wired leaves the decision untouched", func(t *testing.T) {
		d := &Driver{prompt: "make it work"}
		decision := autonomy.LoopDecision{Action: autonomy.LoopComplete, Reason: "mutation applied"}
		d.authorizeBehavioralCompletion(context.Background(), &decision)
		if decision.Action != autonomy.LoopComplete {
			t.Fatalf("action = %s, want LoopComplete: a driver with no behavioral stage must keep prior behaviour", decision.Action)
		}
		if decision.Reason != "mutation applied" {
			t.Fatalf("reason = %q, want it untouched", decision.Reason)
		}
	})

	t.Run("a non-complete decision is never touched", func(t *testing.T) {
		d := &Driver{
			prompt:   "make it work",
			behavior: &BehaviorStage{proposer: &repairingProposer{}},
		}
		for _, action := range []autonomy.LoopAction{autonomy.LoopContinue, autonomy.LoopRepair, autonomy.LoopAskHuman, autonomy.LoopAbort, autonomy.LoopUnsubstantiate} {
			decision := autonomy.LoopDecision{Action: action, Reason: "unchanged"}
			d.authorizeBehavioralCompletion(context.Background(), &decision)
			if decision.Action != action || decision.Reason != "unchanged" {
				t.Fatalf("action %s was mutated by the behavioral gate", action)
			}
		}
	})

	t.Run("an objective that needs no runtime proof is untouched", func(t *testing.T) {
		d := &Driver{
			prompt:   "explain the architecture of this project",
			behavior: &BehaviorStage{proposer: &repairingProposer{}},
		}
		decision := autonomy.LoopDecision{Action: autonomy.LoopComplete, Reason: "read completed"}
		d.authorizeBehavioralCompletion(context.Background(), &decision)
		if decision.Action != autonomy.LoopComplete {
			t.Fatalf("action = %s, want LoopComplete: a read-only objective must not require a runtime", decision.Action)
		}
	})
}

// TestBehaviorStageProvesTheGoldenObjectiveThroughTheDriver wires the stage the
// way production does and requires it to reach PROVEN on the imperfect fixture.
func TestBehaviorStageProvesTheGoldenObjectiveThroughTheDriver(t *testing.T) {
	root := behaviorFixture(t)
	proposer := &repairingProposer{}

	rt := execution.NewBehavioralRuntime(execution.BehavioralConfig{Root: root})
	defer func() { _ = rt.Close() }()
	stage := &BehaviorStage{
		proposer: proposer,
		bounds:   execution.DefaultBehaviorBounds,
	}

	objective := "inspect this project, redesign it, run it, verify its runtime behavior, repair it and leave it working"
	if !BehaviorRequired(objective) {
		t.Fatal("precondition: the golden objective must demand behavioral proof")
	}

	stage.adapter = &ExecutorAdapter{
		root:     root,
		caps:     behaviorCaps(),
		executor: &execution.RuntimeExecutor{},
	}
	// The adapter needs a live executor for its mutation gate; a zero-value one
	// denies, which is the truthful default and is exercised separately below.
	stage.adapter.executor = newBehaviorExecutor(t, root)

	result := stage.Stage(context.Background(), objective, domain.ScopeDynamic)
	if result.Block != nil {
		t.Fatalf("stage blocked: %s", result.Block.Error())
	}
	if !result.Proven {
		t.Fatalf("stage did not prove the objective: %s", result.DefectLine)
	}
	if result.Repairs == 0 {
		t.Fatal("the golden objective must require at least one repair")
	}
	if proposer.calls == 0 {
		t.Fatal("the reasoning backend was never consulted")
	}

	// The workspace must actually be fixed.
	html, err := os.ReadFile(filepath.Join(root, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(html), `href="style.css"`) || !strings.Contains(string(html), `href="styles.css"`) {
		t.Fatalf("index.html was not repaired:\n%s", html)
	}
}

// TestBehaviorStageReportsUnprovenTruthfully: a declining backend must produce
// an unproven result carrying an attributable reason, never a completion.
func TestBehaviorStageReportsUnprovenTruthfully(t *testing.T) {
	root := behaviorFixture(t)
	rt := execution.NewBehavioralRuntime(execution.BehavioralConfig{Root: root})
	defer func() { _ = rt.Close() }()
	stage := &BehaviorStage{
		proposer: &repairingProposer{decline: true},
		bounds:   execution.LoopBounds{MaxRounds: 1, MaxDefectsPerRound: 1},
		adapter:  &ExecutorAdapter{root: root, caps: behaviorCaps(), executor: newBehaviorExecutor(t, root)},
	}

	result := stage.Stage(context.Background(), "fix it so it works", domain.ScopeDynamic)
	if result.Proven {
		t.Fatal("a declining backend must not produce a proven objective")
	}
	if result.Block == nil {
		t.Fatal("an unproven result must carry a block")
	}
	if result.Block.Class != capability.FailureObjectiveUnproven {
		t.Fatalf("block class = %s, want %s", result.Block.Class, capability.FailureObjectiveUnproven)
	}
	// The evidence must still be retained, so an operator can see what was
	// observed even though nothing was proven.
	if result.EvidenceLine == "" {
		t.Fatal("an unproven result must retain its observation evidence")
	}
}

// TestBehaviorStageBlocksWithoutCapabilities: with no capability set bound there
// is no authority, so the stage refuses instead of assuming read access.
func TestBehaviorStageBlocksWithoutCapabilities(t *testing.T) {
	root := behaviorFixture(t)
	rt := execution.NewBehavioralRuntime(execution.BehavioralConfig{Root: root})
	defer func() { _ = rt.Close() }()
	stage := &BehaviorStage{
		proposer: &repairingProposer{},
		adapter:  &ExecutorAdapter{root: root, executor: newBehaviorExecutor(t, root)},
	}
	result := stage.Stage(context.Background(), "make it work", domain.ScopeDynamic)
	if result.Proven {
		t.Fatal("a stage with no capability set must not prove anything")
	}
	if result.Block == nil || result.Block.Class != capability.FailureAuthorizationBlocked {
		t.Fatalf("block = %+v, want %s", result.Block, capability.FailureAuthorizationBlocked)
	}
}

// TestBehaviorStageBlocksWithoutWiring: a nil adapter or a missing backend is
// CAPABILITY_MISSING, reported rather than skipped.
func TestBehaviorStageBlocksWithoutWiring(t *testing.T) {
	var nilStage *BehaviorStage
	if got := nilStage.Stage(context.Background(), "make it work", domain.ScopeDynamic); got.Proven {
		t.Fatal("a nil stage must not prove anything")
	} else if got.Block == nil || got.Block.Class != capability.FailureCapabilityMissing {
		t.Fatalf("block = %+v, want %s", got.Block, capability.FailureCapabilityMissing)
	}

	stage := &BehaviorStage{adapter: &ExecutorAdapter{root: t.TempDir(), caps: behaviorCaps()}}
	got := stage.Stage(context.Background(), "make it work", domain.ScopeDynamic)
	if got.Proven {
		t.Fatal("a stage with no reasoning backend must not prove anything")
	}
	if got.Block == nil || got.Block.Class != capability.FailureCapabilityMissing {
		t.Fatalf("block = %+v, want CAPABILITY_MISSING", got.Block)
	}
}

// TestBehaviorGateRemovesCompletionWhenUnproven: the driver-level property that
// the behavioral gate downgrades a proposed completion when the workspace cannot
// be observed to satisfy the objective.
func TestBehaviorGateRemovesCompletionWhenUnproven(t *testing.T) {
	root := behaviorFixture(t)
	rt := execution.NewBehavioralRuntime(execution.BehavioralConfig{Root: root})
	defer func() { _ = rt.Close() }()
	d := &Driver{
		prompt: "fix the page so it works",
		bus:    events.NewBus(64),
		behavior: &BehaviorStage{
			proposer: &repairingProposer{decline: true},
			bounds:   execution.LoopBounds{MaxRounds: 1, MaxDefectsPerRound: 1},
			adapter:  &ExecutorAdapter{root: root, caps: behaviorCaps(), executor: newBehaviorExecutor(t, root)},
		},
	}
	decision := autonomy.LoopDecision{Action: autonomy.LoopComplete, Reason: "patch applied"}
	d.authorizeBehavioralCompletion(context.Background(), &decision)

	if decision.Action == autonomy.LoopComplete {
		t.Fatal("a completion must be removed when the objective cannot be behaviorally proven")
	}
	if !strings.Contains(decision.Reason, "BEHAVIORALLY UNPROVEN") {
		t.Fatalf("reason = %q, want it to name the behavioral refusal", decision.Reason)
	}
	if d.lastBehavior.Proven {
		t.Fatal("the driver's retained behavioral result must reflect the unproven verdict")
	}
}

// TestScopeProvenanceMapping: the driver must read the scope it actually
// dispatched under, never assume $prompt.
func TestScopeProvenanceMapping(t *testing.T) {
	cases := []struct {
		scope string
		want  domain.ScopeProvenance
	}{
		{"$prompt", domain.ScopeDynamic},
		{"$PROMPT", domain.ScopeDynamic},
		{"$hot", domain.ScopeDeclared},
		{"", domain.ScopeNone},
		{"bare text", domain.ScopeNone},
	}
	for _, tc := range cases {
		d := &Driver{}
		d.req.Scope = tc.scope
		got := d.scopeProvenance()
		if got != tc.want {
			t.Errorf("scope %q resolved to %d, want %d", tc.scope, got, tc.want)
		}
	}
}

func TestBoundedReason(t *testing.T) {
	long := strings.Repeat("x", 1000)
	got := boundedReason(long)
	if len(got) > 601 {
		t.Fatalf("bounded reason is %d bytes, want it capped", len(got))
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatal("a truncated reason must be marked as truncated")
	}
	if got := boundedReason("short"); got != "short" {
		t.Fatalf("a short reason must pass through unchanged, got %q", got)
	}
}
