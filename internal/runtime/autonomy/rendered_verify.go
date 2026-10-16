package autonomy

// ── RENDERED-LAYOUT VERIFICATION ────────────────────────────────────────────
//
// A source-level patch is not proof that a composed page renders. This file is
// the smallest maintainable adapter that turns the LOCAL environment's real
// browser into attributable evidence for the completion authority.
//
// It is deliberately honest about what it proves:
//
//   - It runs a REAL browser against the REAL workspace over a local HTTP
//     server and captures the RENDERED DOM (after scripts executed) plus a
//     screenshot artifact.
//   - It records WHICH artifact revision was inspected (a digest over the
//     served targets) and WHICH browser performed the inspection.
//   - Its acceptance checks are DETERMINISTIC queries over the rendered DOM
//     (contains / not-contains). It never claims a human-style visual
//     judgement from the mere existence of a screenshot.
//   - When no browser is available it returns a BLOCKED result and the
//     completion path stays UNVERIFIED. It never fabricates a pass.
//
// The verifier is a CONSUMER of the runtime, like the behavioural stage: it
// mutates nothing and grants nothing.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/PizenLabs/izen/internal/execution"
)

// RenderedVerification is the attributable result of one rendered check.
type RenderedVerification struct {
	// Ran reports that a real browser rendered the workspace.
	Ran bool
	// Passed reports that every acceptance check held in the rendered output.
	Passed bool
	// Browser names the browser and version that performed the rendering.
	Browser string
	// ArtifactRevision is the digest of the served target set at inspection.
	ArtifactRevision string
	// Entry is the entry document that was rendered.
	Entry string
	// Observations are the deterministic facts observed in the rendered DOM.
	Observations []string
	// Defects are the acceptance checks that failed.
	Defects []string
	// Screenshot is the path of the captured screenshot artifact (may be "").
	Screenshot string
	// Blocked explains why verification could not run (no browser, no entry).
	Blocked string
}

// Evidence projects the result onto the completion authority's evidence shape.
func (r RenderedVerification) Evidence() execution.RenderedVerificationEvidence {
	return execution.RenderedVerificationEvidence{
		Ran:           r.Ran,
		Passed:        r.Passed,
		Revision:      r.ArtifactRevision,
		Browser:       r.Browser,
		Observations:  append([]string(nil), r.Observations...),
		Defects:       append([]string(nil), r.Defects...),
		BlockedReason: r.Blocked,
	}
}

// RenderedCheckKind is the closed vocabulary of rendered acceptance checks.
type RenderedCheckKind string

const (
	// RenderedDOMContains requires the rendered DOM to contain a value.
	RenderedDOMContains RenderedCheckKind = "dom_contains"
	// RenderedDOMNotContains requires the rendered DOM to NOT contain a value.
	RenderedDOMNotContains RenderedCheckKind = "dom_not_contains"
)

// RenderedCheck is one deterministic acceptance check over the rendered DOM.
type RenderedCheck struct {
	ID          string
	Kind        RenderedCheckKind
	Value       string
	Description string
}

// RenderedVerificationRequest is the bounded input of one rendered check.
type RenderedVerificationRequest struct {
	// Root is the workspace root to serve.
	Root string
	// Entry is the workspace-relative entry document to render.
	Entry string
	// Checks are the deterministic acceptance checks.
	Checks []RenderedCheck
	// Targets are the served artifact set; their digest is the inspected
	// revision.
	Targets []string
	// Timeout bounds the browser run.
	Timeout time.Duration
}

// RenderedVerifier renders a workspace and evaluates deterministic checks.
type RenderedVerifier interface {
	Verify(ctx context.Context, req RenderedVerificationRequest) RenderedVerification
}

// WithRenderedVerifier binds the rendered verifier. Passing nil disables
// rendered verification: a run that does not require it is unaffected.
func WithRenderedVerifier(v RenderedVerifier) Option {
	return func(d *Driver) {
		if d != nil {
			d.renderedVerifier = v
		}
	}
}

// WithRenderedChecks binds the deterministic acceptance checks a rendered run
// evaluates, together with the entry document to render. It is required for a
// proposal whose accepted strategy demands rendered verification.
func WithRenderedChecks(entry string, checks []RenderedCheck) Option {
	return func(d *Driver) {
		if d == nil {
			return
		}
		d.renderedEntry = entry
		d.renderedChecks = append([]RenderedCheck(nil), checks...)
	}
}

// HeadlessBrowserVerifier drives a local headless browser. It is intentionally
// minimal: one process per verification, no automation framework, no network.
type HeadlessBrowserVerifier struct {
	// BrowserPath is the browser executable. Empty disables the verifier.
	BrowserPath string
	// ScreenshotDir is where the screenshot artifact is written. Empty uses a
	// temporary directory.
	ScreenshotDir string
}

