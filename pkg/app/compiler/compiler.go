package compiler

import (
	"context"

	"github.com/PizenLabs/izen/pkg/ir"
	"github.com/PizenLabs/izen/pkg/knowledge"
)

// Option configures an IntentCompiler.
type Option func(*IntentCompiler)

// WithKnowledgeGraph binds the compiler to a shared RuntimeKnowledge graph.
// The ConflictDetector then serves the workspace state from the graph's cache
// instead of re-walking the disk on every Compile call. A nil argument is
// ignored.
func WithKnowledgeGraph(kg *knowledge.KnowledgeGraph) Option {
	return func(c *IntentCompiler) {
		if kg != nil {
			c.conflicts.SetKnowledge(kg)
		}
	}
}

// IntentCompiler is the facade that composes the four single-responsibility
// compiler stages into the final ir.IntentIR:
//
//	Normalizer → EntityResolver → ConflictDetector → AmbiguityDetector
//
// Normalizer is language-agnostic Unicode cleaning; EntityResolver performs
// zero-shot semantic JSON schema prompting through a SemanticExtractor, so
// the planner always receives a strongly-typed contract in canonical English
// regardless of the prompt's source language.
type IntentCompiler struct {
	root       string
	normalizer *Normalizer
	resolver   *EntityResolver
	conflicts  *ConflictDetector
	ambiguity  *AmbiguityDetector
}

// NewIntentCompiler builds an IntentCompiler bound to the given workspace
// root (scanned by the ConflictDetector for target-type markers) and backed
// by the given semantic extractor. A nil extractor is allowed at
// construction time; Compile reports ErrNoExtractor when it is used.
func NewIntentCompiler(root string, extractor SemanticExtractor, opts ...Option) *IntentCompiler {
	c := &IntentCompiler{
		root:       root,
		normalizer: NewNormalizer(),
		resolver:   NewEntityResolver(extractor),
		conflicts:  NewConflictDetector(),
		ambiguity:  NewAmbiguityDetector(),
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Compile translates a raw natural language prompt into a strongly-typed
// ir.IntentIR. The returned value is fully bound: Category, TargetType,
// Entities, Technologies, PreserveWorkspace and, when the workspace state is
// in conflict, DecisionAmbiguity plus its ClarificationQuestions.
func (c *IntentCompiler) Compile(ctx context.Context, raw string) (ir.IntentIR, error) {
	normalised := c.normalizer.Process(raw)
	res, err := c.resolver.Process(ctx, normalised)
	if err != nil {
		return ir.IntentIR{}, err
	}

	ws := c.conflicts.Detect(c.root)
	conflict := c.conflicts.Process(&res, ws)

	out := ir.IntentIR{
		Category:          res.Category,
		TargetType:        res.TargetType,
		Entities:          copyEntities(res.Entities),
		Technologies:      append([]string(nil), res.Technologies...),
		PreserveWorkspace: res.Category.PreservesWorkspace(),
		DecisionAmbiguity: c.ambiguity.Process(&res, ws, conflict),
	}
	if out.DecisionAmbiguity {
		out.ClarificationQuestions = c.ambiguity.Questions(&res, ws, conflict)
	}
	return out, nil
}

// copyEntities returns a defensive copy of src so the caller can never
// observe subsequent mutations of the resolution's entity map.
func copyEntities(src map[string]string) map[string]string {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]string, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

// DeterministicIntent is the deterministic, language-agnostic headless fallback
// an outer layer (e.g. the pipeline) uses when a prompt cannot be compiled —
// no extractor is wired, the model fails, or no compiler exists at all. It
// never guesses categories from keyword lists: the category is always
// greenfield creation (the least destructive, context-neutral default that
// maps to a generate policy) and the target type is the canonical workspace
// label, so the returned value always validates.
func DeterministicIntent() ir.IntentIR {
	return ir.IntentIR{
		Category:          ir.CategoryCreate,
		TargetType:        "workspace",
		PreserveWorkspace: true,
	}
}
