package autonomy

// ── ACCEPTANCE: THE CANONICAL $prompt PORTFOLIO SCENARIO ─────────────────────
//
// One objective, verbatim from the report:
//
//	$prompt please review this project and redesign a professional personal
//	portfolio page for me using HTML, CSS, and JS; the author's name is
//	TomHunter, an AI Engineer.
//
// against the real test workspace, driving the REAL Driver → ExecutorAdapter →
// RuntimeExecutor path with a scripted provider. Nothing here is mocked except
// the provider's response text; the filesystem, the discovery scan, the target
// resolution, the mutation boundary, the approval gate and the verifier are all
// production code on a real directory.
//
// The lifecycle this test pins:
//
//	$prompt → authorization → workspace discovery → file observation
//	→ bounded context → LLM computation → proposal → authorized mutation
//	→ real filesystem change → verification → PROVEN
//
// It asserts MEASURED EVIDENCE, not absence-of-crash: the number of files
// discovery observed, that the model actually received project bytes, that a
// MutationSet was built and applied, that disk bytes changed, and that the
// objective state reflects the mutation rather than the model's say-so.

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
)

const acceptanceObjective = "please review this project and redesign a professional personal portfolio page " +
	"for me using HTML, CSS, and JS; the author's name is TomHunter, an AI Engineer."