// DetectBrowser returns the first known local browser executable, or "".
func DetectBrowser() string {
	if p := strings.TrimSpace(os.Getenv("IZEN_BROWSER")); p != "" {
		return p
	}
	for _, candidate := range []string{
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/Applications/Chromium.app/Contents/MacOS/Chromium",
		"/usr/bin/google-chrome",
		"/usr/bin/chromium",
		"/usr/bin/chromium-browser",
	} {
		if p, err := exec.LookPath(candidate); err == nil {
			return p
		}
	}
	if p, err := exec.LookPath("google-chrome"); err == nil {
		return p
	}
	if p, err := exec.LookPath("chromium"); err == nil {
		return p
	}
	return ""
}

// Verify renders the workspace and evaluates the checks. It always returns a
// truthful result: no browser, no entry, or a browser failure yields a BLOCKED
// result, never a pass.
func (v *HeadlessBrowserVerifier) Verify(ctx context.Context, req RenderedVerificationRequest) RenderedVerification {
	blocked := func(reason string) RenderedVerification {
		return RenderedVerification{Blocked: reason}
	}
	if v == nil || strings.TrimSpace(v.BrowserPath) == "" {
		return blocked("no local browser executable is available; rendered verification cannot run")
	}
	root := strings.TrimSpace(req.Root)
	if root == "" {
		return blocked("no workspace root to serve; rendered verification cannot run")
	}
	entry := strings.TrimSpace(req.Entry)
	if entry == "" {
		return blocked("no entry document was determined; rendered verification cannot run")
	}
	entryAbs := filepath.Join(root, filepath.FromSlash(entry))
	if _, err := os.ReadFile(entryAbs); err != nil {
		return blocked("entry document " + entry + " does not exist in the workspace")
	}

	timeout := req.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Serve the workspace on an ephemeral loopback port. The server is bound to
	// 127.0.0.1 only and torn down when Verify returns.
	server := &http.Server{Handler: http.FileServer(http.Dir(root))} //nolint:gosec // serving the local workspace, read-only
	var lc net.ListenConfig
	ln, err := lc.Listen(runCtx, "tcp", "127.0.0.1:0")
	if err != nil {
		return blocked("could not bind a local serving port: " + err.Error())
	}
	defer func() { _ = server.Close() }()
	go func() { _ = server.Serve(ln) }()
	url := "http://" + ln.Addr().String() + "/" + strings.TrimPrefix(filepath.ToSlash(entry), "/")

	// Capture the rendered DOM (after scripts executed).
	domCmd := exec.CommandContext(runCtx, v.BrowserPath,
		"--headless=new", "--disable-gpu", "--no-sandbox", "--disable-dev-shm-usage",
		"--virtual-time-budget=3000", "--dump-dom", url)
	var stdout, stderr bytes.Buffer
	domCmd.Stdout = &stdout
	domCmd.Stderr = &stderr
	if err := domCmd.Run(); err != nil {
		return blocked("browser rendering failed: " + firstLine(stderr.String(), err.Error()))
	}
	dom := stdout.String()
	if strings.TrimSpace(dom) == "" {
		return blocked("browser produced no rendered DOM")
	}

	// Best-effort screenshot artifact. Its existence is RECORDED, never used as
	// proof of visual correctness. The directory is supplied by the caller; the
	// verifier never creates or removes workspace paths of its own.
	screenshot := ""
	if dir := strings.TrimSpace(v.ScreenshotDir); dir != "" {
		shotPath := filepath.Join(dir, "rendered.png")
		shotCmd := exec.CommandContext(runCtx, v.BrowserPath,
			"--headless=new", "--disable-gpu", "--no-sandbox", "--disable-dev-shm-usage",
			"--virtual-time-budget=3000", "--window-size=1280,800",
			"--screenshot="+shotPath, url)
		var shotErr bytes.Buffer
		shotCmd.Stderr = &shotErr
		if err := shotCmd.Run(); err == nil {
			if data, readErr := os.ReadFile(shotPath); readErr == nil && len(data) > 0 {
				screenshot = shotPath
			}
		}
	}

	rev := renderedRevision(root, req.Targets)
	result := RenderedVerification{
		Ran:              true,
		Browser:          browserVersion(runCtx, v.BrowserPath),
		ArtifactRevision: rev,
		Entry:            entry,
		Screenshot:       screenshot,
	}
	observations := []string{
		fmt.Sprintf("rendered %s", entry),
		fmt.Sprintf("artifact_revision=%s", rev),
	}
	passed := true
	for _, check := range req.Checks {
		contains := strings.Contains(dom, check.Value)
		ok := contains
		if check.Kind == RenderedDOMNotContains {
			ok = !contains
		}
		if ok {
			observations = append(observations, "check "+check.ID+": satisfied")
		} else {
			passed = false
			desc := check.Description
			if desc == "" {
				desc = string(check.Kind) + " " + check.Value
			}
			result.Defects = append(result.Defects, "check "+check.ID+" failed: "+desc)
		}
	}
	if len(req.Checks) == 0 {
		// No acceptance checks means nothing was established about the output.
		// Reporting a pass here would be exactly the "screenshot == inspected"
		// error this adapter refuses.
		passed = false
		result.Defects = append(result.Defects, "no rendered acceptance checks were supplied; nothing was established")
	}
	result.Passed = passed
	result.Observations = observations
	return result
}

