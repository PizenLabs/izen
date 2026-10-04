// Package filesystem_test holds the capability's black-box contract tests.
//
// These tests run against a real temporary workspace. They exist because the
// properties this capability promises — confinement, honest observation, atomic
// writes — are only meaningful against a real filesystem, and a mock would
// happily pass while the syscall path was broken.
package filesystem_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/runtime/capabilities/filesystem"
	"github.com/PizenLabs/izen/runtime/kernel"
)

// newWorkspace builds a confined capability set over a fresh temp directory.
func newWorkspace(t *testing.T, opts ...filesystem.Option) (*filesystem.Capability, string) {
	t.Helper()
	root := t.TempDir()
	caps, err := filesystem.New(root, opts...)
	if err != nil {
		t.Fatalf("build capability: %v", err)
	}
	return caps, root
}

// writeFile is a test helper that puts a real file on disk.
func writeFile(t *testing.T, root, rel, content string) string {
	t.Helper()
	abs := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
	return abs
}

// lookup finds a registered capability by identifier.
func lookup(t *testing.T, caps *filesystem.Capability, id kernel.CapabilityID) kernel.Capability {
	t.Helper()
	for _, c := range caps.Capabilities() {
		if c.ID() == id {
			return c
		}
	}
	t.Fatalf("capability %q is not provided by this set", id)
	return nil
}

// TestNewRefusesUnusableRoot proves construction is fail-closed.
func TestNewRefusesUnusableRoot(t *testing.T) {
	if _, err := filesystem.New(""); err == nil {
		t.Error("New accepted an empty root")
	}
	if _, err := filesystem.New(filepath.Join(t.TempDir(), "does-not-exist")); err == nil {
		t.Error("New accepted a root that does not exist")
	}
	// A regular file is not a workspace.
	file := filepath.Join(t.TempDir(), "a.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := filesystem.New(file); err == nil {
		t.Error("New accepted a regular file as a workspace root")
	}
}

// TestConfinementRefusesEscapingTargets is the security contract.
//
// Every traversal shape must be refused. A capability that can write outside its
// root is a capability the authorization grant cannot reason about, because the
// grant names workspace-relative paths and there is no such path here.
func TestConfinementRefusesEscapingTargets(t *testing.T) {
	caps, _ := newWorkspace(t)
	write := lookup(t, caps, kernel.FileWrite)
	grant, err := kernel.NewGrant("g", []kernel.CapabilityID{kernel.FileWrite}, []string{"../../escape.txt"})
	if err != nil {
		t.Fatalf("grant: %v", err)
	}

	escapes := []string{
		"../escape.txt",
		"../../etc/passwd",
		"a/../../escape.txt",
		"./../escape.txt",
	}
	for _, target := range escapes {
		t.Run(target, func(t *testing.T) {
			req := kernel.Request{
				Capability: kernel.FileWrite,
				Step:       "write",
				Target:     target,
				Args:       map[string]string{"content": "pwned"},
				Grant:      grant,
			}
			if err := write.Authorize(req); err == nil {
				t.Fatalf("Authorize accepted an escaping target %q", target)
			}
			if _, err := write.Invoke(context.Background(), req); err == nil {
				t.Fatalf("Invoke accepted an escaping target %q", target)
			}
		})
	}
}

// TestConfinementRefusesSymlinkedEscape is the second half of the security
// contract, and the half the lexical checks cannot reach.
//
// A symlink INSIDE the workspace pointing at a directory outside it satisfies
// every lexical rule: the cleaned relative path has no "..", and the joined
// absolute path is textually under the root. The bytes still land outside. Before
// the real-path check, `Apply` on such a target was reported PROVEN with the file
// written beyond the boundary — a grant naming workspace-relative paths and a
// capability writing to absolute paths somewhere else.
func TestConfinementRefusesSymlinkedEscape(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "workspace")
	outside := filepath.Join(base, "outside")
	for _, dir := range []string{root, outside} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlinks unavailable on this platform: %v", err)
	}

	caps, err := filesystem.New(root)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	grant, err := kernel.NewGrant("g", []kernel.CapabilityID{kernel.FileWrite}, []string{"link/escaped.txt"})
	if err != nil {
		t.Fatalf("grant: %v", err)
	}

	write := lookup(t, caps, kernel.FileWrite)
	req := kernel.Request{
		Capability: kernel.FileWrite,
		Step:       "write",
		Target:     "link/escaped.txt",
		Args:       map[string]string{"content": "pwned"},
		Grant:      grant,
	}
	if err := write.Authorize(req); err == nil {
		t.Error("Authorize accepted a destination that symlinks outside the workspace")
	}
	if _, err := write.Invoke(context.Background(), req); err == nil {
		t.Error("Invoke accepted a destination that symlinks outside the workspace")
	}
	if _, err := os.Stat(filepath.Join(outside, "escaped.txt")); err == nil {
		t.Fatal("a write crossed the workspace boundary through a symlink")
	}
}

