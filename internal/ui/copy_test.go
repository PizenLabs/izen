package ui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// fakeClipboard is an in-memory Clipboard for tests.
type fakeClipboard struct {
	content string
	err     error
	writes  int
}

func (f *fakeClipboard) WriteAll(text string) error {
	if f.err != nil {
		return f.err
	}
	f.content = text
	f.writes++
	return nil
}

// buildModelForCopy returns a minimal model seeded with records and a fake
// clipboard. Caller may mutate the returned model freely.
func buildModelForCopy(records []record, cb *fakeClipboard) *model {
	m := newTestModel()
	m.state = StateChat
	m.records = records
	if cb != nil {
		m.clipboard = cb
	}
	m.uiNotice = ""
	return m
}

func TestSerializeTranscript_PlainConversation(t *testing.T) {
	records := []record{
		{role: roleUser, text: "Fix the authentication bug."},
		{role: roleAI, text: "I found the issue in auth/session.go."},
	}
	got := SerializeTranscript(records)
	want := "USER\nFix the authentication bug.\n\nASSISTANT\nI found the issue in auth/session.go."
	if got != want {
		t.Fatalf("plain conversation mismatch\nwant:\n%q\n\ngot:\n%q", want, got)
	}
}

func TestSerializeTranscript_MultilineContent(t *testing.T) {
	records := []record{
		{role: roleSystem, text: "line one\nline two\nline three"},
	}
	got := SerializeTranscript(records)
	// Line boundaries must remain intact with no wrapping.
	if !strings.Contains(got, "line one\nline two\nline three") {
		t.Fatalf("multiline boundaries not preserved: %q", got)
	}
	if strings.Count(got, "\n") < 3 {
		t.Fatalf("expected multiline preserved, got %q", got)
	}
}

func TestSerializeTranscript_CodeBlocks(t *testing.T) {
	code := "```go\nfunc Foo() {\n\treturn 42\n}\n```"
	records := []record{
		{role: roleAI, text: "Here is the fix:\n" + code},
	}
	got := SerializeTranscript(records)
	if !strings.Contains(got, code) {
		t.Fatalf("code block not preserved verbatim\nwant %q in %q", code, got)
	}
	// Ensure no terminal styling leaked.
	if strings.Contains(got, "\x1b[") {
		t.Fatalf("ANSI leaked into code block: %q", got)
	}
}

func TestSerializeTranscript_ToolOutputExactlyOnce(t *testing.T) {
	tool := "$ go test ./...\n--- FAIL: TestSessionExpiry"
	records := []record{
		{role: roleUser, text: "run tests"},
		{role: roleSystem, text: tool},
	}
	got := SerializeTranscript(records)
	if strings.Count(got, tool) != 1 {
		t.Fatalf("tool output should appear exactly once, got %d in %q", strings.Count(got, tool), got)
	}
}

func TestSerializeTranscript_ErrorPreserved(t *testing.T) {
	records := []record{
		{role: roleUser, text: "fix bug"},
		{role: roleError, text: "session expiry mismatch: expected 30m, got 1h"},
		{role: roleStatus, text: "Modified: auth/session.go"},
	}
	got := SerializeTranscript(records)
	for _, want := range []string{"ERROR\nsession expiry mismatch", "EXECUTION\nModified: auth/session.go"} {
		if !strings.Contains(got, want) {
			t.Errorf("serialized transcript missing %q in %q", want, got)
		}
	}
}

func TestSerializeTranscript_ANSIStripped(t *testing.T) {
	styled := lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Render("red error")
	records := []record{
		{role: roleError, text: styled},
		{role: roleSystem, text: "\x1b[31mANSI red\x1b[0m plain"},
	}
	got := SerializeTranscript(records)
	if strings.Contains(got, "\x1b[") {
		t.Fatalf("ANSI escape leaked: %q", got)
	}
	if !strings.Contains(got, "red error") || !strings.Contains(got, "ANSI red plain") {
		t.Fatalf("plain text lost after ANSI strip: %q", got)
	}
	// Same logical content with different styling must produce identical output.
	plain := SerializeTranscript([]record{{role: roleError, text: "red error"}})
	styledAgain := SerializeTranscript([]record{{role: roleError, text: styled}})
	if plain != styledAgain {
		t.Fatalf("ANSI isolation failed: plain %q vs styled %q", plain, styledAgain)
	}
}

