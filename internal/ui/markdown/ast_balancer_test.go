package markdown

import (
	"strings"
	"testing"
)

// TestBalanceTrailingLineClosesOpenDelimiters is the core contract: a trailing
// line whose inline delimiter has no closer yet is returned with a transient
// closer appended, so the frame's AST pass styles the text on the FIRST tick
// instead of showing the raw marker.
func TestBalanceTrailingLineClosesOpenDelimiters(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"strong", "**The migration is complete", "**The migration is complete**"},
		{"emphasis", "*a thought worth italicising", "*a thought worth italicising*"},
		{"triple", "***loud and clear", "***loud and clear***"},
		{"code span", "call `go build ./...` now", "call `go build ./...` now"},
		{"open code span", "call `go build", "call `go build`"},
		{"strikethrough", "the ~~old name", "the ~~old name~~"},
		{"mid sentence", "the answer is **yes", "the answer is **yes**"},
		{"trailing space keeps content adjacent", "**bold ", "**bold** "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := BalanceTrailingLine(tc.in); got != tc.want {
				t.Errorf("BalanceTrailingLine(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestBalanceTrailingLineLeavesBalancedLinesAlone is the no-op contract, and it
// is what keeps the function free: a line with nothing open must come back as
// the very same string, byte for byte, so the caller can detect "nothing was
// added" with one equality check and never pays for a copy.
func TestBalanceTrailingLineLeavesBalancedLinesAlone(t *testing.T) {
	for _, in := range []string{
		"",
		"   ",
		"a plain sentence with no markup at all",
		"**already bold**",
		"*already italic* and `already code`",
		"2 * 3 = 6",
		"* a list bullet, not emphasis",
		"** a bullet marker run",
		"a * b * c",
		`\*\*escaped markers are literal\*\*`,
		"a single ~ tilde is text",
		"snake_case_identifier",
		"| Name | Age |",
		"## A heading",
		"see [the docs](https://example.com) for detail",
	} {
		got := BalanceTrailingLine(in)
		if got != in {
			t.Errorf("BalanceTrailingLine(%q) = %q, want it unchanged", in, got)
		}
	}
}

// TestBalanceTrailingLineNeverMutatesItsInput is the buffer-safety contract. The
// result must be a separate string whenever anything was appended, so the
// caller's raw message bytes can be kept, re-scanned next frame, and committed
// verbatim at the newline.
func TestBalanceTrailingLineNeverMutatesItsInput(t *testing.T) {
	const raw = "**The migration is complete"
	balanced := BalanceTrailingLine(raw)
	if balanced != "**The migration is complete**" {
		t.Fatalf("balanced = %q", balanced)
	}
	if raw != "**The migration is complete" {
		t.Fatalf("the input was mutated: %q", raw)
	}
	// The raw string is still the input to the next frame's scan, and it is
	// still exactly what gets committed at the newline.
	if again := BalanceTrailingLine(raw); again != balanced {
		t.Errorf("re-balancing the same raw line is not deterministic: %q vs %q", again, balanced)
	}
}

// TestBalanceTrailingLineIsDeterministic pins that the same input always yields
// the same output. The frame loop re-balances the same prefix on every tick it
// has nothing new to do, and a frame that rendered differently from the one
// before it would be exactly the flicker this exists to remove.
func TestBalanceTrailingLineIsDeterministic(t *testing.T) {
	for _, in := range []string{"**a", "`b", "~~c", "*d **e *f", "***g"} {
		first := BalanceTrailingLine(in)
		for i := 0; i < 8; i++ {
			if got := BalanceTrailingLine(in); got != first {
				t.Fatalf("BalanceTrailingLine(%q) drifted: %q then %q", in, first, got)
			}
		}
	}
}

// TestBalancedLineIsStructurallyFinal is the property the frame gate relies on:
// after balancing, the line must no longer be reported as an incomplete inline
// construct. If it were still incomplete the caller would fall back to the
// hold-back, and the balancing would be a no-op.
func TestBalancedLineIsStructurallyFinal(t *testing.T) {
	for _, in := range []string{
		"**The migration is complete",
		"*a thought",
		"the answer is **yes",
		"the ~~old name",
		"call `go build",
	} {
		balanced := BalanceTrailingLine(in)
		if kind, incomplete := Incomplete(balanced, false, false); incomplete {
			t.Errorf("balanced %q (from %q) is still incomplete: (%v, true)", balanced, in, kind)
		}
	}
}

// TestAmbiguousNestedLineIsLeftRaw pins the conservative branch of the verify
// gate. A line that opens two nested delimiters and closes neither cannot be
// closed without creating a marker run that means something else — `**bold
// *italic` would become `***italic***`, a different reading rather than a closed
// one. So the balancer declines to guess and hands the line back untouched, and
// the holdback renders it exactly as it did before this file existed.
//
// The alternative — returning the ambiguous line — would put a marker on screen
// that the holdback detector still considers OPEN, which is the very artefact
// the balancer exists to remove.
func TestAmbiguousNestedLineIsLeftRaw(t *testing.T) {
	for _, in := range []string{
		"nested **bold with *italic",
		"**a *b",
		"*a **b",
	} {
		if got := BalanceTrailingLine(in); got != in {
			t.Errorf("BalanceTrailingLine(%q) = %q, want it left raw (ambiguous nesting)", in, got)
		}
	}
}

// TestBalancerStackDepthIsBounded pins the O(1) memory claim: a line that opens
// far more delimiters than the fixed stack can hold must still terminate and must
// not spill, so a pathological input degrades to partial balancing rather than to
// an unbounded allocation.
func TestBalancerStackDepthIsBounded(t *testing.T) {
	// 64 open strong delimiters, no closers. The stack holds 16.
	in := ""
	for i := 0; i < 64; i++ {
		in += "**x"
	}
	got := BalanceTrailingLine(in)
	if len(got) < len(in) {
		t.Fatalf("balanced line lost content: %d < %d", len(got), len(in))
	}
	// Whatever was appended, it is at most one closer per stack slot.
	const maxCloserBytes = balancerStackDepth * maxMarkerRun
	if len(got) > len(in)+maxCloserBytes {
		t.Errorf("appended %d bytes, more than the %d-byte stack can hold",
			len(got)-len(in), maxCloserBytes)
	}
}

// TestBalanceTrailingLineLeavesFenceAndTableLinesRaw pins the boundary of the
// function: BLOCK grammar is owned by the holdback, not by the balancer. A
// leading backtick run, a leading pipe, and a bare heading marker must come back
// untouched, because a transient closer on any of them would desync the block
// state machine that owns them.
func TestBalanceTrailingLineLeavesFenceAndTableLinesRaw(t *testing.T) {
	for _, in := range []string{
		"`", "``", "```go", "~~~",
		"|", "| Name | Age", "| Name | Age |",
		"#", "###",
		"- ",
		"~~",
	} {
		if got := BalanceTrailingLine(in); got != in {
			t.Errorf("BalanceTrailingLine(%q) = %q, want it unchanged (block grammar)", in, got)
		}
	}
}

// TestBalanceTrailingLineKeepsCodeSpanMarkersOutOfEmphasis pins that a marker
// inside a code span is not an emphasis delimiter. "a `b*c" is a single code span
// whose content happens to contain a star, and balancing it must never emit a
// SECOND star into the middle of that code.
func TestBalanceTrailingLineKeepsCodeSpanMarkersOutOfEmphasis(t *testing.T) {
	const in = "a `b*c"
	got := BalanceTrailingLine(in)
	if n := strings.Count(got, "*"); n != strings.Count(in, "*") {
		t.Errorf("BalanceTrailingLine(%q) = %q: added %d asterisk(s) inside a code span",
			in, got, n-strings.Count(in, "*"))
	}
	if got != in && got != in+"`" {
		t.Errorf("BalanceTrailingLine(%q) = %q, want the line unchanged or closed by one backtick", in, got)
	}
}
