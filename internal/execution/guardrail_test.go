package execution

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// writeApplyLog appends a single action=apply entry to the audit log for root.
func writeApplyLog(t *testing.T, root, ts, ctx, file, patchID string) {
	t.Helper()
	auditDir := filepath.Join(root, ".izen", "audit")
	if err := os.MkdirAll(auditDir, 0755); err != nil {
		t.Fatalf("mkdir audit: %v", err)
	}
	f, err := os.OpenFile(filepath.Join(auditDir, "mutations.log"),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	defer func() { _ = f.Close() }()
	line := fmt.Sprintf("[%s] context=%s file=%s patch=%s action=apply\n", ts, ctx, file, patchID)
	if _, err := f.WriteString(line); err != nil {
		t.Fatalf("write log: %v", err)
	}
}

func TestGuardrailHaltsOnThreshold(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)

	g := NewMutationGuardrail(dir)
	g.now = func() time.Time { return now }
	g.window = 60 * time.Second
	g.limit = 3

	file := "jwt.go"
	ctx := "#42"
	base := now.Add(-10 * time.Second).Format(time.RFC3339)
	for i := 0; i < 3; i++ {
		writeApplyLog(t, dir, base, ctx, file, fmt.Sprintf("p%d", i))
	}

	// The 4th attempt should be halted: 3 prior applies >= limit.
	dec := g.Check(file, ctx)
	if !dec.Halt {
		t.Fatalf("expected halt, got %+v", dec)
	}
	if dec.Count != 3 {
		t.Fatalf("expected count 3, got %d", dec.Count)
	}
	if !strings.Contains(dec.Message(), "jwt.go") {
		t.Fatalf("message missing file: %s", dec.Message())
	}
	if !strings.Contains(dec.Message(), "3 attempts") {
		t.Fatalf("message missing count: %s", dec.Message())
	}
}

func TestGuardrailAllowsBelowThreshold(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)

	g := NewMutationGuardrail(dir)
	g.now = func() time.Time { return now }
	g.window = 60 * time.Second
	g.limit = 3

	file := "routes.go"
	ctx := "#42"
	base := now.Add(-10 * time.Second).Format(time.RFC3339)
	writeApplyLog(t, dir, base, ctx, file, "p0")
	writeApplyLog(t, dir, base, ctx, file, "p1")

	dec := g.Check(file, ctx)
	if dec.Halt {
		t.Fatalf("expected no halt with 2 prior applies, got %+v", dec)
	}
	if dec.Count != 2 {
		t.Fatalf("expected count 2, got %d", dec.Count)
	}
}

func TestGuardrailIgnoresStaleEntries(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)

	g := NewMutationGuardrail(dir)
	g.now = func() time.Time { return now }
	g.window = 60 * time.Second
	g.limit = 3

	file := "jwt.go"
	ctx := "#42"
	// All entries are 90s old — outside the 60s window.
	stale := now.Add(-90 * time.Second).Format(time.RFC3339)
	for i := 0; i < 5; i++ {
		writeApplyLog(t, dir, stale, ctx, file, fmt.Sprintf("p%d", i))
	}

	dec := g.Check(file, ctx)
	if dec.Halt {
		t.Fatalf("expected stale entries to be ignored, got %+v", dec)
	}
	if dec.Count != 0 {
		t.Fatalf("expected count 0, got %d", dec.Count)
	}
}

func TestGuardrailScopedByContext(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)

	g := NewMutationGuardrail(dir)
	g.now = func() time.Time { return now }
	g.window = 60 * time.Second
	g.limit = 3

	file := "jwt.go"
	base := now.Add(-10 * time.Second).Format(time.RFC3339)
	// Another transaction hammered the same file; our context must not see it.
	for i := 0; i < 5; i++ {
		writeApplyLog(t, dir, base, "#other", file, fmt.Sprintf("p%d", i))
	}

	dec := g.Check(file, "#42")
	if dec.Halt {
		t.Fatalf("expected context scoping to ignore other ctx, got %+v", dec)
	}
	if dec.Count != 0 {
		t.Fatalf("expected count 0 across contexts, got %d", dec.Count)
	}
}

