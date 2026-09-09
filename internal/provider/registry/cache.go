package registry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/PizenLabs/izen/internal/provider/detector"
)

// Cache status values for per-provider provenance.
const (
	CacheStatusOK      = "ok"
	CacheStatusTimeout = "timeout"
	CacheStatusError   = "error"
)

// CacheVersion is the current on-disk schema version for ModelCacheFile.
const CacheVersion = 1

// ProviderCacheEntry is the provenance-aware per-provider cache record.
type ProviderCacheEntry struct {
	UpdatedAt time.Time `json:"updated_at"`
	Status    string    `json:"status"`
	ErrorMsg  string    `json:"error_msg,omitempty"`
	Models    []Model   `json:"models"`
}

// ModelCacheFile is the on-disk shape at ~/.izen/cache/models.json: a
// versioned map of provider name -> ProviderCacheEntry. Per-provider writes
// must preserve non-target keys so one provider's failure or timeout never
// purges another provider's cached models.
type ModelCacheFile struct {
	Version   int                           `json:"version"`
	Providers map[string]ProviderCacheEntry `json:"providers"`
}

// cacheEnvelope is the legacy on-disk shape (flat model list). decodeCache
// still accepts it plus a bare array for backward compatibility.
type cacheEnvelope struct {
	Version int               `json:"version"`
	Models  []ModelDescriptor `json:"models"`
}

// ExpandPath resolves a leading ~/ prefix to the OS home directory.
// Go's os package does not expand ~, so ~/.izen/cache/models.json would
// otherwise fail to load. Non-tilde and empty paths pass through unchanged.
func ExpandPath(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err == nil && home != "" {
			return filepath.Join(home, path[2:])
		}
	}
	if path == "~" {
		home, err := os.UserHomeDir()
		if err == nil && home != "" {
			return home
		}
	}
	return path
}

// LoadCacheFile reads a ModelCacheFile from path. A missing file yields an
// empty (non-nil map) file and no error. Malformed content returns an error.
func LoadCacheFile(path string) (ModelCacheFile, error) {
	out := ModelCacheFile{Version: CacheVersion, Providers: map[string]ProviderCacheEntry{}}
	path = ExpandPath(path)
	if path == "" {
		return out, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return out, fmt.Errorf("read model cache %s: %w", path, err)
	}
	return DecodeCacheFile(data)
}

// DecodeCacheFile parses either the current {"providers": {...}} shape, the
// legacy {"models": [...]} envelope, or a bare [...] array.
func DecodeCacheFile(data []byte) (ModelCacheFile, error) {
	out := ModelCacheFile{Version: CacheVersion, Providers: map[string]ProviderCacheEntry{}}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return out, nil
	}
	var modern ModelCacheFile
	if err := json.Unmarshal(trimmed, &modern); err == nil && modern.Providers != nil {
		if modern.Version == 0 {
			modern.Version = CacheVersion
		}
		return modern, nil
	}
	models, err := decodeCache(trimmed)
	if err != nil {
		return out, err
	}
	// Migrate legacy flat list: group by provider with ok status.
	now := time.Now()
	for _, m := range models {
		e := out.Providers[m.Provider]
		e.Status = CacheStatusOK
		e.UpdatedAt = now
		e.Models = append(e.Models, m)
		out.Providers[m.Provider] = e
	}
	if len(models) > 0 && len(out.Providers) == 0 {
		out.Providers["default"] = ProviderCacheEntry{
			UpdatedAt: now,
			Status:    CacheStatusOK,
			Models:    models,
		}
	}
	return out, nil
}

// WriteCacheFile persists file to path (mkdir -p + 0644) atomically.
func WriteCacheFile(path string, file ModelCacheFile) error {
	path = ExpandPath(path)
	if path == "" {
		return nil
	}
	if file.Providers == nil {
		file.Providers = map[string]ProviderCacheEntry{}
	}
	if file.Version == 0 {
		file.Version = CacheVersion
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("mkdir model cache %s: %w", dir, err)
	}
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal model cache: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return fmt.Errorf("write model cache %s: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename model cache %s: %w", path, err)
	}
	return nil
}

// UpsertProviderEntry rewrites only the target provider key, preserving every
// non-target provider entry on disk. It is the lock-free-at-file-level
// per-provider write helper: failures from one provider never destroy the
// cached models of the others.
func UpsertProviderEntry(path string, provider string, entry ProviderCacheEntry) error {
	path = ExpandPath(path)
	if path == "" || provider == "" {
		return nil
	}
	existing, err := LoadCacheFile(path)
	if err != nil {
		// Corrupt cache: start fresh rather than failing the sync, but
		// surface nothing destructive — single-provider write only.
		existing = ModelCacheFile{Version: CacheVersion, Providers: map[string]ProviderCacheEntry{}}
	}
	if existing.Providers == nil {
		existing.Providers = map[string]ProviderCacheEntry{}
	}
	existing.Providers[provider] = entry
	return WriteCacheFile(path, existing)
}

