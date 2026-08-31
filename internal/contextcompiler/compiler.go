package contextcompiler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/PizenLabs/izen/internal/knowledge"
	"github.com/PizenLabs/izen/internal/session"
)

// ArtifactRef is a bounded descriptor of one artifact the compiler may admit
// (its full bytes are owned by the artifact store, never copied into the
// context).
type ArtifactRef struct {
	Path string
	Size int
}

// Input is the compiled-context source set: Snapshot + Project Knowledge +
// Recent Turns + Workflow State + Artifacts.
type Input struct {
	// SnapshotID is the frozen execution context snapshot identity (sealed by
	// the RuntimeExecutor). Empty when no snapshot is attached.
	SnapshotID string
	// UserRequest is the current user request (highest priority).
	UserRequest string
	// WorkflowState is the active workflow phase/direction.
	WorkflowState string
	// RecentTurns is the recent conversation window.
	RecentTurns []session.Message
	// SessionCompact is the active session's compact context generation.
	SessionCompact *session.CompactContext
	// Artifacts is the relevant artifact surface.
	Artifacts []ArtifactRef
	// Knowledge is the relevant project knowledge chunks. The compiler selects
	// the highest-confidence chunks that fit the budget — it never dumps the
	// whole store, and it never reads a monolithic summary.
	Knowledge []knowledge.Asset
}

// Section is one admitted, budget-fitted context block.
type Section struct {
	Source    Source
	Header    string
	Content   string
	Tokens    int
	Truncated bool
}

// CompiledContext is the budget-fitted outcome of one compilation: ordered
// sections plus the telemetry describing how the budget was enforced.
type CompiledContext struct {
	Sections   []Section
	Budget     Budget
	UsedTokens int
	Truncated  bool
	Dropped    int
	CacheHit   bool
	CompiledAt time.Time
}

// Assemble renders the compiled sections into a prompt-ready block.
func (c *CompiledContext) Assemble() string {
	if c == nil {
		return ""
	}
	var b strings.Builder
	for i, s := range c.Sections {
		if i > 0 {
			b.WriteString("\n\n")
		}
		if s.Header != "" {
			b.WriteString("### " + s.Header + "\n")
		}
		b.WriteString(s.Content)
	}
	return b.String()
}

// Compiler is the runtime context compiler. It admits inputs in priority order
// under a strict token budget and caches the result keyed on a fingerprint of
// the underlying state: the context is RE-COMPILED ONLY when an underlying
// state mutation changes the fingerprint.
type Compiler struct {
	maxTokens  int
	shares     map[Source]int
	order      []Source
	cacheLimit int

	mu    sync.Mutex
	cache map[string]*CompiledContext
}

// Option configures a Compiler.
type Option func(*Compiler)

// WithMaxTokens caps the compiled context window (<=0 falls back to the
// default).
func WithMaxTokens(n int) Option {
	return func(c *Compiler) {
		if n > 0 {
			c.maxTokens = n
		}
	}
}

// WithShares overrides the per-source allocation table.
func WithShares(shares map[Source]int) Option {
	return func(c *Compiler) {
		if len(shares) > 0 {
			c.shares = shares
		}
	}
}

// WithCacheLimit bounds the fingerprint cache. A hit avoids re-compilation
// while the underlying state is unchanged.
func WithCacheLimit(n int) Option {
	return func(c *Compiler) {
		if n > 0 {
			c.cacheLimit = n
		}
	}
}