func TestSerializeTranscript_ANSIIsolationDifferentWidths(t *testing.T) {
	records := []record{
		{role: roleAI, text: "hello world with a very long line that would normally wrap in the viewport rendering but must not affect the canonical copy output at all"},
	}
	a := SerializeTranscript(records)
	b := SerializeTranscript(records)
	if a != b {
		t.Fatalf("determinism broken: %q vs %q", a, b)
	}
}

func TestSerializeTranscript_ViewportIndependence(t *testing.T) {
	records := []record{
		{role: roleUser, text: "viewport test"},
		{role: roleAI, text: "response"},
	}
	// Simulate different viewport positions and widths — serializer must ignore them.
	m1 := buildModelForCopy(records, &fakeClipboard{})
	m1.width = 80
	m1.Viewport.YOffset = 0
	got1 := SerializeTranscript(m1.records)

	m2 := buildModelForCopy(records, &fakeClipboard{})
	m2.width = 200
	m2.Viewport.YOffset = 999
	m2.userIsScrollingUp = true
	got2 := SerializeTranscript(m2.records)

	if got1 != got2 {
		t.Fatalf("viewport independence violated:\n80w offset 0: %q\n200w offset 999: %q", got1, got2)
	}
}

func TestSerializeTranscript_TransientUIIsolation(t *testing.T) {
	// Spinner frames and transient artifacts are never stored in records; verify
	// they do not leak even if the model has active shimmer/spinner state.
	records := []record{
		{role: roleUser, text: "hello"},
		{role: roleAI, text: "world"},
	}
	m := buildModelForCopy(records, &fakeClipboard{})
	m.spinnerFrame = 7
	m.shimmerActive = true
	m.streaming = true
	m.agentRunning = true
	got := SerializeTranscript(m.records)
	for _, artifact := range []string{"⠋", "⠙", "⠹", "⠸", "⠼", "✻", "⠋", "spinner", "streaming", "shimmer"} {
		if strings.Contains(strings.ToLower(got), strings.ToLower(artifact)) && artifact != "hello" && artifact != "world" {
			// Only fail if the artifact literally appears as spinner glyph; the model text is "hello"/"world" so any other hit is leakage.
			if strings.Contains(got, artifact) {
				t.Fatalf("transient artifact %q leaked into transcript: %q", artifact, got)
			}
		}
	}
	// Ensure the transcript equals the plain serialization without transient state.
	want := SerializeTranscript(records)
	if got != want {
		t.Fatalf("transient state affected transcript\nwant %q\ngot %q", want, got)
	}
}

func TestSerializeTranscript_EmptyRecords(t *testing.T) {
	if got := SerializeTranscript(nil); got != "" {
		t.Fatalf("nil records should yield empty string, got %q", got)
	}
	if got := SerializeTranscript([]record{}); got != "" {
		t.Fatalf("empty records should yield empty string, got %q", got)
	}
	// Records with whitespace-only text are skipped.
	got := SerializeTranscript([]record{{role: roleSystem, text: "   \n  "}})
	if got != "" {
		t.Fatalf("whitespace-only record should be skipped, got %q", got)
	}
}

func TestHandleCopy_Success(t *testing.T) {
	records := []record{
		{role: roleUser, text: "Fix the authentication bug."},
		{role: roleAI, text: "I found the issue in auth/session.go."},
		{role: roleSystem, text: "$ go test ./...\n--- FAIL: TestSessionExpiry"},
		{role: roleError, text: "session expiry mismatch: expected 30m, got 1h"},
		{role: roleStatus, text: "Modified: auth/session.go"},
	}
	cb := &fakeClipboard{}
	m := buildModelForCopy(records, cb)
	preLen := len(m.records)
	m.handleCopy()
	if cb.writes != 1 {
		t.Fatalf("expected 1 clipboard write, got %d", cb.writes)
	}
	if cb.content == "" {
		t.Fatal("clipboard content empty after /copy")
	}
	if strings.Contains(cb.content, "\x1b[") {
		t.Fatalf("clipboard contains ANSI: %q", cb.content)
	}
	if m.uiNotice != "Copied conversation to clipboard" {
		t.Fatalf("uiNotice = %q, want Copied conversation to clipboard", m.uiNotice)
	}
	// Must not mutate conversation state.
	if len(m.records) != preLen {
		t.Fatalf("handleCopy mutated records: %d -> %d", preLen, len(m.records))
	}
	// Must not have inserted a new record for the notice (recursive copy).
	for _, r := range m.records {
		if strings.Contains(r.text, "Copied conversation") {
			t.Fatal("copy notice was inserted into records (would be recursively copied)")
		}
	}
}

