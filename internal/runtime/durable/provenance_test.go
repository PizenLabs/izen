package durable

import (
	"testing"

	"github.com/PizenLabs/izen/internal/core/domain"
)

// TestCreateTaskWithProvenance pins the Phase 6.4 durable wiring: the
// authorization provenance that created a task is persisted in the ledger,
// survives replay into a fresh store, and defaults to ScopeNone for legacy
// creation paths — a restored pre-Phase-6 session stays read-only.
func TestCreateTaskWithProvenance(t *testing.T) {
	dir := t.TempDir()
	s := NewTaskStore(dir)
	if err := s.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}

	if _, err := s.CreateTaskWithProvenance("t-dyn", "patch", []string{"a.txt"}, domain.ScopeDynamic); err != nil {
		t.Fatalf("create dynamic: %v", err)
	}
	if _, err := s.CreateTask("t-none", "patch", []string{"b.txt"}); err != nil {
		t.Fatalf("create legacy: %v", err)
	}

	// Replay: a fresh store over the same directory must restore the grant
	// exactly from the ledger.
	replay := NewTaskStore(dir)
	if err := replay.Open(); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	st, ok := replay.State("t-dyn")
	if !ok {
		t.Fatal("t-dyn missing after replay")
	}
	if st.ScopeProvenance != domain.ScopeDynamic {
		t.Fatalf("t-dyn provenance = %v, want ScopeDynamic", st.ScopeProvenance)
	}
	legacy, ok := replay.State("t-none")
	if !ok {
		t.Fatal("t-none missing after replay")
	}
	if legacy.ScopeProvenance != domain.ScopeNone {
		t.Fatalf("legacy task provenance = %v, want ScopeNone", legacy.ScopeProvenance)
	}
}
