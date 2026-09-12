package architecture

import (
	"os"
	"strings"
	"testing"
)

// TestRuntimeHasNoUpwardImports pins the top-down dependency DAG:
//
//	cmd/ / tui/ / ui / cli / app  →  internal/runtime/  →  internal/domain/  →  internal/pkg/
//
// internal/runtime/* MUST NEVER import presentation/entry layers
// (internal/tui, internal/cli, internal/app, internal/ui). A runtime import
// of the entry layer is an architectural regression.
func TestRuntimeHasNoUpwardImports(t *testing.T) {
	root := repoRoot(t)
	banned := []string{
		moduleImport("internal/tui"),
		moduleImport("internal/cli"),
		moduleImport("internal/app"),
		moduleImport("internal/ui"),
		moduleImport("cmd/izen"),
	}
	for _, rel := range goFilesUnder(root) {
		if !strings.HasPrefix(rel, "internal/runtime/") {
			continue
		}
		f, _ := parseFile(t, root+"/"+rel)
		got := imports(f)
		for _, b := range banned {
			for imp := range got {
				if imp == b || strings.HasPrefix(imp, b+"/") {
					t.Errorf("architecture: %s imports upward layer %s — runtime must never depend on entry layers", rel, imp)
				}
			}
		}
	}
}

// TestDurableTypesSerializationStable pins replay compatibility: the JSON
// tags in internal/runtime/durable/types.go MUST NOT change. Event logs in
// .izen/runtime/ledger.ndjson must remain byte-for-byte backward compatible.
func TestDurableTypesSerializationStable(t *testing.T) {
	root := repoRoot(t)
	raw, err := os.ReadFile(root + "/internal/runtime/durable/types.go")
	if err != nil {
		t.Fatalf("architecture: read durable types: %v", err)
	}
	src := string(raw)
	for _, tag := range []string{
		`"taskId"`, `"stepId"`, `"operationId"`, `"phase"`,
		`"preconditionDigest"`, `"postconditionDigest"`, `"status"`,
		`"activeTargetScope"`, `"currentStepId"`, `"lastCheckpointId"`,
		`"eventId"`, `"eventType"`, `"timestamp"`, `"payload"`,
	} {
		if !strings.Contains(src, tag) {
			t.Errorf("architecture: durable/types.go lost serialization tag %s — ledger replay compatibility broken", tag)
		}
	}
}