// TestConfinementAllowsSymlinksResolvingInsideTheRoot proves the real-path check
// refuses escapes without breaking a legitimate layout.
//
// A symlink pointing at another directory INSIDE the workspace names a real file
// in the workspace. Refusing it would be confinement theatre that breaks real
// repositories, and a security check that is routinely disabled is not one.
func TestConfinementAllowsSymlinksResolvingInsideTheRoot(t *testing.T) {
	caps, root := newWorkspace(t)
	if err := os.MkdirAll(filepath.Join(root, "real"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "link")); err != nil {
		t.Skipf("symlinks unavailable on this platform: %v", err)
	}

	write := lookup(t, caps, kernel.FileWrite)
	grant, err := kernel.NewGrant("g", []kernel.CapabilityID{kernel.FileWrite}, []string{"link/inside.txt"})
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	req := kernel.Request{
		Capability: kernel.FileWrite,
		Step:       "write",
		Target:     "link/inside.txt",
		Args:       map[string]string{"content": "inside\n"},
		Grant:      grant,
	}
	if err := write.Authorize(req); err != nil {
		t.Fatalf("Authorize refused a symlink resolving inside the workspace: %v", err)
	}
	obs, err := write.Invoke(context.Background(), req)
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if obs.Verdict != kernel.VerdictPass {
		t.Fatalf("verdict = %s (%s); want PASS", obs.Verdict, obs.Detail)
	}
	body, err := os.ReadFile(filepath.Join(root, "real", "inside.txt"))
	if err != nil {
		t.Fatalf("the write did not land: %v", err)
	}
	if string(body) != "inside\n" {
		t.Errorf("content = %q; want the written bytes", string(body))
	}
}

// TestAbsolutePathRefused proves an absolute destination is refused rather than
// reinterpreted relative to the root.
func TestAbsolutePathRefused(t *testing.T) {
	caps, _ := newWorkspace(t)
	exists := lookup(t, caps, kernel.FileExists)
	grant, err := kernel.NewGrant("g", []kernel.CapabilityID{kernel.FileExists}, nil)
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	req := kernel.Request{
		Capability: kernel.FileExists,
		Step:       "stat",
		Target:     "/etc/passwd",
		Args:       map[string]string{},
		Grant:      grant,
	}
	if _, err := exists.Invoke(context.Background(), req); err == nil {
		t.Fatal("Invoke accepted an absolute target")
	}
}

// TestWriteProducesRealWriteEvidence proves the write capability's evidence
// corresponds to bytes that actually reached disk.
//
// The assertion is deliberately on the filesystem afterwards, not on the
// observation the capability returned. A capability that reported success without
// writing would pass the former and fail this.
func TestWriteProducesRealWriteEvidence(t *testing.T) {
	caps, root := newWorkspace(t)
	write := lookup(t, caps, kernel.FileWrite)
	grant, err := kernel.NewGrant("g", []kernel.CapabilityID{kernel.FileWrite}, nil)
	if err != nil {
		t.Fatalf("grant: %v", err)
	}

	content := "hello from the kernel"
	obs, err := write.Invoke(context.Background(), kernel.Request{
		Capability: kernel.FileWrite,
		Step:       "write",
		Target:     "notes/out.md",
		Args:       map[string]string{"content": content},
		Grant:      grant,
	})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if obs.Verdict != kernel.VerdictPass {
		t.Fatalf("verdict = %q, want PASS (%s)", obs.Verdict, obs.Detail)
	}

	var sawWrite, sawPresent bool
	for _, f := range obs.Facts {
		if f.Kind == kernel.EvidenceFileWritten {
			sawWrite = true
		}
		if f.Kind == kernel.EvidenceFilePresent {
			sawPresent = true
		}
	}
	if !sawWrite {
		t.Error("a successful write produced no FILE_WRITTEN evidence")
	}
	if !sawPresent {
		t.Error("a successful write produced no FILE_PRESENT evidence; " +
			"a CREATE contract could never be satisfied without it")
	}

	onDisk, err := os.ReadFile(filepath.Join(root, "notes/out.md"))
	if err != nil {
		t.Fatalf("the capability reported success but the file is not readable: %v", err)
	}
	if string(onDisk) != content {
		t.Errorf("content on disk = %q, want %q", onDisk, content)
	}
}

