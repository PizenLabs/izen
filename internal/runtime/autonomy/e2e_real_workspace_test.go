package autonomy

// ── END-TO-END: THE REAL PORTFOLIO WORKSPACE ────────────────────────────────
//
// The acceptance scenario against the workspace it was actually reported
// against — a real, independently authored web project — rather than a
// synthetic fixture. It is skipped when the workspace is absent so the suite
// stays runnable elsewhere, but on the reporting machine this is the test that
// answers "does IZEN execute?" with evidence instead of an explanation.
//
// Nothing is assumed about the target names. The runtime discovers them; this
// test only supplies the provider's answers and then measures what happened.
//
// It additionally pins the workspace-hygiene rule that discovery relies on:
// the workspace carries a third-party tool state directory, and binding it as
// a mutation target would be as wrong as inventing a file.

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
)

// realPortfolioWorkspace is the reported workspace, resolved from the home
// directory rather than hard-coded to a machine path.
func realPortfolioWorkspace(t *testing.T) (string, bool) {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false
	}
	src := filepath.Join(home, "Documents", "Project", "test", "web-test4", "src")
	if _, err := os.Stat(src); err != nil {
		return "", false
	}
	// A byte-copy keeps the developer's real workspace pristine while running
	// the full mutate-and-verify lifecycle against its authentic contents.
	dst := t.TempDir()
	// The copy is bounded by the test's own lifetime: a hung rsync must fail the
	// test rather than hang it, and ctx carries the deadline.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "rsync", "-a",
		"--exclude=.git", "--exclude=.izen", "--exclude=.crush",
		src+"/", dst+"/").CombinedOutput()
	if err != nil {
		t.Fatalf("copy workspace: %v: %s", err, out)
	}
	return dst, true
}

// realWorkspaceProvider answers each invocation with an artifact SELF-ANCHORED
// in the bytes of the file the runtime dispatched.
//
// Self-anchoring is what makes this test robust AND strict. The provider reads
// the dispatched target out of the working copy and builds its SEARCH block from
// that file's own first non-empty line. So:
//
//   - if the runtime dispatched the WRONG file, the artifact anchors on the
//     wrong content and the mutation boundary refuses it;
//   - if the runtime dispatched the file WITHOUT showing its bytes to the model,
//     the artifact still anchors (the provider read them off disk) — so the
//     separate "the model was shown the file" assertion is what actually proves
//     context was delivered;
//   - if the workspace drifts, the test keeps working, because nothing about the
//     external project's contents is baked into the test.
//
// An earlier version hardcoded the workspace's exact text. That coupled an
// end-to-end runtime test to a mutable directory any developer may edit, and it
// failed the moment the project changed.
type realWorkspaceProvider struct {
	root string

	mu       sync.Mutex
	requests []ai.Request
	calls    int
	served   map[string]int
}

func (p *realWorkspaceProvider) Name() string { return "real-workspace-scripted" }

func (p *realWorkspaceProvider) Execute(_ context.Context, req ai.Request) (*ai.Response, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	p.requests = append(p.requests, req)

	target := dispatchedTarget(req)
	if target == "" {
		return nil, fmt.Errorf("invocation #%d carried no identifiable target", p.calls)
	}
	// The provider does NOT police the mutation scope: it cannot know the derived
	// scope until the run finishes. It records what it was actually asked to
	// change, and the test compares that record against the derived scope
	// afterwards — which is the assertion that actually matters.
	if _, statErr := os.Stat(filepath.Join(p.root, filepath.FromSlash(target))); statErr != nil {
		return nil, fmt.Errorf("invocation #%d dispatched %q, which is not a file in the workspace: %w",
			p.calls, target, statErr)
	}
	artifact, err := anchoredArtifact(p.root, target)
	if err != nil {
		return nil, fmt.Errorf("invocation #%d for %s: %w", p.calls, target, err)
	}
	p.served[target]++
	return artifact, nil
}

func (p *realWorkspaceProvider) ExecuteStream(context.Context, ai.Request) (io.ReadCloser, error) {
	return nil, fmt.Errorf("stream not supported")
}

