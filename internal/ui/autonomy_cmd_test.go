package ui

import (
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/modes"
)

// ── Event projection ─────────────────────────────────────────────────────────

func TestHandleAutonomyEventProjection(t *testing.T) {
	tests := []struct {
		name string
		ev   events.DomainEvent
		want string
	}{
		{"autonomy decision auto_continue", events.NewAutonomyDecision("auto_continue", "modification", 0.9, "build", "low", nil, "mutation granted"),
			"[autonomy] ▶ auto_continue → workspace build (risk low, 90%): mutation granted"},
		{"autonomy decision ask_user with missing caps", events.NewAutonomyDecision("ask_user", "modification", 0.9, "build", "low", []string{"mutate"}, "capability not granted"),
			"[autonomy] ◈ ask_user → workspace build (risk low, 90%): capability not granted\n[autonomy] requesting capability: mutate"}, {"autonomy decision block", events.NewAutonomyDecision("block", "modification", 0.9, "build", "critical", nil, "no rollback"),
			"[autonomy] ■ block → workspace build (risk critical, 90%): no rollback"},
		{"autonomy decision direct response", events.NewAutonomyDecision("direct_response", "conversation", 0.95, "", "low", nil, "conversation intent"),
			"[autonomy] ◇ direct response — no workspace"},
		{"capability granted", events.NewCapabilityGranted("grant-1", "repository", []string{"read", "mutate"}, ""),
			"[grant] grant-1: read+mutate granted for repository (expires never)"},
		{"loop transition", events.NewLoopTransition("plan", "build", "capability_granted", "build authorized"),
			"[loop] plan → build (capability_granted): build authorized"},
		{"loop transition diagnose", events.NewLoopTransition("verify", "diagnose", "verify_failed", "verification failed"),
			"[loop] verify → diagnose (verify_failed): verification failed"},
		{"context compiled", events.NewContextCompiled("index.html", "html", 3),
			"[context] compiled index.html (html): 3 finding(s)"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := &model{}
			m.handleDomainEvent(tc.ev)
			if len(m.records) == 0 {
				t.Fatal("no records produced")
			}
			parts := make([]string, 0, len(m.records))
			for _, r := range m.records {
				if r.role != roleActivity {
					t.Errorf("role = %v, want roleActivity", r.role)
				}
				parts = append(parts, r.text)
			}
			got := strings.Join(parts, "\n")
			if got != tc.want {
				t.Errorf("record = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestHandleAutonomyMultiLineProjection(t *testing.T) {
	m := &model{}
	m.handleDomainEvent(events.NewAutonomyDecision("ask_user", "modification", 0.9, "build", "low", []string{"mutate"}, "grant needed"))
	if len(m.records) != 2 {
		t.Fatalf("got %d records, want 2 (decision + missing caps)", len(m.records))
	}
}

// ── $decide command ─────────────────────────────────────────────────────────

func TestAutonomyMarker(t *testing.T) {
	cases := []struct {
		d     autonomy.Decision
		mark  string
		label string
	}{
		{autonomy.DecisionAutoContinue, "▶ ", "auto_continue"},
		{autonomy.DecisionAskUser, "◈ ", "ask_user"},
		{autonomy.DecisionBlock, "■ ", "block"},
		{autonomy.DecisionDirectResponse, "◇ ", "direct_response"},
	}
	for _, c := range cases {
		mark, label := autonomyMarker(c.d)
		if mark != c.mark || label != c.label {
			t.Errorf("autonomyMarker(%s) = (%q,%q), want (%q,%q)", c.d, mark, label, c.mark, c.label)
		}
	}
}

func TestNewAutonomyLoopPreview(t *testing.T) {
	if got := NewAutonomyLoopPreview(autonomy.IntentModification); strings.Join(got, "→") != "investigate→plan→build→verify→diagnose ↺" {
		t.Errorf("mutation loop preview = %v", got)
	}
	if got := NewAutonomyLoopPreview(autonomy.IntentConversation); got != nil {
		t.Errorf("conversation loop preview = %v, want nil", got)
	}
}

func TestRunAutonomyDecideCmdWired(t *testing.T) {
	m := &model{autonomy: autonomy.NewEngine(autonomy.WithScope("repository"))}
	cmd := m.runAutonomyDecideCmd("remove unused content from @index.html")
	if cmd != nil {
		t.Error("decide command must be synchronous (nil cmd)")
	}
	if len(m.records) == 0 {
		t.Fatal("decide must produce output records")
	}
	joined := ""
	for _, r := range m.records {
		joined += r.text + "\n"
	}
	if !strings.Contains(joined, "AUTONOMY DECISION") {
		t.Errorf("output missing decision banner: %q", joined)
	}
	if !strings.Contains(joined, "modification") {
		t.Errorf("output missing modification intent: %q", joined)
	}
	if !strings.Contains(joined, "ask_user") || !strings.Contains(joined, "needs grant") {
		t.Errorf("pre-grant decision must ask for capability: %q", joined)
	}
}

func TestRunAutonomyDecideCmdConversation(t *testing.T) {
	m := &model{autonomy: autonomy.NewEngine(autonomy.WithScope("repository"))}
	m.runAutonomyDecideCmd("hi")
	joined := ""
	for _, r := range m.records {
		joined += r.text + "\n"
	}
	if !strings.Contains(joined, "direct_response") {
		t.Errorf("conversation must yield direct_response: %q", joined)
	}
	if !strings.Contains(joined, "no execution workspace") {
		t.Errorf("conversation must note no workspace: %q", joined)
	}
}

func TestRunAutonomyDecideCmdUnwired(t *testing.T) {
	m := &model{}
	m.runAutonomyDecideCmd("inspect @file.go")
	if len(m.records) == 0 || !strings.Contains(m.records[0].text, "not wired") {
		t.Errorf("unwired decide must report not wired: %q", m.records[0].text)
	}
}

// ── /grant DEPRECATED ───────────────────────────────────────────────────

// TestHandleAutonomyGrantDeprecatedSeam verifies the deprecated /grant handler
// still works as an internal compatibility seam but is no longer a required
// user-facing command: it issues the BUILD capability vector and records the
// deprecation notice. The autonomy proposal (ask_user → Execute) is the only
// user-facing authorization path.
func TestHandleAutonomyGrant(t *testing.T) {
	m := &model{autonomy: autonomy.NewEngine(autonomy.WithScope("repository"))}
	m.handleAutonomyGrant("")
	if len(m.records) == 0 {
		t.Fatal("grant must produce output")
	}
	joined := ""
	for _, r := range m.records {
		joined += r.text + "\n"
	}
	if !strings.Contains(joined, "Capability granted") || !strings.Contains(joined, "mutate") {
		t.Errorf("grant output missing capability: %q", joined)
	}
	// After the grant, the same mutation request must auto-continue.
	trace := m.autonomy.Decide("remove unused content from @index.html")
	if trace.Decision.Decision != autonomy.DecisionAutoContinue {
		t.Errorf("post-grant decision = %s, want auto_continue", trace.Decision.Decision)
	}
}

func TestHandleAutonomyGrantUnwired(t *testing.T) {
	m := &model{}
	m.handleAutonomyGrant("")
	if len(m.records) == 0 || !strings.Contains(m.records[0].text, "not wired") {
		t.Errorf("unwired grant must report not wired: %q", m.records[0].text)
	}
}

// TestGrantTokenNotARegistryCommand pins requirement E: /grant is NOT a
// registry command and cannot be parsed as a user-facing command through the
// parser pipeline — authorization is internal to the autonomy proposal.
func TestGrantTokenNotARegistryCommand(t *testing.T) {
	m := newTestModel()
	m.resolver.Set(modes.ModeAsk)
	cmd := m.handleInput("/grant")
	if cmd != nil {
		t.Fatalf("/grant must not dispatch a command, got %T", cmd)
	}
	found := false
	for _, r := range m.records {
		if strings.Contains(r.text, "unknown command") && strings.Contains(r.text, "grant") {
			found = true
		}
	}
	if !found {
		t.Error("expected /grant to be rejected as an unknown command")
	}
}

// TestAutonomyProposalActions verifies the proposal carries the planned
// high-level actions for a mutation intent (requirement 1).
func TestAutonomyProposalActions(t *testing.T) {
	tr := autonomy.Trace{
		Input: "remove redundant content from @index.html",
		Intent: autonomy.IntentResult{
			Intent:   autonomy.IntentModification,
			Required: autonomy.RequiredCapabilities(autonomy.IntentModification),
		},
	}
	prop := tr.Proposal()
	if len(prop.Actions) == 0 {
		t.Fatal("mutation proposal must list planned high-level actions")
	}
	joined := strings.Join(prop.Actions, "|")
	for _, want := range []string{"inspect target", "apply mutation", "verify"} {
		if !strings.Contains(joined, want) {
			t.Errorf("proposal actions missing %q: %v", want, prop.Actions)
		}
	}
}
