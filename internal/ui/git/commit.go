// Package git provides the integrated Git Control Panel: diff preview, AI
// commit generation and --amend support.
package git

import (
	"strings"

	giteng "github.com/PizenLabs/izen/internal/git"
	"github.com/PizenLabs/izen/internal/modes/commit"
)

// CommitForm is the bottom section of the Git panel: commit message text
// input + [ ] Amend previous commit checkbox.
type CommitForm struct {
	// Message is the commit message text input buffer.
	Message string
	// Amend is the checkbox state. It only takes effect when HasHEAD is true.
	Amend bool
	// HasHEAD reports whether HEAD exists (false in empty repos with no
	// commits — --amend is then disabled).
	HasHEAD bool
}

// NewCommitForm builds an empty form. Call RefreshHEAD to validate --amend
// availability for the working directory.
func NewCommitForm() CommitForm { return CommitForm{} }

// RefreshHEAD validates whether HEAD exists in dir via
// `git rev-parse --verify HEAD`. Empty repos / non-repos yield HasHEAD=false
// (never errors — the panel degrades gracefully).
func (f *CommitForm) RefreshHEAD(dir string) {
	f.HasHEAD = HasHEAD(dir)
}

// HasHEAD reports whether dir has a resolvable HEAD commit.
func HasHEAD(dir string) bool {
	if dir == "" {
		dir = "."
	}
	return giteng.NewEngine(dir).HasHEAD()
}

// CanAmend reports whether --amend is enabled: checkbox set AND HEAD exists.
func (f CommitForm) CanAmend() bool { return f.Amend && f.HasHEAD }

// ToggleAmend flips the checkbox (the UI binds this to Space on the box).
func (f *CommitForm) ToggleAmend() { f.Amend = !f.Amend }

// SetMessage fills the text input (used by the AI generator to drop its
// draft directly into the input).
func (f *CommitForm) SetMessage(msg string) { f.Message = msg }

// BuildArgs compiles the git argv for the form. When checked (and HEAD
// exists) it executes `git commit --amend -m "<msg>"`, otherwise the
// standard `git commit -m "<msg>"`. An empty message yields no args.
func (f CommitForm) BuildArgs() []string {
	msg := strings.TrimSpace(f.Message)
	if msg == "" {
		return nil
	}
	if f.CanAmend() {
		return []string{"commit", "--amend", "-m", msg}
	}
	return []string{"commit", "-m", msg}
}

// Run executes the compiled commit in dir. It returns an error for empty
// messages (nothing to commit) or git failures.
func (f CommitForm) Run(dir string) error {
	args := f.BuildArgs()
	if len(args) == 0 {
		return errEmptyMessage
	}
	if dir == "" {
		dir = "."
	}
	return giteng.NewEngine(dir).RunCommit(args)
}

// GenerateAIDraft reuses the /build commit engine to draft a message from the
// staged/working diff. It is bound to Ctrl+A in the panel and fills the
// result directly into the text input via SetMessage. The heuristic path is
// fully offline: it sanitizes the diff's subject/body lines through the
// canonical commit pipeline (CleanRawLLMOutput → SanitizeSubject →
// SanitizeBody) and falls back to BuildFallback on empty input — the live LLM
// draft (when a provider is wired) flows through the same sanitizers.
func GenerateAIDraft(status, diff string) commit.CommitMessage {
	raw := strings.TrimSpace(diff)
	if raw == "" {
		return commit.BuildFallback(status)
	}
	lines := commit.CleanRawLLMOutput(raw)
	if len(lines) == 0 {
		return commit.BuildFallback(status)
	}
	subject := commit.SanitizeSubject(lines[0])
	body := commit.SanitizeBody(lines[1:])
	if body == "" {
		body = "- apply repository changes"
	}
	return commit.CommitMessage{Subject: subject, Body: body}
}

// GenerateAIDraftString is the single-string variant filled into the input:
// "subject\n\nbody".
func GenerateAIDraftString(status, diff string) string {
	m := GenerateAIDraft(status, diff)
	if m.Body == "" {
		return m.Subject
	}
	return m.Subject + "\n\n" + m.Body
}

type commitError string

func (e commitError) Error() string { return string(e) }

const errEmptyMessage = commitError("git: empty commit message")
