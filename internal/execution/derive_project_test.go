package execution

// ── Project-level evidence-bound derivation ─────────────────────────────────
//
// A project-level objective names a PROJECT, not a file. When it declares no
// artifact kind, DeriveScope (with ProjectLevel set) resolves the workspace's
// authored implementation artifacts. These tests pin the two properties that
// keep that safe: it excludes non-implementation files, and it refuses an
// over-broad surface instead of binding a monorepo.

import (
	"sort"
	"testing"
)

func projectWorkspace(t *testing.T) WorkspaceProfile {
	t.Helper()
	root := t.TempDir()
	for name, body := range map[string]string{
		"index.html":      "<html></html>\n",
		"styles.css":      ":root {}\n",
		"script.js":       "1;\n",
		"readme.md":       "# Demo\n",
		"docs/notes.md":   "internal notes\n",
		"data.json":       "{}\n",
		"assets/logo.svg": "<svg></svg>\n",
	} {
		writeFile(t, root, name, body)
	}
	return NewWorkspaceDiscovery(root).Discover()
}

func TestProjectDerivation_BindsImplementationArtifactsOnly(t *testing.T) {
	d := DeriveScope(DerivationRequest{
		Prompt:       "redesign this project help me",
		Profile:      projectWorkspace(t),
		ProjectLevel: true,
	})
	if !d.IsUnique() {
		t.Fatalf("status = %s, want UNIQUE (%v / %s)", d.StatusOrUnresolved(), d.Targets, d.Reason)
	}
	got := append([]string(nil), d.Targets...)
	sort.Strings(got)
	want := []string{"index.html", "script.js", "styles.css"}
	if len(got) != len(want) {
		t.Fatalf("targets = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("targets = %v, want %v", got, want)
		}
	}
	for _, excluded := range []string{"readme.md", "docs/notes.md", "data.json", "assets/logo.svg"} {
		for _, target := range got {
			if target == excluded {
				t.Errorf("derivation bound non-implementation file %q", excluded)
			}
		}
	}
}

func TestProjectDerivation_NotProjectLevelKeepsStrictSemantics(t *testing.T) {
	// The same prompt WITHOUT the project-level classification declares no kind,
	// so it must NOT derive the implementation set: the human names the file.
	d := DeriveScope(DerivationRequest{
		Prompt:  "redesign this project help me",
		Profile: projectWorkspace(t),
	})
	if d.IsUnique() {
		t.Fatalf("a non-project-level no-kind objective derived targets: %v", d.Targets)
	}
}

func TestProjectDerivation_OverBroadSurfaceIsUnresolved(t *testing.T) {
	root := t.TempDir()
	// 20 implementation files → beyond the bounded project surface.
	for _, name := range []string{
		"a.go", "b.go", "c.go", "d.go", "e.go", "f.go", "g.go", "h.go", "i.go", "j.go",
		"k.go", "l.go", "m.go", "n.go", "o.go", "p.go", "q.go", "r.go", "s.go", "t.go",
	} {
		writeFile(t, root, name, "package x\n")
	}
	d := DeriveScope(DerivationRequest{
		Prompt:       "refactor this project",
		Profile:      NewWorkspaceDiscovery(root).Discover(),
		ProjectLevel: true,
	})
	if d.IsUnique() {
		t.Fatalf("an over-broad project surface was bound: %v", d.Targets)
	}
	if d.StatusOrUnresolved() != DerivationUnresolved {
		t.Fatalf("status = %s, want UNRESOLVED", d.StatusOrUnresolved())
	}
}

func TestProjectDerivation_EmptyImplementationSurfaceIsUnresolved(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "readme.md", "# docs only\n")
	d := DeriveScope(DerivationRequest{
		Prompt:       "redesign this project",
		Profile:      NewWorkspaceDiscovery(root).Discover(),
		ProjectLevel: true,
	})
	if d.IsUnique() {
		t.Fatal("a docs-only workspace produced an implementation target set")
	}
}
