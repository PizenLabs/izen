package substrate

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/PizenLabs/izen/internal/pkg/atomicio"
)

func TestApplyPatchTransactionalRollback(t *testing.T) {
	workDir := t.TempDir()
	// 5 pre-existing files with known content.
	orig := map[string]string{}
	for i := 1; i <= 5; i++ {
		name := fmt.Sprintf("f%d.txt", i)
		orig[name] = fmt.Sprintf("orig-%d", i)
		if err := os.WriteFile(filepath.Join(workDir, name), []byte(orig[name]), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Build a 5-file patch that rewrites every file.
	var files []PatchFile
	for i := 1; i <= 5; i++ {
		name := fmt.Sprintf("f%d.txt", i)
		c := fmt.Sprintf("patched-%d", i)
		files = append(files, PatchFile{Path: name, Content: &c})
	}
	raw, _ := json.Marshal(Patch{Files: files})
	patchPath := filepath.Join(workDir, "tx.json")
	if err := os.WriteFile(patchPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	// Inject failure on file 3.
	prev := atomicWriteFile
	calls := 0
	atomicWriteFile = func(filename string, data []byte, perm os.FileMode) error {
		calls++
		if calls == 3 {
			return fmt.Errorf("injected failure on %s", filename)
		}
		return atomicio.WriteFileAtomic(filename, data, perm)
	}
	defer func() { atomicWriteFile = prev }()

	err := ApplyPatch(workDir, patchPath)
	if err == nil {
		t.Fatal("expected transaction failure")
	}
	if !errors.Is(err, ErrPatchTransactionFailed) {
		t.Fatalf("expected ErrPatchTransactionFailed, got: %v", err)
	}

	// Files 1 and 2 must be rolled back to pre-patch state; 3-5 untouched.
	for i := 1; i <= 5; i++ {
		name := fmt.Sprintf("f%d.txt", i)
		got, rerr := os.ReadFile(filepath.Join(workDir, name))
		if rerr != nil {
			t.Fatalf("read %s: %v", name, rerr)
		}
		if string(got) != orig[name] {
			t.Fatalf("%s = %q, want pre-patch %q", name, got, orig[name])
		}
	}
	// No atomic temp leftovers.
	for i := 1; i <= 5; i++ {
		entries, _ := os.ReadDir(workDir)
		for _, e := range entries {
			if len(e.Name()) > 4 && contains(e.Name(), ".tmp.") {
				t.Fatalf("leftover temp artifact: %s", e.Name())
			}
		}
		_ = i
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
