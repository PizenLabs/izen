package understanding_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/PizenLabs/izen/internal/adapters/web"
	"github.com/PizenLabs/izen/internal/understanding"
)

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	full := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// PU-01: existing repository evidence produces EXISTING.
func TestPU01_ExistingRepoEvidenceProducesExisting(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module example.com/demo\n\ngo 1.24\n")
	writeFile(t, root, "main.go", "package main\n\nfunc main() {}\n")
	u := understanding.Derive(root)
	if u.Kind != understanding.KindExisting {
		t.Fatalf("kind = %v, want EXISTING", u.Kind)
	}
	if !u.Valid() {
		t.Fatal("understanding must be valid current truth")
	}
	if u.Unavailable {
		t.Fatal("understanding must not be unavailable")
	}
}

// PU-02: a truly empty/new project can produce GREENFIELD.
func TestPU02_EmptyProjectProducesGreenfield(t *testing.T) {
	root := t.TempDir()
	u := understanding.Derive(root)
	if u.Kind != understanding.KindGreenfield {
		t.Fatalf("kind = %v, want GREENFIELD", u.Kind)
	}
	// README/LICENSE-only scaffolding is still greenfield, not existing.
	writeFile(t, root, "README.md", "# demo\n")
	writeFile(t, root, ".gitignore", "bin/\n")
	u = understanding.Derive(root)
	if u.Kind != understanding.KindGreenfield {
		t.Fatalf("kind = %v, want GREENFIELD for trivial-only workspace", u.Kind)
	}
}

// PU-03: insufficient evidence produces UNKNOWN.
func TestPU03_InsufficientEvidenceProducesUnknown(t *testing.T) {
	u := understanding.Derive(filepath.Join(t.TempDir(), "does-not-exist"))
	if u.Kind != understanding.KindUnknown {
		t.Fatalf("kind = %v, want UNKNOWN for inaccessible root", u.Kind)
	}
	if !u.Unavailable {
		t.Fatal("inaccessible root must mark understanding unavailable")
	}
	// A single unrecognized stray file is insufficient evidence.
	root := t.TempDir()
	writeFile(t, root, "notes.zzz-unknown-ext", "hello\n")
	u = understanding.Derive(root)
	if u.Kind != understanding.KindUnknown {
		t.Fatalf("kind = %v, want UNKNOWN for unrecognized content", u.Kind)
	}
}

// PU-04: missing target file does not automatically imply Greenfield.
func TestPU04_MissingTargetFileIsNotGreenfield(t *testing.T) {
	// package.json + src/ + components without index.html: the requested
	// artifact is absent but the project is EXISTING.
	root := t.TempDir()
	writeFile(t, root, "package.json", `{"name":"demo","dependencies":{"react":"18.0.0"}}`+"\n")
	writeFile(t, root, "src/app.js", "console.log(1);\n")
	writeFile(t, root, "components/button.js", "export default 1;\n")
	u := understanding.Derive(root)
	if u.Kind != understanding.KindExisting {
		t.Fatalf("kind = %v, want EXISTING despite missing index.html", u.Kind)
	}
	// Static-web fixture minus index.html is still EXISTING via generic language count.
	root2 := t.TempDir()
	writeFile(t, root2, "styles.css", "body { margin: 0; }\n")
	writeFile(t, root2, "script.js", "console.log(1);\n")
	writeFile(t, root2, "about.html", "<html></html>\n")
	u = understanding.Derive(root2)
	if u.Kind != understanding.KindExisting {
		t.Fatalf("kind = %v, want EXISTING for static surface without index.html", u.Kind)
	}
	if u.Kind == understanding.KindGreenfield {
		t.Fatal("missing index.html must never imply GREENFIELD")
	}
}

// PU-05: repository evidence is sufficient to identify static-web structure via adapter.
// Core understanding is domain-neutral; web-specific surface is derived via adapter.
func TestPU05_StaticWebFixtureUnderstood(t *testing.T) {
	u := understanding.Derive(fixtureRoot(t))
	if u.Kind != understanding.KindExisting {
		t.Fatalf("kind = %v, want EXISTING for the static-web fixture", u.Kind)
	}
	ws := web.Derive(fixtureRoot(t))
	if !ws.Present {
		t.Fatal("static-web surface must be present for the fixture via adapter")
	}
	if len(ws.Entrypoints) == 0 || ws.Entrypoints[0] != "index.html" {
		t.Fatalf("entrypoints = %v, want index.html first", ws.Entrypoints)
	}
	if len(ws.CSS) == 0 || len(ws.Scripts) == 0 {
		t.Fatalf("css = %v scripts = %v, want both evidenced", ws.CSS, ws.Scripts)
	}
	if len(ws.AssetDirs) == 0 {
		t.Fatalf("asset dirs = %v, want assets/ evidenced", ws.AssetDirs)
	}
	if len(ws.ScriptRefs) == 0 || len(ws.StyleRefs) == 0 {
		t.Fatalf("refs = %v / %v, want script + stylesheet references", ws.ScriptRefs, ws.StyleRefs)
	}
	// Core must not claim static-web identity; that is adapter-level.
	if u.Identity == "static-web" {
		t.Fatalf("core identity must not be static-web; got %q (web is adapter-level)", u.Identity)
	}
}

// PU-06: project Understanding is evidence-backed.
func TestPU06_UnderstandingIsEvidenceBacked(t *testing.T) {
	u := understanding.Derive(fixtureRoot(t))
	if len(u.Evidence) == 0 {
		t.Fatal("understanding must carry evidence")
	}
	for _, e := range u.Evidence {
		if e.ID == "" || e.Detail == "" || e.Key() == "" {
			t.Fatalf("evidence record must be traceable: %+v", e)
		}
	}
	if u.Confidence <= 0 || u.Confidence > 1 {
		t.Fatalf("confidence = %v, want (0,1]", u.Confidence)
	}
	if u.Digest == "" || u.SnapshotID == "" {
		t.Fatal("understanding must be bound to a snapshot digest")
	}
	// Core identity is domain-neutral (unknown for html/css/js without manifest), never fabricated stack.
	if u.Identity == "" {
		t.Fatalf("identity must be set, got empty")
	}
	// Must not be static-web via core; adapter holds that.
	if u.Identity == "static-web" {
		t.Fatalf("core identity must not be static-web")
	}
}

// PU-07: model proposal cannot silently become authoritative project fact.
func TestPU07_ModelProposalCannotOverrideEvidence(t *testing.T) {
	u := understanding.Derive(fixtureRoot(t))
	before := u
	after, disp := understanding.ConsiderProposal(u, understanding.ModelProposal{
		Claim: "the project uses React with Next.js and Tailwind",
		Basis: "model prior",
	})
	if disp.Accepted {
		t.Fatal("model proposal must never be accepted as repository truth")
	}
	if after.Kind != before.Kind || after.Identity != before.Identity ||
		after.Digest != before.Digest || len(after.Evidence) != len(before.Evidence) {
		t.Fatal("considering a model proposal must leave authoritative state unchanged")
	}
}

func fixtureRoot(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs("../../testdata/staticweb")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(abs); err != nil {
		t.Fatalf("static-web fixture missing at %s: %v", abs, err)
	}
	return abs
}
