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
	"github.com/PizenLabs/izen/internal/protocol"
	"github.com/PizenLabs/izen/internal/session"
)

// ArtifactRef is a bounded descriptor of one workspace artifact. Content is
// optional: when present it is the already-authorized file projection that the
// compiler may fit into the prompt; when absent only a truthful path/size
// descriptor crosses the boundary.
type ArtifactRef struct {
	Path     string
	Size     int
	Content  string
	Critical bool
	Priority int
	// Truncated marks a source-side read cap applied before compilation. It
	// is surfaced in section telemetry and is fatal for required files.
	Truncated bool
}

// FileContext is the descriptive spelling used by new callers. ArtifactRef is
// retained as the compatibility name used by the original contextcompiler API.
type FileContext = ArtifactRef

// ToolDescriptor is the provider-neutral portion of a native tool definition
// that consumes prompt budget. It is descriptive only and never grants tool or
// execution authority.
type ToolDescriptor struct {
	Name        string
	Description string
	Schema      string
	Critical    bool
}

// Input is the compiled-context source set: the frozen snapshot, project
// knowledge, recent turns, workflow state, workspace artifacts, and the
// interaction contract that governs the provider request.
type Input struct {
	// SnapshotID is the frozen execution context snapshot identity.
	SnapshotID string
	// UserRequest is the current user request (highest non-critical priority).
	UserRequest string
	// WorkflowState is the active workflow phase/direction.
	WorkflowState string
	// RecentTurns is the recent conversation window.
	RecentTurns []session.Message
	// SessionCompact is the active session's compact context generation.
	SessionCompact *session.CompactContext
	// Artifacts is the relevant artifact surface.
	Artifacts []ArtifactRef
	// Files and WorkspaceFiles are equivalent explicit file-context channels.
	// WorkspaceFiles is provided as a descriptive alias for embedding callers.
	Files          []FileContext
	WorkspaceFiles []FileContext
	// Knowledge is the relevant project knowledge chunks.
	Knowledge []knowledge.Asset

	// Phase selects the semantic phase ceiling. An empty phase is inferred
	// from Contract when possible and otherwise uses the legacy compiler cap.
	Phase Phase
	// Provider/Model and the explicit limit fields provide dynamic model
	// budgeting. Explicit values win over the capability heuristics.
	Provider              string
	Model                 string
	ContextWindow         int
	MaxOutputTokens       int
	RequestedOutputTokens int
	// ContextBudget is an optional strategy-owned cap. Zero means no extra cap;
	// a non-zero value is applied after the phase and model limits.
	ContextBudget int

	// SystemInstructions and SchemaOverlay are critical prompt material. They
	// are reserved before any workspace context and are never truncated.
	SystemInstructions string
	System             string // compatibility alias for SystemInstructions
	SchemaOverlay      string
	ToolDescriptors    []ToolDescriptor
	Tools              []ToolDescriptor // compatibility alias

	// Contract supplies the InteractionContract context specification. Its
	// normalized inline schema constraint is used when SchemaOverlay is empty.
	Contract *protocol.ContractDescriptor

	// ContextPolicy is "none" for a zero-workspace-context turn, or a
	// repository/target policy understood by the caller. The compiler does
	// not resolve targets; it only honors the caller's selection.
	ContextPolicy string
	Scope         string
	Lineage       string
	Exclusions    []string
}

// Section is one admitted, budget-fitted context block.
type Section struct {
	Source    Source
	Header    string
	Content   string
	Tokens    int
	Truncated bool
	// Critical marks material that must survive budget fitting. For
	// provider-owned prompt contracts it also stays out of ContextOnly; for
	// required workspace files it remains in the user projection but is
	// protected from global trimming.
	Critical bool
}

// CompiledContext is the budget-fitted outcome of one compilation: ordered
// sections plus the telemetry describing how the budget was enforced.
type CompiledContext struct {
	Sections   []Section
	Budget     Budget
	UsedTokens int
	// ContextTokens is the portion spent on non-critical context sections.
	ContextTokens int
	// SystemTokens, SchemaTokens and ToolTokens expose critical reservation
	// accounting for telemetry and regression tests.
	SystemTokens int
	SchemaTokens int
	ToolTokens   int
	Truncated    bool
	Dropped      int
	CacheHit     bool
	CompiledAt   time.Time

	// TruncatedFiles records only source-side file provenance. It contains
	// paths, never file contents, and is safe to project into audit telemetry.
	TruncatedFiles []string
	// Policy is the caller-selected context policy that produced this result.
	Policy string

	Phase      Phase
	Model      string
	Provider   string
	Scope      string
	Lineage    string
	Exclusions []string
}

// AgentContext is the facade-shaped result consumed by core request builders.
// It records the semantic context identity alongside the compiler-owned
// projection; it carries no authority and cannot mutate the sealed execution
// snapshot.
type AgentContext struct {
	Phase      Phase
	Sources    []Source
	Scope      string
	Lineage    string
	Exclusions []string
	Budget     Budget
	Compiled   *CompiledContext
	Context    *CompiledContext
	// Result is the content-free compiler projection consumed by telemetry
	// and audit sinks. It is a defensive snapshot of the same compilation.
	Result   *CompileResult
	Rendered string

	// Context* aliases mirror the vocabulary used by the AgentTurn design
	// document and make the facade self-describing to non-Go callers.
	ContextSources    []Source
	ContextBudget     Budget
	ContextScope      string
	ContextLineage    string
	ContextExclusions []string
}

