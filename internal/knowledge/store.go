package knowledge

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Store is the granular, independently addressable knowledge store
// (INV-SESSION-15).
//
// Layout:
//
//	.izen/knowledge/
//	  assets/<id>.json        # every asset its own file — independently
//	                          # addressable and retrievable
//	  tombstones/<id>.json    # deprecated assets, retained for audit
//
// There is deliberately NO project-summary.json. The only aggregate the store
// exposes is an Index of asset REFERENCES (id/title/kind/status), which is a
// retrieval aid, never an authoritative copy of the knowledge.
type Store struct {
	root  string
	dir   string
	tombs string

	mu sync.Mutex
}

// NewStore returns a store rooted at .izen/knowledge under the workspace root.
// It is inert until Save/Load/List are called (directories are created on
// demand).
func NewStore(root string) *Store {
	return &Store{
		root:  root,
		dir:   filepath.Join(root, ".izen", "knowledge", "assets"),
		tombs: filepath.Join(root, ".izen", "knowledge", "tombstones"),
	}
}

// Root returns the knowledge root directory.
func (s *Store) Root() string { return s.root }

// Path returns the canonical assets directory.
func (s *Store) Path() string { return s.dir }

// Save atomically persists one asset to its own file. Candidate and promoted
// assets live in assets/; deprecated assets in tombstones/. Saving is
// idempotent: writing the same asset twice converges to one file.
func (s *Store) Save(a Asset) error {
	if err := a.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	dir := s.dir
	if a.Status == StatusDeprecated {
		dir = s.tombs
	}
	data, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(dir, a.ID+".json"), data)
}

// Load retrieves one asset by ID, checking assets/ then tombstones/ so a
// deprecated asset remains auditable.
func (s *Store) Load(id string) (*Asset, error) {
	if id == "" {
		return nil, fmt.Errorf("knowledge: empty asset id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, dir := range []string{s.dir, s.tombs} {
		data, err := os.ReadFile(filepath.Join(dir, id+".json"))
		if err == nil {
			var a Asset
			if err := json.Unmarshal(data, &a); err != nil {
				return nil, fmt.Errorf("knowledge: decode asset %s: %w", id, err)
			}
			return &a, nil
		}
		if !os.IsNotExist(err) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("knowledge: asset %s not found", id)
}

// Delete removes an asset file entirely (both locations). It is the physical
// counterpart of a tombstone for tests/cleanup; lifecycle invalidation should
// prefer Deprecate which retains the record.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var firstErr error
	for _, dir := range []string{s.dir, s.tombs} {
		if err := os.Remove(filepath.Join(dir, id+".json")); err != nil && !os.IsNotExist(err) {
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// removeFrom removes a single file from one of the store's directories. It is
// the primitive the tombstone move uses to take an id out of the active assets
// directory.
func (s *Store) removeFrom(dir, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return os.Remove(filepath.Join(dir, name))
}

// AssetRef is an addressable reference to an asset — the ONLY aggregate the
// store exposes. It is never an authoritative copy of the knowledge.
type AssetRef struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Title  string `json:"title"`
	Status Status `json:"status"`
}

// Index lists references to every stored asset (active first, then
// tombstones), deterministically ordered. Consumers that need a body must
// Load(id) the individual asset — preserving independent addressability.
func (s *Store) Index() ([]AssetRef, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var refs []AssetRef
	collect := func(dir string) error {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				continue
			}
			var a Asset
			if json.Unmarshal(data, &a) != nil {
				continue
			}
			refs = append(refs, AssetRef{ID: a.ID, Kind: a.Kind, Title: a.Title, Status: a.Status})
		}
		return nil
	}
	if err := collect(s.dir); err != nil {
		return nil, err
	}
	if err := collect(s.tombs); err != nil {
		return nil, err
	}

	sort.Slice(refs, func(i, j int) bool {
		if refs[i].Status != refs[j].Status {
			return refs[i].Status == StatusPromoted
		}
		return refs[i].ID < refs[j].ID
	})
	return refs, nil
}

// Count returns the total number of stored assets (active + tombstoned).
func (s *Store) Count() int {
	refs, err := s.Index()
	if err != nil {
		return 0
	}
	return len(refs)
}

// writeAtomic persists data via tmp + fsync + rename so a reader never sees a
// partially written asset.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|os.O_SYNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
