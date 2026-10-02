package capability

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, body := range files {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	return root
}

func fullGrant() Grant {
	return Grant{Provenance: "$prompt", Discover: true, Read: true, Execute: true, Network: true}
}

// ── authorization boundary ──────────────────────────────────────────────────

// TestZeroGrantPermitsNothing: a Runner with no grant installed authorizes
// nothing. This is the default state, so a capability can never be reachable
// merely by constructing the runtime.
func TestZeroGrantPermitsNothing(t *testing.T) {
	r := NewRunner(t.TempDir())
	if got := r.Grant().Catalog(); len(got) != 0 {
		t.Fatalf("zero grant exposed %d capability/capabilities", len(got))
	}
	_, ev, err := r.Discover(context.Background())
	if err == nil {
		t.Fatal("Discover must be refused without a grant")
	}
	if !IsAuthorizationBlocked(err) {
		t.Fatalf("err = %v, want an authorization refusal", err)
	}
	if ev.Class != FailureAuthorizationBlocked {
		t.Fatalf("refusal evidence class = %s, want %s", ev.Class, FailureAuthorizationBlocked)
	}
	if !strings.Contains(ev.Summary, string(WorkspaceDiscover)) {
		t.Fatalf("refusal must name the capability, got %q", ev.Summary)
	}
}

// TestReadGrantDoesNotPermitExecute: read authority is not process authority.
func TestReadGrantDoesNotPermitExecute(t *testing.T) {
	r := NewRunner(t.TempDir())
	r.SetGrant(Grant{Provenance: "/ask", Discover: true, Read: true})
	if r.Grant().Permits(RuntimeServe) {
		t.Fatal("a read-only grant must not permit runtime.serve")
	}
	if r.Grant().Permits(CommandRun) {
		t.Fatal("a read-only grant must not permit command.run")
	}
	if !r.Grant().Permits(FileRead) {
		t.Fatal("a read grant must permit file.read")
	}
}

// TestNetworkGrantIsDistinctFromRead: egress is separately gated.
func TestNetworkGrantIsDistinctFromRead(t *testing.T) {
	r := NewRunner(t.TempDir())
	r.SetGrant(Grant{Provenance: "$prompt", Read: true})
	if r.Grant().Permits(RuntimeFetch) {
		t.Fatal("a read grant must not permit network egress")
	}
}

// TestCatalogContainsOnlyGranted: the model-facing catalog can never advertise
// something the Control Plane will refuse.
func TestCatalogContainsOnlyGranted(t *testing.T) {
	g := Grant{Provenance: "$prompt", Discover: true, Network: true}
	names := g.Names()
	want := map[string]bool{string(WorkspaceDiscover): true, string(RuntimeFetch): true, string(RuntimeInspect): true}
	if len(names) != len(want) {
		t.Fatalf("catalog = %v, want exactly %v", names, want)
	}
	for _, n := range names {
		if !want[n] {
			t.Fatalf("catalog advertised ungranted capability %q", n)
		}
	}
	if !strings.Contains(g.RenderCatalog(), "$prompt") {
		t.Fatal("rendered catalog must name the authorizing directive")
	}
}

// TestUnknownCapabilityIsNeverPermitted: a closed vocabulary, fail-closed.
func TestUnknownCapabilityIsNeverPermitted(t *testing.T) {
	g := fullGrant()
	if g.Permits(ID("browser.screenshot")) {
		t.Fatal("an undeclared capability must never be permitted, even under a full grant")
	}
	if ID("browser.screenshot").Valid() {
		t.Fatal("an undeclared capability must not validate")
	}
}

