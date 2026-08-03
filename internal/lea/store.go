package lea

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/PizenLabs/izen/internal/lea/graph"
	"github.com/klauspost/compress/zstd"
)

// storeMagic prefixes the cache file so stale/foreign data is rejected.
var storeMagic = []byte{0x4c, 0x45, 0x41, 0x47} // "LEAG"

const storeVersion byte = 1

// Store persists the structural graph to a zstd-compressed binary file at
// <root>/.izen/graph.bin.zst.
type Store struct {
	path string
}

func newStore(path string) *Store {
	return &Store{path: path}
}

// Path returns the store file path.
func (s *Store) Path() string {
	return s.path
}

// Save writes the graph snapshot in <10ms-friendly binary form.
func (s *Store) Save(g *graph.Graph) error {
	raw := encodeSnapshot(g.Snapshot())

	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedFastest))
	if err != nil {
		return fmt.Errorf("zstd init: %w", err)
	}
	compressed := enc.EncodeAll(raw, nil)
	_ = enc.Close()

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	out := make([]byte, 0, len(storeMagic)+1+len(compressed))
	out = append(out, storeMagic...)
	out = append(out, storeVersion)
	out = append(out, compressed...)
	return os.WriteFile(s.path, out, 0o644)
}

// Load populates the graph from the store. Returns loaded=false when the file
// does not exist.
func (s *Store) Load(g *graph.Graph) (bool, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return false, err
	}
	if len(data) < len(storeMagic)+1 || !bytes.Equal(data[:len(storeMagic)], storeMagic) {
		return false, fmt.Errorf("invalid graph cache format")
	}

	dec, err := zstd.NewReader(nil)
	if err != nil {
		return false, fmt.Errorf("zstd init: %w", err)
	}
	defer dec.Close()

	raw, err := dec.DecodeAll(data[len(storeMagic)+1:], nil)
	if err != nil {
		return false, fmt.Errorf("zstd decode: %w", err)
	}

	snap, err := decodeSnapshot(raw)
	if err != nil {
		return false, fmt.Errorf("decode snapshot: %w", err)
	}
	g.Restore(snap)
	return true, nil
}

// save persists the current engine graph.
func (e *Engine) save() error {
	e.mu.RLock()
	g := e.g
	e.mu.RUnlock()
	return e.store.Save(g)
}

// load restores the engine graph from the store, if present.
func (e *Engine) load() (bool, error) {
	g := graph.NewGraph(e.root)
	ok, err := e.store.Load(g)
	if err != nil {
		return false, err
	}
	e.mu.Lock()
	e.g = g
	e.mu.Unlock()
	return ok, nil
}
