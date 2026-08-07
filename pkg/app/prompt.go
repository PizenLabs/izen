package app

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/PizenLabs/izen/pkg/capability"
	"github.com/PizenLabs/izen/pkg/knowledge"
	"github.com/PizenLabs/izen/pkg/op"
)

// baselineCap bounds how much of one baseline file is injected into the LLM
// context under PolicyEdit/PolicyPatch, keeping prompts bounded for large files.
const baselineCap = 8 * 1024

// maxBaselineFiles bounds how many workspace files are injected as bounded
// baseline excerpts under PolicyEdit/PolicyPatch. Explicit targets are always
// included first; knowledge-graph files fill the remaining slots.
const maxBaselineFiles = 8

// RewriteDirective is the directive injected under PolicyRewrite. It makes
// User Intent the absolute primary source of truth and forbids preserving the
// obsolete workspace code whose contents were stripped from the context.
const RewriteDirective = "Current workspace code is obsolete. User Intent is the ABSOLUTE Primary Source of Truth. Do NOT preserve existing code."

// PromptBuilder renders the LLM prompts for one pipeline run while enforcing
// the compiled op.ContextPolicy. It is the execution of the Strategy/Registry
// pattern: OperationSemantics are mapped to a ContextPolicy by the
// op.StrategyRegistry, and this builder renders that policy deterministically —
// PolicyRewrite injects target file paths ONLY (stripping obsolete file
// contents), PolicyEdit/PolicyPatch inject bounded baseline code with explicit
// boundary markers, PolicyGenerate injects no baseline at all.
type PromptBuilder struct {
	registry  *op.StrategyRegistry
	kg        *knowledge.KnowledgeGraph
	root      string
	modelTier string
	readFile  func(rel string) ([]byte, error)
	guard     *readGuard
}

// PromptBuilderOption configures a PromptBuilder.
type PromptBuilderOption func(*PromptBuilder)

// WithBuilderRoot sets the workspace root baseline file contents are read from.
func WithBuilderRoot(root string) PromptBuilderOption {
	return func(b *PromptBuilder) {
		if root != "" {
			b.root = root
		}
	}
}

// WithBuilderModelTier selects the capability prompt verbosity tier.
func WithBuilderModelTier(tier string) PromptBuilderOption {
	return func(b *PromptBuilder) {
		if tier != "" {
			b.modelTier = tier
		}
	}
}

// WithBaselineReader overrides the workspace file reader used to inject
// baseline code under PolicyEdit/PolicyPatch. It defaults to reading from disk
// under the workspace root; tests override it to avoid I/O.
func WithBaselineReader(fn func(rel string) ([]byte, error)) PromptBuilderOption {
	return func(b *PromptBuilder) {
		if fn != nil {
			b.readFile = fn
		}
	}
}

// WithBuilderReadGuard binds the pipeline's read guard to the builder. Every
// baseline read is routed through the guard, which blocks (sanitizes) reads
// under a full-overwrite context so obsolete workspace content can never leak
// into the prompt.
func WithBuilderReadGuard(g *readGuard) PromptBuilderOption {
	return func(b *PromptBuilder) {
		if g != nil {
			b.guard = g
		}
	}
}

// NewPromptBuilder returns a builder wired to the given strategy registry and
// RuntimeKnowledge graph. A nil registry makes CompilePolicy return the
// conservative DefaultContextPolicy; a nil knowledge graph simply contributes
// no workspace files.
func NewPromptBuilder(registry *op.StrategyRegistry, kg *knowledge.KnowledgeGraph, opts ...PromptBuilderOption) *PromptBuilder {
	b := &PromptBuilder{
		registry:  registry,
		kg:        kg,
		modelTier: "full",
	}
	for _, opt := range opts {
		opt(b)
	}
	if b.readFile == nil {
		b.readFile = defaultBaselineReader(b.root)
	}
	return b
}

// defaultBaselineReader reads a workspace-relative file from disk under root.
func defaultBaselineReader(root string) func(rel string) ([]byte, error) {
	return func(rel string) ([]byte, error) {
		return os.ReadFile(filepath.Join(root, filepath.Clean(rel)))
	}
}

// CompilePolicy resolves the semantics through the strategy registry into the
// ContextPolicy that governs prompt assembly. A nil receiver or registry falls
// back to DefaultContextPolicy.
func (b *PromptBuilder) CompilePolicy(semantics op.OperationSemantics) op.ContextPolicy {
	if b == nil || b.registry == nil {
		return op.DefaultContextPolicy
	}
	return b.registry.Resolve(semantics, b.kg)
}

// BuildSystem assembles the full system prompt for one run. It always emits the
// capability-constrained base prompt and output format, then enforces the
// compiled policy by appending its context block:
//
//   - PolicyGenerate: no baseline context is injected.
//   - PolicyRewrite: target file paths ONLY plus the RewriteDirective; obsolete
//     file contents are never read into the context.
//   - PolicyEdit / PolicyPatch: bounded baseline excerpts delimited by explicit
//     <<<FILE / </FILE>>> boundary markers.
func (b *PromptBuilder) BuildSystem(policy op.ContextPolicy, caps []capability.Capability, targets []string) string {
	var out strings.Builder
	out.WriteString("You are Izen, the human-centered coding engine. Route the user's intent into a coherent set of workspace files that Izen writes for you.\n")
	if len(targets) > 0 {
		out.WriteString("\nWorkspace target files already referenced:\n")
		for _, t := range targets {
			out.WriteString("  - " + t + "\n")
		}
	}
	out.WriteString("\nACTIVE CAPABILITIES (enforce every constraint, never fall back to a generic template):\n")
	for _, c := range caps {
		out.WriteString(c.PromptRepresentation(b.modelTier) + "\n")
	}
	out.WriteString("\nOUTPUT FORMAT\n")
	out.WriteString("Emit exactly one fenced block per file. The fence header MUST name the language and the workspace-relative target path:\n")
	out.WriteString("```html:index.html\n<!DOCTYPE html>\n...\n```\n")
	out.WriteString("A fence header is always \"```lang:path\" where path is the relative file location (e.g. html:index.html, go:main.go, css:styles/main.css). Plain code fences without a :path header, or prose, will be rejected.\n")
	out.WriteString("You may add brief narration before the blocks. Never write to disk yourself.\n")

	switch policy {
	case op.PolicyRewrite:
		out.WriteString(b.rewriteBlock(targets))
	case op.PolicyEdit:
		out.WriteString(b.baselineBlock(targets, "EDIT"))
	case op.PolicyPatch:
		out.WriteString(b.baselineBlock(targets, "PATCH"))
	}
	return out.String()
}