func TestGuardrailAllowsMultiFileRefactor(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)

	g := NewMutationGuardrail(dir)
	g.now = func() time.Time { return now }
	g.window = 60 * time.Second
	g.limit = 3

	ctx := "#42"
	base := now.Add(-10 * time.Second).Format(time.RFC3339)
	// A legitimate refactor touches many files once each — never the same file
	// three times.
	for _, f := range []string{"a.go", "b.go", "c.go", "d.go"} {
		writeApplyLog(t, dir, base, ctx, f, "p")
	}

	for _, f := range []string{"a.go", "b.go", "c.go", "d.go"} {
		if dec := g.Check(f, ctx); dec.Halt {
			t.Fatalf("multi-file refactor wrongly halted on %s: %+v", f, dec)
		}
	}
}

func TestGuardrailMissingLogSafe(t *testing.T) {
	dir := t.TempDir()
	g := NewMutationGuardrail(dir)
	g.limit = 3
	dec := g.Check("nope.go", "#1")
	if dec.Halt || dec.Count != 0 {
		t.Fatalf("expected safe no-op on missing log, got %+v", dec)
	}
}

// TestApplyHaltOnMutationLoop verifies that the guardrail wired into
// PatchManager.Apply aborts before committing and that the audit log entry
// already written for prior applies is what trips it.
func TestApplyHaltOnMutationLoop(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)

	pm := NewPatchManager(dir)
	pm.SetContextID("#42")

	g := NewMutationGuardrail(dir)
	g.now = func() time.Time { return now }
	g.window = 60 * time.Second
	g.limit = 3
	pm.SetGuardrail(g)

	file := "jwt.go"
	fullPath := filepath.Join(dir, file)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(fullPath, []byte("v0"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	apply := func(content string) error {
		patch := &Patch{ID: fmt.Sprintf("p-%d", now.UnixNano()), File: file, Modified: content}
		return pm.Apply(patch)
	}

	// Two successful applies record two action=apply entries.
	for i := 1; i <= 2; i++ {
		if err := apply(fmt.Sprintf("v%d", i)); err != nil {
			t.Fatalf("unexpected apply error #%d: %v", i, err)
		}
	}

	// Third apply should be halted by the guardrail (count 2 < 3, allowed),
	// so do it a third time then a fourth to trip the limit.
	if err := apply("v3"); err != nil {
		t.Fatalf("third apply should be allowed: %v", err)
	}
	err := apply("v4")
	if err == nil {
		t.Fatal("expected fourth apply to be halted by guardrail")
	}
	if !strings.Contains(err.Error(), "GUARDRAIL TRIGGERED") {
		t.Fatalf("unexpected halt error: %v", err)
	}
	// File must not have been mutated on the halted attempt.
	data, _ := os.ReadFile(fullPath)
	if string(data) != "v3" {
		t.Fatalf("halted apply must not mutate file, got %q", string(data))
	}
}

// TestApplyGuardrailDisabled verifies a nil guardrail does not block applies.
func TestApplyGuardrailDisabled(t *testing.T) {
	dir := t.TempDir()
	pm := NewPatchManager(dir)
	pm.SetGuardrail(nil)

	file := "routes.go"
	fullPath := filepath.Join(dir, file)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(fullPath, []byte("v0"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	for i := 0; i < 10; i++ {
		if err := pm.Apply(&Patch{ID: fmt.Sprintf("p%d", i), File: file, Modified: fmt.Sprintf("v%d", i)}); err != nil {
			t.Fatalf("apply %d should not be blocked when guardrail disabled: %v", i, err)
		}
	}
}

// guardrailConcurrentCheck exercises Check under concurrency to ensure the
// mutex-guarded clock read never races.
func TestGuardrailConcurrentCheck(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	g := NewMutationGuardrail(dir)
	g.now = func() time.Time { return now }
	g.window = 60 * time.Second
	g.limit = 3

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = g.Check("jwt.go", "#1")
		}()
	}
	wg.Wait()
}
