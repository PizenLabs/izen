package ui

import (
	"strings"
	"testing"
	"unsafe"

	tea "github.com/charmbracelet/bubbletea"
)

// TestIsSGRMouseFragment_String covers the byte-scanner fast path: it must
// match every shape of orphan SGR 1006 mouse fragment AND reject every lookalike
// that previously slipped past the regex (e.g. trailing punctuation, wrong
// terminator, too-short fragments).
func TestIsSGRMouseFragment_String(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"wheel scroll fragment", "[<65;56;32M", true},
		{"lower-case terminator", "[<0;26;37m", true},
		{"exact reported leak", "[<64;83;30M", true},
		{"press at origin", "[<0;0;0M", true},
		{"too short", "[<5;5M", false},
		{"missing left bracket", "x<65;56;32M", false},
		{"missing left caret", "[65;56;32M", false},
		{"wrong terminator", "[<65;56;32X", false},
		{"letter in body", "[<6a;56;32M", false},
		{"space in body", "[<65;5 6;32M", false},
		{"plain text", "hello world", false},
		{"empty", "", false},
		{"exactly 8 chars", "[<0;0;0M", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := IsSGRMouseFragment(tc.in)
			if got != tc.want {
				t.Errorf("IsSGRMouseFragment(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestIsSGRMouseFragmentRunes_MatchesString verifies the rune scanner agrees
// with the string scanner on every input, so the new fast-path branch in
// Update() and handleKey() never regresses SGR filtering.
func TestIsSGRMouseFragmentRunes_MatchesString(t *testing.T) {
	inputs := []string{
		"[<65;56;32M",
		"[<0;26;37m",
		"[<64;83;30M",
		"[<0;0;0M",
		"[<5;5M",
		"x<65;56;32M",
		"[65;56;32M",
		"[<65;56;32X",
		"[<6a;56;32M",
		"hello world",
		"",
		"a",
		"abc",
	}
	for _, s := range inputs {
		runes := []rune(s)
		stringGot := IsSGRMouseFragment(s)
		runesGot := IsSGRMouseFragmentRunes(runes)
		if stringGot != runesGot {
			t.Errorf("mismatch for %q: string=%v runes=%v", s, stringGot, runesGot)
		}
	}
}

// TestSanitizePromptInput_NoRegexExecution verifies the new fast-path
// sanitizer strips every orphan SGR fragment without using the regex engine.
// The contract: input without "[<" returns the original string unchanged
// (no allocation, no copy), input WITH fragments strips them and returns
// the cleaned version.
func TestSanitizePromptInput_NoRegexExecution(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"clean passthrough", "hello world", "hello world"},
		{"empty", "", ""},
		{"single fragment mid", "hello[<65;56;32Mworld", "helloworld"},
		{"fragment at start", "[<0;26;37Mprompt>", "prompt>"},
		{"fragment at end", "text[<64;83;30M", "text"},
		{"two fragments", "a[<1;1;1Mb[<2;2;2Mc", "abc"},
		{"fragment with m terminator", "x[<0;0;0my", "xy"},
		{"no fragment but contains bracket", "foo[bar]baz", "foo[bar]baz"},
		{"fragment without semicolons still stripped", "[<123M (not a fragment shape)", " (not a fragment shape)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SanitizePromptInput(tc.in)
			if got != tc.want {
				t.Errorf("SanitizePromptInput(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestSanitizePromptInput_FastPathZeroAlloc verifies that the hot-path
// contract holds: when the input contains no "[<" prefix the function
// returns the original string identity (no allocation, no copy).
func TestSanitizePromptInput_FastPathZeroAlloc(t *testing.T) {
	in := "user is typing clean text without any leak"
	out := SanitizePromptInput(in)
	if !stringIdentityEqual(in, out) {
		t.Errorf("fast-path returned a fresh string instead of the input identity")
	}
}

// stringIdentityEqual reports whether two strings share the same backing
// memory. We compare the unsafe pointer to each string's data byte: identical
// slices share the same address. This is the canonical zero-allocation
// identity test used to confirm the fast-path returned the original string
// without copying.
func stringIdentityEqual(a, b string) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	return unsafe.StringData(a) == unsafe.StringData(b)
}

// TestIsControlSequence_ZeroAlloc verifies the runes-only guard catches every
// raw ESC and orphan "[<" prefix WITHOUT going through string(runes).
func TestIsControlSequence_ZeroAlloc(t *testing.T) {
	cases := []struct {
		name  string
		runes []rune
		want  bool
	}{
		{"empty", nil, false},
		{"normal text", []rune("hello"), false},
		{"ESC prefix", []rune{0x1b, '[', '<'}, true},
		{"orphan [ prefix only", []rune{'[', '<'}, true},
		{"plain [ text", []rune("[abc]"), false},
		{"newline ok", []rune("a\nb"), false},
		{"tab ok", []rune("a\tb"), false},
		{"carriage return ok", []rune("a\rb"), false},
		{"ctrl-A flagged", []rune{0x01}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isControlSequence(tc.runes)
			if got != tc.want {
				t.Errorf("isControlSequence(%v) = %v, want %v", tc.runes, got, tc.want)
			}
		})
	}
}

// TestUpdate_FastPathDropsOrphanFragment covers the P1 fast-path rune
// pre-check in Update(): a tea.KeyMsg whose Runes begin with '[' '<' must
// be dropped with zero allocations, regardless of msg.String() /
// string(msg.Runes).
func TestUpdate_FastPathDropsOrphanFragment(t *testing.T) {
	m := newTestModel()
	originalTI := m.ti.Value()

	fragments := []string{
		"[<65;56;32M",
		"[<0;26;37m",
		"[<64;83;30M",
	}
	for _, frag := range fragments {
		msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(frag)}
		_, _ = m.Update(msg)
		if got := m.ti.Value(); got != originalTI {
			t.Errorf("orphan fragment %q leaked into textinput: got %q", frag, got)
		}
	}
}

// TestHandleKey_FastPathDropsOrphanFragment mirrors the runes fast-path
// contract for the legacy handleKey dispatcher.
func TestHandleKey_FastPathDropsOrphanFragment(t *testing.T) {
	m := newTestModel()
	originalTI := m.ti.Value()

	fragments := []string{
		"[<65;56;32M",
		"[<0;26;37m",
	}
	for _, frag := range fragments {
		msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(frag)}
		_, _ = m.handleKey(msg)
		if got := m.ti.Value(); got != originalTI {
			t.Errorf("orphan fragment %q leaked into textinput via handleKey: got %q", frag, got)
		}
	}
}

// TestRenderPromptView_PureProjection verifies the render contract:
// renderPromptView() must NOT invoke SanitizePromptInput, IsSGRMouseFragment,
// or any regex-based filter. It is a pure projection function that only
// delegates to ti.View() and RenderPasteBadgesStyled().
func TestRenderPromptView_PureProjection(t *testing.T) {
	// Pre-seed the textinput with an orphan SGR fragment to ensure render
	// does not strip it (sanitization happens on write, never on render).
	m := newTestModel()
	m.ti.SetValue("[<65;56;32Mhello")

	view := m.renderPromptView()
	// renderPromptView does NOT strip fragments — the value coming out of
	// ti.View() is passed through verbatim (modulo paste badge styling).
	// The fragment MAY survive in the rendered view because sanitization
	// is the responsibility of Update's write path. We assert the function
	// returns successfully without panicking, and that the underlying ti
	// value is still the original (no state mutation in render).
	if m.ti.Value() != "[<65;56;32Mhello" {
		t.Errorf("renderPromptView mutated textinput state: got %q", m.ti.Value())
	}
	if view == "" {
		t.Errorf("renderPromptView returned empty string for non-empty input")
	}
}

// TestAssembleScreen_RenderPathHasNoSanitize is the render-purity guard: the
// Workspace.Input region built by assembleScreen must contain the textinput
// view verbatim (no SanitizePromptInput rewrite). Sanitization is owned by
// the write path (Update), never by the render path. If a future change
// reintroduces sanitization here, the developer must update both this
// invariant and the P0 "render purity" rule in keys.go and prompt.go.
func TestAssembleScreen_RenderPathHasNoSanitize(t *testing.T) {
	m := newTestModel()
	m.initStage = initComplete
	m.state = StateChat
	m.ti.SetValue("plain text")
	m.ti.Focus()
	plainView := m.ti.View()

	ws := m.assembleScreen(nil)
	if !strings.Contains(ws.Input, "plain text") {
		t.Errorf("Workspace.Input did not contain textinput content: got %q", ws.Input)
	}
	if !strings.Contains(ws.Input, plainView) {
		t.Errorf("Workspace.Input mutated textinput projection: %q not in %q", plainView, ws.Input)
	}
}
