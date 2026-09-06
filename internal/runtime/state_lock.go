package runtime

import (
	"errors"

	"github.com/PizenLabs/izen/internal/core/domain/occ"
)

// ErrStaleProjection is returned when a UI projection rejects a stale version.
// The caller must trigger a state re-sync (CmdRefreshProjection) rather than
// panicking or rendering uncommitted data.
var ErrStaleProjection = errors.New("runtime: stale state version, refresh required")

// CmdRefreshProjection is the command that triggers a UI state re-sync after
// a stale version was observed. It is intentionally a plain string constant so
// the projection layer can emit it without importing the UI package (avoiding
// import cycles).
const CmdRefreshProjection = "CmdRefreshProjection"

// VersionedProjection is the runtime-owned helper that guards UI state
// projections against stale versions. It holds the authoritative committed
// version for a single entity dimension. Projections must call Validate before
// rendering; a stale version returns ErrStaleProjection and the caller must
// emit CmdRefreshProjection.
type VersionedProjection struct {
	committed occ.StateVersion
}

// NewVersionedProjection creates a projection guard at the given committed version.
func NewVersionedProjection(committed occ.StateVersion) *VersionedProjection {
	return &VersionedProjection{committed: committed}
}

// Committed returns the authoritative committed version.
func (p *VersionedProjection) Committed() occ.StateVersion {
	return p.committed
}

// Advance moves the committed version forward monotonically. It is called only
// by the runtime after a successful OCC commit.
func (p *VersionedProjection) Advance(next occ.StateVersion) {
	if next > p.committed {
		p.committed = next
	}
}

// Validate checks that the projection's observed version matches the committed
// version. On mismatch it returns ErrStaleProjection wrapping occ.ErrStaleDependency
// so callers can classify it with errors.Is.
func (p *VersionedProjection) Validate(observed occ.StateVersion) error {
	if observed != p.committed {
		return errors.Join(ErrStaleProjection, occ.ErrStaleDependency)
	}
	return nil
}

// NeedsRefresh reports whether the observed version is stale and a re-sync is
// required.
func (p *VersionedProjection) NeedsRefresh(observed occ.StateVersion) bool {
	return observed != p.committed
}

// RefreshResult is the outcome of handling a stale projection: the caller
// should dispatch CmdRefreshProjection and discard the stale data.
type RefreshResult struct {
	// Refresh indicates a re-sync is required.
	Refresh bool
	// Command is the re-sync command to dispatch (CmdRefreshProjection when Refresh is true).
	Command string
	// Current is the authoritative committed version to re-sync to.
	Current occ.StateVersion
}

// HandleStale returns the re-sync action for a stale observed version. It
// never panics and never returns uncommitted data: the caller must discard
// the stale payload and re-fetch at Current.
func (p *VersionedProjection) HandleStale(observed occ.StateVersion) RefreshResult {
	if observed == p.committed {
		return RefreshResult{Refresh: false, Current: p.committed}
	}
	return RefreshResult{Refresh: true, Command: CmdRefreshProjection, Current: p.committed}
}
