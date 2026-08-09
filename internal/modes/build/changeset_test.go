package build

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/changeset"
)

// TestCompileChangeSets_ReplaceBlock verifies the ChangeSet → FileMutation
// integration: a REPLACE_BLOCK change compiles through the Diff Compiler and
// reduces to the correct final content without touching disk.
func TestCompileChangeSets_ReplaceBlock(t *testing.T) {
	root := t.TempDir()
	original := "<!DOCTYPE html>\n<html><body>\n  <h3>Old Delta</h3>\n</body></html>\n"
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte(original), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	ex := NewExecutor(root, NewEngine())
	muts, err := ex.CompileChangeSets([]changeset.ChangeSet{{
		TargetFile: "index.html",
		Kind:       changeset.KindReplaceBlock,
		OldContent: "<h3>Old Delta</h3>",
		NewContent: "<h3>Project Delta</h3>",
		Confidence: 1.0,
	}})
	if err != nil {
		t.Fatalf("CompileChangeSets: %v", err)
	}
	if len(muts) != 1 {
		t.Fatalf("mutations = %d, want 1", len(muts))
	}
	if got := muts[0].Content; !strings.Contains(got, "<h3>Project Delta</h3>") {
		t.Errorf("final content missing replacement:\n%s", got)
	}
	// Read-only contract: the on-disk file is untouched.
	data, _ := os.ReadFile(filepath.Join(root, "index.html"))
	if string(data) != original {
		t.Errorf("CompileChangeSets mutated disk:\n%s", data)
	}
}

// TestCompileChangeSets_AmbiguousAnchor verifies the ambiguity guard propagates
// through the executor: an anchor missing from the target pauses compilation.
func TestCompileChangeSets_AmbiguousAnchor(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"),
		[]byte("<html><body>Hello</body></html>\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	ex := NewExecutor(root, NewEngine())
	_, err := ex.CompileChangeSets([]changeset.ChangeSet{{
		TargetFile: "index.html",
		Kind:       changeset.KindReplaceBlock,
		OldContent: "<h3>Missing Anchor</h3>",
		NewContent: "<h3>Project Delta</h3>",
	}})
	if err == nil {
		t.Fatal("CompileChangeSets succeeded, want anchor-not-found error")
	}
}
