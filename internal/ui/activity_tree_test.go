package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

func TestActivityTreeStageBadges(t *testing.T) {
	at := NewActivityTree()
	at.Append(NewFileReadEvent("src/worker.go", 2048, 12*time.Millisecond))
	at.Append(NewFileMutateEvent("src/worker.go", 5, 2, 300*time.Millisecond))
	at.Append(NewCommandExecEvent("go test ./...", 0, 1_200*time.Millisecond))
	at.Append(NewSearchEvent("timeout", 3))
	at.Append(NewResolveEvent("RetryLoop", 1))

	// Inactive tree: every entry carries [done], none [running].
	out := at.Render(100)
	stripped := ansi.Strip(out)
	for _, want := range []string{
		"read │", "diff │", "exec │", "grep │", "resolve │",
		"󰈙", "󰏫", "✻", "󰍉",
		"src/worker.go", "go test ./...", "timeout", "RetryLoop",
		"(exit 0", "[done]",
	} {
		if !strings.Contains(stripped, want) {
			t.Errorf("inactive render missing %q:\n%s", want, stripped)
		}
	}
	if strings.Contains(stripped, "[running") {
		t.Errorf("inactive render has [running]:\n%s", stripped)
	}

	// Active tree: the last entry becomes [running] with animated dots.
	outActive := at.RenderActive(100, true, 1)
	strippedActive := ansi.Strip(outActive)
	if !strings.Contains(strippedActive, "[running..]") {
		t.Errorf("active render missing [running..]:\n%s", strippedActive)
	}
	// The [done] badges of completed entries remain.
	if !strings.Contains(strippedActive, "[done]") {
		t.Errorf("active render lost [done] badges:\n%s", strippedActive)
	}
}

func TestActivityTreeExpandedShellOutput(t *testing.T) {
	at := NewActivityTree()
	at.Append(NewFileReadEvent("src/worker.go", 2048, 12*time.Millisecond))
	at.AppendOrUpdateExec("bash npm run build", -1, 0, "")
	at.AppendExecOutput("building...\n")
	at.AppendExecOutput("error: failed\n")

	// Collapsed: output hidden.
	collapsed := ansi.Strip(at.Render(100))
	if strings.Contains(collapsed, "building...") || strings.Contains(collapsed, "error: failed") {
		t.Errorf("collapsed exec must hide output:\n%s", collapsed)
	}
	if !strings.Contains(collapsed, "exec │") {
		t.Errorf("collapsed exec missing stage line:\n%s", collapsed)
	}

	// Expanded (Ctrl+O): output shown inline.
	at.ToggleExpanded()
	expanded := ansi.Strip(at.Render(100))
	if !strings.Contains(expanded, "building...") || !strings.Contains(expanded, "error: failed") {
		t.Errorf("expanded exec must show streamed output:\n%s", expanded)
	}
}

func TestActivityTreeRunningExecSnowflake(t *testing.T) {
	at := NewActivityTree()
	at.AppendOrUpdateExec("bash npm run build", -1, 0, "")

	// The running exec line carries the animated snowflake icon, cycling the
	// full 4-frame sequence ✻ ❅ ❆ ✦ (not just the 3 dot-frames).
	cycles := map[int]string{0: "✻", 1: "❅", 2: "❆", 3: "✦"}
	for frame, want := range cycles {
		out := ansi.Strip(at.RenderActive(100, true, frame))
		if !strings.Contains(out, want+" exec │") {
			t.Errorf("running exec frame %d missing %q icon:\n%s", frame, want, out)
		}
	}
	frame0 := ansi.Strip(at.RenderActive(100, true, 0))
	if !strings.Contains(frame0, "[running") {
		t.Errorf("running exec missing [running] badge:\n%s", frame0)
	}

	// Completion flips to a done line with exit status.
	at.CompleteLastExec(0, 1_200*time.Millisecond)
	done := ansi.Strip(at.Render(100))
	if !strings.Contains(done, "(exit 0") || !strings.Contains(done, "[done]") {
		t.Errorf("completed exec missing exit status / done badge:\n%s", done)
	}
}

func TestActivityTreeAppendOrUpdateExecDedup(t *testing.T) {
	at := NewActivityTree()
	// Running → done updates the same line (no duplicate entries).
	at.AppendOrUpdateExec("go test ./...", -1, 0, "")
	at.AppendOrUpdateExec("go test ./...", 0, 500*time.Millisecond, "PASS\n")
	if at.Len() != 1 {
		t.Fatalf("running→done produced %d entries, want 1", at.Len())
	}
	// A second run of the same command appends a new entry (previous is done).
	at.AppendOrUpdateExec("go test ./...", -1, 0, "")
	if at.Len() != 2 {
		t.Fatalf("second run produced %d entries, want 2", at.Len())
	}
}

func TestActivityTreeRenderEmpty(t *testing.T) {
	at := NewActivityTree()
	if got := at.Render(100); got != "" {
		t.Errorf("empty tree = %q, want empty", got)
	}
	if got := at.RenderActive(100, true, 0); got != "" {
		t.Errorf("empty active tree = %q, want empty", got)
	}
}

func TestActivityTreeResetClearsEntries(t *testing.T) {
	at := NewActivityTree()
	at.Append(NewFileReadEvent("a.go", 10, 0))
	at.Reset()
	if at.Len() != 0 {
		t.Errorf("reset left %d entries", at.Len())
	}
}

func TestStageLabelMapping(t *testing.T) {
	cases := []struct {
		kind EventKind
		want string
	}{
		{EventFileRead, "read"},
		{EventFileMutate, "diff"},
		{EventCommandExec, "exec"},
		{EventSearch, "grep"},
		{EventResolve, "resolve"},
		{EventKind(99), "step"},
	}
	for _, c := range cases {
		if got := stageLabel(c.kind); got != c.want {
			t.Errorf("stageLabel(%v) = %q, want %q", c.kind, got, c.want)
		}
	}
}

func TestStageBadgeForms(t *testing.T) {
	if got := ansi.Strip(stageBadge(false, 0)); got != "[done]" {
		t.Errorf("done badge = %q", got)
	}
	if got := ansi.Strip(stageBadge(true, 0)); got != "[running.]" {
		t.Errorf("running badge frame0 = %q", got)
	}
	if got := ansi.Strip(stageBadge(true, 2)); got != "[running...]" {
		t.Errorf("running badge frame2 = %q", got)
	}
}
