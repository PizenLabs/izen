package autonomy

// ── RENDERED-LAYOUT VERIFICATION ────────────────────────────────────────────
//
// TestRenderedVerifier_RealBrowser is a REAL external integration test: it
// drives the local headless browser against a fixture and checks the RENDERED
// DOM (after the script executed). It is skipped when no browser is present or
// under -short; the blocked path is always tested.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRenderedVerifier_NoBrowserIsBlocked(t *testing.T) {
	v := &HeadlessBrowserVerifier{BrowserPath: ""}
	res := v.Verify(context.Background(), RenderedVerificationRequest{
		Root:  t.TempDir(),
		Entry: "index.html",
		Checks: []RenderedCheck{
			{ID: "c1", Kind: RenderedDOMContains, Value: "x"},
		},
	})
	if res.Ran {
		t.Fatal("a verifier with no browser reported that it ran")
	}
	if res.Blocked == "" {
		t.Fatal("a verifier with no browser must report a blocker")
	}
	if res.Passed {
		t.Fatal("a blocked verifier must not report a pass")
	}
}

func TestRenderedVerifier_MissingEntryIsBlocked(t *testing.T) {
	root := t.TempDir()
	v := &HeadlessBrowserVerifier{BrowserPath: DetectBrowser()}
	res := v.Verify(context.Background(), RenderedVerificationRequest{
		Root:  root,
		Entry: "does-not-exist.html",
	})
	if res.Ran || res.Passed || res.Blocked == "" {
		t.Fatalf("a missing entry must be blocked: %+v", res)
	}
}

// TestRenderedVerifier_RealBrowser renders a fixture whose visible text is
// produced by JavaScript. A pass therefore proves the browser EXECUTED the page,
// not merely that the source contained the string.
func TestRenderedVerifier_RealBrowser(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real browser integration in -short mode")
	}
	browser := DetectBrowser()
	if browser == "" {
		t.Skip("no local browser executable available; rendered verification is UNVERIFIED in this environment")
	}

	root := t.TempDir()
	const page = `<!DOCTYPE html>
<html><body>
<div id="out">initial</div>
<script>document.getElementById('out').textContent = 'rendered-by-script';</script>
</body></html>`
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte(page), 0o644); err != nil {
		t.Fatal(err)
	}

	shotDir := t.TempDir()
	v := &HeadlessBrowserVerifier{BrowserPath: browser, ScreenshotDir: shotDir}
	res := v.Verify(context.Background(), RenderedVerificationRequest{
		Root:    root,
		Entry:   "index.html",
		Targets: []string{"index.html"},
		Timeout: 60 * time.Second,
		Checks: []RenderedCheck{
			{ID: "script-ran", Kind: RenderedDOMContains, Value: "rendered-by-script", Description: "the script's rendered text is present"},
			{ID: "initial-gone", Kind: RenderedDOMNotContains, Value: "<div id=\"out\">initial</div>", Description: "the pre-script DOM was replaced"},
		},
	})
	if res.Blocked != "" {
		t.Skipf("browser could not run in this environment: %s", res.Blocked)
	}
	if !res.Ran {
		t.Fatalf("real browser run did not report Ran: %+v", res)
	}
	if !res.Passed {
		t.Fatalf("real rendered check failed: defects=%v observations=%v", res.Defects, res.Observations)
	}
	if res.ArtifactRevision == "" {
		t.Fatal("rendered verification did not record the inspected artifact revision")
	}
	if res.Browser == "" {
		t.Fatal("rendered verification did not record the browser identity")
	}
	if !strings.Contains(strings.Join(res.Observations, "\n"), "script-ran") {
		t.Fatalf("observations do not name the satisfied check: %v", res.Observations)
	}
	t.Logf("rendered verification: browser=%q revision=%s observations=%v screenshot=%q",
		res.Browser, res.ArtifactRevision, res.Observations, res.Screenshot)
}

// TestRenderedRevision_TracksArtifactChange pins that the inspected revision is
// bound to the served bytes: a change to a served target changes the revision.
func TestRenderedRevision_TracksArtifactChange(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	first := renderedRevision(root, []string{"a.txt"})
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("two"), 0o644); err != nil {
		t.Fatal(err)
	}
	second := renderedRevision(root, []string{"a.txt"})
	if first == second {
		t.Fatal("the artifact revision did not change when the artifact changed")
	}
}
