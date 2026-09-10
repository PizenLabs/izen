package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// RecentModelEntry is one persistent most-recently-used model entry stored in
// ~/.izen/state.json. Provider/Model always travel as a tuple.
type RecentModelEntry struct {
	Provider string `json:"provider"`
	ModelID  string `json:"model_id"`
}

// pickerStateFile is the on-disk shape of ~/.izen/state.json. It carries the
// model picker's RECENTLY USED list (newest first), persisted across sessions.
type pickerStateFile struct {
	RecentModels []RecentModelEntry `json:"recent_models,omitempty"`
}

// PickerStatePath returns the user-level picker state file location
// (~/.izen/state.json), or "" when the home directory is unavailable.
func PickerStatePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".izen", "state.json")
}

// LoadRecentModels reads the persistent MRU list (newest first). Missing or
// corrupt state files yield nil (treated as empty — never an authority).
func LoadRecentModels() []RecentModelEntry {
	path := PickerStatePath()
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var st pickerStateFile
	if err := json.Unmarshal(data, &st); err != nil {
		return nil
	}
	out := make([]RecentModelEntry, 0, len(st.RecentModels))
	for _, e := range st.RecentModels {
		if e.ModelID == "" {
			continue
		}
		out = append(out, e)
	}
	return out
}

// SaveRecentModels writes the MRU list to ~/.izen/state.json. Missing home is
// a silent no-op: MRU is a convenience seam, never an authority.
func SaveRecentModels(recent []RecentModelEntry) error {
	path := PickerStatePath()
	if path == "" {
		return nil
	}
	entries := make([]RecentModelEntry, 0, len(recent))
	for _, e := range recent {
		if e.ModelID == "" {
			continue
		}
		entries = append(entries, e)
	}
	st := pickerStateFile{RecentModels: entries}
	data, err := json.MarshalIndent(&st, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}
