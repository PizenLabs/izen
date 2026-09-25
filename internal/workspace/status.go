// Package workspace contains the read-only workspace inspection primitives.
//
// The status collector in this file is deliberately small and bounded. It is
// used by the TUI's observational commands, so it must never turn a status
// inspection into an unbounded filesystem walk or a Git operation that can
// freeze the event loop.
package workspace

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	// InspectionTimeout is the wall-clock budget for the complete status probe.
	// It is deliberately well below the architectural 50ms subprocess
	// ceiling. A slow or hostile git executable is reported as unavailable
	// rather than allowed to hold the caller hostage; the UI runs the probe
	// asynchronously so this safety budget never blocks the event loop.
	InspectionTimeout = 40 * time.Millisecond

	// GitCommandTimeout is the per-command budget used by the status probe.
	// Every Git subprocess is bounded by this value (and by the caller's
	// earlier deadline, when one exists). It remains below the hard 50ms
	// architectural ceiling.
	GitCommandTimeout = InspectionTimeout

	// MaxGitCommandTimeout is the hard architectural ceiling.
	MaxGitCommandTimeout = 50 * time.Millisecond
)

// Status is the complete, read-only workspace/runtime snapshot rendered by
// /status. The VCS probe is live; the indexer, session, and engine facets are
// supplied by the runtime because they are already owned by the application
// and must not be re-derived by the presentation layer.
type Status struct {
	WorkspaceRoot string `json:"workspace_root"`
	// Root is a compatibility/readability alias for WorkspaceRoot. It is not
	// serialized twice; new callers should prefer WorkspaceRoot.
	Root string `json:"-"`

	VCS     VCSStatus     `json:"vcs"`
	Indexer IndexerStatus `json:"indexer"`
	Session SessionStatus `json:"session"`
	Engine  EngineStatus  `json:"engine"`
}

// VCSStatus is the bounded Git view used by the status panel.
type VCSStatus struct {
	HasGit         bool   `json:"has_git"`
	Available      bool   `json:"available"`
	Branch         string `json:"branch,omitempty"`
	ShortSHA       string `json:"short_sha,omitempty"`
	Commit         string `json:"commit,omitempty"` // alias for ShortSHA
	CommitSHA      string `json:"-"`                // compatibility alias
	IsDirty        bool   `json:"is_dirty"`
	ModifiedFiles  int    `json:"modified_files"`
	UntrackedFiles int    `json:"untracked_files"`
	// Count aliases make the data convenient for callers that use the shorter
	// vocabulary while preserving the explicit file-count fields above.
	ModifiedCount  int    `json:"modified_count"`
	UntrackedCount int    `json:"untracked_count"`
	Error          string `json:"error,omitempty"`
}

// IndexerStatus describes the already-running symbol indexer. No index or
// graph build is started by this package: /status observes the runtime's
// existing Lea/Lynx state only.
type IndexerStatus struct {
	Status string `json:"status"`
	// IndexingStatus is the runtime-facing alias used by the UI's indexing
	// state machine. Status remains the canonical serialized field.
	IndexingStatus string `json:"-"`
	Indexed        bool   `json:"indexed"`
	SymbolCount    int    `json:"symbol_count"`
	// Symbols is a compatibility alias for SymbolCount.
	Symbols   int    `json:"-"`
	ASTStatus string `json:"ast_status"`
	Error     string `json:"error,omitempty"`
}

// TokenUsage is the session's provider-reported token accounting. It is kept
// separate from /usage's richer billing projection; /status only displays the
// compact runtime counters.
type TokenUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

// SessionStatus is the active conversation context at the instant /status is
// requested.
type SessionStatus struct {
	SlotID string `json:"slot_id"`
	// ActiveSlot is a descriptive alias for SlotID.
	ActiveSlot string     `json:"-"`
	Title      string     `json:"title,omitempty"`
	Goal       string     `json:"goal,omitempty"`
	Tokens     TokenUsage `json:"tokens"`
	// TokenUsage is a compatibility alias for Tokens. CollectStatus keeps
	// both views coherent when a caller constructs the input directly.
	TokenUsage TokenUsage `json:"-"`
	TurnCount  int        `json:"turn_count"`
}

