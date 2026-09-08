// Package validator implements the pluggable artifact validation engine of
// the V3 Artifact Protocol. Language validators parse an artifact's content
// with a real parser (HTML via golang.org/x/net/html, JSON via encoding/json,
// Go via go/parser) and return a deterministic error on failure.
//
// The package is decoupled from the capability layer: an ArtifactValidator
// knows only its language scope and how to validate bytes. A ValidatorRegistry
// stores and resolves validators by language tag so the failure policy layer
// can classify validation errors without coupling to any one parser.
package validator

import "context"

// ErrUnregistered is returned when a registry is asked to validate a language
// it has no validator for.
//
// Language tags are lower-case canonical names ("html", "json", "go"). A
// missing validator is a policy-neutral outcome: the caller decides whether an
// unknown language should pass, warn, or fail.
type ErrUnregistered struct{ Language string }

// Error implements error.
func (e ErrUnregistered) Error() string {
	return "validator: no validator registered for language " + e.Language
}

// ArtifactValidator validates artifact content for one or more language tags.
// Validate returns nil when data parses cleanly and a non-nil error carrying
// the exact parser diagnostics when it does not.
type ArtifactValidator interface {
	// ID returns a stable, unique identifier for the validator.
	ID() string
	// Languages returns the canonical language tags this validator serves
	// (e.g. "html", "htm", "xhtml" for HTMLValidator).
	Languages() []string
	// Validate parses data and returns nil, or a detailed error describing
	// the structural failure.
	Validate(ctx context.Context, data []byte) error
}

// ValidatorRegistry is the pluggable store and resolver for artifact
// validators. Implementations must be safe for concurrent use.
type ValidatorRegistry interface {
	// Register adds v under every language tag it serves. A nil validator or
	// a duplicate language tag is rejected.
	Register(v ArtifactValidator) error
	// Lookup returns the validator serving lang, if any.
	Lookup(lang string) (ArtifactValidator, bool)
	// Validate runs the validator serving lang against data. It returns
	// ErrUnregistered when no validator is registered for lang.
	Validate(ctx context.Context, lang string, data []byte) error
	// Languages returns every registered language tag, deduplicated.
	Languages() []string
	// Len returns the number of registered validators.
	Len() int
}