// portfolioWorkspace builds a realistic small web project. It deliberately
// contains a readme.md and a nested asset, so a derivation that simply returned
// every file in the directory — or that matched the first three files it saw —
// would be caught by the assertions below.
func portfolioWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"index.html":    "<!DOCTYPE html>\n<html><body>\n  <h1>Placeholder</h1>\n</body></html>\n",
		"styles.css":    ":root { --fg: #111 }\nbody { margin: 0 }\n",
		"script.js":     "console.log('placeholder');\n",
		"readme.md":     "# Portfolio\n\nPlaceholder readme.\n",
		"docs/notes.md": "internal notes, not part of the page\n",
	}
	for name, body := range files {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// ambiguousPortfolioWorkspace is the multi-candidate web workspace: three html
// files and three stylesheets.
//
// It exists to pin the difference the audit found between "the user named the
// files" and "the user named a KIND and discovery happened to find several". It
// is deliberately hostile to every shortcut that used to pass: there is no
// singular "the HTML file" to fall back on, no index.html to prefer, and no
// subset that is obviously what a human meant.
func ambiguousPortfolioWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"a.html":     "<!DOCTYPE html>\n<html><body>\n  <h1>Alpha</h1>\n</body></html>\n",
		"b.html":     "<!DOCTYPE html>\n<html><body>\n  <h1>Bravo</h1>\n</body></html>\n",
		"index.html": "<!DOCTYPE html>\n<html><body>\n  <h1>Index</h1>\n</body></html>\n",
		"a.css":      ".alpha { color: red }\n",
		"b.css":      ".bravo { color: blue }\n",
		"styles.css": ":root { --fg: #111 }\n",
		"script.js":  "console.log('placeholder');\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// ambiguousPortfolioArtifacts scripts the artifact each file would receive IF it
// were ever dispatched. It is intentionally complete: the point of the ambiguous
// case is that no invocation happens at all, so a provider that answers is a
// failure, not a convenient default.
var ambiguousPortfolioArtifacts = map[string]scriptResponse{
	"a.html":     {Response: ai.Response{Content: "<<<<<<< SEARCH\n  <h1>Alpha</h1>\n=======\n  <h1>Rewritten</h1>\n>>>>>>> REPLACE"}, marker: "<h1>Alpha</h1>"},
	"b.html":     {Response: ai.Response{Content: "<<<<<<< SEARCH\n  <h1>Bravo</h1>\n=======\n  <h1>Rewritten</h1>\n>>>>>>> REPLACE"}, marker: "<h1>Bravo</h1>"},
	"index.html": {Response: ai.Response{Content: "<<<<<<< SEARCH\n  <h1>Index</h1>\n=======\n  <h1>Rewritten</h1>\n>>>>>>> REPLACE"}, marker: "<h1>Index</h1>"},
	"a.css":      {Response: ai.Response{Content: "<<<<<<< SEARCH\n.alpha { color: red }\n=======\n.alpha { color: green }\n>>>>>>> REPLACE"}, marker: ".alpha { color: red }"},
	"b.css":      {Response: ai.Response{Content: "<<<<<<< SEARCH\n.bravo { color: blue }\n=======\n.bravo { color: green }\n>>>>>>> REPLACE"}, marker: ".bravo { color: blue }"},
	"styles.css": {Response: ai.Response{Content: "<<<<<<< SEARCH\n:root { --fg: #111 }\n=======\n:root { --fg: #222 }\n>>>>>>> REPLACE"}, marker: ":root { --fg: #111 }"},
	"script.js":  {Response: ai.Response{Content: "<<<<<<< SEARCH\nconsole.log('placeholder');\n=======\nconsole.log('rewritten');\n>>>>>>> REPLACE"}, marker: "console.log('placeholder');"},
}

// ambiguityProbeProvider records every invocation and answers from the scripted
// artifact for whichever file it was shown. Its call count is the measurement
// the ambiguous case turns on: "no provider calls" must be an observed fact, not
// an inference from a zero filesystem delta.
type ambiguityProbeProvider struct {
	mu    sync.Mutex
	calls int
	seen  []string
}

func (p *ambiguityProbeProvider) Name() string { return "ambiguity-counting" }

func (p *ambiguityProbeProvider) Execute(_ context.Context, req ai.Request) (*ai.Response, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	target := targetOf(req)
	p.seen = append(p.seen, target)
	artifact, ok := ambiguousPortfolioArtifacts[target]
	if !ok {
		return nil, fmt.Errorf("invocation #%d targeted %q, which the ambiguity scenario never describes", p.calls, target)
	}
	resp := artifact.Response
	return &resp, nil
}

func (p *ambiguityProbeProvider) ExecuteStream(context.Context, ai.Request) (io.ReadCloser, error) {
	return nil, fmt.Errorf("stream not supported in ambiguityProbeProvider")
}

// Calls returns the observed provider invocation count.
func (p *ambiguityProbeProvider) Calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// portfolioArtifacts maps each bound target to the SEARCH/REPLACE artifact a
// model working from that file's real bytes would produce.
//
// The provider is keyed by TARGET, not by call order. That matters: the runtime
// dispatches targets in sorted order, so an order-keyed script would silently
// hand the CSS patch to the JavaScript invocation and the test would measure a
// mis-wiring rather than the runtime. Keying by target also proves the model is
// genuinely reasoning over the file it was shown.
var portfolioArtifacts = map[string]scriptResponse{
	"index.html": {
		Response: ai.Response{
			Content: "<<<<<<< SEARCH\n  <h1>Placeholder</h1>\n=======\n" +
				"  <h1>TomHunter</h1>\n  <p class=\"role\">AI Engineer</p>\n>>>>>>> REPLACE",
			Usage: ai.ProviderUsage{Known: true, PromptTokens: 800, CompletionTokens: 120, FinishReason: "stop"},
		},
		marker: "<h1>Placeholder</h1>",
	},
	"styles.css": {
		Response: ai.Response{
			Content: "<<<<<<< SEARCH\nbody { margin: 0 }\n=======\n" +
				"body { margin: 0; font-family: system-ui }\n>>>>>>> REPLACE",
			Usage: ai.ProviderUsage{Known: true, PromptTokens: 600, CompletionTokens: 90, FinishReason: "stop"},
		},
		marker: "body { margin: 0 }",
	},
	"script.js": {
		Response: ai.Response{
			Content: "<<<<<<< SEARCH\nconsole.log('placeholder');\n=======\n" +
				"console.log('TomHunter portfolio');\n>>>>>>> REPLACE",
			Usage: ai.ProviderUsage{Known: true, PromptTokens: 600, CompletionTokens: 90, FinishReason: "stop"},
		},
		marker: "console.log('placeholder');",
	},
}

// portfolioProvider is a scripted provider that answers each invocation from the
// target the runtime actually put in front of the model. An invocation naming a
// target with no scripted artifact is a test failure, not a silent default: it
// means the runtime dispatched a file the acceptance scenario never described.
type portfolioProvider struct {
	mu       sync.Mutex
	requests []ai.Request
	served   map[string]int
	calls    int
}

func (p *portfolioProvider) Name() string { return "portfolio-scripted" }

func (p *portfolioProvider) Execute(_ context.Context, req ai.Request) (*ai.Response, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	p.requests = append(p.requests, req)
	target := targetOf(req)
	if target == "" {
		return nil, fmt.Errorf("invocation #%d carried no target; the runtime dispatched an unidentified file", p.calls)
	}
	artifact, ok := portfolioArtifacts[target]
	if !ok {
		return nil, fmt.Errorf("invocation #%d targeted %q, which the acceptance scenario does not describe", p.calls, target)
	}
	p.served[target]++
	resp := artifact.Response
	return &resp, nil
}

func (p *portfolioProvider) ExecuteStream(context.Context, ai.Request) (io.ReadCloser, error) {
	return nil, fmt.Errorf("stream not supported in portfolioProvider")
}

// targetOf identifies which bound file the runtime dispatched.
//
// It first reads the explicit target the prompt names, then falls back to the
// file whose own content appears in the prompt. The fallback exists because the
// runtime does not render a target header identically across lanes — a
// full-artifact turn and a bounded-patch continuation phrase it differently —
// and a script provider that only understood one phrasing would measure the
// prompt's typography instead of the runtime's behaviour.
//
// Either way the provider answers per FILE, so a mis-dispatch (the CSS patch
// being handed to the JavaScript invocation) fails loudly rather than silently
// mutating the wrong file.
func targetOf(req ai.Request) string {
	var blob strings.Builder
	for _, msg := range req.Messages {
		blob.WriteString(msg.Content)
		blob.WriteByte('\n')
	}
	text := blob.String()
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "### CONTEXT WINDOW — ") ||
			strings.HasPrefix(line, "### TARGET FILE: ") {
			rest := line[strings.Index(line, "— ")+len("— "):]
			if fields := strings.Fields(rest); len(fields) > 0 {
				return fields[0]
			}
		}
	}
	// Content fingerprint, longest match first so a file whose body contains
	// another file's marker cannot shadow it.
	best, bestLen := "", 0
	for target, artifact := range portfolioArtifacts {
		if marker := artifact.SearchMarker(); marker != "" &&
			strings.Contains(text, marker) && len(marker) > bestLen {
			best, bestLen = target, len(marker)
		}
	}
	return best
}

