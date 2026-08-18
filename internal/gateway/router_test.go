package gateway

import (
	"testing"
)

func TestExtractDirectMutationTargets_SingleFile(t *testing.T) {
	target := ExtractDirectMutationTargets("create index.html for landing page")
	if len(target) != 1 {
		t.Fatalf("expected 1 target, got %d", len(target))
	}
	if target[0] != "index.html" {
		t.Errorf("expected index.html, got %q", target[0])
	}
}

func TestExtractDirectMutationTargets_MultiFile(t *testing.T) {
	target := ExtractDirectMutationTargets("create index.html, styles.css, script.js for static portfolio")
	if len(target) != 3 {
		t.Fatalf("expected 3 targets, got %d", len(target))
	}
	expected := []string{"index.html", "styles.css", "script.js"}
	for i, f := range expected {
		if target[i] != f {
			t.Errorf("target[%d] = %q, want %q", i, target[i], f)
		}
	}
}

func TestExtractDirectMutationTargets_AtRefs(t *testing.T) {
	target := ExtractDirectMutationTargets("update @index.html, @styles.css, @script.js")
	if len(target) != 3 {
		t.Fatalf("expected 3 targets, got %d", len(target))
	}
	expected := []string{"index.html", "styles.css", "script.js"}
	for i, f := range expected {
		if target[i] != f {
			t.Errorf("target[%d] = %q, want %q", i, target[i], f)
		}
	}
}

func TestExtractDirectMutationTargets_Empty(t *testing.T) {
	target := ExtractDirectMutationTargets("")
	if target != nil {
		t.Errorf("expected nil for empty input, got %v", target)
	}
}

func TestExtractDirectMutationTargets_NoMatch(t *testing.T) {
	target := ExtractDirectMutationTargets("investigate why the build fails")
	if len(target) != 0 {
		t.Errorf("expected no targets for diagnostic input, got %v", target)
	}
}
