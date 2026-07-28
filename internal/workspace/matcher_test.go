package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolve_ExplicitFilePath(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "templates"), 0755)
	_ = os.WriteFile(filepath.Join(root, "templates", "layout.tmpl"), []byte("test"), 0644)

	resolver := NewTargetFileResolver(root)
	result := resolver.Resolve("move navigation from footer to header in templates/layout.tmpl")
	if result != "templates/layout.tmpl" {
		t.Errorf("expected templates/layout.tmpl, got %q", result)
	}
}

func TestResolve_KeywordMatch(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "assets/css"), 0755)
	_ = os.WriteFile(filepath.Join(root, "assets/css", "main.css"), []byte("test"), 0644)

	resolver := NewTargetFileResolver(root)
	result := resolver.Resolve("fix the css styling")
	if result != "assets/css/main.css" {
		t.Errorf("expected assets/css/main.css, got %q", result)
	}
}

func TestResolve_RejectsWorkspace(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "templates"), 0755)
	_ = os.WriteFile(filepath.Join(root, "templates", "layout.tmpl"), []byte("test"), 0644)

	resolver := NewTargetFileResolver(root)
	result := resolver.Resolve("move workspace navigation")
	if result == "workspace" {
		t.Error("expected workspace to be rejected")
	}
	if result != "" {
		t.Errorf("expected empty result, got %q", result)
	}
}

func TestResolve_EmptyRoot(t *testing.T) {
	resolver := NewTargetFileResolver("")
	result := resolver.Resolve("move navigation")
	if result != "" {
		t.Errorf("expected empty result, got %q", result)
	}
}

func TestResolve_NoMatch(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "templates"), 0755)
	_ = os.WriteFile(filepath.Join(root, "templates", "layout.tmpl"), []byte("test"), 0644)

	resolver := NewTargetFileResolver(root)
	result := resolver.Resolve("completely unrelated prompt about databases and quantum computing")
	if result != "" {
		t.Errorf("expected empty result for no match, got %q", result)
	}
}

func TestValidateTarget_WorkspaceRejected(t *testing.T) {
	if ValidateTarget("workspace") != "" {
		t.Error("expected workspace to be rejected")
	}
	if ValidateTarget("WORKSPACE") != "" {
		t.Error("expected WORKSPACE to be rejected")
	}
	if ValidateTarget("") != "" {
		t.Error("expected empty string to remain empty")
	}
	if got := ValidateTarget("templates/layout.tmpl"); got != "templates/layout.tmpl" {
		t.Errorf("expected templates/layout.tmpl, got %q", got)
	}
}

func TestResolveOrEmpty_WorkspaceRejected(t *testing.T) {
	resolver := NewTargetFileResolver(t.TempDir())
	if resolver.ResolveOrEmpty("workspace") != "" {
		t.Error("expected workspace to be rejected by ResolveOrEmpty")
	}
}

func TestExtractKeywords_FiltersFiller(t *testing.T) {
	keywords := extractKeywords("the quick brown fox moves the file")
	for _, kw := range keywords {
		if kw == "the" {
			t.Error("expected 'the' to be filtered as filler")
		}
	}
}

func TestIsValidTarget_RejectsWorkspace(t *testing.T) {
	if isValidTarget("workspace") {
		t.Error("expected workspace to be invalid")
	}
	if isValidTarget("WORKSPACE") {
		t.Error("expected WORKSPACE to be invalid")
	}
	if !isValidTarget("templates/layout.tmpl") {
		t.Error("expected templates/layout.tmpl to be valid")
	}
}