// EngineStatus identifies the active model authority and its current policy
// surface. PolicyGate is observational: it never grants or mints authority.
type EngineStatus struct {
	Provider string `json:"provider"`
	// ActiveProvider and ActiveModel are descriptive aliases for the
	// provider/model pair resolved by the runtime authority.
	ActiveProvider string `json:"-"`
	Model          string `json:"model"`
	ActiveModel    string `json:"-"`
	PolicyGate     string `json:"policy_gate"`
	// ExecutionPolicy is a compatibility alias for PolicyGate.
	ExecutionPolicy string `json:"-"`
	// ExecutionPolicyGate is the fully qualified alias used by security
	// status consumers.
	ExecutionPolicyGate string `json:"-"`
	Mode                string `json:"mode,omitempty"`
}

// StatusInputs carries runtime-owned state into the bounded VCS collector.
// The zero value is useful in tests and in headless callers.
type StatusInputs struct {
	Indexer IndexerStatus
	Session SessionStatus
	Engine  EngineStatus
}

// Inputs is the short compatibility name for StatusInputs.
type Inputs = StatusInputs

// RuntimeState is another descriptive alias used by application wiring.
type RuntimeState = StatusInputs

// CollectStatus gathers the live workspace status. root is used when supplied
// so the TUI can report the project it is attached to; an empty root means the
// process's current working directory. The optional input argument keeps the
// common no-state call concise.
//
// Git subprocesses run concurrently under one short context deadline. The
// function never waits for a subprocess after that deadline, so even a fake or
// hung git binary cannot block the caller beyond the inspection budget (apart
// from OS-level process cleanup performed by exec.CommandContext).
// ctx must be non-nil, as required by the context package contract.
func CollectStatus(ctx context.Context, root string, inputs ...StatusInputs) Status {
	var in StatusInputs
	if len(inputs) > 0 {
		in = inputs[0]
	}

	absoluteRoot := absoluteWorkspaceRoot(root)
	status := Status{
		WorkspaceRoot: absoluteRoot,
		Root:          absoluteRoot,
		VCS:           inspectVCS(ctx, absoluteRoot),
		Indexer:       normalizeIndexerStatus(in.Indexer),
		Session:       normalizeSessionStatus(in.Session),
		Engine:        normalizeEngineStatus(in.Engine),
	}
	return status
}

// Collect is a concise alias for CollectStatus.
func Collect(ctx context.Context, root string, inputs ...StatusInputs) Status {
	return CollectStatus(ctx, root, inputs...)
}

// Inspect is a descriptive alias for CollectStatus.
func Inspect(ctx context.Context, root string, inputs ...StatusInputs) Status {
	return CollectStatus(ctx, root, inputs...)
}

// GetStatus is a compatibility-friendly constructor for callers that prefer a
// verb-style API. It uses the current working directory and a fresh bounded
// context.
func GetStatus(inputs ...StatusInputs) Status {
	return CurrentStatus(inputs...)
}

// CurrentStatus inspects the process working directory with a fresh bounded
// context. It is convenient for non-TUI callers and tests.
func CurrentStatus(inputs ...StatusInputs) Status {
	return CollectStatus(context.Background(), "", inputs...)
}

func absoluteWorkspaceRoot(root string) string {
	root = strings.TrimSpace(root)
	if root == "" {
		if cwd, err := os.Getwd(); err == nil && strings.TrimSpace(cwd) != "" {
			root = cwd
		} else {
			root = "."
		}
	}
	if abs, err := filepath.Abs(root); err == nil {
		return filepath.Clean(abs)
	}
	return filepath.Clean(root)
}

type gitResult struct {
	kind   string
	output string
	err    error
}