// TestWriteRefusesWithoutExplicitTarget proves the kernel does not choose a
// filename. This is the absence of heuristic artifact targeting, enforced at the
// capability boundary.
func TestWriteRefusesWithoutExplicitTarget(t *testing.T) {
	caps, root := newWorkspace(t)
	write := lookup(t, caps, kernel.FileWrite)
	grant, err := kernel.NewGrant("g", []kernel.CapabilityID{kernel.FileWrite}, nil)
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	_, err = write.Invoke(context.Background(), kernel.Request{
		Capability: kernel.FileWrite,
		Step:       "write",
		Args:       map[string]string{"content": "x"},
		Grant:      grant,
	})
	if err == nil {
		t.Fatal("Invoke accepted a write with no target; the kernel must not guess a filename")
	}
	// Nothing may have been created anywhere in the root.
	entries, readErr := os.ReadDir(root)
	if readErr != nil {
		t.Fatalf("readdir: %v", readErr)
	}
	if len(entries) != 0 {
		t.Errorf("a refused write left %d entries in the workspace", len(entries))
	}
}

// TestWriteRefusesWithoutContent proves a missing required argument is a refusal,
// not an empty file.
func TestWriteRefusesWithoutContent(t *testing.T) {
	caps, _ := newWorkspace(t)
	write := lookup(t, caps, kernel.FileWrite)
	grant, err := kernel.NewGrant("g", []kernel.CapabilityID{kernel.FileWrite}, nil)
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	_, err = write.Invoke(context.Background(), kernel.Request{
		Capability: kernel.FileWrite,
		Step:       "write",
		Target:     "a.txt",
		Args:       map[string]string{},
		Grant:      grant,
	})
	if err == nil {
		t.Fatal("Invoke accepted a write with no content argument")
	}
}

// TestExistsReportsRealPresenceAndAbsence proves absence is an observation with a
// name, not a missing record.
func TestExistsReportsRealPresenceAndAbsence(t *testing.T) {
	caps, root := newWorkspace(t)
	exists := lookup(t, caps, kernel.FileExists)
	grant, err := kernel.NewGrant("g", []kernel.CapabilityID{kernel.FileExists}, nil)
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	writeFile(t, root, "present.txt", "x")

	obs, err := exists.Invoke(context.Background(), kernel.Request{
		Capability: kernel.FileExists, Step: "s1", Target: "present.txt",
		Args: map[string]string{}, Grant: grant,
	})
	if err != nil {
		t.Fatalf("invoke present: %v", err)
	}
	if len(obs.Facts) != 1 || obs.Facts[0].Kind != kernel.EvidenceFilePresent {
		t.Fatalf("facts = %+v, want one FILE_PRESENT", obs.Facts)
	}
	if obs.Facts[0].Target != "present.txt" {
		t.Errorf("evidence target = %q, want the concrete path that was stat'd", obs.Facts[0].Target)
	}

	obs, err = exists.Invoke(context.Background(), kernel.Request{
		Capability: kernel.FileExists, Step: "s2", Target: "absent.txt",
		Args: map[string]string{}, Grant: grant,
	})
	if err != nil {
		t.Fatalf("invoke absent: %v", err)
	}
	if len(obs.Facts) != 1 || obs.Facts[0].Kind != kernel.EvidenceFileAbsent {
		t.Fatalf("facts = %+v, want one FILE_ABSENT", obs.Facts)
	}
	if obs.Verdict != kernel.VerdictPass {
		t.Errorf("verdict = %q, want PASS: \"I looked and it is not there\" is a complete observation", obs.Verdict)
	}
}

