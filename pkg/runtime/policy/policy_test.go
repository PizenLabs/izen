package policy

import (
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/izen/pkg/runtime/analyzer"
	"github.com/PizenLabs/izen/pkg/runtime/registry"
)

func sampleFacts() *analyzer.Facts {
	return &analyzer.Facts{
		Root:          "/ws",
		Intent:        analyzer.IntentBugFix,
		TargetFiles:   []string{"a.go", "b.go"},
		Files:         2,
		TokenEstimate: 800,
		MaxFanout:     3,
		GeneratedAt:   time.Now(),
	}
}

func TestEvaluatorGrants(t *testing.T) {
	e := New(Rule{
		ID:          "r1",
		Description: "allow standard work",
		When:        Matcher{Intents: []analyzer.Intent{analyzer.IntentBugFix}},
		Allow:       []string{"strategy:patch", "capability:coding", "tool_use"},
		Reason:      "standard bug fix work",
	})
	d := e.Evaluate(sampleFacts())
	if !d.StrategyGranted("patch") {
		t.Error("strategy patch should be granted")
	}
	if !d.CapabilityGranted(registry.CapabilityCoding) {
		t.Error("capability coding should be granted")
	}
	if !d.CapabilityGranted(registry.CapabilityToolUse) {
		t.Error("bare tool_use should be granted as capability")
	}
	if !d.ApprovedFor("patch", []registry.Capability{registry.CapabilityCoding}) {
		t.Error("ApprovedFor should pass for granted strategy+caps")
	}
	if len(d.RuleVerdicts) != 1 || !d.RuleVerdicts[0].Applied {
		t.Errorf("verdicts = %v, want one applied verdict", d.RuleVerdicts)
	}
}

func TestDenyWinsOverAllow(t *testing.T) {
	e := New(
		Rule{ID: "allow", When: Matcher{}, Allow: []string{"tool_use"}, Reason: "allow"},
		Rule{ID: "deny", When: Matcher{MaxTokens: 1000}, Deny: []string{"capability:tool_use"}, Reason: "too big"},
	)
	d := e.Evaluate(sampleFacts())
	if d.CapabilityGranted(registry.CapabilityToolUse) {
		t.Error("deny should win over allow")
	}
	if d.ApprovedFor("any", []registry.Capability{registry.CapabilityToolUse}) {
		t.Error("ApprovedFor should fail when capability denied")
	}
}

func TestMatcherDoesNotApply(t *testing.T) {
	e := New(Rule{
		ID:     "r1",
		When:   Matcher{Intents: []analyzer.Intent{analyzer.IntentFeature}},
		Allow:  []string{"patch"},
		Reason: "feature only",
	})
	d := e.Evaluate(sampleFacts())
	if d.StrategyGranted("patch") {
		t.Error("strategy should not be granted when intent does not match")
	}
	if len(d.RuleVerdicts) != 1 || d.RuleVerdicts[0].Applied {
		t.Error("verdict should record the rule as not applied")
	}
}

func TestMatcherConstraints(t *testing.T) {
	big := sampleFacts()
	big.Files = 5000
	big.TokenEstimate = 1_000_000
	m := Matcher{MaxFiles: 1000, MaxTokens: 500_000}
	if m.Matches(big) {
		t.Error("matcher should reject oversized facts")
	}
	if !m.Matches(sampleFacts()) {
		t.Error("matcher should accept small facts")
	}
	noTargets := sampleFacts()
	noTargets.TargetFiles = nil
	has := true
	noHas := Matcher{HasTargets: &has}
	if noHas.Matches(noTargets) {
		t.Error("HasTargets=true should reject facts without targets")
	}
}

func TestMatcherFanoutAndTokenBounds(t *testing.T) {
	lowFanout := sampleFacts()
	lowFanout.MaxFanout = 2
	lowFanout.TokenEstimate = 5_000
	if !(Matcher{MaxFanout: 4}).Matches(lowFanout) {
		t.Error("max_fanout 4 should accept fanout 2")
	}
	highFanout := sampleFacts()
	highFanout.MaxFanout = 6
	if (Matcher{MaxFanout: 4}).Matches(highFanout) {
		t.Error("max_fanout 4 should reject fanout 6")
	}
	if !(Matcher{MinFanout: 4}).Matches(highFanout) {
		t.Error("min_fanout 4 should accept fanout 6")
	}
	if (Matcher{MinFanout: 4}).Matches(lowFanout) {
		t.Error("min_fanout 4 should reject fanout 2")
	}
	if !(Matcher{MinTokens: 25_000}).Matches(&analyzer.Facts{TokenEstimate: 30_000}) {
		t.Error("min_tokens should accept a token estimate above the floor")
	}
	if (Matcher{MinTokens: 25_000}).Matches(&analyzer.Facts{TokenEstimate: 1_000}) {
		t.Error("min_tokens should reject a token estimate below the floor")
	}
}

func TestSummaryReadable(t *testing.T) {
	e := New(Rule{
		ID:          "r1",
		Description: "allow standard work",
		When:        Matcher{},
		Allow:       []string{"strategy:patch"},
		Reason:      "standard work is allowed",
	})
	lines := e.Evaluate(sampleFacts()).Summary()
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "grant strategy patch") {
		t.Errorf("summary missing grant line: %q", joined)
	}
	if !strings.Contains(joined, "r1 applied") {
		t.Errorf("summary missing rule verdict line: %q", joined)
	}
}

func TestLoadRulesBytes(t *testing.T) {
	doc := `
rules:
  - id: workspace.allow_coding
    description: allow coding for small bug fixes
    when:
      intents: [bug_fix, feature]
      max_files: 200
      max_tokens: 100000
    allow:
      - capability:coding
      - strategy:patch
    deny:
      - strategy:bleed
    reason: workspace is small enough for direct edits
`
	rules, err := LoadRulesBytes([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 {
		t.Fatalf("rules = %d, want 1", len(rules))
	}
	e := New(rules...)
	d := e.Evaluate(sampleFacts())
	if !d.CapabilityGranted(registry.CapabilityCoding) {
		t.Error("yaml rule should grant coding")
	}
	if !d.StrategyGranted("patch") || d.StrategyGranted("bleed") {
		t.Error("yaml rule grants/denies strategies incorrectly")
	}
}

func TestLoadRulesErrors(t *testing.T) {
	if _, err := LoadRulesBytes([]byte(`rules:
  - id: bad
    when:
      intents: [not_an_intent]
`)); err == nil {
		t.Error("unknown intent should fail to load")
	}
	if _, err := LoadRulesBytes([]byte("rules:\n  - id: x\n    allow: [\n")); err == nil {
		t.Error("invalid yaml should fail to load")
	}
	if _, err := LoadRulesBytes([]byte("rules:\n  - when:\n      max_files: 5\n")); err == nil {
		t.Error("rule without id should fail to load")
	}
}
