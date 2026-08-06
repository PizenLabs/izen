package capability

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
)

// Errors returned by the Registry. They are wrapped with the offending id so
// callers can map them back to the registration or resolution that failed.
var (
	// ErrNilCapability is returned when Register is handed a nil instance.
	ErrNilCapability = errors.New("capability: nil capability")
	// ErrEmptyID is returned when a capability or id has no identifier.
	ErrEmptyID = errors.New("capability: empty capability id")
	// ErrDuplicate is returned when an id is registered twice.
	ErrDuplicate = errors.New("capability: duplicate registration")
)

// Registry is the thread-safe single source of truth for capabilities. It
// stores Capability instances keyed by CapabilityID and resolves requested id
// sets into the active instances. The Registry never interprets capability
// semantics — prompt generation and validation are owned by the capabilities.
//
// The zero value is not usable; construct with NewRegistry.
type Registry struct {
	mu   sync.RWMutex
	caps map[CapabilityID]Capability
	ids  []CapabilityID
}

// NewRegistry returns an empty capability registry.
func NewRegistry() *Registry {
	return &Registry{caps: make(map[CapabilityID]Capability)}
}

// Register stores c under c.ID(). A nil capability, an empty id, or a
// duplicate id is rejected. Register is safe for concurrent use.
func (r *Registry) Register(c Capability) error {
	if c == nil {
		return ErrNilCapability
	}
	id := c.ID()
	if id == "" {
		return ErrEmptyID
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.caps[id]; ok {
		return fmt.Errorf("%w: %s", ErrDuplicate, id)
	}
	r.caps[id] = c
	r.ids = append(r.ids, id)
	return nil
}

// RegisterAll registers every capability, returning the first registration
// error encountered. Capabilities registered before the failure remain in the
// registry. RegisterAll is safe for concurrent use.
func (r *Registry) RegisterAll(caps ...Capability) error {
	for _, c := range caps {
		if err := r.Register(c); err != nil {
			return err
		}
	}
	return nil
}

// Lookup returns the capability registered under id, if any.
func (r *Registry) Lookup(id CapabilityID) (Capability, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.caps[id]
	return c, ok
}

// Has reports whether id is registered.
func (r *Registry) Has(id CapabilityID) bool {
	_, ok := r.Lookup(id)
	return ok
}

// Resolve resolves the requested id set into the active Capability instances,
// preserving request order. A single unknown id fails the whole resolution so
// callers never operate on a partial capability set.
func (r *Registry) Resolve(ids ...CapabilityID) ([]Capability, error) {
	out := make([]Capability, 0, len(ids))
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, id := range ids {
		c, ok := r.caps[id]
		if !ok {
			return nil, fmt.Errorf("capability: unresolved id %q", id)
		}
		out = append(out, c)
	}
	return out, nil
}

// ResolveOne resolves a single id into its active capability instance.
func (r *Registry) ResolveOne(id CapabilityID) (Capability, error) {
	caps, err := r.Resolve(id)
	if err != nil {
		return nil, err
	}
	return caps[0], nil
}

// IDs returns the registered ids in registration order.
func (r *Registry) IDs() []CapabilityID {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]CapabilityID(nil), r.ids...)
}

// SortedIDs returns the registered ids in sorted order for deterministic
// enumeration.
func (r *Registry) SortedIDs() []CapabilityID {
	ids := r.IDs()
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// Len returns the number of registered capabilities.
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.caps)
}

// Validate resolves id and runs its Validate against data. An unknown id
// returns an error; otherwise the capability's own ValidationResult is
// returned unchanged.
func (r *Registry) Validate(ctx context.Context, id CapabilityID, data []byte) (ValidationResult, error) {
	c, err := r.ResolveOne(id)
	if err != nil {
		return ValidationResult{}, err
	}
	return c.Validate(ctx, data), nil
}

// PromptRepresentation resolves id and renders its prompt for the given
// model tier. An unknown id returns an error.
func (r *Registry) PromptRepresentation(id CapabilityID, modelTier string) (string, error) {
	c, err := r.ResolveOne(id)
	if err != nil {
		return "", err
	}
	return c.PromptRepresentation(modelTier), nil
}
