package registry

import (
	"context"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/PizenLabs/izen/internal/provider/detector"
	"github.com/PizenLabs/izen/internal/provider/discovery"
)

// syncBaseURLs fills gaps the detector table does not cover (deepseek and
// the ollama local runtime). Detector-native providers resolve via
// detector.BaseURLFor.
var syncBaseURLs = map[string]string{
	"deepseek": "https://api.deepseek.com/v1",
	"ollama":   discovery.OllamaBase,
}

// staticAnthropicModels is the fallback registry for the Anthropic provider,
// which exposes no public OpenAI-compatible /models endpoint. Returned when
// the live fetch fails so a configured ANTHROPIC_API_KEY still yields a
// usable catalog instead of an error.
func staticAnthropicModels() []ModelDescriptor {
	return []ModelDescriptor{
		{ID: "claude-3-7-sonnet-latest", Name: "Claude 3.7 Sonnet", Provider: "anthropic", ContextWindow: 200000},
		{ID: "claude-3-5-sonnet-latest", Name: "Claude 3.5 Sonnet", Provider: "anthropic", ContextWindow: 200000},
		{ID: "claude-3-5-haiku-latest", Name: "Claude 3.5 Haiku", Provider: "anthropic", ContextWindow: 200000},
	}
}

// normalizeSyncProviders copies providers, fills empty BaseURLs from known
// endpoints, and auto-appends the Ollama local runtime when reachable and
// not already listed. Ollama needs no API key: an empty key is backfilled
// with the "local" placeholder so the fetch stage does not skip it.
func (r *Registry) normalizeSyncProviders(ctx context.Context, providers []detector.ProviderConfig) []detector.ProviderConfig {
	out := make([]detector.ProviderConfig, 0, len(providers)+1)
	seen := make(map[string]struct{}, len(providers)+1)
	for _, p := range providers {
		if p.Name == "" {
			continue
		}
		if p.BaseURL == "" {
			if b := detector.BaseURLFor(p.Name); b != "" {
				p.BaseURL = b
			} else if b, ok := syncBaseURLs[p.Name]; ok {
				p.BaseURL = b
			}
		}
		if p.Name == "ollama" && p.APIKey == "" {
			p.APIKey = "local"
		}
		seen[p.Name] = struct{}{}
		out = append(out, p)
	}
	if _, ok := seen["ollama"]; !ok {
		// Live probe only on the default HTTP path: a custom test stub
		// replaces the network, so probing localhost there would break
		// determinism (and route stub-tagged models into the ollama key,
		// duplicating them under a foreign provider tag).
		r.mu.RLock()
		custom := r.fetch != nil
		r.mu.RUnlock()
		if !custom && discovery.OllamaAvailable(ctx) {
			out = append(out, detector.ProviderConfig{
				Name:    "ollama",
				APIKey:  "local",
				BaseURL: discovery.OllamaBase,
				Source:  "env",
			})
		}
	}
	return out
}

// fetchForSync resolves one provider's models. With a custom test stub wired
// (SetFetch), the stub is authoritative for every provider so tests stay
// deterministic (no live Ollama probe, no Anthropic static fallback).
// On the default HTTP path, Ollama prefers the native /api/tags list and
// Anthropic falls back to the static registry when /models is unavailable.
func (r *Registry) fetchForSync(ctx context.Context, p detector.ProviderConfig) ([]ModelDescriptor, error) {
	r.mu.RLock()
	custom := r.fetch != nil
	r.mu.RUnlock()
	fetch := r.activeFetch()
	if custom {
		return fetch(ctx, p)
	}
	if p.Name == "ollama" {
		if models, ok := discovery.OllamaModels(ctx, r.client); ok {
			out := make([]ModelDescriptor, 0, len(models))
			for _, m := range models {
				out = append(out, ModelDescriptor{ID: m.ID, Provider: "ollama", Name: m.Name})
			}
			return out, nil
		}
		return r.fetchHTTPModels(ctx, p)
	}
	got, err := r.fetchHTTPModels(ctx, p)
	if err != nil && p.Name == "anthropic" {
		return staticAnthropicModels(), nil
	}
	return got, err
}

// Sync fetches every provider concurrently (errgroup) with per-provider
// provenance: a failure or timeout for one provider updates only that
// provider's status and never purges cached models from any provider
// (memory or disk). A total failure (or an empty aggregate result with an
// empty registry) never clears the existing valid cache.
//
// Live discovery: the Ollama local runtime auto-joins when reachable, and
// Anthropic degrades to a static registry when /models is unavailable.
// Results merge into memory and persist per-provider to
// ~/.izen/cache/models.json; callers emit SnapshotMsg/RegistryUpdatedMsg.
func (r *Registry) Sync(ctx context.Context, providers []detector.ProviderConfig) error {
	providers = r.normalizeSyncProviders(ctx, providers)

	var mu sync.Mutex
	var lastErr error
	succeeded := 0

	g, ctx := errgroup.WithContext(ctx)
	for _, p := range providers {
		// Ollama carries a placeholder key; every other provider still
		// requires a real key and endpoint.
		if p.Name != "ollama" && (p.APIKey == "" || p.BaseURL == "") {
			continue
		}
		if p.Name == "ollama" && p.BaseURL == "" {
			continue
		}
		g.Go(func() error {
			got, err := r.fetchForSync(ctx, p)
			now := time.Now()
			if err != nil {
				mu.Lock()
				lastErr = err
				mu.Unlock()
				status := classifyFetchError(err)
				r.UpdateProvider(p.Name, ProviderCacheEntry{
					UpdatedAt: now,
					Status:    status,
					ErrorMsg:  err.Error(),
				})
				r.persistProviderEntry(p.Name, ProviderCacheEntry{
					UpdatedAt: now,
					Status:    status,
					ErrorMsg:  err.Error(),
					Models:    r.modelsForProvider(p.Name),
				})
				return nil
			}
			mu.Lock()
			succeeded++
			mu.Unlock()
			entry := ProviderCacheEntry{
				UpdatedAt: now,
				Status:    CacheStatusOK,
				Models:    got,
			}
			r.UpdateProvider(p.Name, entry)
			r.persistProviderEntry(p.Name, entry)
			return nil
		})
	}
	_ = g.Wait()

	mu.Lock()
	defer mu.Unlock()
	if succeeded == 0 {
		if lastErr != nil {
			return fmt.Errorf("model sync: all providers failed: %w", lastErr)
		}
		if r.Len() == 0 {
			return fmt.Errorf("model sync: no models returned, keeping existing cache")
		}
		return fmt.Errorf("model sync: no models returned, keeping existing cache")
	}
	return nil
}

// SyncProviders is an alias for Sync kept for explicit call sites.
func (r *Registry) SyncProviders(ctx context.Context, providers []detector.ProviderConfig) error {
	return r.Sync(ctx, providers)
}

// SyncBackground runs Sync in a background goroutine and reports its result
// on the returned channel (buffered, exactly one value, then closed).
// A background failure never clears the existing in-memory cache.
func (r *Registry) SyncBackground(ctx context.Context, providers []detector.ProviderConfig) <-chan error {
	done := make(chan error, 1)
	go func() {
		done <- r.Sync(ctx, providers)
		close(done)
	}()
	return done
}

// SyncDiscovered runs live ENV + local-runtime discovery (API keys from the
// environment plus a reachable Ollama) and syncs the merged provider set.
// It is the one-call path for SyncRequestedMsg / /models invocation.
func (r *Registry) SyncDiscovered(ctx context.Context) error {
	return r.Sync(ctx, discovery.DiscoverProviders(ctx))
}
