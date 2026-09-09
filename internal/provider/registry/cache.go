package registry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/PizenLabs/izen/internal/provider/detector"
)

// cacheFileName is the on-disk model catalog under ~/.izen/cache/.
const cacheFileName = "models.json"

// httpTimeout bounds every /models fetch so background sync can never hang.
const httpTimeout = 15 * time.Second

// FetchFunc fetches model descriptors for one provider. It is a Registry
// field so tests can stub the network without touching the HTTP layer.
type FetchFunc func(ctx context.Context, provider detector.ProviderConfig) ([]ModelDescriptor, error)

// Registry is the thread-safe in-memory model catalog. All reads take RLock
// and never touch disk; writes swap the slice atomically under Lock.
type Registry struct {
	mu        sync.RWMutex
	models    []ModelDescriptor
	cachePath string
	fetch     FetchFunc
	client    *http.Client
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
	return &Registry{
		cachePath: path,
		fetch:     nil, // nil = default HTTP fetch
		client:    &http.Client{Timeout: httpTimeout},
	}
}

// NewRegistryWithCachePath builds a Registry with an explicit cache path
// (tests). An empty path disables persistence.
func NewRegistryWithCachePath(path string) *Registry {
	return &Registry{
		cachePath: path,
		client:    &http.Client{Timeout: httpTimeout},
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

// Len returns the number of descriptors in memory.
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.models)
}

// Snapshot returns a copy of the in-memory slice.
func (r *Registry) Snapshot() []ModelDescriptor {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]ModelDescriptor, len(r.models))
	copy(out, r.models)
	return out
}

// SetSeed replaces the in-memory catalog (tests/benchmarks).
func (r *Registry) SetSeed(models []ModelDescriptor) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]ModelDescriptor, len(models))
	copy(out, models)
	r.models = out
}

// LoadCache reads the cache file into memory synchronously (startup path).
// A missing file is not an error (fresh install → empty registry).
// A malformed file returns an explicit error and leaves memory untouched.
func (r *Registry) LoadCache() error {
	if r.cachePath == "" {
		return nil
	}
	data, err := os.ReadFile(r.cachePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read model cache %s: %w", r.cachePath, err)
	}
	models, err := decodeCache(data)
	if err != nil {
		return fmt.Errorf("parse model cache %s: %w", r.cachePath, err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.models = models
	return nil
}

// Sync fetches every provider synchronously, atomically swaps the in-memory
// slice on success, and flushes to disk. A total failure (or an empty
// aggregate result) never clears the existing valid cache.
func (r *Registry) Sync(ctx context.Context, providers []detector.ProviderConfig) error {
	fetch := r.activeFetch()
	var all []ModelDescriptor
	var lastErr error
	for _, p := range providers {
		if p.APIKey == "" || p.BaseURL == "" {
			continue
		}
		got, err := fetch(ctx, p)
		if err != nil {
			lastErr = err
			continue
		}
		all = append(all, got...)
	}
	if len(all) == 0 {
		if lastErr != nil {
			return fmt.Errorf("model sync: all providers failed: %w", lastErr)
		}
		return fmt.Errorf("model sync: no models returned, keeping existing cache")
	}
	r.mu.Lock()
	r.models = all
	r.mu.Unlock()
	if err := r.flush(all); err != nil {
		return err
	}
	return nil
}

// SyncBackground runs Sync in a background goroutine and reports its result
// on the returned channel (buffered, exactly one value, then closed).
// Callers get non-blocking dispatch:
//
//	select {
//	case err := <-reg.SyncBackground(ctx, providers):
//	case <-ctx.Done():
//	}
//
// A background failure never clears the existing in-memory cache.
func (r *Registry) SyncBackground(ctx context.Context, providers []detector.ProviderConfig) <-chan error {
	done := make(chan error, 1)
	go func() {
		done <- r.Sync(ctx, providers)
		close(done)
	}()
	return done
}

// activeFetch resolves the fetch function under RLock.
func (r *Registry) activeFetch() FetchFunc {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.fetch != nil {
		return r.fetch
	}
	return r.fetchHTTPModels
}

// flush persists models to the cache file (mkdir -p + 0644). Memory-only
// registries (empty path) skip silently. Callers hold no lock.
func (r *Registry) flush(models []ModelDescriptor) error {
	if r.cachePath == "" {
		return nil
	}
	dir := filepath.Dir(r.cachePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("mkdir model cache %s: %w", dir, err)
	}
	payload := cacheEnvelope{Models: models, Version: 1}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal model cache: %w", err)
	}
	if err := os.WriteFile(r.cachePath, data, 0644); err != nil {
		return fmt.Errorf("write model cache %s: %w", r.cachePath, err)
	}
	return nil
}

// cacheEnvelope is the on-disk shape. decodeCache also accepts a bare array.
type cacheEnvelope struct {
	Version int               `json:"version"`
	Models  []ModelDescriptor `json:"models"`
}

// decodeCache parses either {"models": [...]} or a bare [...] array.
func decodeCache(data []byte) ([]ModelDescriptor, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, nil
	}
	var env cacheEnvelope
	if err := json.Unmarshal(trimmed, &env); err == nil && env.Models != nil {
		return env.Models, nil
	}
	var bare []ModelDescriptor
	if err := json.Unmarshal(trimmed, &bare); err != nil {
		return nil, err
	}
	return bare, nil
}

