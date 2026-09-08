package capability

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"
)

// registryTTL is how long a refreshed capability snapshot is considered fresh
// before Refresh re-consults the adapter.
const registryTTL = 5 * time.Minute

// Registry is the dynamic model capability store. It owns the mapping from
// provider/model identity to ModelCapabilities and guarantees every stored
// record is normalized (heuristic enrichment applied). It is safe for
// concurrent use.
type Registry struct {
	mu      sync.RWMutex
	models  []ModelCapabilities
	index   map[string]int
	adapter Adapter
	cached  time.Time
	ttl     time.Duration
}

// NewRegistry returns an empty registry with no adapter wired. Models can be
// registered programmatically via Register.
func NewRegistry() *Registry {
	return &Registry{
		index: make(map[string]int),
		ttl:   registryTTL,
	}
}

// NewRegistryWithAdapter returns a registry wired to inspect a provider surface
// via adapter.
func NewRegistryWithAdapter(adapter Adapter) *Registry {
	r := NewRegistry()
	r.adapter = adapter
	return r
}

// SetAdapter wires or replaces the inspect adapter.
func (r *Registry) SetAdapter(adapter Adapter) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.adapter = adapter
	r.mu.Unlock()
}

// SetTTL overrides the snapshot freshness window.
func (r *Registry) SetTTL(ttl time.Duration) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.ttl = ttl
	r.mu.Unlock()
}

// key builds the canonical registry key for a provider/model pair. OpenRouter
// IDs already embed the vendor prefix, so a provider of "openrouter" with a
// vendor-prefixed ID is keyed by the ID alone to avoid duplicate entries.
func key(provider, modelID string) string {
	prov := strings.ToLower(strings.TrimSpace(provider))
	id := strings.ToLower(strings.TrimSpace(modelID))
	if prov == "openrouter" && strings.Contains(id, "/") {
		return id
	}
	return prov + "/" + id
}

// Register upserts a normalized model capability record. A later Register for
// the same provider/model replaces the earlier record.
func (r *Registry) Register(c ModelCapabilities) {
	if r == nil {
		return
	}
	c = c.Normalize()
	k := key(c.Provider, c.ModelID)
	r.mu.Lock()
	defer r.mu.Unlock()
	if idx, ok := r.index[k]; ok {
		r.models[idx] = c
		return
	}
	r.index[k] = len(r.models)
	r.models = append(r.models, c)
}

// Refresh pulls the provider surface through the wired adapter and replaces
// the stored snapshot. An empty adapter returns ErrNoAdapter. A partial
// adapter failure aborts the refresh so a stale-but-consistent snapshot is
// never mixed with fresh data.
func (r *Registry) Refresh(ctx context.Context) error {
	if r == nil {
		return errRegistryNil
	}
	r.mu.RLock()
	adapter := r.adapter
	ttl := r.ttl
	last := r.cached
	r.mu.RUnlock()

	if adapter == nil {
		return ErrNoAdapter
	}
	if !last.IsZero() && ttl > 0 && time.Since(last) < ttl {
		return nil
	}

	models, err := adapter.Inspect(ctx)
	if err != nil {
		return err
	}
	normalized := make([]ModelCapabilities, len(models))
	for i, m := range models {
		normalized[i] = m.Normalize()
	}
	sort.SliceStable(normalized, func(i, j int) bool {
		if normalized[i].Provider != normalized[j].Provider {
			return normalized[i].Provider < normalized[j].Provider
		}
		return normalized[i].ModelID < normalized[j].ModelID
	})

	r.mu.Lock()
	r.models = normalized
	r.index = make(map[string]int, len(normalized))
	for i, m := range normalized {
		r.index[key(m.Provider, m.ModelID)] = i
	}
	r.cached = time.Now()
	r.mu.Unlock()
	return nil
}

// Models returns a defensive snapshot of the registered models in stable
// (provider, ID) order.
func (r *Registry) Models() []ModelCapabilities {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]ModelCapabilities(nil), r.models...)
}

// Get returns the normalized capabilities for a provider/model pair.
func (r *Registry) Get(provider, modelID string) (ModelCapabilities, bool) {
	if r == nil {
		return ModelCapabilities{}, false
	}
	k := key(provider, modelID)
	r.mu.RLock()
	defer r.mu.RUnlock()
	idx, ok := r.index[k]
	if !ok {
		return ModelCapabilities{}, false
	}
	return r.models[idx], true
}

// Len reports the number of registered models.
func (r *Registry) Len() int {
	if r == nil {
		return 0
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.models)
}