// Assemble renders every compiled section into a prompt-ready block, including
// critical system/schema/tool sections.
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

// ContextOnly renders the non-critical projection that belongs in the user
// turn. System instructions, schema overlays and tool descriptors remain in
// their provider-owned fields and are not accidentally duplicated here.
// Required workspace files are marked Critical as well, but unlike the
// provider-owned prompt contracts they must still cross in the user turn.
func (c *CompiledContext) ContextOnly() string {
	if c == nil {
		return ""
	}
	var b strings.Builder
	first := true
	for _, s := range c.Sections {
		if isProviderReservationSource(s.Source) || (s.Critical && s.Source != SourceArtifacts) {
			continue
		}
		if !first {
			b.WriteString("\n\n")
		}
		first = false
		if s.Header != "" {
			b.WriteString("### " + s.Header + "\n")
		}
		b.WriteString(s.Content)
	}
	return b.String()
}

// SystemText returns the critical system-instruction section without its
// heading. SchemaText and ToolText are the corresponding bounded views.
func (c *CompiledContext) SystemText() string { return c.sectionText(SourceSystemInstructions) }
func (c *CompiledContext) SchemaText() string { return c.sectionText(SourceSchemaOverlay) }
func (c *CompiledContext) ToolText() string   { return c.sectionText(SourceToolDescriptors) }

func (c *CompiledContext) sectionText(source Source) string {
	if c == nil {
		return ""
	}
	for _, s := range c.Sections {
		if s.Source == source {
			return s.Content
		}
	}
	return ""
}

// ContextChannels returns only workspace/session channels. System, schema, and
// tool reservations are budget accounting, not context channels exposed as
// workspace provenance to event consumers. Required workspace files remain
// visible because they are part of the model-facing projection.
func (c *CompiledContext) ContextChannels() []string {
	if c == nil {
		return nil
	}
	seen := make(map[Source]struct{})
	var channels []string
	for _, s := range c.Sections {
		if isProviderReservationSource(s.Source) || s.Source == SourceUserRequest || s.Source == SourceWorkflow {
			continue
		}
		if _, ok := seen[s.Source]; ok {
			continue
		}
		seen[s.Source] = struct{}{}
		channels = append(channels, string(s.Source))
	}
	return channels
}

// RenderedPrompt is the full compiler projection, useful to embedders that do
// not carry separate provider system/schema fields.
func (c *CompiledContext) RenderedPrompt() string { return c.Assemble() }

// Prompt is a concise alias for RenderedPrompt.
func (c *CompiledContext) Prompt() string { return c.RenderedPrompt() }

// CompileAgentContext is the facade form of Compile. Keeping this thin makes
// the compiler itself the single budget authority while giving core request
// builders a named AgentContext boundary.
func (c *Compiler) CompileAgentContext(ctx context.Context, in Input) (*AgentContext, error) {
	compiled, err := c.Compile(ctx, in)
	if err != nil {
		return nil, err
	}
	var sources []Source
	seen := make(map[Source]struct{})
	for _, s := range compiled.Sections {
		if _, ok := seen[s.Source]; ok {
			continue
		}
		seen[s.Source] = struct{}{}
		sources = append(sources, s.Source)
	}
	exclusions := append([]string(nil), compiled.Exclusions...)
	budget := compiled.Budget
	budget.BySource = cloneSourceMap(compiled.Budget.BySource)
	result := compiled.Metrics()
	return &AgentContext{
		Phase:             compiled.Phase,
		Sources:           sources,
		Scope:             compiled.Scope,
		Lineage:           compiled.Lineage,
		Exclusions:        exclusions,
		Budget:            budget,
		Compiled:          compiled,
		Context:           compiled,
		Result:            &result,
		Rendered:          compiled.RenderedPrompt(),
		ContextSources:    append([]Source(nil), sources...),
		ContextBudget:     budget,
		ContextScope:      compiled.Scope,
		ContextLineage:    compiled.Lineage,
		ContextExclusions: append([]string(nil), exclusions...),
	}, nil
}

// Compiler is the runtime context compiler. It admits inputs in priority order
// under a strict token budget and caches the result keyed on a fingerprint of
// the underlying state.
type Compiler struct {
	maxTokens         int
	maxTokensExplicit bool
	shares            map[Source]int
	order             []Source
	cacheLimit        int

	initOnce sync.Once
	mu       sync.Mutex
	cache    map[string]*CompiledContext
}

// Option configures a Compiler.
type Option func(*Compiler)

// WithMaxTokens caps the compiled context window. An explicit option is also a
// hard ceiling when a phase/model budget would otherwise be larger.
func WithMaxTokens(n int) Option {
	return func(c *Compiler) {
		if n > 0 {
			c.maxTokens = n
			c.maxTokensExplicit = true
		}
	}
}

