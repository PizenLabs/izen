package autonomy

import (
	"fmt"
	"sync"
	"time"
)

// GrantID identifies a capability grant within a session.
type GrantID string

// Grant is a session capability authorization: a named scope (e.g. the current
// repository), the granted capability vector, the permissions it unlocks, and
// an optional expiry. A grant is NOT a per-file approval — it authorizes every
// operation inside its boundary, so the runtime can loop without asking again.
type Grant struct {
	ID           GrantID
	Scope        string
	Capabilities CapabilitySet
	Intent       Intent
	Target       string
	IssuedAt     time.Time
	ExpiresAt    time.Time // zero = session-lifetime grant
}

// Permissions describes what the grant unlocks in human terms.
func (g Grant) Permissions() []string {
	var perms []string
	for _, cap := range g.Capabilities {
		switch cap {
		case CapRead:
			perms = append(perms, "read files")
		case CapAnalyze:
			perms = append(perms, "search, trace, analyze, collect evidence")
		case CapPropose:
			perms = append(perms, "produce execution plan, estimate impact, propose changes")
		case CapMutate:
			perms = append(perms, "edit files, create patches, run mutations")
		case CapVerify:
			perms = append(perms, "run verification and tests")
		}
	}
	return perms
}

// IsExpired reports whether the grant has lapsed.
func (g Grant) IsExpired() bool {
	return !g.ExpiresAt.IsZero() && time.Now().After(g.ExpiresAt)
}

// Covers reports whether the grant authorizes every required capability for
// the scope.
func (g Grant) Covers(scope string, required CapabilitySet) bool {
	if g.IsExpired() {
		return false
	}
	if g.Scope != "" && g.Scope != scope {
		return false
	}
	for _, cap := range required {
		if !g.Capabilities.Has(cap) {
			return false
		}
	}
	return true
}

// GrantLedger records the session's capability grants. It is the single source
// of truth for "what is the runtime authorized to do right now". The ledger is
// thread-safe: the UI grants on the input goroutine while engines read from
// worker goroutines.
type GrantLedger struct {
	mu     sync.RWMutex
	grants []Grant
}

// NewGrantLedger returns an empty session grant ledger.
func NewGrantLedger() *GrantLedger {
	return &GrantLedger{}
}

// Issue records a new grant and returns it. Grants are append-only until
// revoked or expired.
func (l *GrantLedger) Issue(g Grant) Grant {
	if l == nil {
		return g
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if g.ID == "" {
		g.ID = GrantID(fmt.Sprintf("grant-%d", time.Now().UnixNano()))
	}
	if g.IssuedAt.IsZero() {
		g.IssuedAt = time.Now().UTC()
	}
	l.grants = append(l.grants, g)
	return g
}

// GrantCapability is a convenience that issues a fresh grant for the given
// scope and capabilities.
func (l *GrantLedger) GrantCapability(scope string, caps ...Capability) Grant {
	return l.Issue(Grant{
		Scope:        scope,
		Capabilities: caps,
	})
}

// Has reports whether an active grant covers the required capabilities for the
// scope. Expired grants are ignored.
func (l *GrantLedger) Has(scope string, required CapabilitySet) bool {
	if l == nil {
		return false
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	for _, g := range l.grants {
		if g.Covers(scope, required) {
			return true
		}
	}
	return false
}

// Active returns the active (non-expired, non-revoked) grants in issue order.
func (l *GrantLedger) Active() []Grant {
	if l == nil {
		return nil
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]Grant, 0, len(l.grants))
	for _, g := range l.grants {
		if !g.IsExpired() {
			out = append(out, g)
		}
	}
	return out
}

// ActiveCaps returns the union of capabilities granted by active grants bound
// to the scope. It is the grant surface the decision model reads.
func (l *GrantLedger) ActiveCaps(scope string) CapabilitySet {
	if l == nil {
		return nil
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	seen := make(map[Capability]bool)
	var out CapabilitySet
	for _, g := range l.grants {
		if g.IsExpired() {
			continue
		}
		if g.Scope != "" && g.Scope != scope {
			continue
		}
		for _, cap := range g.Capabilities {
			if !seen[cap] {
				seen[cap] = true
				out = append(out, cap)
			}
		}
	}
	return out
}

// Revoke removes the named grant from the ledger.
func (l *GrantLedger) Revoke(id GrantID) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for i, g := range l.grants {
		if g.ID == id {
			l.grants = append(l.grants[:i], l.grants[i+1:]...)
			return
		}
	}
}

// RevokeScope removes every active grant bound to the scope.
func (l *GrantLedger) RevokeScope(scope string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	var kept []Grant
	for _, g := range l.grants {
		if g.Scope == scope {
			continue
		}
		kept = append(kept, g)
	}
	l.grants = kept
}

// Count returns the number of recorded grants (active and expired).
func (l *GrantLedger) Count() int {
	if l == nil {
		return 0
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	return len(l.grants)
}
