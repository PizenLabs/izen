package context

import (
	"fmt"
	"sort"
	"strings"

	stdctx "context"
)

// PlanningContext is the immutable, assembled context handed to a strategy.
// It is built once by a Collector and never mutated afterwards: chunks are
// copied in on assembly and every accessor returns a defensive copy.
type PlanningContext struct {
	chunks   []ContextChunk
	index    map[ProviderName]int
	failures []error
}

// Chunks returns a defensive copy of the assembled chunks in registration
// order.
func (p PlanningContext) Chunks() []ContextChunk {
	return append([]ContextChunk(nil), p.chunks...)
}

// Providers returns the provider names present in the context, sorted.
func (p PlanningContext) Providers() []ProviderName {
	names := make([]ProviderName, 0, len(p.index))
	for n := range p.index {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool { return names[i] < names[j] })
	return names
}

// Len returns the number of assembled chunks.
func (p PlanningContext) Len() int { return len(p.chunks) }

// Get returns the chunk produced by the named provider. The boolean is
// false when the provider contributed no chunk.
func (p PlanningContext) Get(name ProviderName) (ContextChunk, bool) {
	i, ok := p.index[name]
	if !ok {
		return ContextChunk{}, false
	}
	return p.chunks[i], true
}

// Prompt returns the raw prompt chunk, or the empty string when no prompt
// provider contributed one.
func (p PlanningContext) Prompt() string {
	c, ok := p.Get(ProviderPrompt)
	if !ok {
		return ""
	}
	return c.Content
}

// Errors returns the non-fatal provider failures recorded during assembly.
func (p PlanningContext) Errors() []error {
	return append([]error(nil), p.failures...)
}

// Merge returns a new PlanningContext combining this context with other.
// Chunks are appended in order; a provider present in both contexts keeps
// only the first occurrence. The receiver is left unchanged.
func (p PlanningContext) Merge(other PlanningContext) PlanningContext {
	merged := PlanningContext{index: make(map[ProviderName]int, len(p.index)+len(other.index))}
	merged.chunks = append(merged.chunks, p.chunks...)
	for i, c := range p.chunks {
		merged.index[c.Provider] = i
	}
	for _, c := range other.chunks {
		if _, exists := merged.index[c.Provider]; exists {
			continue
		}
		merged.index[c.Provider] = len(merged.chunks)
		merged.chunks = append(merged.chunks, c)
	}
	merged.failures = append(append([]error(nil), p.failures...), other.failures...)
	return merged
}

// Render assembles every non-empty chunk into a prompt-ready block. Chunks
// are rendered in registration order, each under a "### Provider" header.
func (p PlanningContext) Render() string {
	var b strings.Builder
	for _, c := range p.chunks {
		if c.Content == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("### " + string(c.Provider) + "\n")
		b.WriteString(c.Content)
	}
	return b.String()
}

// RenderChunk renders a single named provider's chunk, or "" when absent.
func (p PlanningContext) RenderChunk(name ProviderName) string {
	c, ok := p.Get(name)
	if !ok {
		return ""
	}
	return c.Content
}

// ─── Collector ───────────────────────────────────────────────────────────────

// Collector assembles a PlanningContext from an ordered set of modular
// ContextProvider plugins. Providers are collected in registration order and
// their chunks copied into an immutable PlanningContext. A provider that
// fails degrades gracefully: its error is recorded on the context and the
// remaining providers still run.
type Collector struct {
	providers []ProviderName
	plugins   map[ProviderName]ContextProvider
}

// NewCollector returns an empty Collector.
func NewCollector() *Collector {
	return &Collector{plugins: make(map[ProviderName]ContextProvider)}
}

// Register adds a provider under a stable name. Registering an existing name
// replaces the previous provider and preserves its registration position.
func (c *Collector) Register(name ProviderName, p ContextProvider) {
	if p == nil {
		return
	}
	if _, exists := c.plugins[name]; !exists {
		c.providers = append(c.providers, name)
	}
	c.plugins[name] = p
}

// ProviderNames returns the registered provider names in registration order.
func (c *Collector) ProviderNames() []ProviderName {
	return append([]ProviderName(nil), c.providers...)
}

// Collect runs every registered provider and assembles the immutable
// PlanningContext. It returns an error only when the context is cancelled or
// no providers are registered; individual provider failures are recorded on
// the returned context and never abort the assembly.
func (c *Collector) Collect(ctx stdctx.Context) (PlanningContext, error) {
	if len(c.providers) == 0 {
		return PlanningContext{}, fmt.Errorf("context: no providers registered")
	}
	pc := PlanningContext{index: make(map[ProviderName]int, len(c.providers))}
	for _, name := range c.providers {
		if ctx.Err() != nil {
			return PlanningContext{}, ctx.Err()
		}
		chunk, err := c.plugins[name].Collect(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return PlanningContext{}, ctx.Err()
			}
			pc.failures = append(pc.failures, fmt.Errorf("context: provider %s: %w", name, err))
			continue
		}
		if chunk.Provider == "" {
			chunk.Provider = name
		}
		pc.index[name] = len(pc.chunks)
		pc.chunks = append(pc.chunks, chunk)
	}
	return pc, nil
}
