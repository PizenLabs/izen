package strategy

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// ContextSource is the authoritative owner of a context item. Every item in an
// envelope must name its source so $inspect can answer "why did Izen read this
// file?" and "where did this context come from?".
type ContextSource string

const (
	SourceUser       ContextSource = "user"
	SourceDirective  ContextSource = "directive_contract"
	SourceParser     ContextSource = "parser"
	SourceWorkspace  ContextSource = "workspace"
	SourceFilesystem ContextSource = "filesystem"
	SourceFileGraph  ContextSource = "file_graph"
	SourceSession    ContextSource = "session"
	SourceLedger     ContextSource = "ledger"
	SourceProvider   ContextSource = "provider_usage"
	SourceEngine     ContextSource = "engine"
)

// Label returns the compact source label.
func (s ContextSource) Label() string { return string(s) }

// ContextItem is one context channel with its ownership and inclusion reason.
// It is the unit the context compiler emits: nothing crosses to a model
// without an owner, a source, a relevance and a reason for inclusion.
type ContextItem struct {
	// Kind is the context channel.
	Kind ContextKind
	// Owner is "engine" when Izen supplies the evidence deterministically and
	// "model" when the item is a model-produced artifact. Context ownership is
	// the engine's job — the model never owns filesystem or lifecycle facts.
	Owner string
	// Source names where the item came from.
	Source ContextSource
	// Relevance is the concrete role the item plays in the decision.
	Relevance string
	// Authority is the evidence grade (deterministic / workspace-verified).
	Authority string
	// ReasonForInclusion is why the compiler kept this item.
	ReasonForInclusion string
	// Content is the item payload (paths, sizes, or bounded content). Full
	// file bytes are captured only for small files; large targets record their
	// size so the account stays truthful without duplicating megabytes.
	Content string
}

// ContextEnvelope is the minimum-sufficient context account for one operation.
// It is the engine's answer to "what context does this execution require" and
// grows ONLY when evidence demands it (context escalation), never because a
// framework assumes it should.
type ContextEnvelope struct {
	// Items is the ordered context channel set.
	Items []ContextItem
	// CompiledAt is when the envelope was assembled.
	CompiledAt time.Time
	// Expanded reports whether the envelope was expanded beyond the initial
	// sufficient set.
	Expanded bool
	// ExpansionReason names why expansion was justified.
	ExpansionReason string
}

// Has reports whether an item of the given kind is present.
func (e ContextEnvelope) Has(k ContextKind) bool {
	for _, it := range e.Items {
		if it.Kind == k {
			return true
		}
	}
	return false
}

// ItemCount returns the number of context items.
func (e ContextEnvelope) ItemCount() int { return len(e.Items) }

// ItemOf returns the first item of the given kind (zero value when absent).
func (e ContextEnvelope) ItemOf(k ContextKind) (ContextItem, bool) {
	for _, it := range e.Items {
		if it.Kind == k {
			return it, true
		}
	}
	return ContextItem{}, false
}

// String renders the envelope compactly for $inspect.
func (e ContextEnvelope) String() string {
	if len(e.Items) == 0 {
		return "context: none"
	}
	var b strings.Builder
	b.WriteString("context:")
	for _, it := range e.Items {
		b.WriteString(" " + it.Kind.Label())
		b.WriteString("(" + it.Owner + "/" + it.Source.Label())
		if it.Content != "" {
			b.WriteString(":" + it.Content)
		}
		b.WriteString(")")
	}
	if e.Expanded {
		b.WriteString(" expanded=" + e.ExpansionReason)
	}
	return b.String()
}

// maxInlineContentBytes bounds how much target content the compiler captures
// into the account. Larger files record their size only — the provider path
// (hotfix block resolution) supplies the actual bounded content.
const maxInlineContentBytes = 8 * 1024

// Compiler assembles the minimum-sufficient ContextEnvelope for a strategy
// profile. It is a strict compiler, not a dumper: it adds a context item only
// when the strategy's decision record requires it, and it never includes
// conversation history, repository scans, or tool schemas unless the strategy
// explicitly demands them.
type Compiler struct {
	deps Deps
}

// NewCompiler returns a compiler over the given deterministic inputs.
func NewCompiler(deps Deps) *Compiler { return &Compiler{deps: deps} }