// WithShares overrides the non-critical per-source allocation table.
func WithShares(shares map[Source]int) Option {
	return func(c *Compiler) {
		if len(shares) > 0 {
			c.shares = cloneShares(shares)
		}
	}
}

// WithCacheLimit bounds the fingerprint cache.
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

func (c *Compiler) ensureInitialized() {
	if c == nil {
		return
	}
	c.initOnce.Do(func() {
		if c.maxTokens <= 0 {
			c.maxTokens = DefaultMaxTokens
		}
		if len(c.shares) == 0 {
			c.shares = cloneShares(defaultShares)
		}
		if len(c.order) == 0 {
			c.order = append([]Source(nil), priorityOrder...)
		}
		if c.cacheLimit <= 0 {
			c.cacheLimit = 64
		}
		if c.cache == nil {
			c.cache = make(map[string]*CompiledContext)
		}
	})
}

// Compile assembles the budget-fitted context for one input. Critical prompt
// contracts are reserved first; optional workspace/session context is then
// admitted in priority order and truncated or dropped to fit.
func (c *Compiler) Compile(ctx context.Context, in Input) (*CompiledContext, error) {
	if c == nil {
		return nil, fmt.Errorf("contextcompiler: nil compiler")
	}
	if ctx == nil {
		return nil, fmt.Errorf("contextcompiler: nil context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.ensureInitialized()
	normalized, err := normalizeInput(in)
	if err != nil {
		return nil, err
	}
	fp := c.fingerprint(normalized)

	c.mu.Lock()
	if cached, ok := c.cache[fp]; ok {
		out := cloneCompiled(cached)
		out.CacheHit = true
		c.mu.Unlock()
		return out, nil
	}
	c.mu.Unlock()

	total, tokenBudget, err := c.resolveTotal(normalized)
	if err != nil {
		return nil, err
	}
	limits := tokenBudget
	critical := []Section{
		criticalSection(SourceSystemInstructions, normalized.SystemInstructions),
		criticalSection(SourceSchemaOverlay, normalized.SchemaOverlay),
		criticalSection(SourceToolDescriptors, renderToolDescriptors(normalizedToolDescriptors(normalized))),
		criticalSection(SourceUserRequest, normalized.UserRequest),
	}
	critical = compactSections(critical)
	criticalTokens := EstimateTokens(assembleSections(critical))
	if criticalTokens > total {
		return nil, fmt.Errorf("%w: critical prompt material requires %d tokens, budget is %d", ErrBudgetExceeded, criticalTokens, total)
	}

	budget := Budget{
		Total:         total,
		Phase:         normalized.Phase,
		ContextWindow: limits.ContextWindow,
		OutputReserve: limits.OutputReserve,
		Reserved:      criticalTokens,
		Available:     total - criticalTokens,
		BySource:      make(map[Source]int),
	}
	remainingShares := nonCriticalShares(c.shares)
	if budget.Available > 0 {
		allocated := Allocate(budget.Available, remainingShares)
		budget.BySource = allocated.BySource
	}
	excludedFiles := 0
	filteredFiles := make([]ArtifactRef, 0, len(normalized.Artifacts))
	for _, file := range normalized.Artifacts {
		if !file.Critical && pathExcluded(file.Path, normalized.Exclusions) {
			excludedFiles++
			continue
		}
		filteredFiles = append(filteredFiles, file)
	}
	normalized.Artifacts = filteredFiles
	out := &CompiledContext{
		Budget:     budget,
		CompiledAt: time.Now(),
		Phase:      normalized.Phase,
		Model:      normalized.Model,
		Provider:   normalized.Provider,
		Policy:     normalized.ContextPolicy,
		Scope:      normalized.Scope,
		Lineage:    normalized.Lineage,
		Exclusions: append([]string(nil), normalized.Exclusions...),
	}
	for _, file := range normalized.Artifacts {
		if file.Truncated && strings.TrimSpace(file.Path) != "" {
			out.TruncatedFiles = append(out.TruncatedFiles, file.Path)
		}
	}
	out.Dropped = excludedFiles
	for _, section := range critical {
		if section.Content == "" {
			continue
		}
		section.Tokens = EstimateTokens(sectionText(section))
		out.Sections = append(out.Sections, section)
		out.Budget.BySource[section.Source] += section.Tokens
		switch section.Source {
		case SourceSystemInstructions:
			out.SystemTokens = section.Tokens
		case SourceSchemaOverlay:
			out.SchemaTokens = section.Tokens
		case SourceToolDescriptors:
			out.ToolTokens = section.Tokens
		}
	}

	contextPolicy := normalized.ContextPolicy
	workspaceAllowed := contextPolicy != "none"
	var requiredArtifactSection *Section
	if workspaceAllowed {
		// A target-only policy spends the remaining allowance on the resolved
		// files instead of reserving fixed percentages for sources the policy
		// explicitly forbids.
		if contextPolicy == "target_file_only" {
			budget.BySource[SourceArtifacts] = budget.Available
		}
		// Required workspace files are a separate, protected projection. They
		// are not allowed to compete with optional project knowledge for a
		// percentage and are never silently shortened. If the required files
		// cannot fit alongside the mandatory prompt contracts, fail before
		// transport instead of asking the model to mutate unseen bytes.
		required, optional := splitArtifacts(normalized.Artifacts)
		if len(required) > 0 {
			for _, file := range required {
				if file.Truncated {
					return nil, fmt.Errorf("%w: required workspace file %q was truncated before compilation", ErrBudgetExceeded, file.Path)
				}
			}
			content := renderArtifactBlocks(required)
			section := Section{
				Source:   SourceArtifacts,
				Header:   headerFor(SourceArtifacts),
				Content:  content,
				Tokens:   EstimateTokens(sectionText(Section{Source: SourceArtifacts, Header: headerFor(SourceArtifacts), Content: content})),
				Critical: true,
			}
			if section.Tokens > budget.Available {
				return nil, fmt.Errorf("%w: critical workspace files require %d tokens, remaining budget is %d", ErrBudgetExceeded, section.Tokens, budget.Available)
			}
			requiredArtifactSection = &section
			// The required section consumes the artifact allowance first. If it
			// is larger than the ordinary share, optional artifact material is
			// dropped rather than displacing required target bytes.
			artifactShare := budget.Source(SourceArtifacts)
			if artifactShare < section.Tokens {
				artifactShare = section.Tokens
			}
			budget.BySource[SourceArtifacts] = artifactShare
			normalized.Artifacts = optional
		}
	}
	for _, src := range c.order {
		excluded := sourceExcluded(src, normalized.Exclusions)
		// Required workspace files are never excluded by a source-level
		// policy; their path-level exclusions were already filtered and
		// their bytes are mandatory for this turn.
		if src == SourceArtifacts && requiredArtifactSection != nil {
			excluded = false
		}
		if excluded || isReservedSource(src) || !sourceAllowedByPolicy(contextPolicy, src) {
			continue
		}
		share := budget.Source(src)
		if src == SourceArtifacts && requiredArtifactSection != nil {
			// Optional files may use only the unconsumed artifact share.
			share -= requiredArtifactSection.Tokens
			if share < 0 {
				share = 0
			}
		}
		if src == SourceArtifacts && requiredArtifactSection != nil {
			out.Sections = append(out.Sections, *requiredArtifactSection)
			out.ContextTokens += requiredArtifactSection.Tokens
		}
		content, dropped, truncatedFiles := c.fitSource(src, normalized, share)
		out.Dropped += dropped
		out.TruncatedFiles = appendUniqueStrings(out.TruncatedFiles, truncatedFiles...)
		if content == "" {
			out.Truncated = out.Truncated || (src == SourceArtifacts && dropped > 0)
			continue
		}
		content, truncated := fitSectionContent(src, content, share)
		// renderArtifacts may shorten its first oversized file before the
		// outer section header is applied. Propagate that fact into section
		// telemetry instead of reporting a false "complete" projection.
		if src == SourceArtifacts && dropped > 0 {
			truncated = true
		}
		if src == SourceArtifacts && truncated {
			for _, file := range normalized.Artifacts {
				if file.Path != "" {
					out.TruncatedFiles = appendUniqueStrings(out.TruncatedFiles, file.Path)
				}
			}
		}
		if content == "" {
			out.Truncated = out.Truncated || truncated
			continue
		}
		section := Section{
			Source:    src,
			Header:    headerFor(src),
			Content:   content,
			Tokens:    EstimateTokens(sectionText(Section{Source: src, Header: headerFor(src), Content: content})),
			Truncated: truncated,
		}
		out.Sections = append(out.Sections, section)
		out.ContextTokens += section.Tokens
		out.Truncated = out.Truncated || truncated
	}

	out.UsedTokens = EstimateTokens(out.Assemble())
	if out.UsedTokens > out.Budget.Total {
		if err := trimToTotal(out, out.Budget.Total); err != nil {
			return nil, err
		}
	}
	// The rendered estimate is the authoritative local accounting value. Keep
	// source telemetry consistent after any final trim.
	out.Budget.Reserved = criticalTokens
	out.Budget.Available = out.Budget.Total - criticalTokens
	out.Budget.BySource = sourceTokens(out.Sections)
	out.ContextTokens = 0
	for _, section := range out.Sections {
		if !isReservedSource(section.Source) {
			out.ContextTokens += section.Tokens
		}
	}
	out.UsedTokens = EstimateTokens(out.Assemble())
	if out.UsedTokens > out.Budget.Total {
		return nil, fmt.Errorf("%w: rendered prompt is %d tokens, budget is %d", ErrBudgetExceeded, out.UsedTokens, out.Budget.Total)
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	if len(c.cache) >= c.cacheLimit {
		c.cache = make(map[string]*CompiledContext)
	}
	c.cache[fp] = cloneCompiled(out)
	c.mu.Unlock()
	return out, nil
}

func (c *Compiler) resolveTotal(in Input) (int, TokenBudget, error) {
	// Preserve the original fixed-cap behavior for legacy callers that do not
	// declare a phase, model, or contract. Production request facades always
	// provide a semantic phase and therefore take the dynamic path below.
	dynamic := in.Phase.Valid() || in.Provider != "" || in.Model != "" || in.ContextWindow > 0 || in.MaxOutputTokens > 0 || in.RequestedOutputTokens > 0 || in.ContextBudget > 0 || in.Contract != nil
	if !dynamic {
		return c.maxTokens, TokenBudget{Total: c.maxTokens}, nil
	}
	limits := ModelLimits{
		Provider:              in.Provider,
		Model:                 in.Model,
		ContextWindow:         in.ContextWindow,
		MaxOutputTokens:       in.MaxOutputTokens,
		RequestedOutputTokens: in.RequestedOutputTokens,
	}
	if limits.ContextWindow <= 0 || limits.MaxOutputTokens <= 0 {
		heuristic := ModelLimitsFor(in.Provider, in.Model, in.RequestedOutputTokens)
		if limits.ContextWindow <= 0 {
			limits.ContextWindow = heuristic.ContextWindow
		}
		if limits.MaxOutputTokens <= 0 && limits.RequestedOutputTokens <= 0 {
			limits.MaxOutputTokens = heuristic.MaxOutputTokens
		}
		if limits.RequestedOutputTokens <= 0 {
			limits.RequestedOutputTokens = heuristic.RequestedOutputTokens
		}
	}
	resolved := ResolveTokenBudget(in.Phase, limits)
	total := resolved.Total
	if c.maxTokensExplicit && total > c.maxTokens {
		total = c.maxTokens
	}
	if in.ContextBudget > 0 && total > in.ContextBudget {
		total = in.ContextBudget
	}
	if total <= 0 {
		return 0, resolved, fmt.Errorf("%w: effective context budget is zero", ErrBudgetExceeded)
	}
	resolved.Total = total
	resolved.Available = total
	return total, resolved, nil
}

func normalizeInput(in Input) (Input, error) {
	out := in
	if out.SystemInstructions == "" {
		out.SystemInstructions = out.System
	}
	out.ContextPolicy = normalizeContextPolicy(out.ContextPolicy)
	if out.Phase == "" && out.Contract != nil {
		out.Phase = PhaseForContract(out.Contract.Contract)
	}
	rawPhase := out.Phase
	out.Phase = out.Phase.Normalize()
	if rawPhase != "" && !out.Phase.Valid() {
		return Input{}, fmt.Errorf("contextcompiler: invalid phase %q", rawPhase)
	}
	if out.Contract != nil {
		descriptor, err := out.Contract.Clone().Normalize()
		if err != nil {
			return Input{}, err
		}
		out.Contract = &descriptor
		if out.SchemaOverlay == "" && !strings.Contains(out.SystemInstructions, "[STRUCTURED_OUTPUT_CONTRACT]") {
			overlay, err := descriptor.InlineSchemaConstraint()
			if err != nil {
				return Input{}, err
			}
			out.SchemaOverlay = strings.TrimSpace(overlay)
		}
	}
	out.ToolDescriptors = append([]ToolDescriptor(nil), normalizedToolDescriptors(out)...)
	out.Files = mergeFiles(out.Files, out.WorkspaceFiles)
	out.Artifacts = mergeFiles(out.Artifacts, out.Files, out.WorkspaceFiles)
	return out, nil
}

func normalizedToolDescriptors(in Input) []ToolDescriptor {
	out := make([]ToolDescriptor, 0, len(in.ToolDescriptors)+len(in.Tools))
	seen := make(map[string]struct{}, cap(out))
	for _, descriptor := range append(append([]ToolDescriptor(nil), in.ToolDescriptors...), in.Tools...) {
		key := descriptor.Name + "\x00" + descriptor.Description + "\x00" + descriptor.Schema
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, descriptor)
	}
	return out
}

func mergeFiles(groups ...[]ArtifactRef) []ArtifactRef {
	var out []ArtifactRef
	seen := make(map[string]int)
	for _, group := range groups {
		for _, file := range group {
			if file.Path == "" && file.Content == "" {
				continue
			}
			if idx, ok := seen[file.Path]; ok && file.Path != "" {
				if out[idx].Content == "" && file.Content != "" {
					out[idx].Content = file.Content
				}
				if out[idx].Size == 0 {
					out[idx].Size = file.Size
				}
				out[idx].Critical = out[idx].Critical || file.Critical
				out[idx].Truncated = out[idx].Truncated || file.Truncated
				if file.Priority > out[idx].Priority {
					out[idx].Priority = file.Priority
				}
				continue
			}
			copy := file
			if copy.Size == 0 && copy.Content != "" {
				copy.Size = len([]byte(copy.Content))
			}
			if file.Path != "" {
				seen[file.Path] = len(out)
			}
			out = append(out, copy)
		}
	}
	return out
}

func nonCriticalShares(configured map[Source]int) map[Source]int {
	shares := make(map[Source]int)
	for _, src := range priorityOrder {
		if isReservedSource(src) {
			continue
		}
		if pct := configured[src]; pct > 0 {
			shares[src] = pct
		}
	}
	if len(shares) == 0 {
		return cloneShares(defaultShares)
	}
	return shares
}

func isProviderReservationSource(src Source) bool {
	return src == SourceSystemInstructions || src == SourceSchemaOverlay || src == SourceToolDescriptors
}

func isCriticalSource(src Source) bool {
	return isProviderReservationSource(src)
}

func isReservedSource(src Source) bool {
	return isCriticalSource(src) || src == SourceUserRequest
}

func sourceExcluded(src Source, exclusions []string) bool {
	if isReservedSource(src) {
		return false
	}
	label := string(src)
	for _, exclusion := range exclusions {
		exclusion = strings.ToLower(strings.TrimSpace(exclusion))
		if exclusion == "" {
			continue
		}
		if exclusion == label || exclusion == strings.TrimPrefix(label, "source_") {
			return true
		}
		switch src {
		case SourceWorkflow:
			if exclusion == "workflow" {
				return true
			}
		case SourceRecentTurns:
			if exclusion == "history" || exclusion == "conversation" {
				return true
			}
		case SourceSessionCompact:
			if exclusion == "compact" {
				return true
			}
		case SourceArtifacts:
			if exclusion == "files" || exclusion == "workspace_files" || exclusion == "file_context" {
				return true
			}
		case SourceProjectKnowledge:
			if exclusion == "knowledge" {
				return true
			}
		}
	}
	return false
}

func pathExcluded(path string, exclusions []string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	for _, exclusion := range exclusions {
		exclusion = strings.TrimSpace(exclusion)
		if exclusion == path || strings.HasPrefix(exclusion, "file:") && strings.TrimPrefix(exclusion, "file:") == path {
			return true
		}
	}
	return false
}

func splitArtifacts(files []ArtifactRef) (required, optional []ArtifactRef) {
	required = make([]ArtifactRef, 0, len(files))
	optional = make([]ArtifactRef, 0, len(files))
	for _, file := range files {
		if file.Critical {
			required = append(required, file)
			continue
		}
		optional = append(optional, file)
	}
	return required, optional
}

func normalizeContextPolicy(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "", "repository", "repo", "workspace":
		return "repository"
	case "target", "target_file", "target_file_only", "target-only":
		return "target_file_only"
	case "none", "zero", "off":
		return "none"
	default:
		return value
	}
}

func isWorkspaceSource(src Source) bool {
	switch src {
	case SourceRecentTurns, SourceSessionCompact, SourceArtifacts, SourceProjectKnowledge:
		return true
	default:
		return false
	}
}

func sourceAllowedByPolicy(policy string, src Source) bool {
	switch policy {
	case "none":
		return !isWorkspaceSource(src) && src != SourceWorkflow
	case "target_file_only":
		return src == SourceArtifacts
	default:
		return true
	}
}

func (c *Compiler) fitSource(src Source, in Input, share int) (string, int, []string) {
	if share <= 0 {
		switch src {
		case SourceUserRequest, SourceWorkflow, SourceRecentTurns, SourceSessionCompact, SourceArtifacts, SourceProjectKnowledge:
			if src == SourceArtifacts {
				paths := make([]string, 0, len(in.Artifacts))
				for _, file := range in.Artifacts {
					if file.Path != "" {
						paths = append(paths, file.Path)
					}
				}
				return "", 1, paths
			}
			return "", 1, nil
		default:
			return "", 0, nil
		}
	}
	switch src {
	case SourceUserRequest:
		return in.UserRequest, 0, nil
	case SourceWorkflow:
		return in.WorkflowState, 0, nil
	case SourceRecentTurns:
		content, dropped := renderTurns(in.RecentTurns, share)
		return content, dropped, nil
	case SourceSessionCompact:
		content := renderCompact(in.SessionCompact)
		if EstimateTokens(content) > share {
			return content, 1, nil
		}
		return content, 0, nil
	case SourceArtifacts:
		return renderArtifacts(in.Artifacts, share)
	case SourceProjectKnowledge:
		content, dropped := selectKnowledge(in.Knowledge, share)
		return content, dropped, nil
	default:
		return "", 0, nil
	}
}

func criticalSection(src Source, content string) Section {
	content = strings.TrimSpace(content)
	return Section{Source: src, Header: headerFor(src), Content: content, Critical: src != SourceUserRequest}
}

func compactSections(sections []Section) []Section {
	out := make([]Section, 0, len(sections))
	for _, section := range sections {
		if strings.TrimSpace(section.Content) != "" {
			out = append(out, section)
		}
	}
	return out
}

func sectionText(section Section) string {
	if section.Header == "" {
		return section.Content
	}
	return "### " + section.Header + "\n" + section.Content
}

func assembleSections(sections []Section) string {
	var b strings.Builder
	for i, section := range sections {
		if i > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(sectionText(section))
	}
	return b.String()
}

func fitSectionContent(src Source, content string, share int) (string, bool) {
	if content == "" {
		return "", false
	}
	header := headerFor(src)
	prefix := ""
	if header != "" {
		prefix = "### " + header + "\n"
	}
	if share <= 0 {
		return "", true
	}
	if EstimateTokens(prefix+content) <= share {
		return content, false
	}
	contentBudget := share - EstimateTokens(prefix)
	if contentBudget <= 0 {
		return "", true
	}
	return truncateToTokens(content, contentBudget), true
}

func trimToTotal(out *CompiledContext, total int) error {
	if out == nil {
		return fmt.Errorf("%w: nil compiled context", ErrBudgetExceeded)
	}
	for EstimateTokens(out.Assemble()) > total {
		idx := -1
		for i := len(out.Sections) - 1; i >= 0; i-- {
			if !isReservedSource(out.Sections[i].Source) && !out.Sections[i].Critical {
				idx = i
				break
			}
		}
		if idx < 0 {
			return fmt.Errorf("%w: critical prompt material exceeds %d tokens", ErrBudgetExceeded, total)
		}
		out.Sections = append(out.Sections[:idx], out.Sections[idx+1:]...)
		out.Truncated = true
		out.Dropped++
	}
	return nil
}

func appendUniqueStrings(dst []string, values ...string) []string {
	seen := make(map[string]struct{}, len(dst)+len(values))
	for _, value := range dst {
		seen[value] = struct{}{}
	}
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		dst = append(dst, value)
	}
	return dst
}