// inspectVCS runs branch, short-SHA, and porcelain-status probes in parallel.
// The three calls are independent and each has the same strict deadline.
// parent must be non-nil, as required by the context package contract.
func inspectVCS(parent context.Context, root string) VCSStatus {
	gitTimeout := GitCommandTimeout
	if gitTimeout <= 0 || gitTimeout > MaxGitCommandTimeout {
		gitTimeout = MaxGitCommandTimeout
	}
	ctx := parent
	cancel := func() {}
	if deadline, ok := parent.Deadline(); !ok || time.Until(deadline) > gitTimeout {
		ctx, cancel = context.WithTimeout(parent, gitTimeout)
	}
	defer cancel()

	results := make(chan gitResult, 3)
	go func() {
		result := runGit(ctx, root, "rev-parse", "--abbrev-ref", "HEAD")
		result.kind = "branch"
		results <- result
	}()
	go func() {
		result := runGit(ctx, root, "rev-parse", "--short", "HEAD")
		result.kind = "sha"
		results <- result
	}()
	go func() {
		result := runGit(ctx, root, "status", "--porcelain")
		result.kind = "status"
		results <- result
	}()

	var branch, shortSHA, porcelain gitResult
	branch.kind, shortSHA.kind, porcelain.kind = "branch", "sha", "status"
	gotBranch, gotSHA, gotStatus := false, false, false
	received := 0
	for received < 3 {
		select {
		case result := <-results:
			received++
			switch result.kind {
			case "branch":
				branch, gotBranch = result, true
			case "sha":
				shortSHA, gotSHA = result, true
			case "status":
				porcelain, gotStatus = result, true
			}
		case <-ctx.Done():
			// Do not wait for a misbehaving child process. CommandContext has
			// already received cancellation and will perform best-effort kill.
			received = 3
		}
	}

	vcs := VCSStatus{}
	if gotBranch && branch.err == nil {
		vcs.Branch = strings.TrimSpace(branch.output)
	}
	if gotSHA && shortSHA.err == nil {
		vcs.ShortSHA = strings.TrimSpace(shortSHA.output)
		vcs.Commit = vcs.ShortSHA
		vcs.CommitSHA = vcs.ShortSHA
	}
	if gotStatus && porcelain.err == nil {
		vcs.Available = true
		vcs.ModifiedFiles, vcs.UntrackedFiles = ParsePorcelainStatus([]byte(porcelain.output))
	}
	vcs.HasGit = (gotBranch && branch.err == nil) || (gotSHA && shortSHA.err == nil) || (gotStatus && porcelain.err == nil)
	vcs.ModifiedCount = vcs.ModifiedFiles
	vcs.UntrackedCount = vcs.UntrackedFiles
	vcs.IsDirty = vcs.ModifiedFiles > 0 || vcs.UntrackedFiles > 0

	if !vcs.HasGit {
		vcs.Error = "git unavailable"
	} else if !vcs.Available {
		vcs.Error = "git status timed out"
	}
	return vcs
}

func runGit(ctx context.Context, root string, args ...string) gitResult {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = root
	// Prevent a status probe from taking the repository's optional index lock
	// and ensure a misconfigured credential helper cannot prompt on a TTY.
	cmd.Env = gitProbeEnvironment()

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return gitResult{kind: args[0], output: stdout.String(), err: &gitCommandError{detail: detail}}
	}
	return gitResult{kind: args[0], output: stdout.String()}
}

type gitCommandError struct{ detail string }

func (e *gitCommandError) Error() string { return e.detail }

func gitProbeEnvironment() []string {
	env := make([]string, 0, len(os.Environ())+2)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if key == "GIT_OPTIONAL_LOCKS" || key == "GIT_TERMINAL_PROMPT" {
			continue
		}
		env = append(env, entry)
	}
	return append(env,
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_TERMINAL_PROMPT=0",
	)
}

// ParsePorcelainStatus counts tracked changes and untracked paths from the
// stable `git status --porcelain` format. A tracked add/delete/rename counts
// as a modified file because it is a dirty tracked path; untracked entries are
// counted separately. The parser is intentionally tolerant of malformed
// lines so a partial or unusual Git implementation cannot crash the TUI.
func ParsePorcelainStatus(data []byte) (modified, untracked int) {
	for len(data) > 0 {
		line := data
		if idx := bytes.IndexByte(data, '\n'); idx >= 0 {
			line = data[:idx]
			data = data[idx+1:]
		} else {
			data = nil
		}
		if len(line) > 0 && line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}
		if len(line) < 2 {
			continue
		}
		x, y := line[0], line[1]
		if x == '?' && y == '?' {
			untracked++
			continue
		}
		// Porcelain XY: an unstated column is a space, never an empty byte.
		if x != ' ' || y != ' ' {
			modified++
		}
	}
	return modified, untracked
}