// anchoredArtifact builds a real SEARCH/REPLACE artifact from the target's own
// first non-empty line, appending a marker the test can find on disk.
func anchoredArtifact(root, target string) (*ai.Response, error) {
	body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(target)))
	if err != nil {
		return nil, fmt.Errorf("read dispatched target: %w", err)
	}
	anchor := ""
	for _, line := range strings.Split(string(body), "\n") {
		if strings.TrimSpace(line) != "" {
			anchor = strings.TrimRight(line, "\r")
			break
		}
	}
	if anchor == "" {
		return nil, fmt.Errorf("target %s is empty; nothing to anchor a replacement on", target)
	}
	replacement := anchor + "\n" + acceptanceMarker
	return &ai.Response{
		Content: "<<<<<<< SEARCH\n" + anchor + "\n=======\n" + replacement + "\n>>>>>>> REPLACE",
		Usage: ai.ProviderUsage{
			Known: true, PromptTokens: 1800, CompletionTokens: 260, FinishReason: "stop",
		},
	}, nil
}

// acceptanceMarker is the byte sequence the acceptance run must place on disk.
// It is a COMMENT so the file stays valid in every language the derived scope
// can contain (HTML, CSS and JS alike).
const acceptanceMarker = "<!-- izen acceptance marker: TomHunter -->"

// dispatchedTarget identifies the file the runtime named in this invocation.
// The full-artifact lane announces each file in a "### FILE: <path>" block, the
// bounded-patch lane names it in the context-window header, and every mutation
// lane names it in the system prompt's ONE-file statement.
func dispatchedTarget(req ai.Request) string {
	var blob strings.Builder
	for _, msg := range req.Messages {
		blob.WriteString(msg.Content)
		blob.WriteByte('\n')
	}
	text := blob.String()
	for _, line := range strings.Split(text, "\n") {
		switch {
		case strings.HasPrefix(line, "### CONTEXT WINDOW — "),
			strings.HasPrefix(line, "### TARGET FILE: "):
			if f := strings.Fields(line[strings.Index(line, "— ")+len("— "):]); len(f) > 0 {
				return f[0]
			}
		case strings.HasPrefix(line, "### FILE: "):
			return strings.TrimSpace(strings.TrimPrefix(line, "### FILE: "))
		}
	}
	for _, msg := range req.Messages {
		if i := strings.Index(msg.Content, "strictly modifying ONE file: "); i >= 0 {
			rest := msg.Content[i+len("strictly modifying ONE file: "):]
			if f := strings.Fields(rest); len(f) > 0 {
				return f[0]
			}
		}
	}
	return ""
}

