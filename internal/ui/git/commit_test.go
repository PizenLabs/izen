package git

import (
	"strings"
	"testing"
)

func TestGitCommitAmendFlags(t *testing.T) {
	// Standard commit.
	f := CommitForm{Message: "feat(ui): add panel", HasHEAD: true}
	args := f.BuildArgs()
	if len(args) != 3 || args[0] != "commit" || args[1] != "-m" {
		t.Fatalf("standard args = %v, want [commit -m msg]", args)
	}
	for _, a := range args {
		if a == "--amend" {
			t.Fatalf("standard commit must not contain --amend: %v", args)
		}
	}

	// Amend enabled with HEAD.
	a := CommitForm{Message: "fix(ui): polish", Amend: true, HasHEAD: true}
	args = a.BuildArgs()
	if len(args) != 4 || args[1] != "--amend" {
		t.Fatalf("amend args = %v, want [commit --amend -m msg]", args)
	}
	if !a.CanAmend() {
		t.Fatalf("CanAmend must be true with HEAD + checkbox")
	}

	// Amend checked but no HEAD (empty repo): falls back to standard, never
	// emits --amend.
	e := CommitForm{Message: "feat: init", Amend: true, HasHEAD: false}
	if e.CanAmend() {
		t.Fatalf("CanAmend must be false without HEAD")
	}
	for _, arg := range e.BuildArgs() {
		if arg == "--amend" {
			t.Fatalf("empty-repo commit must not amend: %v", e.BuildArgs())
		}
	}

	// Empty message yields no args (nothing to run).
	if args := (CommitForm{}).BuildArgs(); len(args) != 0 {
		t.Fatalf("empty message args = %v, want none", args)
	}
}

func TestGitAIDraftFillsInput(t *testing.T) {
	p := NewPanel(".")
	p.DiffPreview = "feat(ui): add git panel\n- old line\n- legacy helper"
	p.AIDraft()
	if strings.TrimSpace(p.Form.Message) == "" {
		t.Fatalf("AI draft must fill the text input")
	}
	// Offline fallback on empty diff never empties the input.
	q := NewPanel(".")
	q.AIDraft()
	if strings.TrimSpace(q.Form.Message) == "" {
		t.Fatalf("fallback draft must fill the input")
	}
}

func TestGitPanelEmptySafe(t *testing.T) {
	p := NewPanel(".")
	if got := p.SelectedPath(); got != "" {
		t.Fatalf("empty selection = %q, want empty", got)
	}
	p.MoveUp()
	p.MoveDown()
	if out := p.Render(10); out != "" {
		t.Fatalf("width<20 must return empty, got %q", out)
	}
	if out := p.Render(40); out == "" {
		t.Fatalf("narrow render must not be empty")
	}
	if out := p.Render(100); !strings.Contains(out, "Git Control Panel") {
		t.Fatalf("render missing title:\n%s", out)
	}
}