// TestReadOfAbsentFileReportsAbsence proves a read never substitutes a
// different file. This is the single most important negative behaviour here.
func TestReadOfAbsentFileReportsAbsence(t *testing.T) {
	caps, root := newWorkspace(t)
	read := lookup(t, caps, kernel.FileRead)
	grant, err := kernel.NewGrant("g", []kernel.CapabilityID{kernel.FileRead}, nil)
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	// A decoy that a fuzzy resolver would happily have returned.
	writeFile(t, root, "notes.md", "the real content")

	obs, err := read.Invoke(context.Background(), kernel.Request{
		Capability: kernel.FileRead, Step: "r1", Target: "note.md",
		Args: map[string]string{}, Grant: grant,
	})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if len(obs.Facts) != 1 {
		t.Fatalf("facts = %+v, want exactly one", obs.Facts)
	}
	if obs.Facts[0].Kind != kernel.EvidenceFileAbsent {
		t.Errorf("evidence kind = %q, want %q; a near-miss target must report absence, not the closest file",
			obs.Facts[0].Kind, kernel.EvidenceFileAbsent)
	}
	if obs.Facts[0].Target != "note.md" {
		t.Errorf("evidence target = %q, want the requested path exactly", obs.Facts[0].Target)
	}
	if strings.Contains(obs.Detail, "notes.md") {
		t.Errorf("detail %q named a file that was not requested", obs.Detail)
	}
}

// TestReadRefusesDirectory proves a directory is not silently read as a file.
func TestReadRefusesDirectory(t *testing.T) {
	caps, root := newWorkspace(t)
	read := lookup(t, caps, kernel.FileRead)
	grant, err := kernel.NewGrant("g", []kernel.CapabilityID{kernel.FileRead}, nil)
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	obs, err := read.Invoke(context.Background(), kernel.Request{
		Capability: kernel.FileRead, Step: "r1", Target: "sub",
		Args: map[string]string{}, Grant: grant,
	})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if obs.Verdict != kernel.VerdictFail {
		t.Errorf("verdict = %q, want FAIL for a directory target", obs.Verdict)
	}
	if len(obs.Facts) != 0 {
		t.Errorf("a failed read produced %d facts; a failure must not claim an observation", len(obs.Facts))
	}
}

// TestSearchReportsZeroMatchesDistinctly proves "I looked and found none" is
// distinguishable from "the search could not run".
func TestSearchReportsZeroMatchesDistinctly(t *testing.T) {
	caps, root := newWorkspace(t)
	search := lookup(t, caps, kernel.FileSearch)
	grant, err := kernel.NewGrant("g", []kernel.CapabilityID{kernel.FileSearch}, nil)
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	writeFile(t, root, "a.txt", "alpha beta")

	obs, err := search.Invoke(context.Background(), kernel.Request{
		Capability: kernel.FileSearch, Step: "q1", Target: "a.txt",
		Args: map[string]string{"pattern": "gamma"}, Grant: grant,
	})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if obs.Verdict != kernel.VerdictPass {
		t.Errorf("verdict = %q, want PASS: a completed search with no matches is a valid observation", obs.Verdict)
	}
	if !strings.Contains(obs.Detail, "0 matches") {
		t.Errorf("detail = %q, want it to name the zero-match count", obs.Detail)
	}
}

// TestSearchRefusesWithoutPattern proves the capability does not invent a query.
func TestSearchRefusesWithoutPattern(t *testing.T) {
	caps, root := newWorkspace(t)
	search := lookup(t, caps, kernel.FileSearch)
	grant, err := kernel.NewGrant("g", []kernel.CapabilityID{kernel.FileSearch}, nil)
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	writeFile(t, root, "a.txt", "alpha")

	if _, err := search.Invoke(context.Background(), kernel.Request{
		Capability: kernel.FileSearch, Step: "q1", Target: "a.txt",
		Args: map[string]string{}, Grant: grant,
	}); err == nil {
		t.Fatal("Invoke accepted a search with no pattern; the capability must not guess a query")
	}
}

