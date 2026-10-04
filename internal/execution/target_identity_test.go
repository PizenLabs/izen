package execution

// ── TARGET IDENTITY: the exactness tests ───────────────────────────────────
//
// The live failure this file pins down:
//
//	resolved scope: [index.html, script.js, styles.css]
//	model request:  read_file(style.css)
//	observed:       "style.css: no such file or directory"  (twice)
//
// The invariant under test is that `style.css` NEVER becomes `styles.css`, and
// that a request for it produces typed evidence rather than a silent substitute.
//
// These tests are pure functions of the resolver — no provider, no filesystem —
// because the whole point is that the decision does not depend on anything the
// model said or on any similarity heuristic.

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// portfolioScope is the authoritative resolved scope from the benchmark run.
var portfolioScope = []string{"index.html", "script.js", "styles.css"}

func TestResolveTargetRequest_ExactScopeMemberIsAuthorized(t *testing.T) {
	for _, target := range portfolioScope {
		got := ResolveTargetRequest(target, TargetScopeEvidence{
			Scope:  portfolioScope,
			Exists: func(string) bool { return true },
		})
		if got.Status != TargetRequestInScope {
			t.Errorf("%s: status = %s, want IN_SCOPE", target, got.Status)
		}
		if !got.Authorized() {
			t.Errorf("%s: must carry mutation authority as a scope member", target)
		}
		if got.Resolved != target {
			t.Errorf("%s: resolved = %q, want %q", target, got.Resolved, target)
		}
	}
}

// TestResolveTargetRequest_NearMissIsNeverSubstituted is THE test for the live
// failure. It asserts every approximation is refused, and — critically — that
// `Resolved` stays EMPTY, so "nothing was substituted" is checkable rather than
// merely asserted.
func TestResolveTargetRequest_NearMissIsNeverSubstituted(t *testing.T) {
	// Every one of these is a plausible typo of an in-scope target. The runtime
	// must refuse each and substitute none.
	nearMisses := []string{
		"style.css",      // missing trailing 's'  (the live failure)
		"styles.cs",      // missing trailing 's' of the extension
		"stylesCSS",      // wrong case in the stem
		"Styles.css",     // capitalised basename
		"style",          // stem only
		"styles",         // stem only
		"main.css",       // different name, same extension
		"css/styles.css", // different directory
		"styles.css.bak", // different extension
		"./style.css",    // decoration without the real typo
	}
	exists := func(p string) bool {
		for _, m := range portfolioScope {
			if m == p {
				return true
			}
		}
		return false
	}
	for _, req := range nearMisses {
		got := ResolveTargetRequest(req, TargetScopeEvidence{Scope: portfolioScope, Exists: exists})
		if got.Authorized() {
			t.Errorf("%q: status = %s — a near-miss was authorized", req, got.Status)
		}
		if got.Resolved != "" {
			t.Errorf("%q: resolved = %q — a target was SUBSTITUTED", req, got.Resolved)
		}
		if got.Requested != req {
			t.Errorf("%q: requested was rewritten to %q", req, got.Requested)
		}
		if got.Status != TargetRequestNotFound {
			t.Errorf("%q: status = %s, want NOT_FOUND", req, got.Status)
		}
	}
}

// TestResolveTargetRequest_ScopeIsNeverMutated proves a refusal leaves the
// authoritative scope byte-for-byte unchanged.
func TestResolveTargetRequest_ScopeIsNeverMutated(t *testing.T) {
	before := append([]string(nil), portfolioScope...)
	got := ResolveTargetRequest("style.css", TargetScopeEvidence{
		Scope:  portfolioScope,
		Exists: func(string) bool { return false },
	})
	if got.Status != TargetRequestNotFound {
		t.Fatalf("status = %s, want NOT_FOUND", got.Status)
	}
	if len(got.Scope) != len(before) {
		t.Fatalf("returned scope = %v, want the unchanged %v", got.Scope, before)
	}
	for i := range before {
		if got.Scope[i] != before[i] {
			t.Fatalf("returned scope = %v, want the unchanged %v", got.Scope, before)
		}
	}
}