// BuildUser assembles the user-facing prompt for one run, honoring the policy.
// Under PolicyRewrite the workspace files are declared obsolete so the model
// anchors on User Intent instead of the stripped content.
func (b *PromptBuilder) BuildUser(policy op.ContextPolicy, intent string, targets []string) string {
	var out strings.Builder
	out.WriteString(intent)
	switch policy {
	case op.PolicyRewrite:
		out.WriteString("\n\nREWRITE CONTEXT: the current workspace files are obsolete and their contents were stripped from this prompt. Follow the intent above as the absolute source of truth; do NOT preserve existing code.")
	case op.PolicyEdit, op.PolicyPatch:
		if len(targets) > 0 {
			out.WriteString("\n\nWorkspace context files: " + strings.Join(targets, ", "))
		}
	}
	return out.String()
}

// rewriteBlock renders the PolicyRewrite context: the stripped file paths ONLY
// plus the RewriteDirective. It never reads file contents.
func (b *PromptBuilder) rewriteBlock(targets []string) string {
	var out strings.Builder
	out.WriteString("\nCONTEXT POLICY: REWRITE\n")
	out.WriteString("Current workspace contents have been STRIPPED from this prompt; only target file paths are provided:\n")
	paths := b.contextPaths(targets)
	if len(paths) == 0 {
		out.WriteString("  (none — synthesize the target paths from User Intent)\n")
	}
	for _, p := range paths {
		out.WriteString("  - " + p + "\n")
	}
	out.WriteString("Directive: " + RewriteDirective + "\n")
	out.WriteString("Do not anchor on the existing implementation or preserve its structure. Synthesize every target file from the user intent.\n")
	return out.String()
}

// baselineBlock renders the PolicyEdit/PolicyPatch context: bounded baseline
// excerpts for the target/workspace files, each wrapped in explicit boundary
// markers, plus a mode-appropriate directive.
func (b *PromptBuilder) baselineBlock(targets []string, mode string) string {
	paths := b.contextPaths(targets)
	if len(paths) == 0 {
		return ""
	}
	if len(paths) > maxBaselineFiles {
		paths = paths[:maxBaselineFiles]
	}
	var out strings.Builder
	out.WriteString("\nCONTEXT POLICY: " + mode + "\n")
	out.WriteString("Baseline workspace context (bounded excerpts delimited by explicit boundary markers; for reference only):\n")
	for _, p := range paths {
		out.WriteString("<<<FILE " + p + ">>>\n")
		if content := b.readBaseline(p); content != "" {
			out.WriteString(content)
			if !strings.HasSuffix(content, "\n") {
				out.WriteString("\n")
			}
		} else {
			out.WriteString("<unreadable or empty — preserve the path, do not guess its content>\n")
		}
		out.WriteString("</FILE " + p + ">>>\n")
	}
	if mode == "EDIT" {
		out.WriteString("Directive: make the localized structural change while preserving unrelated baseline code inside and outside these boundaries.\n")
	} else {
		out.WriteString("Directive: treat the user's error description as the error trace. Apply the minimal corrective diff to the target files within these boundaries.\n")
	}
	return out.String()
}

// contextPaths collects the workspace-relative file paths the policy should
// reference: explicit targets first, then every scanned knowledge-graph file in
// stable order. Paths are cleaned, slash-normalised and de-duplicated.
func (b *PromptBuilder) contextPaths(targets []string) []string {
	seen := make(map[string]bool)
	paths := make([]string, 0, len(targets))
	add := func(p string) {
		p = filepath.ToSlash(filepath.Clean(p))
		if p == "" || p == "." || seen[p] {
			return
		}
		seen[p] = true
		paths = append(paths, p)
	}
	for _, t := range targets {
		add(t)
	}
	if b.kg != nil {
		for _, rec := range b.kg.Files() {
			add(rec.Path)
		}
	}
	return paths
}

// readBaseline returns the bounded content of a workspace-relative baseline
// file. Paths that would escape the workspace root are refused, and unreadable
// or oversized files degrade to an empty string so the prompt stays safe and
// bounded. Every read is routed through the pipeline read guard, which blocks
// (sanitizes) it under a full-overwrite context: obsolete content is never
// injected, not even through the baseline path.
func (b *PromptBuilder) readBaseline(rel string) string {
	if b.readFile == nil {
		return ""
	}
	clean := filepath.ToSlash(filepath.Clean(rel))
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
		return ""
	}
	var (
		data []byte
		err  error
	)
	if b.guard != nil {
		data, err = b.guard.read(rel, b.readFile)
	} else {
		data, err = b.readFile(rel)
	}
	if err != nil {
		return ""
	}
	if len(data) == 0 {
		return ""
	}
	content := strings.TrimRight(string(data), "\n")
	if len(content) > baselineCap {
		content = content[:baselineCap] + "\n... (baseline excerpt truncated)"
	}
	return content
}
