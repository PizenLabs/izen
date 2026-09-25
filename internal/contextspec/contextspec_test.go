package contextspec

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/session"
)

// ── Test A — Normal conversation ────────────────────────────────────────────
// Three accepted user turns advance the conversation revision exactly three
// times, and no compilation happens during conversation.
func TestA_ConversationRevisionAdvancesPerUserTurn(t *testing.T) {
	s := session.New()
	if got := s.ConversationRevision(); got != 0 {
		t.Fatalf("new session revision = %d, want 0", got)
	}
	s.AddMessage("user", "one", 50)
	s.AddMessage("assistant", "ack", 50)
	s.AddMessage("user", "two", 50)
	s.AddMessage("user", "three", 50)
	if got := s.ConversationRevision(); got != 3 {
		t.Fatalf("revision after 3 user turns = %d, want 3", got)
	}
}

// ── Test B — Lazy compilation ───────────────────────────────────────────────
// Three /ask turns followed by one hand-off produce exactly ONE compilation.
func TestB_LazyCompilationAtHandoff(t *testing.T) {
	s := session.New()
	s.AddMessage("user", "read @index.html and explain it", 50)
	s.AddMessage("user", "what does the header do?", 50)
	s.AddMessage("user", "and the footer?", 50)

	counter := &countingCompiler{inner: NewRuleCompiler()}
	p := NewPipeline(PipelineOptions{Compiler: counter})

	cs := ConversationStateFromSession(s, "explain the page")
	if _, compiled, err := p.EnsureFresh(context.Background(), cs); err != nil || !compiled {
		t.Fatalf("first EnsureFresh: compiled=%v err=%v", compiled, err)
	}
	if counter.calls != 1 {
		t.Fatalf("compilations after hand-off = %d, want 1", counter.calls)
	}
	// A second turn with no revision change must not recompile.
	if _, compiled, err := p.EnsureFresh(context.Background(), cs); err != nil || compiled {
		t.Fatalf("fresh EnsureFresh: compiled=%v err=%v, want false/nil", compiled, err)
	}
	if counter.calls != 1 {
		t.Fatalf("compilations after fresh check = %d, want 1", counter.calls)
	}
}

// ── Test C — Revision coherence ─────────────────────────────────────────────
// A spec compiled at revision 9 is stale at revision 10, with no semantic
// classifier involved.
func TestC_RevisionCoherence(t *testing.T) {
	s := session.New()
	for i := 0; i < 9; i++ {
		s.AddMessage("user", "turn", 50)
	}
	p := NewPipeline(PipelineOptions{})
	spec, _, err := p.EnsureFresh(context.Background(), ConversationStateFromSession(s, "goal"))
	if err != nil {
		t.Fatal(err)
	}
	if !spec.IsFresh(9) || spec.IsFresh(10) {
		t.Fatalf("spec freshness wrong: rev=%d fresh9=%v fresh10=%v", spec.ConversationRevision, spec.IsFresh(9), spec.IsFresh(10))
	}
	s.AddMessage("user", "turn ten", 50)
	if spec.IsFresh(s.ConversationRevision()) {
		t.Fatal("spec must be stale after the conversation revision advanced")
	}
	next, compiled, err := p.EnsureFresh(context.Background(), ConversationStateFromSession(s, "goal"))
	if err != nil || !compiled {
		t.Fatalf("recompile: compiled=%v err=%v", compiled, err)
	}
	if next.ConversationRevision != 10 {
		t.Fatalf("recompiled spec revision = %d, want 10", next.ConversationRevision)
	}
}

