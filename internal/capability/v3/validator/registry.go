package validator

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
)

// Errors returned by the registry.
var (
	// ErrNilValidator is returned when Register is handed a nil instance.
	ErrNilValidator = errors.New("validator: nil validator")
	// ErrEmptyLanguages is returned when a validator declares no language
	// tags.
	ErrEmptyLanguages = errors.New("validator: validator declares no languages")
	// ErrDuplicateLanguage is returned when two validators claim the same
	// language tag.
	ErrDuplicateLanguage = errors.New("validator: duplicate language registration")
)

// Registry is the thread-safe, pluggable store of artifact validators, keyed
// by canonical language tag. Construct with NewRegistry. The zero value is
// not usable.
type Registry struct {
	mu        sync.RWMutex
	byID      map[string]ArtifactValidator
	byLang    map[string]ArtifactValidator
	languages []string
}

// NewRegistry returns an empty validator registry.
func NewRegistry() *Registry {
	return &Registry{
		byID:   make(map[string]ArtifactValidator),
		byLang: make(map[string]ArtifactValidator),
	}
}

// Register adds v under every language tag it serves. A nil validator, an
// empty language set, or a duplicate tag is rejected. Register is safe for
// concurrent use.
func (r *Registry) Register(v ArtifactValidator) error {
	if v == nil {
		return ErrNilValidator
	}
	if v.ID() == "" {
		return fmt.Errorf("validator: empty validator id")
	}
	langs := v.Languages()
	if len(langs) == 0 {
		return ErrEmptyLanguages
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	seen := make(map[string]struct{}, len(langs))
	for _, l := range langs {
		if _, ok := seen[l]; ok {
			return fmt.Errorf("validator: duplicate language %q within one validator", l)
		}
		seen[l] = struct{}{}
	}
	for _, l := range langs {
		if _, ok := r.byLang[l]; ok {
			return fmt.Errorf("%w: %s", ErrDuplicateLanguage, l)
		}
	}
	if _, ok := r.byID[v.ID()]; ok {
		return fmt.Errorf("validator: duplicate id %q", v.ID())
	}
	r.byID[v.ID()] = v
	for _, l := range langs {
		r.byLang[l] = v
		r.languages = append(r.languages, l)
	}
	return nil
}

// RegisterAll registers every validator, returning the first error. Validators
// registered before a failure remain in the registry.
func (r *Registry) RegisterAll(vs ...ArtifactValidator) error {
	for _, v := range vs {
		if err := r.Register(v); err != nil {
			return err
		}
	}
	return nil
}

// Lookup returns the validator serving lang, if any.
func (r *Registry) Lookup(lang string) (ArtifactValidator, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	v, ok := r.byLang[lang]
	return v, ok
}

// Has reports whether a validator is registered for lang.
func (r *Registry) Has(lang string) bool {
	_, ok := r.Lookup(lang)
	return ok
}

// Validate runs the validator serving lang against data. An unregistered
// language yields ErrUnregistered.
func (r *Registry) Validate(ctx context.Context, lang string, data []byte) error {
	v, ok := r.Lookup(lang)
	if !ok {
		return ErrUnregistered{Language: lang}
	}
	return v.Validate(ctx, data)
}

// Languages returns every registered language tag in sorted order for
// deterministic enumeration.
func (r *Registry) Languages() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := append([]string(nil), r.languages...)
	sort.Strings(out)
	return out
}

// Len returns the number of registered validators.
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.byID)
}

// DefaultRegistry returns the registry pre-loaded with every active language
// validator: HTML, JSON and Go. It is the canonical default validator set for
// the V3 artifact pipeline.
func DefaultRegistry() *Registry {
	r := NewRegistry()
	_ = r.RegisterAll(
		NewHTMLValidator(),
		NewJSONValidator(),
		NewGoValidator(),
	)
	return r
}
