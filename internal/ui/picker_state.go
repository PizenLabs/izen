package ui

import (
	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/runtime/authority"
)

// loadRecentBindings reads the persistent MRU list (newest first) through the
// config store. The UI performs zero direct filesystem I/O (Phase 0 write
// lock): all state-file access lives in internal/config.
func loadRecentBindings() []authority.ModelBinding {
	entries := config.LoadRecentModels()
	out := make([]authority.ModelBinding, 0, len(entries))
	for _, e := range entries {
		if e.ModelID == "" {
			continue
		}
		out = append(out, authority.ModelBinding{
			ProviderID: authority.ProviderID(e.Provider),
			ModelID:    authority.ModelID(e.ModelID),
		})
	}
	return out
}

// saveRecentBindings writes the MRU list to ~/.izen/state.json through the
// config store. Silent on failure: MRU is a convenience, never an authority.
func saveRecentBindings(recent []authority.ModelBinding) {
	entries := make([]config.RecentModelEntry, 0, len(recent))
	for _, b := range recent {
		if b.ModelID == "" {
			continue
		}
		entries = append(entries, config.RecentModelEntry{
			Provider: string(b.ProviderID),
			ModelID:  string(b.ModelID),
		})
	}
	_ = config.SaveRecentModels(entries)
}