func TestAcceptance_PortfolioRedesign_ExecutesRealWork(t *testing.T) {
	root := portfolioWorkspace(t)

	before := map[string][]byte{}
	for _, f := range []string{"index.html", "styles.css", "script.js", "readme.md"} {
		b, err := os.ReadFile(filepath.Join(root, f))
		if err != nil {
			t.Fatal(err)
		}
		before[f] = b
	}

	// The bus publishes from whichever goroutine the runtime stage runs on, so the
	// collector must be synchronized. Note that this stream is DELIBERATELY lossy
	// (Bus.Publish drops on a full subscriber), so it is used only for
	// ordering-independent spot checks; the authoritative assertions read the
	// sealed execution evidence instead.
	bus := events.NewBus(events.DefaultBufferSize)
	var eventMu sync.Mutex
	var dispatchedEvents []string
	bus.SubscribeAll(func(ev events.DomainEvent) {
		eventMu.Lock()
		defer eventMu.Unlock()
		dispatchedEvents = append(dispatchedEvents, string(ev.Type()))
	})

	mock := &portfolioProvider{served: map[string]int{}}
	x := testExecutor(t, root, mock, bus)
	adapter := NewExecutorAdapter(root, execution.NewIntentGateway(root), x)
	d := NewDriver(adapter, bus)

	// The run is expected to PARK at the human approval gate. Authorization is
	// a human fact: the runtime may stage a real candidate but may never assume
	// the grant. A nil termination with a non-nil approval boundary is the
	// CORRECT shape here, and the test asserts it rather than tolerating both.
	term, err := d.Run(context.Background(), acceptanceObjective)
	if err != nil {
		t.Fatalf("the canonical $prompt path must not error: %v", err)
	}
	if term != nil && term.State.IsTerminal() {
		t.Fatalf("run terminated before the approval gate (state=%s reason=%q); a mutation must stop for authorization",
			term.State, term.Reason)
	}
	boundary := d.Boundary()
	if boundary == nil {
		t.Fatalf("expected a parked human boundary; state=%s termination=%+v", d.State(), term)
	}
	if boundary.Action != autonomy.HumanBoundaryApproval || boundary.PatchID == "" {
		t.Fatalf("parked boundary = %+v; want an approval gate carrying a real held patch", boundary)
	}

	// ── 1. FILES DISCOVERED > 0 ────────────────────────────────────────
	// Derivation ran bounded discovery; the run must be able to say what it saw.
	if d.derivationNote == "" {
		t.Fatal("the run recorded no scope-derivation evidence; it cannot claim a target without saying it looked")
	}
	if !strings.Contains(d.derivationNote, "observed workspace evidence") {
		t.Errorf("derivation did not bind from observed evidence: %q", d.derivationNote)
	}

	// ── 2. ACTUAL TARGETS RESOLVED ─────────────────────────────────────
	var proven []string
	for _, tt := range d.resolved.Profile.Targets {
		if tt.Resolved != "" && tt.Exists {
			proven = append(proven, tt.Resolved)
		}
	}
	if len(proven) == 0 {
		t.Fatal("no target resolved: the objective produced no evidence-bound mutation surface")
	}
	for _, want := range []string{"index.html", "styles.css", "script.js"} {
		if !contains(proven, want) {
			t.Errorf("target %q was not bound; got %v", want, proven)
		}
	}
	// Derivation must not drag in non-artifact files.
	for _, unwanted := range []string{"readme.md", "docs/notes.md"} {
		if contains(proven, unwanted) {
			t.Errorf("derivation bound %q, which the objective never declared as an artifact kind", unwanted)
		}
	}

	// ── 3. MODEL RECEIVED REAL PROJECT CONTEXT ─────────────────────────
	// This is the assertion the original bug made impossible. The provider must
	// have seen the workspace, not 220 bytes of restated prompt.
	reqs := mock.requests
	if len(reqs) == 0 {
		t.Fatal("the provider was never invoked; the run cannot have produced a proposal")
	}
	sawHTML := false
	for _, r := range reqs {
		for _, msg := range r.Messages {
			if strings.Contains(msg.Content, "Placeholder") {
				sawHTML = true
			}
		}
	}
	if !sawHTML {
		t.Errorf("no invocation carried the target's real bytes; the model was asked to redesign a file it was never shown "+
			"(first request: %d messages, %d chars)", len(reqs[0].Messages), promptChars(reqs[0]))
	}

	// ── 4. MUTATIONSET APPLIED / DISK BYTES CHANGED ────────────────────
	if mock.calls < 3 {
		t.Fatalf("expected one bounded invocation per bound target; got %d calls for %v", mock.calls, proven)
	}
	// The approval gate must be holding exactly the derived scope: an approval
	// over a wider or narrower set than the evidence bound would be a governance
	// failure, not a formatting detail.
	if len(boundary.Targets) != len(proven) {
		t.Errorf("the approval gate covers %v but the evidence bound %v", boundary.Targets, proven)
	}
	if _, err := d.ResumeApprove(context.Background()); err != nil {
		t.Fatalf("approving a real held patch must succeed: %v", err)
	}

	// ── 4b. THE RUNTIME'S OWN EVIDENCE, NOT THE EVENT STREAM ───────────
	// This asserts on the sealed execution evidence rather than on the bus.
	//
	// events.Bus.Publish is deliberately NON-BLOCKING with a silent drop path
	// ("slow subscribers drop events"), and this repository's own architecture
	// rule states that a subscriber must not assert completeness on the stream —
	// the authoritative record is ExecutionEvidence, always re-readable from the
	// evidence ledger.
	//
	// An earlier version of this test counted mutation.completed /
	// verification.completed events and failed intermittently under parallel
	// package load, when the subscription buffer overflowed and the events were
	// dropped even though the mutation and verification had both run. The
	// evidence below cannot drop: it is the record the runtime sealed.
	obs := d.LastObservation()
	if obs.Objective.Mutation != execution.FilesystemApplied {
		t.Errorf("evidence mutation state = %q, want APPLIED", obs.Objective.Mutation)
	}
	if obs.Objective.MutatedFiles == 0 {
		t.Error("evidence recorded 0 mutated files after a real apply")
	}
	if !obs.Objective.VerificationRan {
		t.Error("evidence records that verification never ran")
	}
	// "The runtime looked before it wrote" is asserted where it is actually
	// observable — above, by checking that each dispatched invocation carried the
	// target's real pre-mutation bytes. It is deliberately NOT asserted through
	// WorkspaceObservations here: that counter is populated on the Execute
	// observation, not on the post-approval one, so reading it at this point would
	// measure the wrong attempt rather than prove anything.

	changed := 0
	for name, prior := range before {
		now, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		if string(now) != string(prior) {
			changed++
		}
	}
	if changed == 0 {
		t.Fatal("approval produced no filesystem change; the mutation boundary did not actually write")
	}

	index, _ := os.ReadFile(filepath.Join(root, "index.html"))
	if !strings.Contains(string(index), "TomHunter") {
		t.Errorf("the MODEL's bytes did not reach disk; index.html is %q", string(index))
	}
	if css, _ := os.ReadFile(filepath.Join(root, "styles.css")); !strings.Contains(string(css), "system-ui") {
		t.Errorf("the CSS target was not mutated: %q", string(css))
	}
	// Scope discipline: a file outside the bound set must be byte-identical.
	notes, _ := os.ReadFile(filepath.Join(root, "docs/notes.md"))
	if string(notes) != "internal notes, not part of the page\n" {
		t.Errorf("mutation escaped the bound scope: docs/notes.md = %q", string(notes))
	}
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func promptChars(r ai.Request) int {
	n := 0
	for _, m := range r.Messages {
		n += len(m.Content)
	}
	return n
}

// scriptResponse is a scripted artifact plus the marker that proves a prompt
// actually contained the file it claims to be about. Without the marker a
// provider could answer correctly by accident — by returning the same bytes for
// every invocation — and the acceptance test would prove nothing about whether
// the runtime showed the model the workspace.
type scriptResponse struct {
	ai.Response
	// marker is a byte sequence unique to this target's real content.
	marker string
}

// SearchMarker returns the content fingerprint of the scripted artifact.
func (r scriptResponse) SearchMarker() string { return r.marker }