// ── Test D — Semantic correction ────────────────────────────────────────────
// Contradictory historical statements collapse to the active decision.
func TestD_SemanticCorrection(t *testing.T) {
	s := session.New()
	s.AddMessage("user", "Use Tailwind.", 50)
	s.AddMessage("user", "Don't use Tailwind.", 50)
	s.AddMessage("user", "Use existing CSS.", 50)

	p := NewPipeline(PipelineOptions{})
	spec, _, err := p.EnsureFresh(context.Background(), ConversationStateFromSession(s, "style the page"))
	if err != nil {
		t.Fatal(err)
	}
	active := spec.ActiveDecisions()
	if len(active) != 1 || !strings.Contains(active[0].Text, "existing CSS") {
		t.Fatalf("active decisions = %+v, want exactly [existing CSS]", active)
	}
	// Superseded decisions remain as lineage, never as active state.
	if len(spec.Decisions) <= len(active) {
		t.Fatalf("expected superseded lineage retained, got %d total decisions", len(spec.Decisions))
	}
	// The execution payload must contain only the active decision.
	es, err := p.FreezeExecution(context.Background(), ConversationStateFromSession(s, "style the page"), FreezeOptions{Intent: "style the page"})
	if err != nil {
		t.Fatal(err)
	}
	payload := p.ExecutionPayload(es)
	if !strings.Contains(payload, "existing CSS") {
		t.Fatalf("payload missing active decision:\n%s", payload)
	}
	if strings.Contains(payload, "decision: Tailwind") {
		t.Fatalf("payload leaked a superseded decision:\n%s", payload)
	}
}

// ── Test E — CAS rejection ──────────────────────────────────────────────────
// A candidate compiled at revision 10 is discarded when the conversation moves
// to revision 11 before commit; it never overwrites newer state.
func TestE_CASRejection(t *testing.T) {
	s := session.New()
	for i := 0; i < 10; i++ {
		s.AddMessage("user", "old turn", 50)
	}
	// The fake "current revision" advances while the compiler runs.
	current := s.ConversationRevision()
	fake := &racingCompiler{inner: NewRuleCompiler(), after: func() { current = 11 }}
	p := NewPipeline(PipelineOptions{
		Compiler:        fake,
		CurrentRevision: func() uint64 { return current },
	})

	_, _, err := p.EnsureFresh(context.Background(), ConversationStateFromSession(s, "goal"))
	if !errors.Is(err, ErrStaleContextCandidate) {
		t.Fatalf("err = %v, want ErrStaleContextCandidate", err)
	}
	if got := p.Current(); got != nil {
		t.Fatalf("stale candidate overwrote state: %+v", got)
	}
	if fake.calls != 1 {
		t.Fatalf("compiler calls = %d, want 1", fake.calls)
	}
}