// TestFailureTaxonomyIsTotalAndClosed: the 13 required classes exist and
// nothing else does. The last two are CONTROL-PLANE terminal outcomes rather
// than capability failures, and the vocabulary still covers them: a loop that
// stops without making progress must be able to say so.
func TestFailureTaxonomyIsTotalAndClosed(t *testing.T) {
	required := []FailureClass{
		FailureAuthorizationBlocked, FailureCapabilityMissing, FailureCapabilityFailed,
		FailureTargetUncertain, FailureContextInsufficient, FailureExecutionFailed,
		FailureObservationFailed, FailureDiagnosisUncertain, FailureRepairFailed,
		FailureVerificationFailed, FailureObjectiveUnproven,
		FailureNoProgress, FailureHumanRequired,
	}
	for _, c := range required {
		if !c.Valid() {
			t.Errorf("required failure class %s is not in the taxonomy", c)
		}
	}
	if len(AllFailureClasses) != len(required) {
		t.Fatalf("taxonomy has %d entries, want exactly %d", len(AllFailureClasses), len(required))
	}
}

// ── workspace discovery ─────────────────────────────────────────────────────

// TestDiscoveryDerivesEntryFromStructure: the entry document is chosen by
// structural evidence (it declares a document and references resources), not by
// a filename convention.
func TestDiscoveryDerivesEntryFromStructure(t *testing.T) {
	root := writeTree(t, map[string]string{
		"index.html": "<!DOCTYPE html><html><head><link rel=stylesheet href=styles.css></head><body><script src=a.js></script></body></html>",
		"styles.css": "body{}",
		"a.js":       "// a",
		"readme.md":  "# notes",
	})
	r := NewRunner(root)
	r.SetGrant(fullGrant())
	profile, ev, err := r.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if !ev.OK || ev.Capability != WorkspaceDiscover {
		t.Fatalf("evidence = %+v, want a successful discover record", ev)
	}
	if profile.Entry == nil {
		t.Fatal("discovery must derive an entry document from a document that references resources")
	}
	if profile.Entry.Path != "index.html" {
		t.Fatalf("entry = %q, want index.html (the only structural document)", profile.Entry.Path)
	}
	if profile.Entry.References != 2 {
		t.Fatalf("entry references = %d, want 2 (stylesheet + script)", profile.Entry.References)
	}
	if len(profile.Paths()) != 4 {
		t.Fatalf("observed %d files, want 4: %v", len(profile.Paths()), profile.Paths())
	}
}

// TestDiscoveryIgnoresDependencyTrees: entering node_modules is what makes a
// bounded scan expensive and what turns evidence into noise.
func TestDiscoveryIgnoresDependencyTrees(t *testing.T) {
	root := writeTree(t, map[string]string{
		"index.html":                "<html><body>x</body></html>",
		"node_modules/pkg/index.js": "module.exports={}",
		".git/config":               "[core]",
		"dist/bundle.js":            "bundled",
	})
	r := NewRunner(root)
	r.SetGrant(fullGrant())
	profile, _, err := r.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	for _, p := range profile.Paths() {
		if strings.HasPrefix(p, "node_modules/") || strings.HasPrefix(p, ".git/") || strings.HasPrefix(p, "dist/") {
			t.Fatalf("discovery entered an excluded tree: %s", p)
		}
	}
}

// TestDiscoveryValidatesManifestsByContent: a manifest is admitted by what it
// parses as, never by what it is called.
func TestDiscoveryValidatesManifestsByContent(t *testing.T) {
	root := writeTree(t, map[string]string{
		"index.html":   "<html><body>x</body></html>",
		"package.json": `{"name":"app","scripts":{"build":"tsc","test":"vitest"}}`,
		"data.json":    `{"some":"unrelated shape"}`,
	})
	r := NewRunner(root)
	r.SetGrant(fullGrant())
	profile, _, err := r.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if !profile.ToolchainSet()["node"] {
		t.Fatalf("toolchains = %v, want node from a validated package manifest", profile.Toolchains)
	}
	var scripts []string
	for _, m := range profile.Manifests {
		if m.Toolchain == "node" {
			scripts = m.Scripts
		}
	}
	if len(scripts) != 2 || scripts[0] != "build" || scripts[1] != "test" {
		t.Fatalf("detected scripts = %v, want [build test] read off the manifest", scripts)
	}
	for _, m := range profile.Manifests {
		if m.Path == "data.json" {
			t.Fatal("an unrelated JSON document must not be admitted as a manifest")
		}
	}
}

