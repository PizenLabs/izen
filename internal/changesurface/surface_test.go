package changesurface_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/PizenLabs/izen/internal/adapters/web"
	"github.com/PizenLabs/izen/internal/changesurface"
	"github.com/PizenLabs/izen/internal/understanding"
)

func fixtureUnderstanding(t *testing.T) understanding.ProjectUnderstanding {
	t.Helper()
	abs, err := filepath.Abs("../../testdata/staticweb")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(abs); err != nil {
		t.Fatalf("static-web fixture missing: %v", err)
	}
	u := understanding.Derive(abs)
	if !u.Valid() {
		t.Fatal("fixture understanding must be valid")
	}
	return u
}

func containsPath(cands []changesurface.Candidate, path string) *changesurface.Candidate {
	for i, c := range cands {
		if c.Path == path {
			return &cands[i]
		}
	}
	return nil
}

// CS-01: intent + Project Understanding can derive a Change Surface.
// Generic surface must be domain-neutral; web-specific DIRECT is via adapter.
func TestCS01_IntentDerivesSurface(t *testing.T) {
	u := fixtureUnderstanding(t)
	s := changesurface.Derive("Redesign the portfolio website.", nil, u)
	if s.Status == changesurface.StatusUnresolved {
		t.Fatalf("status = %v, want RESOLVED or PARTIAL for generic surface", s.Status)
	}
	if len(s.Candidates) == 0 {
		t.Fatalf("candidates = %v, want at least one evidence-backed candidate", s.Candidates)
	}
	// Web adapter should still yield DIRECT index.html for the same intent
	ws := web.DeriveSurface("Redesign the portfolio website.", nil, u)
	if got := containsPath(ws.Candidates, "index.html"); got == nil {
		t.Fatalf("web adapter candidates = %v, want index.html", ws.Candidates)
	} else if got.Certainty != changesurface.CertaintyDirect {
		t.Fatalf("web adapter index.html certainty = %v, want DIRECT", got.Certainty)
	}
}

// CS-02: Change Surface contains evidence/provenance.
func TestCS02_SurfaceCarriesProvenance(t *testing.T) {
	u := fixtureUnderstanding(t)
	s := changesurface.Derive("Update the homepage styles.", []string{"@index.html"}, u)
	if len(s.Evidence) == 0 {
		t.Fatal("surface must carry derivation provenance")
	}
	for _, c := range s.Candidates {
		if c.Reason == "" || len(c.Evidence) == 0 {
			t.Fatalf("candidate must carry reason + evidence: %+v", c)
		}
	}
	if !s.DigestMatches(u) {
		t.Fatal("surface must bind to the understanding digest it was derived from")
	}
}

// CS-03: Change Surface does not become Authorization Scope.
func TestCS03_SurfaceGrantsNothing(t *testing.T) {
	u := fixtureUnderstanding(t)
	s := changesurface.Derive("Redesign the portfolio website.", nil, u)
	// The surface type exposes no grant, token, approver, or allowlist:
	// candidates are paths + certainty + reason + evidence only.
	for _, c := range s.Candidates {
		if c.Path == "" {
			t.Fatal("candidate path must be explicit; no wildcard authorization")
		}
	}
	if s.UnderstandingDigest == "" {
		t.Fatal("surface must bind to a digest, never to an authority")
	}
}

// CS-04: Target Resolution remains distinct from Change Surface.
func TestCS04_ExplicitTargetAdmittedOnlyWithEvidence(t *testing.T) {
	u := fixtureUnderstanding(t)
	// An evidenced explicit target is admitted as DIRECT ...
	s := changesurface.Derive("Update the homepage.", []string{"@index.html"}, u)
	if got := containsPath(s.Candidates, "index.html"); got == nil || got.Certainty != changesurface.CertaintyDirect {
		t.Fatalf("evidenced explicit target must be DIRECT: %+v", s.Candidates)
	}
	// ... but an unevidenced target is dropped, never invented.
	s = changesurface.Derive("Update the homepage.", []string{"@dashboard.tsx"}, u)
	if got := containsPath(s.Candidates, "dashboard.tsx"); got != nil {
		t.Fatalf("unevidenced target must never enter the surface: %+v", s.Candidates)
	}
}

// CS-05: Change Surface does not produce mutation operations.
func TestCS05_NoMutationOperations(t *testing.T) {
	u := fixtureUnderstanding(t)
	s := changesurface.Derive("Redesign the portfolio website.", nil, u)
	for _, c := range s.Candidates {
		// Candidates describe WHERE a change may be relevant; verbs like
		// overwrite/replace/delete would be Mutation Strategy (out of scope).
		for _, forbidden := range []string{"overwrite", "replace", "delete", "create", "modify"} {
			if c.Reason == forbidden || c.Path == forbidden {
				t.Fatalf("surface must not express mutation operations: %+v", c)
			}
		}
	}
}

// CS-06: unknown/ambiguous understanding produces a conservative
// unresolved surface rather than fabricated targets.
func TestCS06_UnknownYieldsUnresolved(t *testing.T) {
	unknown := understanding.Derive(filepath.Join(t.TempDir(), "does-not-exist"))
	if unknown.Kind != understanding.KindUnknown {
		t.Fatalf("setup: kind = %v, want UNKNOWN", unknown.Kind)
	}
	s := changesurface.Derive("Redesign the portfolio website.", nil, unknown)
	if s.Status != changesurface.StatusUnresolved {
		t.Fatalf("status = %v, want UNRESOLVED", s.Status)
	}
	if len(s.Candidates) != 0 {
		t.Fatalf("unresolved surface must be empty, got %v", s.Candidates)
	}
	// Stale understanding input is equally conservative.
	empty := understanding.ProjectUnderstanding{}
	s = changesurface.Derive("Redesign the portfolio website.", nil, empty)
	if s.Status != changesurface.StatusUnresolved || len(s.Candidates) != 0 {
		t.Fatalf("invalid understanding must yield an empty unresolved surface: %+v", s)
	}
}

// CS-07: stale Project Understanding cannot silently masquerade as current.
func TestCS07_StaleUnderstandingDetectable(t *testing.T) {
	root := t.TempDir()
	write := func(rel, content string) {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("index.html", "<html></html>\n")
	write("styles.css", "body {}\n")
	u := understanding.Derive(root)
	if u.IsStale() {
		t.Fatal("fresh understanding must not be stale")
	}
	s := changesurface.Derive("Redesign the site.", nil, u)
	if !s.DigestMatches(u) {
		t.Fatal("surface must match the fresh understanding")
	}
	// Material change invalidates both the understanding and the surface.
	write("script.js", "console.log(1);\n")
	if !u.IsStale() {
		t.Fatal("understanding must report stale after a material workspace change")
	}
	if s.DigestMatches(understanding.Derive(root)) {
		t.Fatal("surface derived from stale understanding must not match the refreshed digest")
	}
}