// Compile builds the envelope for a profile. The profile.ContextKinds is the
// required channel set; the compiler maps each kind onto an owned item.
func (c *Compiler) Compile(p ExecutionStrategyProfile) ContextEnvelope {
	env := ContextEnvelope{CompiledAt: time.Now()}

	need := p.HasContext

	if need(ContextUserIntent) || len(p.ContextKinds) == 0 {
		env.Items = append(env.Items, ContextItem{
			Kind: ContextUserIntent, Owner: "engine", Source: SourceUser,
			Relevance:          "the exact goal the engine will execute",
			Authority:          "user-provided",
			ReasonForInclusion: "every execution is anchored to the user intent",
			Content:            truncateContent(p.Intent, 200),
		})
	}

	if need(ContextExplicitTargets) && len(p.Targets) > 0 {
		names := make([]string, 0, len(p.Targets))
		for _, t := range p.Targets {
			names = append(names, t.Resolved)
		}
		env.Items = append(env.Items, ContextItem{
			Kind: ContextExplicitTargets, Owner: "engine", Source: SourceFilesystem,
			Relevance:          "the resolved target set the execution is confined to",
			Authority:          "deterministic workspace resolution",
			ReasonForInclusion: "explicit targets bound the mutation scope",
			Content:            strings.Join(names, ","),
		})
	}

	if need(ContextTargetContent) && len(p.Targets) > 0 {
		c.compileTargetContent(&env, p)
	}

	if need(ContextStructuralEvidence) {
		env.Items = append(env.Items, ContextItem{
			Kind: ContextStructuralEvidence, Owner: "engine", Source: SourceEngine,
			Relevance:          "the located defect or target block",
			Authority:          "deterministic block resolution",
			ReasonForInclusion: "the mutation anchors to a located block",
		})
	}

	if need(ContextRepositoryConstraints) {
		env.Items = append(env.Items, ContextItem{
			Kind: ContextRepositoryConstraints, Owner: "engine", Source: SourceWorkspace,
			Relevance:          "absolute repository constraints (stack, capability, policy)",
			Authority:          "workspace detection",
			ReasonForInclusion: "the execution must honor the workspace contract",
			Content:            filepath.Clean(c.deps.Root),
		})
	}

	if need(ContextDependencyEvidence) {
		env.Items = append(env.Items, ContextItem{
			Kind: ContextDependencyEvidence, Owner: "engine", Source: SourceFileGraph,
			Relevance:          "cross-file structural dependencies of the targets",
			Authority:          "structural graph",
			ReasonForInclusion: "cross-file coupling must be visible before reasoning",
		})
	}

	if need(ContextPriorExecution) {
		env.Items = append(env.Items, ContextItem{
			Kind: ContextPriorExecution, Owner: "engine", Source: SourceLedger,
			Relevance:          "evidence from a previous execution consumed by this one",
			Authority:          "session ledger",
			ReasonForInclusion: "execution continuity preserves prior evidence ($fix)",
		})
	}

	if need(ContextArtifactContract) && p.Artifact.Kind != "" {
		desc := p.Artifact.Description
		if desc == "" {
			desc = p.Artifact.Kind
		}
		env.Items = append(env.Items, ContextItem{
			Kind: ContextArtifactContract, Owner: "engine", Source: SourceDirective,
			Relevance:          "the exact artifact the model must return",
			Authority:          "artifact contract",
			ReasonForInclusion: "the model must produce a bounded, parseable artifact",
			Content:            fmt.Sprintf("%s (bounded=%v)", desc, p.Artifact.Bounded),
		})
	}

	if need(ContextVerificationContract) {
		env.Items = append(env.Items, ContextItem{
			Kind: ContextVerificationContract, Owner: "engine", Source: SourceDirective,
			Relevance:          "the deterministic verification gate after mutation",
			Authority:          "verification contract",
			ReasonForInclusion: "success is defined by the verification gate, not the artifact",
		})
	}

	return env
}

// compileTargetContent records the target content channel. Only existing files
// with bounded size capture their content inline; large files record their
// size (the provider path supplies the located block).
func (c *Compiler) compileTargetContent(env *ContextEnvelope, p ExecutionStrategyProfile) {
	for _, t := range p.Targets {
		if !t.Exists {
			continue
		}
		content := ""
		if c.deps.Workspace != nil {
			size := c.fileSize(t.Resolved)
			if size >= 0 && size <= maxInlineContentBytes {
				content = c.readContent(t.Resolved)
			} else if size > 0 {
				content = fmt.Sprintf("file=%d bytes", size)
			}
		}
		env.Items = append(env.Items, ContextItem{
			Kind: ContextTargetContent, Owner: "engine", Source: SourceFilesystem,
			Relevance:          "the required target content for " + t.Resolved,
			Authority:          "workspace file read",
			ReasonForInclusion: "the mutation requires the target's content",
			Content:            truncateContent(content, 2048),
		})
	}
}

// fileSize returns the size of a workspace-relative file, -1 when missing.
func (c *Compiler) fileSize(path string) int64 {
	if f, ok := c.deps.Workspace.(interface{ Size(string) int64 }); ok {
		return f.Size(path)
	}
	return -1
}

// readContent returns the content of a workspace-relative file ("" on error).
func (c *Compiler) readContent(path string) string {
	if f, ok := c.deps.Workspace.(interface{ Read(string) string }); ok {
		return f.Read(path)
	}
	return ""
}

func truncateContent(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// Escalator implements progressive context expansion: it starts with the
// smallest sufficient envelope and grows it only when evidence demands it.
// Expansion is always recorded with its reason, so $inspect can answer "why
// did Izen read more context".
type Escalator struct {
	env ContextEnvelope
}

// NewEscalator starts escalation from the initial sufficient envelope.
func NewEscalator(env ContextEnvelope) *Escalator {
	return &Escalator{env: env}
}

// Envelope returns the current envelope.
func (e *Escalator) Envelope() ContextEnvelope { return e.env }

// Expand grows the envelope with the given items, recording the escalation
// reason. Duplicate kinds are never added twice.
func (e *Escalator) Expand(reason string, items ...ContextItem) ContextEnvelope {
	var added []ContextItem
	for _, it := range items {
		if e.env.Has(it.Kind) {
			continue
		}
		added = append(added, it)
	}
	if len(added) == 0 {
		return e.env
	}
	e.env.Items = append(e.env.Items, added...)
	e.env.Expanded = true
	e.env.ExpansionReason = reason
	return e.env
}