// TestDiscoveryReportsAmbiguousEntryRatherThanChoosing: two equally-ranked
// documents have no evidence-backed winner, so nobody picks one.
func TestDiscoveryReportsAmbiguousEntryRatherThanChoosing(t *testing.T) {
	body := "<html><head><link rel=stylesheet href=a.css></head><body>x</body></html>"
	root := writeTree(t, map[string]string{"one.html": body, "two.html": body})
	r := NewRunner(root)
	r.SetGrant(fullGrant())
	profile, _, err := r.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if profile.Entry != nil {
		t.Fatalf("entry = %v, want nil: an ambiguous entry must not be silently resolved", profile.Entry)
	}
	if len(profile.EntryCandidates) != 2 {
		t.Fatalf("entry candidates = %v, want both equally-ranked documents reported", profile.EntryCandidates)
	}
	_, serveEv, serveErr := r.Serve(context.Background(), profile)
	if serveErr == nil {
		t.Fatal("Serve must refuse an ambiguous entry")
	}
	if serveEv.Class != FailureTargetUncertain {
		t.Fatalf("serve refusal class = %s, want %s", serveEv.Class, FailureTargetUncertain)
	}
	if serveEv.Field("candidates") == "" {
		t.Fatal("a TARGET_UNCERTAIN refusal must carry its candidates")
	}
}

// ── read / search ───────────────────────────────────────────────────────────

func TestReadFileRespectsWorkspaceBoundary(t *testing.T) {
	root := writeTree(t, map[string]string{"index.html": "<html>\n<body>\nhi\n</body>\n</html>"})
	r := NewRunner(root)
	r.SetGrant(fullGrant())
	body, ev, err := r.ReadFile(context.Background(), "index.html", 0, 0)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !ev.OK || !strings.Contains(body, "hi") {
		t.Fatalf("evidence = %+v body = %q", ev, body)
	}
	if _, _, err := r.ReadFile(context.Background(), "../escape.html", 0, 0); err == nil {
		t.Fatal("a path escaping the workspace must be refused")
	}
}

func TestReadFileLineWindow(t *testing.T) {
	root := writeTree(t, map[string]string{"f.txt": "l1\nl2\nl3\nl4\nl5"})
	r := NewRunner(root)
	r.SetGrant(fullGrant())
	body, _, err := r.ReadFile(context.Background(), "f.txt", 2, 4)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if body != "l2\nl3\nl4" {
		t.Fatalf("windowed read = %q, want l2..l4", body)
	}
}

func TestSearchReturnsRealLocations(t *testing.T) {
	root := writeTree(t, map[string]string{
		"index.html": "<link rel=stylesheet href=style.css>\n<p>x</p>",
		"styles.css": "body{}",
	})
	r := NewRunner(root)
	r.SetGrant(fullGrant())
	matches, ev, err := r.Search(context.Background(), "style.css", "", 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("matches = %+v, want exactly the one real reference", matches)
	}
	if matches[0].Path != "index.html" || matches[0].Line != 1 {
		t.Fatalf("match = %+v, want index.html:1", matches[0])
	}
	if ev.Field("matches") != "1" {
		t.Fatalf("evidence must report the match count, got %q", ev.Field("matches"))
	}
}

// TestSearchAnswersWhetherANameExistsAnywhere: this is the capability a
// diagnosis uses to produce a real candidate instead of a guess. It locates
// REFERENCES in content, which is the question a runtime defect actually poses.
func TestSearchAnswersWhetherANameExistsAnywhere(t *testing.T) {
	root := writeTree(t, map[string]string{
		"index.html": `<link rel="stylesheet" href="style.css">`,
		"styles.css": "body{}",
	})
	r := NewRunner(root)
	r.SetGrant(fullGrant())
	if m, _, err := r.Search(context.Background(), "style.css", "", 0); err != nil || len(m) != 1 {
		t.Fatalf("Search(style.css) = %v, %v; want the one real reference", m, err)
	}
	if m, _, err := r.Search(context.Background(), "stylesheet.css", "", 0); err != nil || len(m) != 0 {
		t.Fatalf("Search(stylesheet.css) = %v, %v; want no hit (nothing references it)", m, err)
	}
	// A filename the workspace does NOT provide is absent from the file list,
	// which is the filesystem answer a diagnosis needs.
	profile, _, err := r.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	provided := false
	for _, p := range profile.Paths() {
		if strings.HasSuffix(p, "style.css") {
			provided = true
		}
	}
	if provided {
		t.Fatalf("workspace must not provide style.css; observed %v", profile.Paths())
	}
}