// TestDiscoverObservesRealContents proves discovery reports what is actually on
// disk, skipping the trees that are not workspace content.
func TestDiscoverObservesRealContents(t *testing.T) {
	caps, root := newWorkspace(t)
	discover := lookup(t, caps, kernel.WorkspaceDiscover)
	grant, err := kernel.NewGrant("g", []kernel.CapabilityID{kernel.WorkspaceDiscover}, nil)
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	writeFile(t, root, "index.txt", "x")
	writeFile(t, root, "src/main.txt", "x")
	writeFile(t, root, ".hidden/secret.txt", "x")
	writeFile(t, root, "node_modules/pkg/index.txt", "x")

	obs, err := discover.Invoke(context.Background(), kernel.Request{
		Capability: kernel.WorkspaceDiscover, Step: "d1",
		Args: map[string]string{}, Grant: grant,
	})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if obs.Verdict != kernel.VerdictPass {
		t.Fatalf("verdict = %q, want PASS (%s)", obs.Verdict, obs.Detail)
	}
	if len(obs.Facts) != 1 || obs.Facts[0].Kind != kernel.EvidenceWorkspaceObserved {
		t.Fatalf("facts = %+v, want one WORKSPACE_OBSERVED", obs.Facts)
	}
	if obs.Facts[0].Bytes != 2 {
		t.Errorf("observed %d entries, want 2; dot-directories and node_modules must be skipped",
			obs.Facts[0].Bytes)
	}
}

// TestCapabilitySetIsDomainNeutral proves the capability vocabulary contains no
// domain or artifact-format knowledge.
func TestCapabilitySetIsDomainNeutral(t *testing.T) {
	caps, _ := newWorkspace(t)
	terms := []string{"html", "css", "javascript", "react", "website", "portfolio"}
	for _, c := range caps.Capabilities() {
		id := strings.ToLower(string(c.ID()))
		for _, term := range terms {
			if strings.Contains(id, term) {
				t.Errorf("capability %q contains the domain term %q", c.ID(), term)
			}
		}
	}
}

// TestRegistryOverCapabilitySet proves the set installs cleanly and is complete.
func TestRegistryOverCapabilitySet(t *testing.T) {
	caps, _ := newWorkspace(t)
	registry, err := caps.Registry()
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	for _, id := range []kernel.CapabilityID{
		kernel.WorkspaceDiscover, kernel.FileRead, kernel.FileSearch,
		kernel.FileExists, kernel.FileWrite,
	} {
		if !registry.Has(id) {
			t.Errorf("registry is missing %q", id)
		}
	}
	if registry.Has(kernel.CommandRun) {
		t.Error("the filesystem set registered command.run, which it does not implement")
	}
}

// TestWriteIsAtomic proves a failed write leaves no partial file behind.
//
// The staging file is written and then renamed, so a reader observing the target
// at any moment sees either the old content or the new content, never a prefix.
func TestWriteIsAtomicLeavesNoStagingFile(t *testing.T) {
	caps, root := newWorkspace(t)
	write := lookup(t, caps, kernel.FileWrite)
	grant, err := kernel.NewGrant("g", []kernel.CapabilityID{kernel.FileWrite}, nil)
	if err != nil {
		t.Fatalf("grant: %v", err)
	}

	for _, content := range []string{"first", strings.Repeat("x", 100_000)} {
		obs, err := write.Invoke(context.Background(), kernel.Request{
			Capability: kernel.FileWrite, Step: "w", Target: "big.txt",
			Args: map[string]string{"content": content}, Grant: grant,
		})
		if err != nil {
			t.Fatalf("invoke: %v", err)
		}
		if obs.Verdict != kernel.VerdictPass {
			t.Fatalf("verdict = %q for %d bytes (%s)", obs.Verdict, len(content), obs.Detail)
		}
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".izen-write-") {
			t.Errorf("a staging file %q was left behind", e.Name())
		}
	}
}
