package autonomy

import (
	"testing"
	"time"
)

func timeNowMinusHour() time.Time {
	return time.Now().Add(-time.Hour)
}

func TestGrantLedgerIssueAndHas(t *testing.T) {
	l := NewGrantLedger()
	if l.Has("repository", CapabilitySet{CapRead}) {
		t.Fatal("empty ledger must not authorize anything")
	}
	l.GrantCapability("repository", CapRead, CapAnalyze, CapMutate)
	if !l.Has("repository", CapabilitySet{CapRead, CapMutate}) {
		t.Error("ledger must cover read+mutate after grant")
	}
	if l.Has("other-scope", CapabilitySet{CapRead}) {
		t.Error("grant scope must be honored")
	}
}

func TestGrantLedgerEmptyScopeIsGlobal(t *testing.T) {
	l := NewGrantLedger()
	l.GrantCapability("", CapRead)
	if !l.Has("repository", CapabilitySet{CapRead}) {
		t.Error("empty-scope grant must match any lookup scope")
	}
	if !l.Has("", CapabilitySet{CapRead}) {
		t.Error("empty-scope grant must match empty lookup scope")
	}
}

func TestGrantLedgerExpiry(t *testing.T) {
	l := NewGrantLedger()
	// No expiry helper on Grant in tests: construct an expired grant directly.
	g := Grant{Scope: "repository", Capabilities: CapabilitySet{CapRead}, ExpiresAt: timeNowMinusHour()}
	l.Issue(g)
	if l.Has("repository", CapabilitySet{CapRead}) {
		t.Error("expired grant must not authorize")
	}
}

func TestGrantLedgerRevoke(t *testing.T) {
	l := NewGrantLedger()
	g := l.GrantCapability("repository", CapRead)
	if !l.Has("repository", CapabilitySet{CapRead}) {
		t.Fatal("grant not recorded")
	}
	l.Revoke(g.ID)
	if l.Has("repository", CapabilitySet{CapRead}) {
		t.Error("revoked grant must not authorize")
	}
}

func TestGrantLedgerActiveCaps(t *testing.T) {
	l := NewGrantLedger()
	l.GrantCapability("repository", CapRead)
	l.GrantCapability("repository", CapAnalyze, CapMutate)
	caps := l.ActiveCaps("repository")
	for _, want := range []Capability{CapRead, CapAnalyze, CapMutate} {
		if !caps.Has(want) {
			t.Errorf("ActiveCaps missing %s: %v", want, caps)
		}
	}
}

func TestGrantLedgerRevokeScope(t *testing.T) {
	l := NewGrantLedger()
	l.GrantCapability("repo-a", CapRead)
	l.GrantCapability("repo-b", CapRead)
	l.RevokeScope("repo-a")
	if l.Has("repo-a", CapabilitySet{CapRead}) {
		t.Error("repo-a grant must be revoked")
	}
	if !l.Has("repo-b", CapabilitySet{CapRead}) {
		t.Error("repo-b grant must survive")
	}
}

func TestGrantPermissions(t *testing.T) {
	g := Grant{Scope: "repository", Capabilities: CapabilitySet{CapMutate, CapRead}}
	perms := g.Permissions()
	if len(perms) == 0 {
		t.Error("grant must expose human-readable permissions")
	}
}
