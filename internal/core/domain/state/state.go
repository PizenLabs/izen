package state

import (
	"sync"
	"time"

	"github.com/PizenLabs/izen/internal/core/domain/occ"
)

// VersionedState is a generic monotonic-versioned domain state container. It
// provides the shared primitive for all three orthogonal dimensions (workflow,
// artifact, execution) when a lightweight state holder is needed without the
// full domain-specific logic. The version is advanced under OCC validation so
// stale writers are rejected deterministically.
type VersionedState struct {
	mu   sync.RWMutex
	gate occ.OCCGate
	occ.VersionedEntity
	data map[string]string
}

// NewVersionedState creates an empty versioned state at version 0.
func NewVersionedState() *VersionedState {
	return &VersionedState{
		data: make(map[string]string),
		VersionedEntity: occ.VersionedEntity{
			Version:   0,
			UpdatedAt: time.Now(),
		},
	}
}

// Version returns the committed version.
func (s *VersionedState) Version() occ.StateVersion {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.VersionedEntity.Version
}

// Get returns the value for key, if present.
func (s *VersionedState) Get(key string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.data[key]
	return v, ok
}

// Set attempts to set key to value with OCC validation. The caller must
// supply the version it observed; on stale version it returns ErrStaleDependency.
func (s *VersionedState) Set(key, value string, expected occ.StateVersion) (occ.StateVersion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	newVer, err := s.gate.ValidateAndAdvance(&s.VersionedEntity.Version, expected)
	if err != nil {
		return newVer, err
	}
	s.data[key] = value
	s.UpdatedAt = time.Now()
	return newVer, nil
}

// SetForce sets key to value without OCC validation. It is used only by the
// authoritative control plane.
func (s *VersionedState) SetForce(key, value string) occ.StateVersion {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[key] = value
	s.VersionedEntity.Version = s.VersionedEntity.Version.Next()
	s.UpdatedAt = time.Now()
	return s.VersionedEntity.Version
}

// Snapshot returns a copy of the data map.
func (s *VersionedState) Snapshot() map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]string, len(s.data))
	for k, v := range s.data {
		out[k] = v
	}
	return out
}
