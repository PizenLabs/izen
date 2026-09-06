package artifact

import (
	"sync"
	"time"

	"github.com/PizenLabs/izen/internal/core/domain/occ"
)

// ArtifactStore is the versioned in-memory store for artifact diffs and patch
// stages. Every committed file diff or patch stage advances its monotonic
// StateVersion by exactly one under OCC validation. Concurrent writers against
// the same expected version result in exactly one committed patch and N-1
// ErrStaleDependency rejections, preventing dual-sovereignty mutations.
type ArtifactStore struct {
	mu   sync.RWMutex
	gate occ.OCCGate
	occ.VersionedEntity
	patches map[string]string // artifactID -> patch content (diff)
	order   []string          // insertion order for deterministic iteration
}

// NewArtifactStore creates an empty versioned store at version 0.
func NewArtifactStore() *ArtifactStore {
	return &ArtifactStore{
		patches: make(map[string]string),
		VersionedEntity: occ.VersionedEntity{
			Version:   0,
			UpdatedAt: time.Now(),
		},
	}
}

// Version returns the current committed version.
func (s *ArtifactStore) Version() occ.StateVersion {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.VersionedEntity.Version
}

// StagePatch attempts to stage a patch for artifact id with OCC validation.
// The caller must supply the version it observed; on success the store version
// is advanced and UpdatedAt refreshed.
func (s *ArtifactStore) StagePatch(id string, content string, expected occ.StateVersion) (occ.StateVersion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	newVer, err := s.gate.ValidateAndAdvance(&s.VersionedEntity.Version, expected)
	if err != nil {
		return newVer, err
	}
	if _, exists := s.patches[id]; !exists {
		s.order = append(s.order, id)
	}
	s.patches[id] = content
	s.UpdatedAt = time.Now()
	return newVer, nil
}

// StagePatchForce stages a patch without OCC validation. It is used only by
// the runtime's authoritative commit pipeline when it owns the clock.
func (s *ArtifactStore) StagePatchForce(id string, content string) occ.StateVersion {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.patches[id]; !exists {
		s.order = append(s.order, id)
	}
	s.patches[id] = content
	s.VersionedEntity.Version = s.VersionedEntity.Version.Next()
	s.UpdatedAt = time.Now()
	return s.VersionedEntity.Version
}

// Get returns the staged patch content for id, if any.
func (s *ArtifactStore) Get(id string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.patches[id]
	return v, ok
}

// List returns the artifact IDs in insertion order.
func (s *ArtifactStore) List() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, len(s.order))
	copy(out, s.order)
	return out
}

// Count returns the number of staged patches.
func (s *ArtifactStore) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.patches)
}

// ClearUncommitted removes all uncommitted diffs and resets versioning
// to the last committed baseline. It is used by CheckpointCoordinator.Rollback
// to ensure strict alignment between disk, store, and workflow.
func (s *ArtifactStore) ClearUncommitted() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.patches = make(map[string]string)
	s.order = nil
	// Version is not rewound to 0; callers that need version reset handle it
	// via the snapshot's baseline. Here we clear contents only.
	s.UpdatedAt = time.Now()
}

// ResetToBaseline rewinds the store's version to the snapshot baseline and
// clears diffs. This is the transactional rollback path.
func (s *ArtifactStore) ResetToBaseline(baseline occ.StateVersion) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.patches = make(map[string]string)
	s.order = nil
	s.VersionedEntity.Version = baseline
	s.UpdatedAt = time.Now()
}
