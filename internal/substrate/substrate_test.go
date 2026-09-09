package substrate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func strPtr(s string) *string { return &s }

func TestStageAndApplyPatch(t *testing.T) {
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "a.txt"), []byte("orig"), 0o644); err != nil {
		t.Fatal(err)
	}
	patch := Patch{Files: []PatchFile{{Path: "a.txt", Content: strPtr("patched")}, {Path: "new.txt", Content: strPtr("new")}}}
	staged, err := StagePatch(workDir, "run1", 1, patch)
	if err != nil {
		t.Fatalf("StagePatch: %v", err)
	}
	if !strings.Contains(staged, "run-run1-patch-1.json") {
		t.Fatalf("staged path = %q", staged)
	}
	if err := ApplyPatch(workDir, staged); err != nil {
		t.Fatalf("ApplyPatch: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(workDir, "a.txt")); string(got) != "patched" {
		t.Fatalf("a.txt = %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(workDir, "new.txt")); string(got) != "new" {
		t.Fatalf("new.txt = %q", got)
	}
	// Pre-patch checkpoint must exist.
	entries, err := os.ReadDir(filepath.Join(workDir, ".izen", "checkpoints"))
	if err != nil || len(entries) == 0 {
		t.Fatalf("expected pre-patch checkpoint, entries=%v err=%v", entries, err)
	}
	// Mutations log must be parseable ndjson.
	data, err := os.ReadFile(filepath.Join(workDir, ".izen", "audit", "mutations.log"))
	if err != nil {
		t.Fatalf("read mutations.log: %v", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("mutation line not parseable: %v", err)
		}
	}
}

func TestApplyPatchMalformedLeavesWorkspaceUntouched(t *testing.T) {
	workDir := t.TempDir()
	before := "stable"
	if err := os.WriteFile(filepath.Join(workDir, "a.txt"), []byte(before), 0o644); err != nil {
		t.Fatal(err)
	}
	badPath := filepath.Join(workDir, "bad.json")
	if err := os.WriteFile(badPath, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	checkpointsBefore := countCheckpoints(t, workDir)
	if err := ApplyPatch(workDir, badPath); err == nil {
		t.Fatal("expected error for malformed patch")
	}
	if got, _ := os.ReadFile(filepath.Join(workDir, "a.txt")); string(got) != before {
		t.Fatalf("workspace touched: a.txt = %q", got)
	}
	if got := countCheckpoints(t, workDir); got != checkpointsBefore {
		t.Fatalf("checkpoints before=%d after=%d: malformed JSON must not checkpoint", checkpointsBefore, got)
	}

	// Semantic violation (path escape) must fail AFTER the pre-patch
	// checkpoint while leaving the workspace untouched.
	evil := Patch{Files: []PatchFile{{Path: "../evil.txt", Content: strPtr("x")}}}
	evilPath := filepath.Join(workDir, "evil.json")
	raw, _ := json.Marshal(evil)
	if err := os.WriteFile(evilPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ApplyPatch(workDir, evilPath); err == nil {
		t.Fatal("expected error for escaping path")
	}
	if got, _ := os.ReadFile(filepath.Join(workDir, "a.txt")); string(got) != before {
		t.Fatalf("workspace touched on evil patch: %q", got)
	}
	if _, err := os.Stat(filepath.Join(workDir, "evil.txt")); !os.IsNotExist(err) {
		t.Fatal("evil.txt must not exist")
	}
	if got := countCheckpoints(t, workDir); got != checkpointsBefore+1 {
		t.Fatalf("expected preserved pre-patch checkpoint, before=%d after=%d", checkpointsBefore, got)
	}
}

func TestSaveEvidence(t *testing.T) {
	workDir := t.TempDir()
	path, err := SaveEvidence(workDir, map[string]any{"ok": true})
	if err != nil {
		t.Fatalf("SaveEvidence: %v", err)
	}
	base := filepath.Base(path)
	if !strings.HasPrefix(base, "evidence_") || !strings.HasSuffix(base, ".json") {
		t.Fatalf("evidence path = %q", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc Evidence
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("decode evidence: %v", err)
	}
	if len(doc.ID) != 26 {
		t.Fatalf("ULID length = %d, want 26 (%q)", len(doc.ID), doc.ID)
	}
}

func countCheckpoints(t *testing.T, workDir string) int {
	t.Helper()
	dir := filepath.Join(workDir, ".izen", "checkpoints")
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}
