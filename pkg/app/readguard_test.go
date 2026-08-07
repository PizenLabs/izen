package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/pkg/ir"
)

// TestPipelineReadGuardBlocksObsoleteReadsUnderRewrite proves a model
// `read`/`inspect`-style workspace read is sanitized to empty output whenever
// the run is a full-content overwrite (PolicyRewrite): obsolete workspace code
// can never re-enter the model context, and the blocked read is observable.
func TestPipelineReadGuardBlocksObsoleteReadsUnderRewrite(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("<div>OUTDATED LAYOUT CONTENT</div>"), 0o644); err != nil {
		t.Fatal(err)
	}
	gen := &scriptedGenerator{resp: []string{fenced("html", "index.html", portfolioPage)}}
	p := mustPipeline(t, WithRoot(root), WithGenerator(gen))
	intentIR := &ir.IntentIR{Category: ir.CategoryRedesign, TargetType: "portfolio", PreserveWorkspace: false}

	res, err := p.Run(t.Context(), Request{Intent: "redesign", IntentIR: intentIR})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Mode != ModeGreenfield {
		t.Fatalf("mode = %s, want greenfield", res.Mode)
	}
	if strings.Contains(res.SystemPrompt, "OUTDATED LAYOUT CONTENT") {
		t.Fatal("obsolete content leaked into the prompt context")
	}

	// A read-tool call during/after a rewrite run returns sanitized output.
	data, err := p.ReadWorkspaceFile("index.html")
	if err != nil {
		t.Fatalf("ReadWorkspaceFile: %v", err)
	}
	if len(data) != 0 {
		t.Fatalf("read under rewrite must be sanitized, got %q", data)
	}
	if p.BlockedReads() != 1 {
		t.Errorf("blocked reads = %d, want 1", p.BlockedReads())
	}
}

// TestPipelineReadGuardAllowsReadsOutsideRewrite proves reads return live
// content when the run is NOT a full-content overwrite (e.g. a create or
// refactor policy): the read guard only blocks under a rewrite context.
func TestPipelineReadGuardAllowsReadsOutsideRewrite(t *testing.T) {
	root := t.TempDir()
	gen := &scriptedGenerator{resp: []string{fenced("html", "index.html", portfolioPage)}}
	p := mustPipeline(t, WithRoot(root), WithGenerator(gen))
	intentIR := &ir.IntentIR{Category: ir.CategoryRefactor, TargetType: "portfolio", PreserveWorkspace: true}

	if _, err := p.Run(t.Context(), Request{Intent: "refactor", IntentIR: intentIR}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	data, err := p.ReadWorkspaceFile("index.html")
	if err != nil {
		t.Fatalf("ReadWorkspaceFile: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("read under a non-rewrite context must return live content")
	}
	if p.BlockedReads() != 0 {
		t.Errorf("blocked reads = %d, want 0", p.BlockedReads())
	}
}

// TestReadGuardModes is a focused unit test of the guard's two modes and its
// blocked-read counter.
func TestReadGuardModes(t *testing.T) {
	g := &readGuard{mode: readAllowed}
	live := func(string) ([]byte, error) { return []byte("LIVE"), nil }

	data, err := g.read("index.html", live)
	if err != nil || string(data) != "LIVE" {
		t.Fatalf("read under allowed mode = %q, err %v", data, err)
	}

	g.setMode(readBlocked)
	data, err = g.read("index.html", live)
	if err != nil {
		t.Fatalf("blocked read must not error: %v", err)
	}
	if len(data) != 0 {
		t.Fatalf("blocked read must sanitize content, got %q", data)
	}
	if g.blockedReads() != 1 {
		t.Errorf("blocked reads = %d, want 1", g.blockedReads())
	}

	g.setMode(readAllowed)
	if _, err := g.read("index.html", live); err != nil {
		t.Fatalf("read after re-allow: %v", err)
	}
	if g.blockedReads() != 1 {
		t.Errorf("blocked reads must not grow in allowed mode, got %d", g.blockedReads())
	}
}

// TestReadWorkspaceFileRejectsEscapingPath proves the read boundary refuses
// paths that escape the workspace root.
func TestReadWorkspaceFileRejectsEscapingPath(t *testing.T) {
	p := mustPipeline(t, WithRoot(t.TempDir()))
	if _, err := p.ReadWorkspaceFile("../evil.html"); err == nil {
		t.Fatal("expected escaping read path to be rejected")
	}
}