func sourceTokens(sections []Section) map[Source]int {
	out := make(map[Source]int)
	for _, section := range sections {
		out[section.Source] += EstimateTokens(sectionText(section))
	}
	return out
}

func renderToolDescriptors(tools []ToolDescriptor) string {
	if len(tools) == 0 {
		return ""
	}
	var b strings.Builder
	for _, tool := range tools {
		if tool.Name == "" && tool.Description == "" && tool.Schema == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString("tool: ")
		b.WriteString(tool.Name)
		if tool.Description != "" {
			b.WriteString(" — ")
			b.WriteString(tool.Description)
		}
		if tool.Schema != "" {
			b.WriteString("\n")
			b.WriteString(tool.Schema)
		}
	}
	return b.String()
}

func selectKnowledge(assets []knowledge.Asset, share int) (string, int) {
	if share <= 0 {
		return "", len(assets)
	}
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
		if used+toks > share {
			dropped++
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(block)
		used += toks
	}
	return b.String(), dropped
}

func renderTurns(msgs []session.Message, share int) (string, int) {
	if share <= 0 {
		return "", len(msgs)
	}
	var b strings.Builder
	used := 0
	dropped := 0
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		line := m.Role + ": " + m.Content
		toks := EstimateTokens(line)
		if used+toks > share {
			dropped++
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(line)
		used += toks
	}
	return b.String(), dropped
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
			b.WriteByte('\n')
		}
		b.WriteString("summary: " + cc.Summary)
	}
	if len(cc.DirtyFiles) > 0 {
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString("uncommitted workspace changes (from a previous session): " + strings.Join(cc.DirtyFiles, ", "))
	}
	for _, m := range cc.Recent {
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(m.Role + ": " + m.Content)
	}
	return b.String()
}