func TestSearchRejectsEmptyQuery(t *testing.T) {
	r := NewRunner(t.TempDir())
	r.SetGrant(fullGrant())
	_, ev, err := r.Search(context.Background(), "   ", "", 0)
	if err == nil {
		t.Fatal("an empty query must be refused")
	}
	if ev.Class != FailureContextInsufficient {
		t.Fatalf("class = %s, want %s", ev.Class, FailureContextInsufficient)
	}
}

// ── runtime observation ─────────────────────────────────────────────────────

func TestServeFetchInspectLifecycle(t *testing.T) {
	root := writeTree(t, map[string]string{
		"index.html": "<!DOCTYPE html><html><head><link rel=\"stylesheet\" href=\"styles.css\"></head><body><script src=\"board.js\"></script></body></html>",
		"styles.css": "body{}",
		"board.js":   "// board",
	})
	r := NewRunner(root)
	r.SetGrant(fullGrant())

	profile, _, err := r.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	handle, serveEv, err := r.Serve(context.Background(), profile)
	if err != nil {
		t.Fatalf("Serve: %v (%+v)", err, serveEv)
	}
	if !strings.HasPrefix(handle.BaseURL, "http://127.0.0.1:") {
		t.Fatalf("base URL = %q, want a real bound loopback address", handle.BaseURL)
	}
	if serveEv.Field("base_url") != handle.BaseURL {
		t.Fatal("readiness evidence must name the URL that actually answered")
	}

	obs, err := r.Inspect(context.Background(), handle.BaseURL, "/index.html")
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if obs.Verdict != VerdictPass || len(obs.Defects) != 0 {
		t.Fatalf("verdict = %s defects = %v, want a clean pass on a correct workspace", obs.Verdict, obs.Defects)
	}
	if len(obs.Resources) != 2 {
		t.Fatalf("resources = %+v, want both real subresources probed", obs.Resources)
	}
	for _, res := range obs.Resources {
		if res.Status != 200 {
			t.Fatalf("resource %s answered HTTP %d, want 200", res.Ref, res.Status)
		}
	}

	stopEv, err := r.Stop(context.Background())
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if !stopEv.OK {
		t.Fatalf("stop evidence = %+v", stopEv)
	}
}

// TestInspectDetectsRuntimeOnlyMissingSubresource is the central property: the
// `style.css` / `styles.css` mismatch is invisible to a static read and visible
// only to a real probe, and the runtime must report it WITH the real candidate.
func TestInspectDetectsRuntimeOnlyMissingSubresource(t *testing.T) {
	root := writeTree(t, map[string]string{
		"index.html": "<!DOCTYPE html><html><head><link rel=\"stylesheet\" href=\"style.css\"></head><body></body></html>",
		"styles.css": "body{}",
	})
	r := NewRunner(root)
	r.SetGrant(fullGrant())
	profile, _, err := r.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	handle, _, err := r.Serve(context.Background(), profile)
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	defer func() { _ = handle.Close() }()

	obs, err := r.Inspect(context.Background(), handle.BaseURL, "/index.html")
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if obs.Verdict != VerdictFail {
		t.Fatalf("verdict = %s, want FAIL: the workspace cannot serve what it references", obs.Verdict)
	}
	if len(obs.Defects) != 1 {
		t.Fatalf("defects = %+v, want exactly the missing subresource", obs.Defects)
	}
	d := obs.Defects[0]
	if d.Code != CodeMissingSubresource {
		t.Fatalf("defect code = %s, want %s", d.Code, CodeMissingSubresource)
	}
	if d.Class != FailureExecutionFailed {
		t.Fatalf("defect class = %s, want %s", d.Class, FailureExecutionFailed)
	}
	if len(d.Candidates) != 1 || d.Candidates[0] != "styles.css" {
		t.Fatalf("defect candidates = %v, want the real existing workspace file styles.css", d.Candidates)
	}
	if len(d.Evidence) == 0 {
		t.Fatal("a defect must carry the evidence IDs it was derived from")
	}
	// The evidence must be traceable to a real HTTP observation.
	var found bool
	for _, ev := range obs.Evidence {
		if ev.Capability == RuntimeFetch && ev.Field("status") == "404" {
			found = true
		}
	}
	if !found {
		t.Fatalf("observation must include the real 404 probe; evidence = %s", obs.EvidenceLine())
	}
}