// ParseGitStatus is a convenience wrapper for callers that want a complete
// VCSStatus from porcelain output.
func ParseGitStatus(data []byte) VCSStatus {
	modified, untracked := ParsePorcelainStatus(data)
	return VCSStatus{
		Available:      true,
		HasGit:         true,
		IsDirty:        modified > 0 || untracked > 0,
		ModifiedFiles:  modified,
		UntrackedFiles: untracked,
		ModifiedCount:  modified,
		UntrackedCount: untracked,
	}
}

func normalizeIndexerStatus(in IndexerStatus) IndexerStatus {
	if in.SymbolCount == 0 && in.Symbols > 0 {
		in.SymbolCount = in.Symbols
	}
	if in.Symbols == 0 && in.SymbolCount > 0 {
		in.Symbols = in.SymbolCount
	}
	if in.SymbolCount < 0 {
		in.SymbolCount = 0
	}
	if in.Symbols < 0 {
		in.Symbols = 0
	}
	in.Status = strings.ToLower(strings.TrimSpace(in.Status))
	if in.Status == "" {
		in.Status = strings.ToLower(strings.TrimSpace(in.IndexingStatus))
	}
	if in.Status == "" {
		switch {
		case in.Indexed || in.SymbolCount > 0:
			in.Status = "indexed"
		default:
			in.Status = "unavailable"
		}
	}
	in.IndexingStatus = in.Status
	if in.ASTStatus == "" {
		switch in.Status {
		case "indexed", "ready":
			in.ASTStatus = "valid"
		case "indexing", "pending", "starting":
			in.ASTStatus = "pending"
		case "error", "failed":
			in.ASTStatus = "error"
		default:
			in.ASTStatus = "unavailable"
		}
	}
	return in
}

func normalizeSessionStatus(in SessionStatus) SessionStatus {
	if in.SlotID == "" {
		in.SlotID = strings.TrimSpace(in.ActiveSlot)
	}
	in.ActiveSlot = in.SlotID
	if in.Tokens == (TokenUsage{}) && in.TokenUsage != (TokenUsage{}) {
		in.Tokens = in.TokenUsage
	}
	if in.TokenUsage == (TokenUsage{}) && in.Tokens != (TokenUsage{}) {
		in.TokenUsage = in.Tokens
	}
	if in.Tokens.InputTokens < 0 {
		in.Tokens.InputTokens = 0
	}
	if in.Tokens.OutputTokens < 0 {
		in.Tokens.OutputTokens = 0
	}
	if in.Tokens.TotalTokens == 0 && (in.Tokens.InputTokens > 0 || in.Tokens.OutputTokens > 0) {
		in.Tokens.TotalTokens = in.Tokens.InputTokens + in.Tokens.OutputTokens
	}
	in.TokenUsage = in.Tokens
	if in.TurnCount < 0 {
		in.TurnCount = 0
	}
	return in
}

func normalizeEngineStatus(in EngineStatus) EngineStatus {
	in.Provider = strings.TrimSpace(in.Provider)
	if in.Provider == "" {
		in.Provider = strings.TrimSpace(in.ActiveProvider)
	}
	in.ActiveProvider = in.Provider
	in.Model = strings.TrimSpace(in.Model)
	if in.Model == "" {
		in.Model = strings.TrimSpace(in.ActiveModel)
	}
	in.ActiveModel = in.Model
	in.PolicyGate = strings.TrimSpace(in.PolicyGate)
	if in.PolicyGate == "" {
		in.PolicyGate = strings.TrimSpace(in.ExecutionPolicy)
	}
	if in.PolicyGate == "" {
		in.PolicyGate = strings.TrimSpace(in.ExecutionPolicyGate)
	}
	if in.PolicyGate == "" {
		in.PolicyGate = "Unknown"
	}
	in.ExecutionPolicy = in.PolicyGate
	in.ExecutionPolicyGate = in.PolicyGate
	return in
}
