package intent

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestNewMirrorsRawPrompt(t *testing.T) {
	i := New("  refactor the auth layer  ")
	if i == nil {
		t.Fatal("New returned nil")
	}
	if i.ID == "" {
		t.Error("ID is empty")
	}
	if i.Goal.RawPrompt != "refactor the auth layer" {
		t.Errorf("RawPrompt = %q, want trimmed prompt", i.Goal.RawPrompt)
	}
	if i.Goal.Primary != "refactor the auth layer" {
		t.Errorf("Primary = %q, want trimmed prompt", i.Goal.Primary)
	}
	if i.Goal.Facet != "" {
		t.Errorf("Facet = %q, want empty", i.Goal.Facet)
	}
	if i.CreatedAt.IsZero() {
		t.Error("CreatedAt is zero")
	}
	if _, offset := i.CreatedAt.Zone(); offset != 0 {
		t.Errorf("CreatedAt zone offset = %d, want UTC", offset)
	}
	if len(i.Evidence) != 0 {
		t.Errorf("Evidence = %v, want empty", i.Evidence)
	}
	if i.Constraints.ReadonlyOnly {
		t.Error("Constraints.ReadonlyOnly = true, want false by default")
	}
}

func TestNewWithWhitespaceOnlyPrompt(t *testing.T) {
	i := New("   \n\t  ")
	if i.Goal.RawPrompt != "" {
		t.Errorf("RawPrompt = %q, want empty after trim", i.Goal.RawPrompt)
	}
	if i.Goal.Primary != "" {
		t.Errorf("Primary = %q, want empty after trim", i.Goal.Primary)
	}
}

func TestWithConstraintsDoesNotMutateReceiver(t *testing.T) {
	base := New("refactor auth")
	updated := base.WithConstraints(Constraints{
		ForbiddenPaths: []string{"internal/legacy/"},
		MaxDepth:       3,
		ReadonlyOnly:   true,
	})
	if updated == base {
		t.Fatal("WithConstraints returned the receiver, not a copy")
	}
	if base.Constraints.ReadonlyOnly {
		t.Error("receiver mutated by WithConstraints")
	}
	if !updated.Constraints.ReadonlyOnly {
		t.Error("returned copy did not apply ReadonlyOnly")
	}
	if updated.Constraints.MaxDepth != 3 {
		t.Errorf("MaxDepth = %d, want 3", updated.Constraints.MaxDepth)
	}
	if len(updated.Constraints.ForbiddenPaths) != 1 || updated.Constraints.ForbiddenPaths[0] != "internal/legacy/" {
		t.Errorf("ForbiddenPaths = %v, want [internal/legacy/]", updated.Constraints.ForbiddenPaths)
	}
	if updated.ID != base.ID {
		t.Errorf("copy ID = %q, want %q (identity preserved)", updated.ID, base.ID)
	}
}

func TestWithEvidenceDoesNotMutateReceiver(t *testing.T) {
	base := New("refactor auth")
	one := base.WithEvidence(EvidenceRef{Source: "file", URI: "internal/auth/auth.go"})
	if len(base.Evidence) != 0 {
		t.Error("receiver mutated by WithEvidence")
	}
	two := one.WithEvidence(EvidenceRef{Source: "issue", URI: "https://example.com/42"})
	if len(one.Evidence) != 1 {
		t.Errorf("one.Evidence length = %d, want 1", len(one.Evidence))
	}
	if len(two.Evidence) != 2 {
		t.Errorf("two.Evidence length = %d, want 2", len(two.Evidence))
	}
	if two.Evidence[0].URI != "internal/auth/auth.go" || two.Evidence[1].URI != "https://example.com/42" {
		t.Errorf("evidence order lost: %v", two.Evidence)
	}
}

func TestWithConstraintsPreservesEvidence(t *testing.T) {
	base := New("refactor auth").WithEvidence(EvidenceRef{Source: "file", URI: "a.go"})
	updated := base.WithConstraints(Constraints{ReadonlyOnly: true})
	if len(updated.Evidence) != 1 || updated.Evidence[0].URI != "a.go" {
		t.Errorf("constraints builder dropped evidence: %v", updated.Evidence)
	}
}

func TestJSONRoundTrip(t *testing.T) {
	i := New("refactor auth").WithConstraints(Constraints{
		ForbiddenPaths: []string{"internal/legacy/"},
		MaxDepth:       2,
		ReadonlyOnly:   true,
	}).WithEvidence(EvidenceRef{Source: "file", URI: "internal/auth/auth.go"})
	i.Goal.Facet = "readonly"

	data, err := json.Marshal(i)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var back UserIntent
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.ID != i.ID {
		t.Errorf("ID = %q, want %q", back.ID, i.ID)
	}
	if back.Goal.RawPrompt != i.Goal.RawPrompt || back.Goal.Primary != i.Goal.Primary || back.Goal.Facet != "readonly" {
		t.Errorf("Goal = %+v, want %+v", back.Goal, i.Goal)
	}
	if !back.CreatedAt.Equal(i.CreatedAt) {
		t.Errorf("CreatedAt = %v, want %v", back.CreatedAt, i.CreatedAt)
	}
	if len(back.Constraints.ForbiddenPaths) != 1 || back.Constraints.MaxDepth != 2 || !back.Constraints.ReadonlyOnly {
		t.Errorf("Constraints = %+v", back.Constraints)
	}
	if len(back.Evidence) != 1 || back.Evidence[0].Source != "file" {
		t.Errorf("Evidence = %v", back.Evidence)
	}
}

func TestJSONOmitsEmptyOptionalFields(t *testing.T) {
	i := New("ask about the cache")
	data, err := json.Marshal(i)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(data)
	if strings.Contains(s, `"evidence"`) {
		t.Errorf("empty Evidence should be omitted, got %s", s)
	}
	if strings.Contains(s, `"max_depth"`) {
		t.Errorf("zero MaxDepth should be omitted, got %s", s)
	}
	if strings.Contains(s, `"forbidden_paths"`) {
		t.Errorf("nil ForbiddenPaths should be omitted, got %s", s)
	}
	if !strings.Contains(s, `"readonly_only":false`) {
		t.Errorf("ReadonlyOnly default must be explicit, got %s", s)
	}
}

func TestNilReceiverBuilders(t *testing.T) {
	var i *UserIntent
	if got := i.WithConstraints(Constraints{}); got != nil {
		t.Error("WithConstraints on nil receiver must return nil")
	}
	if got := i.WithEvidence(EvidenceRef{}); got != nil {
		t.Error("WithEvidence on nil receiver must return nil")
	}
}

func TestCreatedAtSkewIndependentOfWallClock(t *testing.T) {
	before := time.Now().UTC()
	i := New("refactor")
	after := time.Now().UTC()
	if i.CreatedAt.Before(before) || i.CreatedAt.After(after) {
		t.Errorf("CreatedAt = %v, outside [%v, %v]", i.CreatedAt, before, after)
	}
}