func renderArtifactBlock(file ArtifactRef) string {
	if file.Content == "" {
		return fmt.Sprintf("%s (%d bytes)", file.Path, file.Size)
	}
	return fmt.Sprintf("### FILE: %s\n```\n%s\n```", file.Path, file.Content)
}

func sortedArtifacts(refs []ArtifactRef) []ArtifactRef {
	files := append([]ArtifactRef(nil), refs...)
	sort.SliceStable(files, func(i, j int) bool {
		if files[i].Critical != files[j].Critical {
			return files[i].Critical
		}
		if files[i].Priority != files[j].Priority {
			return files[i].Priority > files[j].Priority
		}
		return false
	})
	return files
}

func renderArtifactBlocks(refs []ArtifactRef) string {
	var b strings.Builder
	for _, file := range sortedArtifacts(refs) {
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(renderArtifactBlock(file))
	}
	return b.String()
}

func renderArtifacts(refs []ArtifactRef, share int) (string, int, []string) {
	truncatedFiles := make([]string, 0)
	if share <= 0 {
		for _, file := range sortedArtifacts(refs) {
			if file.Path != "" {
				truncatedFiles = append(truncatedFiles, file.Path)
			}
		}
		return "", len(refs), truncatedFiles
	}
	var b strings.Builder
	used := 0
	dropped := 0
	for _, file := range sortedArtifacts(refs) {
		if file.Truncated {
			dropped++
			if file.Path != "" {
				truncatedFiles = append(truncatedFiles, file.Path)
			}
		}
		content := renderArtifactBlock(file)
		toks := EstimateTokens(content)
		if used+toks > share {
			if file.Path != "" {
				truncatedFiles = append(truncatedFiles, file.Path)
			}
			if b.Len() == 0 {
				prefix := ""
				if file.Path != "" {
					prefix = "### FILE: " + file.Path + "\n"
				}
				contentBudget := share - EstimateTokens(prefix)
				if contentBudget > 0 {
					b.WriteString(prefix)
					b.WriteString(truncateToTokens(strings.TrimPrefix(content, prefix), contentBudget))
					used = share
				}
			}
			if !file.Truncated {
				dropped++
			}
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(content)
		used += toks
	}
	return b.String(), dropped, appendUniqueStrings(nil, truncatedFiles...)
}

// fingerprint is a SHA-256 over the canonical encoding of every underlying
// state input. Content, model limits, phase and contract metadata are included
// so a changed budget or schema cannot reuse an old projection.
func (c *Compiler) fingerprint(in Input) string {
	var b strings.Builder
	write := func(s string) {
		fmt.Fprintf(&b, "%d:%s\x00", len(s), s)
	}
	write(in.SnapshotID)
	write(string(in.Phase))
	write(in.Provider)
	write(in.Model)
	fmt.Fprintf(&b, "%d:%d:%d:%d:%d\x00", in.ContextWindow, in.MaxOutputTokens, in.RequestedOutputTokens, in.ContextBudget, len(in.Exclusions))
	write(in.SystemInstructions)
	write(in.SchemaOverlay)
	write(in.ContextPolicy)
	write(in.Scope)
	write(in.Lineage)
	for _, exclusion := range in.Exclusions {
		write(exclusion)
	}
	if in.Contract != nil {
		write(string(in.Contract.Contract))
		write(string(in.Contract.Kind))
		write(string(in.Contract.OutputSchema))
		write(in.Contract.StructuralOutputSchema)
		write(in.Contract.Schema)
		write(in.Contract.SchemaVersion)
		fmt.Fprintf(&b, "%d:%d:%t:%t:%t:%t:%t\x00",
			in.Contract.MaxOutputTokens,
			in.Contract.MaxTasks,
			in.Contract.Tools,
			in.Contract.StructuredOutput,
			in.Contract.Streaming,
			in.Contract.Reasoning,
			in.Contract.ConstrainedModel,
		)
		write(string(in.Contract.AuthorityCeiling))
		write(string(in.Contract.ContextProfile))
		write(string(in.Contract.PromptProfile))
		write(string(in.Contract.Archetype))
	}
	for _, tool := range normalizedToolDescriptors(in) {
		write(tool.Name)
		write(tool.Description)
		write(tool.Schema)
		fmt.Fprintf(&b, "%t\x00", tool.Critical)
	}
	write(in.UserRequest)
	write(in.WorkflowState)
	fmt.Fprintf(&b, "%d\x00", len(in.RecentTurns))
	for _, message := range in.RecentTurns {
		write(message.Role + ":" + message.Content)
	}
	if in.SessionCompact != nil {
		fmt.Fprintf(&b, "%d\x00", in.SessionCompact.Generation)
		write(in.SessionCompact.Summary)
		write(in.SessionCompact.Objective)
		for _, dirty := range in.SessionCompact.DirtyFiles {
			write(dirty)
		}
		for _, message := range in.SessionCompact.Recent {
			write(message.Role + ":" + message.Content)
		}
	}
	for _, file := range mergeFiles(in.Files, in.WorkspaceFiles, in.Artifacts) {
		write(file.Path)
		fmt.Fprintf(&b, "%d:%t:%t:%d\x00", file.Size, file.Critical, file.Truncated, file.Priority)
		write(file.Content)
	}
	for _, asset := range in.Knowledge {
		write(asset.ID)
		write(string(asset.Kind))
		write(asset.Title)
		write(asset.Body)
		fmt.Fprintf(&b, "%f\x00", asset.Confidence)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

func cloneCompiled(in *CompiledContext) *CompiledContext {
	if in == nil {
		return nil
	}
	out := *in
	out.Sections = append([]Section(nil), in.Sections...)
	out.TruncatedFiles = append([]string(nil), in.TruncatedFiles...)
	out.Exclusions = append([]string(nil), in.Exclusions...)
	out.Budget.BySource = cloneSourceMap(in.Budget.BySource)
	return &out
}

func cloneSourceMap(in map[Source]int) map[Source]int {
	if in == nil {
		return nil
	}
	out := make(map[Source]int, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func truncateToTokens(s string, budget int) string {
	if budget <= 0 {
		return ""
	}
	if EstimateTokens(s) <= budget {
		return s
	}
	runes := []rune(s)
	maxChars := budget * 4
	if maxChars > len(runes) {
		maxChars = len(runes)
	}
	out := string(runes[:maxChars]) + "…"
	for len(out) > 0 && EstimateTokens(out) > budget {
		r := []rune(out)
		if len(r) <= 1 {
			return ""
		}
		out = string(r[:len(r)-1])
	}
	return out
}

func headerFor(src Source) string {
	switch src {
	case SourceSystemInstructions:
		return "SYSTEM INSTRUCTIONS"
	case SourceSchemaOverlay:
		return "OUTPUT SCHEMA"
	case SourceToolDescriptors:
		return "TOOL DESCRIPTORS"
	case SourceUserRequest:
		return "USER REQUEST"
	case SourceWorkflow:
		return "WORKFLOW STATE"
	case SourceRecentTurns:
		return "RECENT TURNS"
	case SourceSessionCompact:
		return "SESSION COMPACT"
	case SourceArtifacts:
		return "WORKSPACE FILES"
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

// Priority is a small compatibility helper for callers that use the original
// artifact descriptor as a generic context item.
func (a ArtifactRef) PriorityValue() int { return a.Priority }