// openAIModels is the tolerant OpenAI-compatible /models response shape.
type openAIModels struct {
	Data []openAIModel `json:"data"`
}

type openAIModel struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	ContextLength *int     `json:"context_length"`
	ContextWindow *int     `json:"context_window"`
	MaxTokens     *int     `json:"max_tokens"`
	MaxOutput     *int     `json:"max_output_tokens"`
	Pricing       *pricing `json:"pricing"`
}

type pricing struct {
	Prompt     json.Number `json:"prompt"`
	Completion json.Number `json:"completion"`
}

// fetchHTTPModels performs one GET {BaseURL}/models with bearer auth and maps
// the response to ModelDescriptors. It never mutates the registry.
func (r *Registry) fetchHTTPModels(ctx context.Context, p detector.ProviderConfig) ([]ModelDescriptor, error) {
	base := p.BaseURL
	if len(base) > 0 && base[len(base)-1] == '/' {
		base = base[:len(base)-1]
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/models", nil)
	if err != nil {
		return nil, fmt.Errorf("models request %s: %w", p.Name, err)
	}
	req.Header.Set("Authorization", "Bearer "+p.APIKey)
	req.Header.Set("Accept", "application/json")

	client := r.client
	if client == nil {
		client = &http.Client{Timeout: httpTimeout}
	}
	//nolint:contextcheck,bodyclose // client.Do honors ctx via request; body closed below.
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("models fetch %s: %w", p.Name, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("models read %s: %w", p.Name, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("models fetch %s: HTTP %d", p.Name, resp.StatusCode)
	}
	var parsed openAIModels
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("models parse %s: %w", p.Name, err)
	}
	out := make([]ModelDescriptor, 0, len(parsed.Data))
	for _, m := range parsed.Data {
		if m.ID == "" {
			continue
		}
		name := m.Name
		if name == "" {
			name = m.ID
		}
		d := ModelDescriptor{
			ID:       m.ID,
			Provider: p.Name,
			Name:     name,
		}
		switch {
		case m.ContextLength != nil:
			d.ContextWindow = *m.ContextLength
		case m.ContextWindow != nil:
			d.ContextWindow = *m.ContextWindow
		}
		switch {
		case m.MaxOutput != nil:
			d.MaxOutputTokens = *m.MaxOutput
		case m.MaxTokens != nil:
			d.MaxOutputTokens = *m.MaxTokens
		}
		if m.Pricing != nil {
			if f, err := m.Pricing.Prompt.Float64(); err == nil {
				d.InputCostPerM = f * 1e6
			}
			if f, err := m.Pricing.Completion.Float64(); err == nil {
				d.OutputCostPerM = f * 1e6
			}
		}
		out = append(out, d)
	}
	return out, nil
}