// TestInspectDetectsStructuralDefectInServedBytes: structure is audited on the
// DELIVERED document.
func TestInspectDetectsStructuralDefectInServedBytes(t *testing.T) {
	root := writeTree(t, map[string]string{
		"index.html": "<!DOCTYPE html><html><body><main id=\"b\"><p>x</p><script src=\"a.js\"></script></body></html>",
		"a.js":       "// a",
	})
	r := NewRunner(root)
	r.SetGrant(fullGrant())
	profile, _, err := r.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	handle, _, err := r.Serve(context.Background(), profile)
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	defer func() { _ = handle.Close() }()

	obs, err := r.Inspect(context.Background(), handle.BaseURL, "/index.html")
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if obs.Verdict != VerdictFail {
		t.Fatalf("verdict = %s, want FAIL for an unclosed <main>", obs.Verdict)
	}
	var structural bool
	for _, d := range obs.Defects {
		if d.Code == CodeDocumentStructureInvalid {
			structural = true
			if !strings.Contains(d.Summary, "main") {
				t.Fatalf("structural defect must name the offending element: %s", d.Summary)
			}
		}
	}
	if !structural {
		t.Fatalf("defects = %+v, want a structural defect", obs.Defects)
	}
}

// TestObservationRepairsItselfWhenTheRuntimeIsFixed: after the mismatch is
// corrected on disk, the SAME observation pass reports clean. That is what
// makes the verdict evidence about the current runtime rather than a one-time
// claim.
func TestObservationRepairsItselfWhenTheRuntimeIsFixed(t *testing.T) {
	root := writeTree(t, map[string]string{
		"index.html": "<!DOCTYPE html><html><head><link rel=\"stylesheet\" href=\"style.css\"></head><body></body></html>",
		"styles.css": "body{}",
	})
	r := NewRunner(root)
	r.SetGrant(fullGrant())
	profile, _, err := r.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	handle, _, err := r.Serve(context.Background(), profile)
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	defer func() { _ = handle.Close() }()

	if obs, _ := r.Inspect(context.Background(), handle.BaseURL, "/index.html"); obs.Clean() {
		t.Fatal("precondition: the broken workspace must not observe clean")
	}

	// The repair: point the reference at the file that actually exists.
	abs := filepath.Join(root, "index.html")
	data, err := os.ReadFile(abs)
	if err != nil {
		t.Fatal(err)
	}
	fixed := strings.Replace(string(data), `href="style.css"`, `href="styles.css"`, 1)
	if err := os.WriteFile(abs, []byte(fixed), 0o644); err != nil {
		t.Fatal(err)
	}

	obs, err := r.Inspect(context.Background(), handle.BaseURL, "/index.html")
	if err != nil {
		t.Fatalf("Inspect after repair: %v", err)
	}
	if !obs.Clean() {
		t.Fatalf("post-repair verdict = %s defects = %s", obs.Verdict, obs.DefectLine())
	}
}

