package session

import "testing"

func TestPruneLastUserMessage(t *testing.T) {
	s := New()
	s.AddMessage("user", "hello", 5)
	s.AddMessage("assistant", "hi", 5)
	s.AddMessage("user", "failed prompt", 5)
	if len(s.History) != 3 {
		t.Fatalf("history len = %d", len(s.History))
	}
	ok := s.PruneLastUserMessage("failed prompt")
	if !ok {
		t.Fatal("PruneLastUserMessage should return true")
	}
	if len(s.History) != 2 {
		t.Fatalf("after prune len = %d, want 2", len(s.History))
	}
	if s.History[len(s.History)-1].Role != "assistant" {
		t.Fatalf("last role = %s, want assistant", s.History[len(s.History)-1].Role)
	}
	// Prune non-matching content should not prune
	s.AddMessage("user", "another", 5)
	ok = s.PruneLastUserMessage("different")
	if ok {
		t.Fatal("should not prune when content mismatch")
	}
	if len(s.History) != 3 {
		t.Fatalf("len should remain 3, got %d", len(s.History))
	}
	// Prune with empty content should prune last user regardless? Our impl returns false for mismatch; but test empty content not needed
	// Non-user last should not prune
	s.AddMessage("assistant", "ans", 5)
	ok = s.PruneLastUserMessage("ans")
	if ok {
		t.Fatal("should not prune when last role is assistant")
	}
}
