package config

import (
	"os"
	"testing"
)

// MRU state round-trips through ~/.izen/state.json under a redirected HOME,
// newest-first, and tolerates a missing file.
func TestRecentModelsStateRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())

	if got := LoadRecentModels(); got != nil {
		t.Fatalf("missing state file must yield nil, got %v", got)
	}

	recent := []RecentModelEntry{
		{Provider: "openrouter", ModelID: "openrouter/deepseek/deepseek-r1"},
		{Provider: "gemini", ModelID: "google/gemini-2.5-flash"},
		{Provider: "openai", ModelID: ""}, // intentionally empty: skipped
	}
	if err := SaveRecentModels(recent); err != nil {
		t.Fatalf("SaveRecentModels: %v", err)
	}

	loaded := LoadRecentModels()
	if len(loaded) != 2 {
		t.Fatalf("loaded %d entries, want 2 (empty skipped)", len(loaded))
	}
	if loaded[0].ModelID != "openrouter/deepseek/deepseek-r1" || loaded[0].Provider != "openrouter" {
		t.Errorf("loaded[0] = %+v, want openrouter deepseek-r1", loaded[0])
	}
	if loaded[1].ModelID != "google/gemini-2.5-flash" {
		t.Errorf("loaded[1] = %+v, want gemini flash", loaded[1])
	}
}

// Corrupt state files degrade silently to nil instead of a load error.
func TestRecentModelsStateCorruptFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	if err := SaveRecentModels([]RecentModelEntry{
		{Provider: "openai", ModelID: "openai/gpt-4o-mini"},
	}); err != nil {
		t.Fatalf("SaveRecentModels: %v", err)
	}
	path := PickerStatePath()
	if path == "" {
		t.Skip("no home dir")
	}
	if err := os.WriteFile(path, []byte("{not json"), 0644); err != nil {
		t.Fatalf("corrupt write: %v", err)
	}
	if got := LoadRecentModels(); got != nil {
		t.Fatalf("corrupt state must yield nil, got %v", got)
	}
}