// New returns a context compiler with default budgets and an empty cache.
func New(opts ...Option) *Compiler {
	c := &Compiler{
		maxTokens:  DefaultMaxTokens,
		shares:     cloneShares(defaultShares),
		order:      append([]Source(nil), priorityOrder...),
		cacheLimit: 64,
		cache:      make(map[string]*CompiledContext),
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Compile assembles the budget-fitted context for one input. It returns the
// cached result when the underlying state fingerprint is unchanged (re-compile
// only on mutation), otherwise compiles fresh and caches.
func (c *Compiler) Compile(_ context.Context, in Input) (*CompiledContext, error) {
	fp := c.fingerprint(in)

	c.mu.Lock()
	if cached, ok := c.cache[fp]; ok {
		out := *cached
		out.CacheHit = true
		c.mu.Unlock()
		return &out, nil
	}
	c.mu.Unlock()

	budget := Allocate(c.maxTokens, c.shares)
	out := &CompiledContext{Budget: budget, CompiledAt: time.Now()}
	usedBySource := make(map[Source]int)

	for _, src := range c.order {
		share := budget.Source(src)
		// Build the source's candidate content and fit it to its share.
		content, dropped := c.fitSource(src, in, share, usedBySource)
		if content == "" {
			out.Dropped += dropped
			continue
		}
		toks := EstimateTokens(content)
		truncated := false
		if toks > share {
			content = truncateToTokens(content, share)
			toks = EstimateTokens(content)
			truncated = true
		}
		usedBySource[src] = toks
		out.Sections = append(out.Sections, Section{
			Source:    src,
			Header:    headerFor(src),
			Content:   content,
			Tokens:    toks,
			Truncated: truncated,
		})
		out.UsedTokens += toks
		out.Dropped += dropped
		out.Truncated = out.Truncated || truncated
	}

	c.mu.Lock()
	if len(c.cache) >= c.cacheLimit {
		c.cache = make(map[string]*CompiledContext)
	}
	c.cache[fp] = out
	c.mu.Unlock()

	return out, nil
}

// fitSource renders the source's candidate content and reports how many items
// were dropped because they did not fit the source share. Lower-priority
// sources are the first to lose items — project knowledge (granular chunks)
// is selected greedily by confidence so the most valuable chunks survive.
func (c *Compiler) fitSource(src Source, in Input, share int, used map[Source]int) (string, int) {
	switch src {
	case SourceUserRequest:
		return in.UserRequest, 0
	case SourceWorkflow:
		return in.WorkflowState, 0
	case SourceRecentTurns:
		return renderTurns(in.RecentTurns, share), 0
	case SourceSessionCompact:
		return renderCompact(in.SessionCompact), 0
	case SourceArtifacts:
		return renderArtifacts(in.Artifacts), 0
	case SourceProjectKnowledge:
		return selectKnowledge(in.Knowledge, share)
	default:
		return "", 0
	}
}

// selectKnowledge greedily admits the highest-confidence knowledge chunks that
// fit the source share, dropping the rest. This is the granular, addressable
// retrieval path (INV-SESSION-15) — no monolithic summary is ever rendered.
func selectKnowledge(assets []knowledge.Asset, share int) (string, int) {
	sorted := append([]knowledge.Asset(nil), assets...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Confidence != sorted[j].Confidence {
			return sorted[i].Confidence > sorted[j].Confidence
		}
		return sorted[i].ID < sorted[j].ID
	})
	var b strings.Builder
	used := 0
	dropped := 0
	for _, a := range sorted {
		block := fmt.Sprintf("[%s] %s: %s", a.Kind, a.Title, a.Body)
		toks := EstimateTokens(block)
		if share > 0 && used+toks > share {
			dropped++
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(block)
		used += toks
	}
	return b.String(), dropped
}

// renderTurns renders the most recent turns that fit the share, newest first.
func renderTurns(msgs []session.Message, share int) string {
	var b strings.Builder
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		line := m.Role + ": " + m.Content
		toks := EstimateTokens(line)
		if share > 0 && EstimateTokens(b.String())+toks > share {
			break
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(line)
	}
	return b.String()
}

func renderCompact(cc *session.CompactContext) string {
	if cc == nil {
		return ""
	}
	var b strings.Builder
	if cc.Objective != "" {
		b.WriteString("objective: " + cc.Objective)
	}
	if cc.Summary != "" {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString("summary: " + cc.Summary)
	}
	// Workspace boundary guard: surface uncommitted changes left by another
	// session so the model never silently overwrites them.
	if len(cc.DirtyFiles) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString("uncommitted workspace changes (from a previous session): " +
			strings.Join(cc.DirtyFiles, ", "))
	}
	for _, m := range cc.Recent {
		b.WriteString("\n" + m.Role + ": " + m.Content)
	}
	return b.String()
}

func renderArtifacts(refs []ArtifactRef) string {
	parts := make([]string, 0, len(refs))
	for _, r := range refs {
		parts = append(parts, fmt.Sprintf("%s (%d bytes)", r.Path, r.Size))
	}
	return strings.Join(parts, "\n")
}

// fingerprint is a SHA-256 over the canonical encoding of every underlying
// state input. Any state mutation changes the fingerprint and forces a
// re-compile; an unchanged fingerprint serves the cached compilation.
func (c *Compiler) fingerprint(in Input) string {
	var b strings.Builder
	write := func(s string) {
		fmt.Fprintf(&b, "%d:%s\x00", len(s), s)
	}
	write(in.SnapshotID)
	write(in.UserRequest)
	write(in.WorkflowState)
	write(fmt.Sprintf("%d", len(in.RecentTurns)))
	for _, m := range in.RecentTurns {
		write(m.Role + ":" + m.Content)
	}
	if in.SessionCompact != nil {
		write(fmt.Sprintf("%d", in.SessionCompact.Generation))
		write(in.SessionCompact.Summary)
		write(in.SessionCompact.Objective)
		write(fmt.Sprintf("%d", len(in.SessionCompact.Recent)))
		write(fmt.Sprintf("%d", len(in.SessionCompact.DirtyFiles)))
		for _, d := range in.SessionCompact.DirtyFiles {
			write(d)
		}
	} else {
		write("")
	}
	write(fmt.Sprintf("%d", len(in.Artifacts)))
	for _, a := range in.Artifacts {
		write(a.Path)
		write(fmt.Sprintf("%d", a.Size))
	}
	write(fmt.Sprintf("%d", len(in.Knowledge)))
	for _, a := range in.Knowledge {
		write(a.ID)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

func truncateToTokens(s string, budget int) string {
	if EstimateTokens(s) <= budget {
		return s
	}
	runes := []rune(s)
	maxChars := budget * 4
	if maxChars >= len(runes) {
		return s
	}
	return string(runes[:maxChars]) + "…"
}

func headerFor(src Source) string {
	switch src {
	case SourceUserRequest:
		return "USER REQUEST"
	case SourceWorkflow:
		return "WORKFLOW STATE"
	case SourceRecentTurns:
		return "RECENT TURNS"
	case SourceSessionCompact:
		return "SESSION COMPACT"
	case SourceArtifacts:
		return "ARTIFACTS"
	case SourceProjectKnowledge:
		return "PROJECT KNOWLEDGE"
	default:
		return string(src)
	}
}

func cloneShares(m map[Source]int) map[Source]int {
	out := make(map[Source]int, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