// TestResolveTargetRequest_StatusVocabularyIsTotal pins the closed vocabulary:
// every distinguishable situation maps to exactly one member, and no member is
// reachable by accident.
func TestResolveTargetRequest_StatusVocabularyIsTotal(t *testing.T) {
	realFile := func(name string) func(string) bool {
		return func(p string) bool { return p == name }
	}
	cases := []struct {
		name string
		req  string
		ev   TargetScopeEvidence
		want TargetRequestStatus
	}{
		{
			name: "exact scope member",
			req:  "styles.css",
			ev:   TargetScopeEvidence{Scope: portfolioScope},
			want: TargetRequestInScope,
		},
		{
			name: "exists on disk but out of scope is OBSERVED context",
			req:  "notes.md",
			ev:   TargetScopeEvidence{Scope: portfolioScope, Exists: realFile("notes.md")},
			want: TargetRequestObserved,
		},
		{
			name: "absent everywhere is NOT_FOUND",
			req:  "style.css",
			ev:   TargetScopeEvidence{Scope: portfolioScope, Exists: realFile("styles.css")},
			want: TargetRequestNotFound,
		},
		{
			name: "directory is never a file",
			req:  "assets",
			ev: TargetScopeEvidence{
				Scope:  portfolioScope,
				IsDir:  func(string) bool { return true },
				Exists: func(string) bool { return true },
			},
			want: TargetRequestOutsideScope,
		},
		{
			name: "empty request against a multi-target scope is AMBIGUOUS",
			req:  "",
			ev:   TargetScopeEvidence{Scope: portfolioScope},
			want: TargetRequestAmbiguous,
		},
		{
			name: "empty request against an empty scope is not a target at all",
			req:  "",
			ev:   TargetScopeEvidence{},
			want: TargetRequestOutsideScope,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolveTargetRequest(tc.req, tc.ev)
			if got.Status != tc.want {
				t.Fatalf("status = %s, want %s (reason: %s)", got.Status, tc.want, got.Reason)
			}
		})
	}
	if n := len(AllTargetRequestStatuses()); n != 5 {
		t.Fatalf("target-request vocabulary has %d members, want a closed set of 5", n)
	}
}

// TestReadFile_TypedRefusalForMissingFile proves the failure the live run saw is
// now TYPED rather than a bare *PathError, so classification never depends on
// matching "no such file or directory" in prose.
func TestReadFile_TypedRefusalForMissingFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "styles.css"), []byte("body{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := NewReadOnlyToolRunner(root)

	_, err := r.readFile(`{"path":"style.css"}`)
	if err == nil {
		t.Fatal("reading a nonexistent target must fail")
	}
	if !errors.Is(err, ErrTargetNotFound) {
		t.Fatalf("err = %v, want it to classify as ErrTargetNotFound", err)
	}
	var typed *TargetNotFoundError
	if !errors.As(err, &typed) {
		t.Fatalf("err = %v, want a *TargetNotFoundError", err)
	}
	// The requested name is preserved verbatim — the refusal names what was
	// asked, not what the runtime wished had been asked.
	if typed.Request.Requested != "style.css" {
		t.Fatalf("requested = %q, want the verbatim %q", typed.Request.Requested, "style.css")
	}
	if typed.Request.Resolved != "" {
		t.Fatalf("resolved = %q — the refusal substituted a target", typed.Request.Resolved)
	}

	// The real file is still readable, and reading it returns its own bytes.
	out, err := r.readFile(`{"path":"styles.css"}`)
	if err != nil {
		t.Fatalf("reading the real file failed: %v", err)
	}
	if out != "body{}" {
		t.Fatalf("content = %q, want the real file bytes", out)
	}
}
