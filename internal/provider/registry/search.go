package registry

import (
	"strings"
)

// Filter returns descriptors matching query against ID and Name plus an
// optional exact provider filter. It runs strictly against the in-memory
// slice: zero disk I/O. Matching is case-insensitive substring with a
// subsequence-fuzzy fallback (all query chars appear in order).
//
// An empty query matches everything (subject to providerFilter); an empty
// providerFilter matches all providers. The result is a fresh slice; the
// registry is never mutated and callers may freely modify the output.
func (r *Registry) Filter(query, providerFilter string) []ModelDescriptor {
	// Lock-free read via the immutable atomic snapshot: zero disk I/O,
	// zero mutex contention with background Sync/UpdateProvider writers.
	return filterSlice(r.Load().Models, query, providerFilter)
}

// filterSlice is the lock-free core so benchmarks can isolate matching cost.
func filterSlice(models []ModelDescriptor, query, providerFilter string) []ModelDescriptor {
	q := strings.ToLower(strings.TrimSpace(query))
	pf := strings.ToLower(strings.TrimSpace(providerFilter))
	out := make([]ModelDescriptor, 0, len(models))
	for _, m := range models {
		if pf != "" && strings.ToLower(m.Provider) != pf {
			continue
		}
		if q == "" {
			out = append(out, m)
			continue
		}
		id := strings.ToLower(m.ID)
		name := strings.ToLower(m.Name)
		if strings.Contains(id, q) || strings.Contains(name, q) {
			out = append(out, m)
			continue
		}
		if isSubsequence(q, id) || isSubsequence(q, name) {
			out = append(out, m)
		}
	}
	return out
}

// isSubsequence reports whether every rune of q appears in s in order.
func isSubsequence(q, s string) bool {
	if q == "" {
		return true
	}
	qi := 0
	qRunes := []rune(q)
	for _, sr := range s {
		if sr == qRunes[qi] {
			qi++
			if qi == len(qRunes) {
				return true
			}
		}
	}
	return false
}