// TestInspectBlocksWithoutNetworkGrant: observation is separately authorized.
func TestInspectBlocksWithoutNetworkGrant(t *testing.T) {
	r := NewRunner(t.TempDir())
	r.SetGrant(Grant{Provenance: "/ask", Discover: true, Read: true})
	obs, err := r.Inspect(context.Background(), "http://127.0.0.1:1", "/index.html")
	if err == nil {
		t.Fatal("Inspect must be refused without a network grant")
	}
	if obs.Verdict != VerdictBlocked || obs.Block == nil {
		t.Fatalf("observation = %+v, want a blocked verdict carrying its block", obs)
	}
	if obs.Block.Class != FailureAuthorizationBlocked {
		t.Fatalf("block class = %s, want %s", obs.Block.Class, FailureAuthorizationBlocked)
	}
}

// TestUnreachableRuntimeIsObservationFailed: a runtime that never answers is
// OBSERVATION_FAILED, not a fabricated pass and not a hang.
func TestUnreachableRuntimeIsObservationFailed(t *testing.T) {
	r := NewRunner(t.TempDir())
	r.SetGrant(fullGrant())
	// 127.0.0.1:1 is reserved and never listening.
	obs, err := r.Inspect(context.Background(), "http://127.0.0.1:1", "/")
	if err == nil {
		t.Fatal("Inspect against a dead endpoint must fail")
	}
	if obs.Verdict != VerdictBlocked {
		t.Fatalf("verdict = %s, want BLOCKED", obs.Verdict)
	}
	if obs.Block == nil || obs.Block.Class != FailureObservationFailed {
		t.Fatalf("block = %+v, want %s", obs.Block, FailureObservationFailed)
	}
}

// TestCrossOriginReferencesAreNotProbed: a workspace-local objective does not
// silently become an internet request.
func TestCrossOriginReferencesAreNotProbed(t *testing.T) {
	root := writeTree(t, map[string]string{
		"index.html": "<html><head><link rel=\"stylesheet\" href=\"https://cdn.example.com/x.css\"></head><body></body></html>",
	})
	r := NewRunner(root)
	r.SetGrant(fullGrant())
	profile, _, err := r.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	handle, _, err := r.Serve(context.Background(), profile)
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	defer func() { _ = handle.Close() }()

	obs, err := r.Inspect(context.Background(), handle.BaseURL, "/index.html")
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if !obs.Clean() {
		t.Fatalf("an unprobed cross-origin reference must not fail the observation: %s", obs.DefectLine())
	}
	if len(obs.Resources) != 1 || obs.Resources[0].Local {
		t.Fatalf("resources = %+v, want one recorded non-local reference", obs.Resources)
	}
}

// TestStaticServerContainsTraversal: the served surface cannot escape its root.
func TestStaticServerContainsTraversal(t *testing.T) {
	root := writeTree(t, map[string]string{
		"index.html":  "<html><body>x</body></html>",
		"secret.html": "SECRET",
	})
	inner := filepath.Join(root, "public")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inner, "index.html"), []byte("<html><body>inner</body></html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := NewRunner(root)
	r.SetGrant(fullGrant())
	handle, _, err := r.ServeDir(context.Background(), "public")
	if err != nil {
		t.Fatalf("ServePath: %v", err)
	}
	defer func() { _ = handle.Close() }()

	res, _, err := r.Fetch(context.Background(), handle.BaseURL+"/../secret.html")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if res.OK() || res.Status != 404 {
		t.Fatalf("traversal answered HTTP %d, want 404 (the served root is a boundary)", res.Status)
	}
}

// ── command capability ──────────────────────────────────────────────────────

// TestCommandWithoutRunnerIsCapabilityMissing: no unauthorized spawn fallback.
func TestCommandWithoutRunnerIsCapabilityMissing(t *testing.T) {
	r := NewRunner(t.TempDir())
	r.SetGrant(fullGrant())
	_, ev, err := r.Command(context.Background(), CommandRequest{Command: "echo hi"})
	if err == nil {
		t.Fatal("Command must not succeed without an authorized runner")
	}
	if !IsCapabilityMissing(err) {
		t.Fatalf("err = %v, want a missing-capability failure", err)
	}
	if ev.Class != FailureCapabilityMissing {
		t.Fatalf("class = %s, want %s", ev.Class, FailureCapabilityMissing)
	}
}

