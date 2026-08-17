package retrieval

import (
	"os"
	"path/filepath"
	"testing"
)

// ── Phase 1 Invariant 8: Lynx is an optional evidence/search capability ─────
//
// Lynx (the external `lx` binary) is a separate Rust repository. Izen detects
// it at startup and enables hybrid search when present; it MUST continue
// operating through its native search when absent. Lynx availability must
// NEVER determine which execution architecture is used — the RuntimeExecutor
// is the execution authority regardless.

// TestRouterFallsBackToNativeWithoutLynx proves the Lynx-unavailable path:
// with `lx` absent from PATH, the router activates the native Go search
// engine — Izen keeps working with its fallback search capability.
func TestRouterFallsBackToNativeWithoutLynx(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // a PATH with no lx binary
	r := NewRouter(".", nil)
	if r.Type() != EngineNative {
		t.Fatalf("router type = %s, want native (lx unavailable → fallback search)", r.Type())
	}
	if r.IsLynx() {
		t.Fatal("router must not report lynx when the lx binary is absent")
	}
	if _, ok := r.Engine().(*NativeGoEngine); !ok {
		t.Fatalf("fallback engine = %T, want *NativeGoEngine", r.Engine())
	}
}

// TestRouterEnablesLynxWhenAvailable proves the Lynx-available path: a real
// `lx` binary in PATH activates the hybrid Lynx search engine.
func TestRouterEnablesLynxWhenAvailable(t *testing.T) {
	// A fake lx binary on PATH is enough for detection — the adapter only
	// shells out lazily on actual searches.
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "lx"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)
	r := NewRouter(".", nil)
	if r.Type() != EngineLynx {
		t.Fatalf("router type = %s, want lynx (lx available → enhanced hybrid search)", r.Type())
	}
	if !r.IsLynx() {
		t.Fatal("router must report lynx when the lx binary is present")
	}
	if _, ok := r.Engine().(*LynxAdapter); !ok {
		t.Fatalf("engine = %T, want *LynxAdapter", r.Engine())
	}
}

// TestRouterExplicitEngineOverrideIsDeterministic proves the router accepts an
// explicit engine — the seam tests use to pin behavior without a real lx
// binary. The execution architecture does not depend on this choice.
func TestRouterExplicitEngineOverrideIsDeterministic(t *testing.T) {
	r := NewRouterWithEngine(NewNativeGoEngine("."), EngineNative, nil)
	if r.Type() != EngineNative || r.IsLynx() {
		t.Fatal("explicit native engine must be honored")
	}
	r2 := NewRouterWithEngine(NewLynxAdapter("/usr/bin/lx", "."), EngineLynx, nil)
	if r2.Type() != EngineLynx || !r2.IsLynx() {
		t.Fatal("explicit lynx engine must be honored")
	}
}
