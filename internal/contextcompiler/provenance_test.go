package contextcompiler

import (
	"context"
	"strings"
	"testing"
)

// ── PHASE 14: SEMANTIC CONTEXT PROVENANCE ───────────────────────────────────
//
// Context validity is a semantic question — did the compilation carry the
// workspace material the active intent's contract requires? — and NOT an
// arithmetic one. The rule this suite replaces treated an arbitrary token
// threshold as a proxy for validity, which is wrong in both directions at once:
// a huge read-only projection under a mutation intent is invalid no matter how
// well it filled the budget, and a tiny but scope-correct projection is valid no
// matter how little it spent.

func compiledWith(paths ...string) *CompiledContext {
	out := &CompiledContext{UsedTokens: 10, AdmittedPaths: append([]string(nil), paths...)}
	if len(paths) > 0 {
		out.Sections = append(out.Sections, Section{
			Source:  SourceArtifacts,
			Header:  "Workspace files",
			Content: "package main\n",
		})
	}
	return out
}

// TestProvenance_ScopeMustMatchRequestedTargets pins condition (b): every
// requested target must appear in the payload's admitted scope. A context that
// silently dropped a requested target is invalid no matter its size.
func TestProvenance_ScopeMustMatchRequestedTargets(t *testing.T) {
	c := compiledWith("note.txt")
	p := c.ValidateContextProvenance(IntentBinding{
		Active: "modification", Required: IntentContextWorkspace,
	}, []string{"note.txt"})
	if !p.Valid || !p.ScopeMatched {
		t.Fatalf("provenance = %+v, want a valid, scope-matched verdict", p)
	}

	missing := c.ValidateContextProvenance(IntentBinding{
		Active: "modification", Required: IntentContextWorkspace,
	}, []string{"note.txt", "other.go"})
	if missing.Valid {
		t.Fatal("a payload missing a requested target was accepted")
	}
	if missing.ScopeMatched {
		t.Fatal("scope must be reported as unmatched")
	}
	if len(missing.MissingTargets) != 1 || missing.MissingTargets[0] != "other.go" {
		t.Fatalf("missing targets = %v, want [other.go]", missing.MissingTargets)
	}
	if !strings.Contains(missing.Reason, "other.go") {
		t.Fatalf("reason must name the missing target, got %q", missing.Reason)
	}
}

// TestProvenance_TruncatedTargetCountsAsPresent pins that a source-side read cap
// is a TRUTHFUL ADMISSION, not a silent drop. The payload says "I could not
// include all of this", which is a different fact from "I never looked at it".
func TestProvenance_TruncatedTargetCountsAsPresent(t *testing.T) {
	c := compiledWith("big.go")
	c.TruncatedFiles = []string{"big.go"}
	p := c.ValidateContextProvenance(IntentBinding{
		Active: "modification", Required: IntentContextWorkspace,
	}, []string{"big.go"})
	if !p.Valid {
		t.Fatalf("a truncated target must still satisfy scope, got %+v", p)
	}
	if len(p.TruncatedTargets) != 1 || p.TruncatedTargets[0] != "big.go" {
		t.Fatalf("truncated targets = %v, want [big.go]", p.TruncatedTargets)
	}
	if len(p.MissingTargets) != 0 {
		t.Fatalf("a truncated target must not be reported as missing: %v", p.MissingTargets)
	}
}

// TestProvenance_TokenCountsAreTelemetryOnly is the anti-regression for the rule
// this phase removes. Two payloads with OPPOSITE verdicts and wildly different
// token counts must both be judged on scope/provenance/intent alone, and a
// token count must never appear in the verdict logic.
func TestProvenance_TokenCountsAreTelemetryOnly(t *testing.T) {
	tiny := compiledWith("note.txt")
	tiny.UsedTokens = 1
	huge := compiledWith("note.txt")
	huge.UsedTokens = 500_000

	binding := IntentBinding{Active: "modification", Required: IntentContextWorkspace}
	small := tiny.ValidateContextProvenance(binding, []string{"note.txt"})
	large := huge.ValidateContextProvenance(binding, []string{"note.txt"})
	if !small.Valid || !large.Valid {
		t.Fatalf("both correctly-scoped payloads must be valid: small=%+v large=%+v", small, large)
	}
	if small.ObservedTokens != 1 || large.ObservedTokens != 500_000 {
		t.Fatalf("token counts must be carried as telemetry: %d / %d", small.ObservedTokens, large.ObservedTokens)
	}

	// The inverse: a payload with NO workspace material is refused whatever it
	// claims to have spent.
	empty := &CompiledContext{UsedTokens: 900_000}
	refused := empty.ValidateContextProvenance(binding, nil)
	if refused.Valid {
		t.Fatal("an empty payload satisfied a workspace contract")
	}
	if !strings.Contains(refused.Reason, "workspace material") {
		t.Fatalf("reason must name the missing material, got %q", refused.Reason)
	}
	if refused.ObservedTokens != 900_000 {
		t.Fatalf("a refused verdict must still carry the telemetry figure, got %d", refused.ObservedTokens)
	}
}

