package autonomy_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/core/domain"
	domaincap "github.com/PizenLabs/izen/internal/domain/capability"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/execution/capability"
)

// These tests lock the two boundaries this audit found missing on the canonical
// $prompt path:
//
//  1. a model-requested capability is authorized by the Control Plane and leaves
//     evidence;
//  2. post-execution observation of the declared target set is available to the
//     next computation, and is bounded by the same grant.
//
// They use the canonical capability vocabulary and the canonical event bus. No
// parallel event system and no second capability implementation is introduced.

// ── P0: model capability requests are Control-Plane authorized ────────────────

// TestCapabilityToolRunnerRefusesUngrantedCapability is the fail-closed case: a
// zero admission vector admits nothing, so a model-requested read is REFUSED with
// the canonical AUTHORIZATION_BLOCKED classification and never reaches the disk.
func TestCapabilityToolRunnerRefusesUngrantedCapability(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "secret.txt"), []byte("classified\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bus := events.NewBus(16)
	defer bus.Close()

	deny := execution.AdmittedCapabilities{} // ReadOnly:false — nothing admitted
	runner := execution.NewCapabilityToolRunner(root, func() execution.AdmittedCapabilities {
		return deny
	}, bus)

	// No tool may even be advertised.
	if names := runner.CapabilityToolNames(); len(names) != 0 {
		t.Fatalf("an ungranted runner must advertise no tools, got %v", names)
	}

	out, err := runner.Run(context.Background(), ai.ToolCall{
		ID:       "call-1",
		Function: ai.ToolCallFunction{Name: ai.ToolReadFile, Arguments: `{"path":"secret.txt"}`},
	})
	if err != nil {
		t.Fatalf("a refusal is a bounded tool result, not a transport error: %v", err)
	}
	// The refusal must name the canonical capability and the canonical refusal
	// sentinel, so a model (and a reader of the trace) can tell "not permitted"
	// apart from "attempted and failed".
	if !strings.Contains(out, string(capability.FileRead)) {
		t.Fatalf("refusal must name the canonical capability, got %q", out)
	}
	if !strings.Contains(out, "not authorized") {
		t.Fatalf("refusal must be the canonical authorization refusal, got %q", out)
	}
	if strings.Contains(out, "classified") {
		t.Fatal("fail-closed: an ungranted read must never return file content")
	}
	if runner.Calls() != 0 {
		t.Fatalf("a refused capability must not count as executed, got %d", runner.Calls())
	}
}

