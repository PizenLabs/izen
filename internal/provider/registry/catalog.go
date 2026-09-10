package registry

// DefaultModels returns nil. Hardcoded mock catalogs are prohibited; the live
// registry is always populated from provider evidence (API endpoints, tags).
func DefaultModels() []ModelDescriptor {
	return nil
}

// DefaultSnapshot returns an empty snapshot. Cold-start shows an actionable
// empty state ("No models loaded for provider") so the user triggers a live
// sync via Ctrl+R or Alt+A instead of trusting stale mock data.
func DefaultSnapshot() *ModelSnapshot {
	return emptySnapshot()
}