// TestProvenance_IntentBindingIsMandatory pins condition (a): a compilation with
// no canonical intent binding is refused outright, because every other fact
// about it is about a different task.
func TestProvenance_IntentBindingIsMandatory(t *testing.T) {
	c := compiledWith("note.txt")
	p := c.ValidateContextProvenance(IntentBinding{Required: IntentContextWorkspace}, []string{"note.txt"})
	if p.Valid {
		t.Fatal("a payload with no intent binding was accepted")
	}
	if p.IntentMatched {
		t.Fatal("an unbound payload must report a mismatched intent")
	}
	if !strings.Contains(p.Reason, "canonical intent") {
		t.Fatalf("reason must name the missing binding, got %q", p.Reason)
	}
}

// TestProvenance_NilPayloadFailsClosed pins the degenerate case.
func TestProvenance_NilPayloadFailsClosed(t *testing.T) {
	var c *CompiledContext
	p := c.ValidateContextProvenance(IntentBinding{Active: "modification", Required: IntentContextWorkspace}, []string{"note.txt"})
	if p.Valid {
		t.Fatal("a nil payload was accepted")
	}
	if p.Reason == "" {
		t.Fatal("a nil payload must explain itself")
	}
}

// TestProvenance_SelfContainedAndNoneContractsDoNotDemandWorkspaceMaterial
// pins the anti-over-refusal direction: a direct answer with no workspace
// projection is a COMPLETE answer, and the gate must not invent a failure for
// it.
func TestProvenance_SelfContainedAndNoneContractsDoNotDemandWorkspaceMaterial(t *testing.T) {
	empty := &CompiledContext{UsedTokens: 0}
	for _, required := range []string{IntentContextNone, IntentContextSelfContained, ""} {
		p := empty.ValidateContextProvenance(IntentBinding{Active: "conversation", Required: required}, nil)
		if !p.Valid {
			t.Fatalf("required=%q: a zero-workspace turn must be valid, got %+v", required, p)
		}
		if p.WorkspaceMaterialPresent {
			t.Fatalf("required=%q: workspace material must not be reported present", required)
		}
	}
}

// TestProvenance_IntentRevisionDropsTheCachedProjection is INVARIANT 5 step 1
// at the compiler layer: a blocking intent revision must invalidate every
// payload compiled under the PREVIOUS intent, so a read-only projection can
// never be served as the context for a mutation contract.
//
// The two compiles below are byte-identical inputs, so without the invalidation
// the second would be a pure cache hit — indistinguishable, to the caller, from
// a correct re-compilation under the mutation contract.
func TestProvenance_IntentRevisionDropsTheCachedProjection(t *testing.T) {
	compiler := New(WithMaxTokens(4000))
	input := Input{
		UserRequest:   "modification",
		Phase:         PhaseExecute,
		ContextPolicy: "target_file_only",
		Scope:         "note.txt",
		Files: []FileContext{{
			Path: "note.txt", Size: 12, Content: "foo\nbar\nbaz\n", Critical: true,
		}},
	}
	first, err := compiler.Compile(context.Background(), input)
	if err != nil {
		t.Fatalf("first Compile: %v", err)
	}
	if first.CacheHit {
		t.Fatal("the first compilation cannot be a cache hit")
	}
	second, err := compiler.Compile(context.Background(), input)
	if err != nil {
		t.Fatalf("second Compile: %v", err)
	}
	if !second.CacheHit {
		t.Fatal("identical inputs must reuse the cached projection")
	}

	// The blocking revision drops every projection compiled under the old intent.
	compiler.InvalidateCache()
	if compiler.CacheSize() != 0 {
		t.Fatalf("cache size after revision = %d, want 0", compiler.CacheSize())
	}
	third, err := compiler.Compile(context.Background(), input)
	if err != nil {
		t.Fatalf("post-revision Compile: %v", err)
	}
	if third.CacheHit {
		t.Fatal("the post-revision compilation reused a pre-revision projection")
	}
	// The re-compiled payload still satisfies the mutation contract on its own
	// merits, which is the point: the verdict comes from provenance, not from
	// the fact that the cache was flushed.
	p := third.ValidateContextProvenance(IntentBinding{
		Active: "modification", Required: IntentContextWorkspace,
	}, []string{"note.txt"})
	if !p.Valid {
		t.Fatalf("post-revision provenance = %+v, want valid", p)
	}
}

// TestProvenance_CompilesRealWorkspaceMaterialAndValidates is the end-to-end
// wiring proof: a real compilation over a real file carries the admitted scope
// and satisfies the mutation contract without any token arithmetic.
func TestProvenance_CompilesRealWorkspaceMaterialAndValidates(t *testing.T) {
	compiler := New(WithMaxTokens(4000))
	compiled, err := compiler.Compile(context.Background(), Input{
		UserRequest:   "modification",
		Phase:         PhaseExecute,
		ContextPolicy: "target_file_only",
		Scope:         "note.txt",
		Files: []FileContext{{
			Path: "note.txt", Size: 12, Content: "foo\nbar\nbaz\n", Critical: true,
		}},
	})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	p := compiled.ValidateContextProvenance(IntentBinding{
		Active: "modification", Required: IntentContextWorkspace,
	}, []string{"note.txt"})
	if !p.Valid {
		t.Fatalf("a real compilation of the declared target must be valid: %+v", p)
	}
	if !p.ScopeMatched || !p.WorkspaceMaterialPresent {
		t.Fatalf("provenance = %+v, want scope matched with workspace material", p)
	}
	// A different target was never in the payload.
	other := compiled.ValidateContextProvenance(IntentBinding{
		Active: "modification", Required: IntentContextWorkspace,
	}, []string{"other.go"})
	if other.Valid {
		t.Fatal("a payload that never carried other.go satisfied a request for it")
	}
}
