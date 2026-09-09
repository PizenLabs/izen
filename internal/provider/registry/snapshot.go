package registry

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/PizenLabs/izen/internal/provider/detector"
)

// ProviderSummary is the per-provider rollup carried by a ModelSnapshot.
type ProviderSummary struct {
	Name       string    `json:"name"`
	ModelCount int       `json:"model_count"`
	Status     string    `json:"status"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// ModelSnapshot is the immutable, lock-free read view of the model catalog.
// Instances are never mutated after publication: every UpdateProvider builds
// a completely new ModelSnapshot and swaps it in with CompareAndSwap. Readers
// use Load() for O(1) RAM access with no locks.
type ModelSnapshot struct {
	Models    []Model           `json:"models"`
	Providers []ProviderSummary `json:"providers"`
	Version   uint64            `json:"version"`
	UpdatedAt time.Time         `json:"updated_at"`
}

// FetchFunc fetches model descriptors for one provider. It is a Registry
// field so tests can stub the network without touching the HTTP layer.
type FetchFunc func(ctx context.Context, provider detector.ProviderConfig) ([]ModelDescriptor, error)

// Registry is the thread-safe in-memory model catalog. Reads go through the
// lock-free atomic snapshot; writes use Copy-On-Write CompareAndSwap loops.
// The mutex only guards fetch-function swaps and on-disk persistence — never
// the read path.
type Registry struct {
	snapshot  atomic.Pointer[ModelSnapshot]
	cachePath string

	mu     sync.RWMutex
	fetch  FetchFunc
	client *http.Client
	fileMu sync.Mutex
}

// httpTimeout bounds every /models fetch so background sync can never hang.
const httpTimeout = 15 * time.Second

// cacheFileName is the on-disk model catalog under ~/.izen/cache/.
const cacheFileName = "models.json"

func emptySnapshot() *ModelSnapshot {
	return &ModelSnapshot{
		Models:    []Model{},
		Providers: []ProviderSummary{},
		Version:   0,
		UpdatedAt: time.Now(),
	}
}

// NewRegistry builds a Registry backed by ~/.izen/cache/models.json.
// A missing or unreadable home directory yields an in-memory-only registry
// (cache path empty; flush becomes a no-op).
func NewRegistry() *Registry {
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	return NewRegistryWithHome(home)
}

// NewRegistryWithHome is NewRegistry with an injectable home directory.
func NewRegistryWithHome(home string) *Registry {
	path := ""
	if home != "" {
		path = filepath.Join(home, ".izen", "cache", cacheFileName)
	}
	return NewRegistryWithCachePath(path)
}

// NewRegistryWithCachePath builds a Registry with an explicit cache path
// (tests). A leading ~/ prefix is expanded via ExpandPath before any file
// I/O. An empty path disables persistence.
//
// Cold-start guarantee: non-empty (real) cache paths boot with the embedded
// DefaultSnapshot baseline so the catalog is never empty even without
// network or API keys. Memory-only registries (empty path, used by tests)
// boot empty to preserve deterministic test isolation; LoadCache seeds the
// fallback on missing/empty files.
func NewRegistryWithCachePath(path string) *Registry {
	r := &Registry{
		cachePath: ExpandPath(path),
		client:    &http.Client{Timeout: httpTimeout},
	}
	if ExpandPath(path) == "" {
		r.snapshot.Store(emptySnapshot())
	} else {
		r.snapshot.Store(DefaultSnapshot())
	}
	return r
}

// Load returns the current immutable snapshot via lock-free atomic load
// (O(1) RAM access). The returned pointer must be treated as read-only:
// never mutate its slices or maps.
func (r *Registry) Load() *ModelSnapshot {
	snap := r.snapshot.Load()
	if snap == nil {
		return emptySnapshot()
	}
	return snap
}

// UpdateProvider executes a Copy-On-Write atomic update for a single
// provider using a CompareAndSwap loop. A completely new ModelSnapshot is
// constructed on every update; existing slices in oldSnap are NEVER mutated.
//
// Provenance rule: entries with Status != "ok" (timeout/error) preserve the
// provider's existing models — only the ProviderSummary status is refreshed.
// Models from non-target providers are always carried over untouched.
func (r *Registry) UpdateProvider(providerName string, entry ProviderCacheEntry) {
	if providerName == "" {
		return
	}
	now := time.Now()
	if !entry.UpdatedAt.IsZero() {
		now = entry.UpdatedAt
	} else {
		entry.UpdatedAt = now
	}
	for {
		old := r.Load()

		// Collect this provider's previous models (preserved on non-ok).
		var prevModels []Model
		var rest []Model
		for _, m := range old.Models {
			if m.Provider == providerName {
				prevModels = append(prevModels, m)
			} else {
				rest = append(rest, m)
			}
		}

		// Determine the provider's new model set.
		var nextModels []Model
		if entry.Status == "" {
			entry.Status = CacheStatusOK
		}
		if entry.Status == CacheStatusOK {
			nextModels = make([]Model, 0, len(entry.Models))
			for _, m := range entry.Models {
				if m.Provider == "" {
					m.Provider = providerName
				}
				nextModels = append(nextModels, m)
			}
		} else {
			// Failure/timeout: keep previously cached models for this
			// provider so one bad provider never purges good data.
			nextModels = prevModels
			if nextModels == nil {
				nextModels = []Model{}
			}
		}

		// Rebuild the full model slice without touching old slices.
		combined := make([]Model, 0, len(rest)+len(nextModels))
		combined = append(combined, rest...)
		combined = append(combined, nextModels...)

		// Rebuild provider summaries: copy all non-target summaries, then
		// upsert the target one with fresh status/count.
		summaries := make([]ProviderSummary, 0, len(old.Providers)+1)
		found := false
		for _, s := range old.Providers {
			if s.Name == providerName {
				found = true
				summaries = append(summaries, ProviderSummary{
					Name:       providerName,
					ModelCount: len(nextModels),
					Status:     entry.Status,
					UpdatedAt:  entry.UpdatedAt,
				})
			} else {
				summaries = append(summaries, s)
			}
		}
		if !found {
			summaries = append(summaries, ProviderSummary{
				Name:       providerName,
				ModelCount: len(nextModels),
				Status:     entry.Status,
				UpdatedAt:  entry.UpdatedAt,
			})
		}
		// Refresh counts for non-target providers from the combined slice
		// (cheap and keeps summaries consistent after merges).
		counts := make(map[string]int, len(summaries))
		for _, m := range combined {
			counts[m.Provider]++
		}
		for i, s := range summaries {
			if s.Name == providerName {
				continue
			}
			if c, ok := counts[s.Name]; ok {
				summaries[i].ModelCount = c
			} else {
				summaries[i].ModelCount = 0
			}
		}

		next := &ModelSnapshot{
			Models:    combined,
			Providers: summaries,
			Version:   old.Version + 1,
			UpdatedAt: now,
		}
		if r.snapshot.CompareAndSwap(old, next) {
			return
		}
	}
}

// SetFetch overrides the fetch function (tests).
func (r *Registry) SetFetch(fn FetchFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fetch = fn
}

// CachePath reports the backing cache file path ("" = memory-only).
func (r *Registry) CachePath() string {
	return r.cachePath
}

// Len returns the number of descriptors in memory (lock-free).
func (r *Registry) Len() int {
	return len(r.Load().Models)
}

// Snapshot returns a copy of the in-memory slice (lock-free read + copy).
func (r *Registry) Snapshot() []ModelDescriptor {
	src := r.Load().Models
	out := make([]ModelDescriptor, len(src))
	copy(out, src)
	return out
}

// SetSeed replaces the in-memory catalog (tests/benchmarks). It rebuilds
// provider summaries from the seed set and publishes a fresh snapshot.
func (r *Registry) SetSeed(models []ModelDescriptor) {
	out := make([]ModelDescriptor, len(models))
	copy(out, models)
	counts := make(map[string]int)
	for _, m := range out {
		counts[m.Provider]++
	}
	summaries := make([]ProviderSummary, 0, len(counts))
	now := time.Now()
	for name, c := range counts {
		summaries = append(summaries, ProviderSummary{
			Name:       name,
			ModelCount: c,
			Status:     CacheStatusOK,
			UpdatedAt:  now,
		})
	}
	var ver uint64
	if old := r.snapshot.Load(); old != nil {
		ver = old.Version + 1
	} else {
		ver = 1
	}
	r.snapshot.Store(&ModelSnapshot{
		Models:    out,
		Providers: summaries,
		Version:   ver,
		UpdatedAt: now,
	})
}