type stubCommandRunner struct {
	res CommandResult
	err error
	got string
}

func (s *stubCommandRunner) RunCommand(_ context.Context, command string) (CommandResult, error) {
	s.got = command
	return s.res, s.err
}

func TestCommandReportsRealExitCode(t *testing.T) {
	stub := &stubCommandRunner{res: CommandResult{Command: "false", ExitCode: 1, Stderr: "boom"}}
	r := NewRunner(t.TempDir(), WithCommandRunner(stub))
	r.SetGrant(fullGrant())
	res, ev, err := r.Command(context.Background(), CommandRequest{Command: "false"})
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	if res.ExitCode != 1 || ev.OK {
		t.Fatalf("a non-zero exit must be a failed observation, not a capability error: %+v", ev)
	}
	if ev.Class != FailureExecutionFailed {
		t.Fatalf("class = %s, want %s", ev.Class, FailureExecutionFailed)
	}
	if ev.Field("exit_code") != "1" {
		t.Fatalf("evidence must carry the real exit code, got %q", ev.Field("exit_code"))
	}
	if stub.got != "false" {
		t.Fatalf("runner received %q, want the verbatim command", stub.got)
	}
}

// ── reference extraction + structure primitives ─────────────────────────────

func TestExtractSubresourceRefsIsDeterministic(t *testing.T) {
	body := `<link rel="stylesheet" href="a.css"><script src='b.js'></script><img src="a.css"><a href="#frag">x</a>`
	first := extractSubresourceRefs(body)
	second := extractSubresourceRefs(body)
	if len(first) != 2 {
		t.Fatalf("refs = %v, want the two distinct local resources", first)
	}
	if strings.Join(first, ",") != strings.Join(second, ",") {
		t.Fatal("reference extraction must be deterministic")
	}
	for _, ref := range first {
		if ref == "#frag" {
			t.Fatal("a pure fragment is not a subresource")
		}
	}
}

func TestAuditDocumentStructure(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantNil bool
		detail  string
	}{
		{"well formed", "<html><body><p>x</p></body></html>", true, ""},
		{"void elements", "<html><head><meta charset=\"utf-8\"><link rel=\"stylesheet\" href=\"a.css\"></head><body><img src=\"a.png\"><br></body></html>", true, ""},
		{"optional end tags", "<ul><li>a<li>b</ul>", true, ""},
		{"optional end tag closed explicitly", "<ul><li>a</li><li>b</li></ul>", true, ""},
		{"unclosed", "<html><body><main><p>x</p></body></html>", false, "main"},
		{"mismatched", "<html><body><div><span>x</div></body></html>", false, "span"},
		{"stray close", "<html><body></span></body></html>", false, "span"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fault := auditDocumentStructure(tc.body)
			if tc.wantNil {
				if fault != nil {
					t.Fatalf("fault = %+v, want nil", fault)
				}
				return
			}
			if fault == nil {
				t.Fatal("expected a structural fault")
			}
			if tc.detail != "" && !strings.Contains(fault.detail, tc.detail) {
				t.Fatalf("fault %q must name %q", fault.detail, tc.detail)
			}
		})
	}
}

func TestBasenameSimilarIsEvidenceShaped(t *testing.T) {
	if !basenameSimilar("style.css", "styles.css") {
		t.Fatal("a singular/plural mismatch is the exact candidate shape this must catch")
	}
	if basenameSimilar("index.html", "script.js") {
		t.Fatal("unrelated names must not be proposed as candidates")
	}
	if !basenameSimilar("app.js", "app.js") {
		t.Fatal("an identical name must match itself")
	}
}

func TestSortEvidenceIsStable(t *testing.T) {
	in := []Evidence{
		{ID: "b", Capability: RuntimeInspect},
		{ID: "a", Capability: RuntimeFetch},
		{ID: "c", Capability: RuntimeFetch},
	}
	sortEvidence(in)
	got := in[0].ID + in[1].ID + in[2].ID
	if got != "acb" {
		t.Fatalf("evidence order = %q, want %q (capability then id)", got, "acb")
	}
}
