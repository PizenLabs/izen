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
		":: explore", ":: diff", ":: exec", ":: search", ":: resolve",
		"Read src/worker.go", "Patch src/worker.go", "go test ./...",
		"[done]",
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
		{EventFileRead, "explore"},
		{EventFileMutate, "diff"},
		{EventCommandExec, "exec"},
		{EventSearch, "search"},
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