// firstNonEmptyLine returns the first non-blank line of a file body, which is
// the anchor the scripted artifact is built from.
func firstNonEmptyLine(body string) string {
	for _, line := range strings.Split(body, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func sortedKeysOf(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestEndToEnd_RealPortfolioWorkspace(t *testing.T) {
	root, ok := realPortfolioWorkspace(t)
	if !ok {
		t.Skip("reported workspace ~/Documents/Project/test/web-test4/src is not present")
	}

	// ── Preconditions measured, not assumed ────────────────────────────
	before := map[string][]byte{}
	ents, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) == 0 {
		t.Fatal("the copied workspace is empty; discovery would have nothing to observe")
	}
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(root, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		before[e.Name()] = b
	}

	// The bus publishes from whichever goroutine the runtime stage runs on, so the
	// collector must be synchronized. This stream is DELIBERATELY lossy
	// (Bus.Publish drops on a full subscriber) and is therefore used only for
	// ordering-independent spot checks; the authoritative assertions read the
	// sealed execution evidence.
	bus := events.NewBus(events.DefaultBufferSize)
	var eventMu sync.Mutex
	var eventTypes []string
	bus.SubscribeAll(func(ev events.DomainEvent) {
		eventMu.Lock()
		defer eventMu.Unlock()
		eventTypes = append(eventTypes, string(ev.Type()))
	})

	provider := &realWorkspaceProvider{root: root, served: map[string]int{}}
	x := testExecutor(t, root, provider, bus)
	adapter := NewExecutorAdapter(root, execution.NewIntentGateway(root), x)
	d := NewDriver(adapter, bus)

	term, err := d.Run(context.Background(), acceptanceObjective)
	if err != nil {
		t.Fatalf("canonical $prompt path errored: %v", err)
	}

	// ── 1. DISCOVERY > 0 ───────────────────────────────────────────────
	if d.derivationNote == "" {
		t.Fatal("no scope-derivation evidence was recorded")
	}
	t.Logf("derivation: %s", d.derivationNote)

	// ── 2. TARGETS RESOLVED FROM OBSERVED EVIDENCE ─────────────────────
	var proven []string
	for _, tt := range d.resolved.Profile.Targets {
		if tt.Resolved != "" && tt.Exists {
			proven = append(proven, tt.Resolved)
		}
	}
	if len(proven) == 0 {
		t.Fatalf("no target bound; boundary=%+v termination=%+v", d.Boundary(), term)
	}
	t.Logf("targets bound: %v", proven)

	// The workspace's readme is a real file the discovery pass observed, but the
	// objective declared no markdown artifact kind. Binding it would be scope
	// invention, so it must stay out.
	for _, p := range proven {
		if p == "readme.md" {
			t.Error("derivation bound readme.md; the objective declared no markdown kind")
		}
	}
	if _, ok := before["readme.md"]; !ok {
		t.Skip("the workspace no longer carries a readme; the scope-ceiling assertion needs one to be meaningful")
	}

	// ── 3. MODEL SAW REAL PROJECT BYTES ────────────────────────────────
	// The provider reads the target off disk to anchor its artifact, so it can
	// succeed without ever having been shown the file. This assertion is the one
	// that actually proves CONTEXT was delivered: for every bound target, some
	// invocation must have carried that file's own pre-mutation content.
	if provider.calls == 0 {
		t.Fatal("the provider was never invoked")
	}
	for _, target := range proven {
		marker := firstNonEmptyLine(string(before[filepath.Base(target)]))
		if marker == "" {
			continue
		}
		seen := false
		for _, r := range provider.requests {
			for _, m := range r.Messages {
				if strings.Contains(m.Content, marker) {
					seen = true
				}
			}
		}
		if !seen {
			t.Errorf("no invocation carried %s's real bytes (%q); the model was asked to redesign a file it was never shown",
				target, marker)
		}
	}

	// ── 4. MUTATION APPLIED THROUGH THE CANONICAL BOUNDARY ────────────
	// The model was asked to change exactly the derived scope — no more, no
	// less. This is the scope-discipline assertion, and it is only possible after
	// the fact because the derived scope is itself an outcome of the run.
	dispatched := sortedKeysOf(provider.served)
	if len(dispatched) != len(proven) {
		t.Errorf("the model was asked to change %v but the evidence bound %v", dispatched, proven)
	}
	for _, target := range proven {
		if !contains(dispatched, target) {
			t.Errorf("bound target %q was never dispatched to the model", target)
		}
	}

	boundary := d.Boundary()
	if boundary == nil || boundary.PatchID == "" {
		t.Fatalf("expected a parked approval gate holding a real candidate; got %+v (term=%+v)", boundary, term)
	}
	if len(boundary.Targets) != len(proven) {
		t.Errorf("approval covers %v but evidence bound %v", boundary.Targets, proven)
	}
	if _, err := d.ResumeApprove(context.Background()); err != nil {
		t.Fatalf("approve: %v", err)
	}

	changed := 0
	for name, prior := range before {
		now, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		if string(now) != string(prior) {
			changed++
			t.Logf("CHANGED %s", name)
		}
	}
	if changed == 0 {
		t.Fatal("approval produced no filesystem change")
	}

	// Every file the model was actually asked about must now carry the marker
	// it wrote, and every file it was not asked about must not.
	for _, target := range proven {
		body, readErr := os.ReadFile(filepath.Join(root, filepath.FromSlash(target)))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if !strings.Contains(string(body), acceptanceMarker) {
			t.Errorf("%s does not carry the model's marker; the artifact did not reach disk", target)
		}
	}
	for name := range before {
		if contains(proven, name) {
			continue
		}
		body, readErr := os.ReadFile(filepath.Join(root, name))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if strings.Contains(string(body), acceptanceMarker) {
			t.Errorf("%s carries the acceptance marker but was never a bound target", name)
		}
	}

	// ── 5. OBJECTIVE STATE REFLECTS EVIDENCE ───────────────────────────
	// Assertions read the SEALED EXECUTION EVIDENCE, not the event stream.
	//
	// events.Bus.Publish is non-blocking with a silent drop path, and this
	// repository's own architecture rule states a subscriber must not assert
	// completeness on it — the authoritative record is ExecutionEvidence. An
	// earlier version of this test required mutation.completed /
	// verification.completed to be observed and failed intermittently under
	// parallel package load, when the subscription buffer overflowed and the
	// events dropped even though both had run. Evidence cannot drop.
	obs := d.LastObservation()
	if obs.Objective.Mutation != execution.FilesystemApplied {
		t.Errorf("objective mutation state = %q, want APPLIED", obs.Objective.Mutation)
	}
	if obs.Objective.MutatedFiles == 0 {
		t.Errorf("objective recorded 0 mutated files after a real apply")
	}
	t.Logf("mutation=%s mutated_files=%d verification_ran=%t outcome=%s",
		obs.Objective.Mutation, obs.Objective.MutatedFiles,
		obs.Objective.VerificationRan, obs.Outcome)
}