// TestCapabilityToolRunnerExecutesAndEvidences is the reachable case: with a
// read-only admission vector, a model-requested read executes against the live
// workspace and is recorded as evidence on the canonical event bus.
func TestCapabilityToolRunnerExecutesAndEvidences(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "notes.md"), []byte("alpha\nbeta\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bus := events.NewBus(64)
	defer bus.Close()

	var stages []string
	var mu sync.Mutex
	sub := bus.Subscribe(events.EventStageCompleted, func(ev events.DomainEvent) {
		p, ok := ev.Payload().(events.StageCompletedPayload)
		if !ok {
			return
		}
		mu.Lock()
		stages = append(stages, p.Stage)
		mu.Unlock()
	})
	defer sub.Cancel()

	admit := execution.StandardAdmittedCapabilities()
	runner := execution.NewCapabilityToolRunner(root, func() execution.AdmittedCapabilities {
		return *admit
	}, bus)

	if names := runner.CapabilityToolNames(); len(names) != 4 {
		t.Fatalf("a read-only grant must advertise all four inspection tools, got %v", names)
	}

	out, err := runner.Run(context.Background(), ai.ToolCall{
		ID:       "call-1",
		Function: ai.ToolCallFunction{Name: ai.ToolReadFile, Arguments: `{"path":"notes.md"}`},
	})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(out, "alpha") || !strings.Contains(out, "beta") {
		t.Fatalf("read must return real workspace content, got %q", out)
	}
	if runner.Calls() != 1 {
		t.Fatalf("executed capability count = %d, want 1", runner.Calls())
	}

	// Handlers run on a per-subscription goroutine and stage.completed is
	// drop-tolerant telemetry, so the assertion waits for delivery instead of
	// racing it.
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		found := false
		for _, s := range stages {
			if s == "capability.execute" {
				found = true
			}
		}
		mu.Unlock()
		if found {
			break
		}
		if time.Now().After(deadline) {
			mu.Lock()
			snapshot := append([]string(nil), stages...)
			mu.Unlock()
			t.Fatalf("capability execution must publish capability.execute evidence, got stages %v", snapshot)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestCapabilityToolRunnerToolNamesMapToCanonicalCapabilities pins the tool→capability
// mapping to the canonical vocabulary, and proves an unknown name has NO capability
// (so it is ungranted by construction, never by a heuristic).
func TestCapabilityToolRunnerToolNamesMapToCanonicalCapabilities(t *testing.T) {
	cases := map[string]capability.ID{
		ai.ToolReadFile:       capability.FileRead,
		ai.ToolListDirectory:  capability.FileRead,
		ai.ToolSearchCodebase: capability.FileSearch,
		ai.ToolSymbolLookup:   capability.FileSearch,
	}
	for name, want := range cases {
		got, ok := execution.ToolCapabilityFor(name)
		if !ok || got != want {
			t.Fatalf("ToolCapabilityFor(%q) = (%q,%v), want (%q,true)", name, got, ok, want)
		}
	}
	// Mutating tools must have no capability on this read-only seam.
	for _, name := range []string{ai.ToolWriteFile, ai.ToolApplyPatch, "run_command", ""} {
		if id, ok := execution.ToolCapabilityFor(name); ok {
			t.Fatalf("ToolCapabilityFor(%q) must be unauthorized on the read-only seam, got %q", name, id)
		}
	}
}

// TestCapabilityToolRunnerDeniesWorkspaceEscape proves the traversal guard still
// holds through the authorized seam: a ../ path is refused by the capability layer
// rather than followed.
func TestCapabilityToolRunnerDeniesWorkspaceEscape(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside-bytes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bus := events.NewBus(16)
	defer bus.Close()

	admit := execution.StandardAdmittedCapabilities()
	runner := execution.NewCapabilityToolRunner(root, func() execution.AdmittedCapabilities {
		return *admit
	}, bus)

	out, err := runner.Run(context.Background(), ai.ToolCall{
		ID:       "call-1",
		Function: ai.ToolCallFunction{Name: ai.ToolReadFile, Arguments: `{"path":"../outside.txt"}`},
	})
	if err != nil {
		t.Fatalf("a refused escape is a bounded result, not a transport error: %v", err)
	}
	if strings.Contains(out, "outside-bytes") {
		t.Fatal("workspace escape must never return content")
	}
}

// ── P0/P1: post-execution observation is grant-bounded ───────────────────────

// TestObservationAuthorityReadsCurrentState proves the observation boundary returns
// LIVE on-disk state (the fact a post-mutation computation needs) under a read grant.
func TestObservationAuthorityReadsCurrentState(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "report.txt")
	if err := os.WriteFile(target, []byte("one\ntwo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	auth := execution.NewCapabilityAuthority(root, nil)
	auth.SetGrant(execution.CapabilityGrantFor(*execution.StandardAdmittedCapabilities()))

	content, ev, ok := auth.ObserveTarget(context.Background(), "report.txt")
	if !ok {
		t.Fatalf("observation refused under a read grant: class=%s summary=%s", ev.Class, ev.Summary)
	}
	if content != "one\ntwo\n" {
		t.Fatalf("observed content = %q, want the live bytes", content)
	}
	if ev.ID == "" {
		t.Fatal("observation must carry an evidence identity")
	}

	// Mutate on disk; observation must reflect the CURRENT state, not a snapshot.
	if err := os.WriteFile(target, []byte("one\ntwo\nthree\nfour\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	content2, _, ok2 := auth.ObserveTarget(context.Background(), "report.txt")
	if !ok2 {
		t.Fatal("second observation refused")
	}
	if content2 == content {
		t.Fatal("post-execution observation must read live state, not a cached snapshot")
	}
	if !strings.Contains(auth.ObserveEvidence(context.Background(), "report.txt"), "lines=4") {
		t.Fatalf("evidence must report real current line count, got %q",
			auth.ObserveEvidence(context.Background(), "report.txt"))
	}
}

// TestObservationAuthorityRefusesWithoutGrant is the fail-closed case: the zero
// grant authorizes nothing, so observation reports the refusal instead of reading.
func TestObservationAuthorityRefusesWithoutGrant(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	auth := execution.NewCapabilityAuthority(root, nil)
	// Deliberately no SetGrant: the zero grant permits nothing.

	content, ev, ok := auth.ObserveTarget(context.Background(), "a.txt")
	if ok {
		t.Fatal("an ungranted observation must not succeed")
	}
	if content != "" {
		t.Fatal("an ungranted observation must not return content")
	}
	if ev.Class != capability.FailureAuthorizationBlocked {
		t.Fatalf("class = %s, want AUTHORIZATION_BLOCKED", ev.Class)
	}
	if !strings.Contains(auth.ObserveEvidence(context.Background(), "a.txt"), "not granted") {
		t.Fatal("evidence must report the authorization refusal truthfully")
	}
}

// TestObservationAuthoritySearchesAndDiscovers proves the remaining two canonical
// read capabilities are reachable through the same authority, so "search" and
// "project inspection" are real capabilities rather than orphaned implementations.
func TestObservationAuthoritySearchesAndDiscovers(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/demo\n\ngo 1.24\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "internal", "worker"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "internal", "worker", "pool.go"),
		[]byte("package worker\n\nfunc Pool() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	auth := execution.NewCapabilityAuthority(root, nil)
	auth.SetGrant(execution.CapabilityGrantFor(*execution.StandardAdmittedCapabilities()))

	matches, ev, ok := auth.Search(context.Background(), "func Pool", "", 10)
	if !ok {
		t.Fatalf("search refused under a read grant: class=%s", ev.Class)
	}
	if len(matches) == 0 {
		t.Fatal("search must find the literal query in the live workspace")
	}

	profile, pev, ok := auth.Discover(context.Background())
	if !ok {
		t.Fatalf("discover refused under a read grant: class=%s", pev.Class)
	}
	if len(profile.Toolchains) == 0 {
		t.Fatalf("discover must derive toolchains from validated manifests, got %+v", profile)
	}
	if !profileHas(profile.Toolchains, "go") {
		t.Fatalf("discover must detect the go toolchain from go.mod, got %v", profile.Toolchains)
	}
}

func profileHas(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// TestObservationGrantIsProjectionOfExistingAuthority proves the observation grant is
// a PROJECTION of the existing provenance × capability-set authority, not a widening:
// a read-only scope provenance gets read, and nothing else.
func TestObservationGrantIsProjectionOfExistingAuthority(t *testing.T) {
	caps := domaincap.NewCapabilitySet()
	caps.Grant(domaincap.CapabilityRead)

	// $prompt (ScopeDynamic) with read authority: read + discover, never execute or
	// network. The observation seam must never be able to reach a process or a socket.
	g := execution.GrantFor(domain.ScopeDynamic, caps)
	if !g.Permits(capability.FileRead) || !g.Permits(capability.WorkspaceDiscover) {
		t.Fatal("a $prompt scope with read authority must permit read and discover")
	}
	if g.Permits(capability.CommandRun) || g.Permits(capability.RuntimeServe) {
		t.Fatal("read-only authority must never permit process execution")
	}
	if g.Permits(capability.RuntimeFetch) || g.Permits(capability.RuntimeInspect) {
		t.Fatal("read-only authority must never permit network egress")
	}

	// An empty capability set grants only discovery, which grants nothing by itself.
	empty := execution.GrantFor(domain.ScopeDynamic, domaincap.NewCapabilitySet())
	if empty.Permits(capability.FileRead) {
		t.Fatal("an empty capability set must not permit file reads")
	}
	if !empty.Permits(capability.WorkspaceDiscover) {
		t.Fatal("discovery is always permitted: it is read-only and bounded")
	}
}