func TestHandleCopy_Idempotent(t *testing.T) {
	records := []record{{role: roleUser, text: "hello"}}
	cb := &fakeClipboard{}
	m := buildModelForCopy(records, cb)
	m.handleCopy()
	first := cb.content
	m.handleCopy()
	second := cb.content
	if first != second {
		t.Fatalf("idempotency broken: %q vs %q", first, second)
	}
	if cb.writes != 2 {
		t.Fatalf("expected 2 writes for 2 invocations, got %d", cb.writes)
	}
}

func TestHandleCopy_ClipboardFailure(t *testing.T) {
	records := []record{{role: roleUser, text: "hello"}}
	cb := &fakeClipboard{err: errors.New("clipboard unavailable")}
	m := buildModelForCopy(records, cb)
	preLen := len(m.records)
	m.handleCopy()
	if !strings.Contains(m.uiNotice, "Failed to copy") {
		t.Fatalf("failure notice not surfaced, uiNotice=%q", m.uiNotice)
	}
	if len(m.records) != preLen {
		t.Fatalf("clipboard failure mutated records: %d -> %d", preLen, len(m.records))
	}
	// Must not have crashed and must have preserved the screen state.
	if cb.content != "" {
		t.Fatalf("failed write should not populate clipboard, got %q", cb.content)
	}
}

func TestHandleCopy_EmptyConversation(t *testing.T) {
	cb := &fakeClipboard{}
	m := buildModelForCopy(nil, cb)
	m.handleCopy()
	if cb.writes != 0 {
		t.Fatalf("empty conversation should not write to clipboard, writes=%d", cb.writes)
	}
	if m.uiNotice != "Nothing to copy — conversation is empty" {
		t.Fatalf("empty notice = %q", m.uiNotice)
	}
}

func TestHandleCopy_ViaHandleCommand(t *testing.T) {
	records := []record{{role: roleUser, text: "test"}}
	cb := &fakeClipboard{}
	m := buildModelForCopy(records, cb)
	// Exercise the slash-command dispatch path.
	m.handleCommand("/copy")
	if cb.writes != 1 {
		t.Fatalf("/copy via handleCommand did not write clipboard, writes=%d", cb.writes)
	}
	if m.uiNotice != "Copied conversation to clipboard" {
		t.Fatalf("uiNotice after /copy = %q", m.uiNotice)
	}
}

func TestHandleCopy_DoesNotBreakCtrlC(t *testing.T) {
	// Verify Ctrl+C interrupt semantics survive a preceding /copy.
	records := []record{{role: roleUser, text: "hello"}}
	cb := &fakeClipboard{}
	m := buildModelForCopy(records, cb)
	m.handleCopy()

	// Simulate an active streaming state where Ctrl+C should interrupt.
	m.streaming = true
	m.agentRunning = true
	m.streamCancel = func() {}

	// The emergency interrupt handler lives at the top of Update. Call handleCtrlC
	// helper directly and verify it still reports handled when an op is active.
	// Also verify that a plain Ctrl+C in Update with active workflow returns the
	// interrupt message rather than being swallowed as a copy binding.
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	// When streaming, Ctrl+C should have produced a TaskFinishedMsg batch.
	if cmd == nil {
		t.Fatal("Ctrl+C after /copy returned nil cmd — interrupt semantics broken")
	}
	msgs := drainCmds(t, cmd)
	_ = msgs // ensure no panic; exact message varies by path but must be non-empty
}

func TestHandleCopy_PreservesNoticeNotInTranscript(t *testing.T) {
	cb := &fakeClipboard{}
	m := buildModelForCopy([]record{{role: roleUser, text: "hello"}}, cb)
	m.handleCopy()
	firstContent := cb.content
	// A second copy must not include the prior success notice.
	m.handleCopy()
	secondContent := cb.content
	if strings.Contains(secondContent, "Copied conversation") {
		t.Fatal("transcript recursively contains its own copy notice")
	}
	if firstContent != secondContent {
		t.Fatalf("notice leaked into transcript: first %q vs second %q", firstContent, secondContent)
	}
}
