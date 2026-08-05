// Package context is the microkernel's context layer. It replaces the legacy
// Evidence collector with a modular, plugin-based ContextProvider surface:
// each provider Collects one unit of PlanningContext without assuming the
// workspace or environment is in any particular state. Providers never fail
// an assembly — a provider that cannot produce a chunk records its error in
// the chunk metadata and the rest of the assembly continues.
//
// The package is named context to sit beside the microkernel engine packages,
// so imports of the standard library context.Context must alias the package.
package context

import stdctx "context"

// ProviderName identifies a ContextProvider in the assembled PlanningContext.
type ProviderName string

// Well-known provider names. Strategies read chunks by name, so providers
// must be registered under one of these stable identifiers.
const (
	// ProviderFilesystem gathers the workspace file surface.
	ProviderFilesystem ProviderName = "filesystem"
	// ProviderEnvironment gathers runtime and OS facts.
	ProviderEnvironment ProviderName = "environment"
	// ProviderRepository gathers version-control facts.
	ProviderRepository ProviderName = "repository"
	// ProviderPrompt carries the raw user prompt.
	ProviderPrompt ProviderName = "prompt"
)

// MetaKey is a metadata key stored on a ContextChunk.
type MetaKey string

// Well-known chunk metadata keys.
const (
	// MetaKeyError records a provider-side failure so the assembly can
	// continue without assuming the provider succeeded.
	MetaKeyError MetaKey = "error"
	// MetaKeyEmpty records that the provider produced no content.
	MetaKeyEmpty MetaKey = "empty"
	// MetaKeyProvider tags the chunk with its originating provider name.
	MetaKeyProvider MetaKey = "provider"
)

// ContextChunk is one unit of context produced by a single ContextProvider.
// It is a read-only value: providers produce it, the Collector copies it
// into the PlanningContext, and no stage mutates it afterwards.
type ContextChunk struct {
	// Provider is the stable name of the source provider.
	Provider ProviderName `json:"provider"`
	// Content is the collected context payload.
	Content string `json:"content"`
	// Meta carries optional key/value annotations.
	Meta map[string]string `json:"meta,omitempty"`
}

// GetMeta returns a metadata value, or "" when absent.
func (c ContextChunk) GetMeta(key MetaKey) string {
	if c.Meta == nil {
		return ""
	}
	return c.Meta[string(key)]
}

// Errored reports whether the provider recorded a failure on the chunk.
func (c ContextChunk) Errored() bool { return c.GetMeta(MetaKeyError) != "" }

// Empty reports whether the chunk carries no content payload.
func (c ContextChunk) Empty() bool {
	return c.GetMeta(MetaKeyEmpty) == "true" && c.Content == ""
}

// ContextProvider is a modular source of planning context. Implementations
// must not assume the workspace, environment or repository is in any
// particular state: a missing directory, absent git checkout or unset
// variable must degrade to an empty chunk (with metadata describing the
// situation) rather than an error.
type ContextProvider interface {
	// Collect gathers one ContextChunk. The error return is reserved for
	// catastrophic failures (for example a cancelled context); ordinary
	// environmental absences are reported via chunk metadata.
	Collect(ctx stdctx.Context) (ContextChunk, error)
}

// ProviderFunc adapts a plain function to the ContextProvider interface.
type ProviderFunc func(ctx stdctx.Context) (ContextChunk, error)

// Collect implements ContextProvider.
func (f ProviderFunc) Collect(ctx stdctx.Context) (ContextChunk, error) { return f(ctx) }
