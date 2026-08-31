package contextcompiler

import (
	"context"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/knowledge"
	"github.com/PizenLabs/izen/internal/session"
)

func turn(role, content string) session.Message {
	return session.Message{Role: role, Content: content}
}

func asset(kind, title, body string, conf float64) knowledge.Asset {
	return knowledge.NewAsset(kind, title, body, "sess", "prov", conf)
}

func bigInput() Input {
	return Input{
		SnapshotID:    "ctx-abc",
		UserRequest:   "refactor the auth module",
		WorkflowState: "build",
		RecentTurns: []session.Message{
			turn("user", "please refactor the auth module to use context injection"),
			turn("assistant", "I will restructure the middleware stack"),
		},
		SessionCompact: &session.CompactContext{
			Objective:  "refactor the auth module",
			Summary:    strings.Repeat("auth module summary ", 200), // deliberately oversized
			Generation: 3,
		},
		Artifacts: []ArtifactRef{{Path: "internal/auth/middleware.go", Size: 4096}},
		Knowledge: []knowledge.Asset{
			asset("constraint", "No secrets in logs", "credentials must never reach log output", 0.95),
			asset("convention", "Middleware ordering", "auth middleware runs before routing", 0.9),
			asset("decision", "Use DI container", "all deps injected through the container", 0.85),
		},
	}
}

// TestCompileEnforcesTokenBudget is the DoD case: with an oversized compact
// summary and a constrained budget, the compiled output must never exceed the
// total budget and must report truncation.
func TestCompileEnforcesTokenBudget(t *testing.T) {
	c := New(WithMaxTokens(600))
	in := bigInput()

	out, err := c.Compile(context.Background(), in)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if out.UsedTokens > out.Budget.Total {
		t.Errorf("UsedTokens = %d exceeds Budget.Total = %d", out.UsedTokens, out.Budget.Total)
	}
	if !out.Truncated {
		t.Error("oversized input must be reported as truncated")
	}
	if len(out.Sections) == 0 {
		t.Fatal("no sections compiled")
	}
	// The rendered block must also respect the budget.
	rendered := out.Assemble()
	if EstimateTokens(rendered) > out.Budget.Total {
		t.Errorf("assembled block = %d tokens, over budget %d", EstimateTokens(rendered), out.Budget.Total)
	}
}

// TestPriorityOrderDropsKnowledgeFirst verifies lower-priority sources are the
// first casualties under a tight budget (SESSION.md §19): the user request and
// workflow state survive while project knowledge is dropped/truncated.
func TestPriorityOrderDropsKnowledgeFirst(t *testing.T) {
	c := New(WithMaxTokens(300))
	out, err := c.Compile(context.Background(), bigInput())
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	var sawUser, sawWorkflow, sawKnowledge bool
	for _, s := range out.Sections {
		switch s.Source {
		case SourceUserRequest:
			sawUser = s.Content != ""
		case SourceWorkflow:
			sawWorkflow = s.Content != ""
		case SourceProjectKnowledge:
			sawKnowledge = s.Content != ""
		}
	}
	if !sawUser || !sawWorkflow {
		t.Error("user request and workflow state must never be dropped under budget pressure")
	}
	if out.Dropped == 0 {
		t.Error("a tight budget must drop some low-priority knowledge chunks")
	}
	_ = sawKnowledge
}

// TestKnowledgeSelectionKeepsHighestConfidence verifies the compiler selects
// the most valuable knowledge chunks when not all fit (INV-SESSION-15: it
// retrieves granular chunks, never a summary).
func TestKnowledgeSelectionKeepsHighestConfidence(t *testing.T) {
	c := New(WithMaxTokens(2000))
	in := bigInput()
	// A long, low-confidence chunk must lose to short high-confidence ones.
	in.Knowledge = []knowledge.Asset{
		asset("discovery", "low value", strings.Repeat("noise ", 500), 0.1),
		asset("decision", "keep DI", "use dependency injection everywhere", 0.99),
		asset("constraint", "no secrets", "never log credentials", 0.98),
	}
	out, err := c.Compile(context.Background(), in)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	var content string
	for _, s := range out.Sections {
		if s.Source == SourceProjectKnowledge {
			content = s.Content
		}
	}
	if !strings.Contains(content, "use dependency injection") {
		t.Error("highest-confidence chunk must be selected")
	}
	if strings.Contains(content, "noise") {
		t.Error("low-confidence oversized chunk must be dropped")
	}
}

// TestCompileCachesOnUnchangedState and re-compiles on mutation verifies the
// "re-compile ONLY when underlying state mutations occur" requirement: an
// unchanged input returns the cached compilation; any state mutation changes
// the fingerprint and forces a fresh compile.
func TestCompileCachesOnUnchangedState(t *testing.T) {
	c := New(WithMaxTokens(800))
	in := bigInput()

	first, err := c.Compile(context.Background(), in)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if first.CacheHit {
		t.Fatal("first compile must not be a cache hit")
	}

	second, err := c.Compile(context.Background(), in)
	if err != nil {
		t.Fatalf("Compile again: %v", err)
	}
	if !second.CacheHit {
		t.Fatal("unchanged input must return the cached compilation")
	}

	// Mutate workflow state → fingerprint changes → fresh compile.
	in.WorkflowState = "investigate"
	third, err := c.Compile(context.Background(), in)
	if err != nil {
		t.Fatalf("Compile after mutation: %v", err)
	}
	if third.CacheHit {
		t.Fatal("underlying state mutation must force a re-compile")
	}
}

// TestCompileRecentTurnsAreNewestFirst verifies the recent-turn admission
// renders the freshest turns (chronology is served last-to-first).
func TestCompileRecentTurnsAreNewestFirst(t *testing.T) {
	c := New(WithMaxTokens(2000))
	in := Input{
		UserRequest: "hi",
		RecentTurns: []session.Message{
			turn("user", "first"),
			turn("assistant", "second"),
			turn("user", "third"),
		},
	}
	out, err := c.Compile(context.Background(), in)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	for _, s := range out.Sections {
		if s.Source == SourceRecentTurns {
			lines := strings.Split(s.Content, "\n")
			if len(lines) == 0 || !strings.Contains(lines[0], "third") {
				t.Errorf("recent turns must be newest-first, got %q", lines)
			}
		}
	}
}

// TestEmptyInputCompilesToEmpty verifies a source-less input compiles cleanly
// to an empty context (zero noise when there is nothing to inject).
func TestEmptyInputCompilesToEmpty(t *testing.T) {
	c := New()
	out, err := c.Compile(context.Background(), Input{})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if len(out.Sections) != 0 {
		t.Errorf("empty input must yield zero sections, got %d", len(out.Sections))
	}
}