// FlattenModels aggregates all provider entries into a single model slice.
func (f ModelCacheFile) FlattenModels() []Model {
	var out []Model
	for _, e := range f.Providers {
		out = append(out, e.Models...)
	}
	return out
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

// LoadCache reads the cache file into memory synchronously (startup path).
// A missing file is not an error: the registry falls back to the embedded
// DefaultSnapshot baseline so cold-start is never empty (fresh install →
// default catalog instead of 0 models). A file that parses to 0 models
// likewise seeds the default baseline when memory is empty. A malformed
// file returns an explicit error and leaves memory untouched.
// Both the modern per-provider shape and legacy flat shapes are accepted.
//
// When the file yields models, its contents are authoritative: the snapshot
// is reset before applying entries so the embedded defaults never leak into
// a populated cache (no double-counting).
func (r *Registry) LoadCache() error {
	if r.cachePath == "" {
		return nil
	}
	data, err := os.ReadFile(r.cachePath)
	if err != nil {
		if os.IsNotExist(err) {
			if r.Len() == 0 {
				r.snapshot.Store(DefaultSnapshot())
			}
			return nil
		}
		return fmt.Errorf("read model cache %s: %w", r.cachePath, err)
	}
	file, err := DecodeCacheFile(data)
	if err != nil {
		return fmt.Errorf("parse model cache %s: %w", r.cachePath, err)
	}
	if len(file.FlattenModels()) == 0 {
		if r.Len() == 0 {
			r.snapshot.Store(DefaultSnapshot())
		}
		return nil
	}
	// Authoritative reset: file contents replace the embedded defaults.
	r.snapshot.Store(emptySnapshot())
	for name, entry := range file.Providers {
		if entry.Status == "" {
			entry.Status = CacheStatusOK
		}
		r.UpdateProvider(name, entry)
	}
	// Legacy flat files with zero providers but decodable models are handled
	// inside DecodeCacheFile (grouped by provider), so nothing else to do.
	return nil
}

// sync.go owns Sync/SyncProviders/SyncBackground (concurrent errgroup live
// discovery). This file owns persistence, caching, and HTTP mapping.

// modelsForProvider returns a copy of the current snapshot models for one
// provider (used to persist failure statuses without wiping disk models).
func (r *Registry) modelsForProvider(provider string) []Model {
	var out []Model
	for _, m := range r.Load().Models {
		if m.Provider == provider {
			out = append(out, m)
		}
	}
	if out == nil {
		out = []Model{}
	}
	return out
}

// persistProviderEntry upserts one provider key on disk, preserving all
// non-target provider keys. Memory-only registries skip silently.
func (r *Registry) persistProviderEntry(provider string, entry ProviderCacheEntry) {
	if r.cachePath == "" || provider == "" {
		return
	}
	r.fileMu.Lock()
	defer r.fileMu.Unlock()
	_ = UpsertProviderEntry(r.cachePath, provider, entry)
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
// registries (empty path) skip silently. Callers hold no lock. The modern
// shape groups models by provider with ok status; per-provider provenance
// for failures is persisted via UpsertProviderEntry in syncProviders.
func (r *Registry) flush(models []ModelDescriptor) error {
	if r.cachePath == "" {
		return nil
	}
	r.fileMu.Lock()
	defer r.fileMu.Unlock()
	existing, err := LoadCacheFile(r.cachePath)
	if err != nil {
		existing = ModelCacheFile{Version: CacheVersion, Providers: map[string]ProviderCacheEntry{}}
	}
	now := time.Now()
	grouped := map[string][]Model{}
	for _, m := range models {
		grouped[m.Provider] = append(grouped[m.Provider], m)
	}
	for name, ms := range grouped {
		existing.Providers[name] = ProviderCacheEntry{
			UpdatedAt: now,
			Status:    CacheStatusOK,
			Models:    ms,
		}
	}
	return WriteCacheFile(r.cachePath, existing)
}

// classifyFetchError maps a fetch error to a cache status + message.
func classifyFetchError(err error) string {
	if err == nil {
		return CacheStatusOK
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return CacheStatusTimeout
	}
	var osTimeout interface{ Timeout() bool }
	if errors.As(err, &osTimeout) && osTimeout.Timeout() {
		return CacheStatusTimeout
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "timeout") || strings.Contains(msg, "deadline") {
		return CacheStatusTimeout
	}
	return CacheStatusError
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
