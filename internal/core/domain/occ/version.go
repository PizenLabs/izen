package occ

import "time"

// StateVersion is a monotonic sequence counter for optimistic concurrency
// control. It is the single linearizable clock for all core domain entities.
type StateVersion uint64

// VersionedEntity is the embedded version clock for any domain entity. The
// version is monotonic and strictly increasing; every committed mutation
// advances it by exactly one. UpdatedAt is wall-clock metadata for observability.
type VersionedEntity struct {
	Version   StateVersion `json:"version"`
	UpdatedAt time.Time    `json:"updated_at"`
}

// Next returns the successor version. It is pure and does not mutate the
// receiver.
func (v StateVersion) Next() StateVersion {
	return v + 1
}