// renderedRevision digests the served target set so the inspection is bound to
// a concrete artifact revision.
func renderedRevision(root string, targets []string) string {
	h := sha256.New()
	ordered := append([]string(nil), targets...)
	sort.Strings(ordered)
	for _, t := range ordered {
		_, _ = fmt.Fprintf(h, "%s\x00", t)
		if data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(t))); err == nil {
			sum := sha256.Sum256(data)
			_, _ = fmt.Fprintf(h, "%s", hex.EncodeToString(sum[:]))
		}
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// browserVersion returns a stable, bounded browser identity.
func browserVersion(ctx context.Context, path string) string {
	versionCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(versionCtx, path, "--version").Output()
	if err != nil {
		return filepath.Base(path)
	}
	return strings.TrimSpace(string(out))
}

func firstLine(s, fallback string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return fallback
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 240 {
		s = s[:240]
	}
	return s
}

// ensureRenderedVerification runs the rendered verification at most once per
// run, only when an accepted proposal demanded it and the surface is authorized
// to observe a running workspace.
func (d *Driver) ensureRenderedVerification(ctx context.Context) {
	if d == nil || d.renderedChecked {
		return
	}
	if !d.renderedRequired {
		// OPT-IN POLICY VISIBILITY. Rendered verification is enabled by policy
		// (in production, the IZEN_RENDERED_VERIFY environment variable). When a
		// broad, project-level objective runs WITHOUT it, the objective's
		// acceptance criteria stay UNVERIFIED — a fact the trace must state, not
		// leave to inference. Emitted once per run.
		if !d.renderedPolicyNoted && d.isProjectLevelObjective() {
			d.renderedPolicyNoted = true
			if d.bus != nil {
				d.bus.Publish(eventsActivity(
					"[render] SKIPPED: rendered verification is opt-in and not enabled for this run " +
						"(IZEN_RENDERED_VERIFY=1 plus IZEN_RENDERED_ENTRY/IZEN_RENDERED_CONTAINS); " +
						"the broad objective's acceptance criteria remain UNVERIFIED"))
			}
		}
		return
	}
	d.renderedChecked = true
	if d.renderedVerifier == nil {
		d.renderedEvidence = RenderedVerification{Blocked: "rendered verification is required but no verifier is bound"}
		d.publishRenderedSummary()
		return
	}
	// $hot is a strict local target boundary; it never gains a browser/network
	// capability through a proposal. Rendered verification is refused, not
	// silently skipped as a pass.
	if IsHot(d.scope) {
		d.renderedEvidence = RenderedVerification{Blocked: "rendered verification is not authorized under the strict local $hot boundary"}
		d.publishRenderedSummary()
		return
	}
	root := ""
	if d.adapter != nil {
		root = d.adapter.Root()
	}
	d.renderedEvidence = d.renderedVerifier.Verify(ctx, RenderedVerificationRequest{
		Root:    root,
		Entry:   d.renderedEntry,
		Checks:  append([]RenderedCheck(nil), d.renderedChecks...),
		Targets: d.objectiveTargets(),
		Timeout: 45 * time.Second,
	})
	d.publishRenderedSummary()
}

// publishRenderedSummary emits the rendered verification outcome on the trace.
// It is called on EVERY terminal path of ensureRenderedVerification — including
// the blocked ones — so a required-but-unrun check is visible rather than
// silent. A skipped check that leaves no line is indistinguishable from a check
// that was never requested.
func (d *Driver) publishRenderedSummary() {
	if d == nil || d.bus == nil {
		return
	}
	d.bus.Publish(eventsActivity("[render] " + d.renderedEvidence.summary()))
}

// summary renders a bounded one-line result for telemetry.
func (r RenderedVerification) summary() string {
	if r.Blocked != "" {
		return "BLOCKED: " + r.Blocked
	}
	state := "UNPROVEN"
	if r.Passed {
		state = "PROVEN"
	}
	return fmt.Sprintf("%s browser=%q revision=%s entry=%s observations=%d defects=%d",
		state, r.Browser, r.ArtifactRevision, r.Entry, len(r.Observations), len(r.Defects))
}