// ── Test F — Workspace divergence ───────────────────────────────────────────
// An ExecutionSpec frozen against hash A must refuse execution after the file
// changes to hash B.
func TestF_WorkspaceDivergence(t *testing.T) {
	files := &fakeSnapshot{files: map[string]string{"index.html": "A"}}
	p := NewPipeline(PipelineOptions{Snapshot: files})
	s := session.New()
	s.AddMessage("user", "remove redundant content from @index.html", 50)

	es, err := p.FreezeExecution(context.Background(), ConversationStateFromSession(s, "edit index"), FreezeOptions{
		Intent:         "remove redundant content from @index.html",
		Targets:        []string{"index.html"},
		RequireTargets: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.ValidateFrozenSnapshot(es); err != nil {
		t.Fatalf("unchanged workspace must validate: %v", err)
	}
	files.files["index.html"] = "B"
	if err := p.ValidateFrozenSnapshot(es); !errors.Is(err, ErrStaleWorkspaceSnapshot) {
		t.Fatalf("err = %v, want ErrStaleWorkspaceSnapshot", err)
	}
}

// ── Test I — Raw conversation isolation ─────────────────────────────────────
// An execution payload carries only active semantic state; unrelated raw
// conversation never crosses the boundary.
func TestI_RawConversationIsolation(t *testing.T) {
	const secret = "IRRELEVANT_RAW_CHATTER_9f3a"
	s := session.New()
	s.AddMessage("user", "unrelated small talk: "+secret, 50)
	s.AddMessage("assistant", "ok", 50)
	s.AddMessage("user", "Use existing CSS.", 50)

	p := NewPipeline(PipelineOptions{})
	es, err := p.FreezeExecution(context.Background(), ConversationStateFromSession(s, "style"), FreezeOptions{Intent: "style"})
	if err != nil {
		t.Fatal(err)
	}
	payload := p.ExecutionPayload(es)
	if strings.Contains(payload, secret) {
		t.Fatalf("execution payload leaked raw conversation:\n%s", payload)
	}
	if !strings.Contains(payload, "existing CSS") {
		t.Fatalf("execution payload missing active state:\n%s", payload)
	}
}

// ── Unresolved target / validation ──────────────────────────────────────────
func TestUnresolvedExecutionTarget(t *testing.T) {
	p := NewPipeline(PipelineOptions{})
	s := session.New()
	s.AddMessage("user", "do the thing", 50)
	_, err := p.FreezeExecution(context.Background(), ConversationStateFromSession(s, "do the thing"), FreezeOptions{
		Intent:         "do the thing",
		RequireTargets: true,
	})
	if !errors.Is(err, ErrUnresolvedExecutionTarget) {
		t.Fatalf("err = %v, want ErrUnresolvedExecutionTarget", err)
	}
}

// ── Section 5 example — multi-turn supersession with an explicit reset ───────
func TestD2_NeverMindReset(t *testing.T) {
	s := session.New()
	s.AddMessage("user", "Use Tailwind.", 50)
	s.AddMessage("user", "Don't use Tailwind.", 50)
	s.AddMessage("user", "Actually keep Tailwind only for the modal.", 50)
	s.AddMessage("user", "Never mind, use existing CSS.", 50)

	p := NewPipeline(PipelineOptions{})
	spec, _, err := p.EnsureFresh(context.Background(), ConversationStateFromSession(s, "style"))
	if err != nil {
		t.Fatal(err)
	}
	active := spec.ActiveDecisions()
	if len(active) != 1 || !strings.Contains(active[0].Text, "existing CSS") {
		t.Fatalf("active decisions = %+v, want exactly [existing CSS]", active)
	}
}

// ── Concurrency — CAS under parallel commits ────────────────────────────────
func TestConcurrentCommitCAS(t *testing.T) {
	store := NewStore()
	const current = uint64(10)
	var wg sync.WaitGroup
	var accepted, rejected int64
	var mu sync.Mutex

	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			base := current
			if i%2 == 1 {
				base = current - 1 // stale
			}
			spec := ContextSpec{Version: ContextSpecVersion, ConversationRevision: base, Goal: "g"}
			_, err := store.Commit(CompiledCandidate{BaseConversationRevision: base, Spec: spec}, current, time.Now())
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				accepted++
			case errors.Is(err, ErrStaleContextCandidate):
				rejected++
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if accepted != 16 || rejected != 16 {
		t.Fatalf("accepted=%d rejected=%d, want 16/16", accepted, rejected)
	}
	// Exactly the 16 accepted commits advanced the revision, each by one.
	if got := store.Current().SpecRevision; got != 16 {
		t.Fatalf("final spec revision = %d, want 16", got)
	}
}

// ── test doubles ────────────────────────────────────────────────────────────

type countingCompiler struct {
	inner ContextCompiler
	calls int
}

func (c *countingCompiler) Compile(ctx context.Context, in CompileInput) (CompiledCandidate, error) {
	c.calls++
	return c.inner.Compile(ctx, in)
}

type racingCompiler struct {
	inner ContextCompiler
	after func()
	calls int
}

func (c *racingCompiler) Compile(ctx context.Context, in CompileInput) (CompiledCandidate, error) {
	c.calls++
	out, err := c.inner.Compile(ctx, in)
	if c.after != nil {
		c.after()
	}
	return out, err
}

type fakeSnapshot struct {
	mu    sync.Mutex
	files map[string]string
}

func (f *fakeSnapshot) Observe(targets []string) WorkspaceSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := WorkspaceSnapshot{Targets: append([]string(nil), targets...), Hashes: map[string]string{}}
	cleaned := append([]string(nil), targets...)
	sort.Strings(cleaned)
	var b strings.Builder
	for _, t := range cleaned {
		h := f.files[t]
		out.Hashes[t] = h
		b.WriteString(t)
		b.WriteByte('=')
		b.WriteString(h)
		b.WriteByte(';')
	}
	sum := sha256.Sum256([]byte(b.String()))
	out.Digest = hex.EncodeToString(sum[:])
	return out
}
