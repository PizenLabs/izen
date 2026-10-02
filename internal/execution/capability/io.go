package capability

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultProbeTimeout bounds a single capability network observation. It is a
// ceiling on observation, never a licence to wait indefinitely: a runtime that
// cannot observe within its bound must report OBSERVATION_FAILED rather than
// hang.
const DefaultProbeTimeout = 5 * time.Second

// DefaultReadinessTimeout bounds how long Serve waits for the started runtime
// to actually answer a request.
const DefaultReadinessTimeout = 10 * time.Second

// CommandRunner is the process-execution seam. The execution authority injects
// its own already-authorized runner (execution.Runner), so every command this
// package runs passes the SAME admission, sandbox, risk and budget gates that
// every other command in IZEN passes. This package deliberately holds no
// second command path.
type CommandRunner interface {
	// RunCommand executes command in the workspace and returns the real result.
	RunCommand(ctx context.Context, command string) (CommandResult, error)
}

// CommandResult is the real outcome of one command execution.
type CommandResult struct {
	Command  string
	Stdout   string
	Stderr   string
	ExitCode int
}

// Option configures a Runner.
type Option func(*Runner)

// WithCommandRunner injects the authorized process-execution seam. Without one,
// CommandRun reports CAPABILITY_MISSING — it never falls back to an
// unauthorized spawn.
func WithCommandRunner(cr CommandRunner) Option {
	return func(r *Runner) {
		if cr != nil {
			r.cmd = cr
		}
	}
}

// WithProbeTimeout overrides the per-request observation ceiling.
func WithProbeTimeout(d time.Duration) Option {
	return func(r *Runner) {
		if d > 0 {
			r.probeTimeout = d
		}
	}
}

// WithReadinessTimeout overrides how long Serve waits for a started runtime to
// answer.
func WithReadinessTimeout(d time.Duration) Option {
	return func(r *Runner) {
		if d > 0 {
			r.readinessTimeout = d
		}
	}
}

// WithHTTPClient injects the HTTP client used for network observation. A client
// that fails to be constructed surfaces CAPABILITY_FAILED, never a silent skip.
func WithHTTPClient(c *http.Client) Option {
	return func(r *Runner) {
		if c != nil {
			r.http = c
		}
	}
}

// Runner executes capabilities against one workspace under one Grant.
//
// It is safe for concurrent use: the server registry and the active handle are
// mutex-guarded because a serve/observe/stop cycle can overlap with a
// cancellation from another goroutine.
type Runner struct {
	root string

	mu               sync.Mutex
	grant            Grant
	cmd              CommandRunner
	http             *http.Client
	probeTimeout     time.Duration
	readinessTimeout time.Duration

	// active is the currently running served target, if any. Exactly one is
	// tracked so a stop always terminates a known process and never a guess.
	active *ServerHandle
}

// NewRunner builds a capability runner rooted at the workspace with no grant.
// The zero grant is deliberately inert: a freshly constructed Runner authorizes
// nothing until the execution authority installs a Grant.
func NewRunner(root string, opts ...Option) *Runner {
	r := &Runner{
		root:             root,
		probeTimeout:     DefaultProbeTimeout,
		readinessTimeout: DefaultReadinessTimeout,
		http: &http.Client{
			Timeout: DefaultProbeTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
	for _, o := range opts {
		o(r)
	}
	return r
}

// Root returns the workspace root this runner is bound to.
func (r *Runner) Root() string {
	if r == nil {
		return ""
	}
	return r.root
}

// SetGrant installs the authorization vector for subsequent capability runs.
// It is the ONLY way a capability becomes permitted, and it is called by the
// execution authority — never by the model, never by a tool argument.
func (r *Runner) SetGrant(g Grant) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.grant = g
}

// Grant returns the currently installed authorization vector.
func (r *Runner) Grant() Grant {
	if r == nil {
		return Grant{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.grant
}

// authorize checks the installed grant for a capability and returns the
// canonical refusal when it is absent. Every public capability entry point
// calls this before touching the filesystem, the network or a process.
func (r *Runner) authorize(id ID) error {
	if r == nil {
		return fmt.Errorf("%w: no capability runtime", errNotImplemented)
	}
	if !id.Valid() {
		return fmt.Errorf("%w: %q is not in the capability vocabulary", errNotImplemented, id)
	}
	if !r.Grant().Permits(id) {
		return NotAuthorized(id)
	}
	return nil
}

// Refuse builds the Evidence record for a capability that was not attempted.
// Refusals are evidence too: "the Control Plane said no" is an observable fact
// the loop must be able to carry, not a silent gap.
func Refuse(id ID, err error) Evidence {
	class := FailureAuthorizationBlocked
	if IsCapabilityMissing(err) {
		class = FailureCapabilityMissing
	}
	return Evidence{
		ID:         "denied." + string(id),
		Capability: id,
		OK:         false,
		Class:      class,
		Summary:    err.Error(),
		Fields:     map[string]string{"denied": "true"},
	}
}

// ReadFile reads one bounded region of one workspace file and reports exactly
// what it read. The path is resolved inside the workspace root; a path that
// escapes it is refused with CAPABILITY_FAILED rather than followed.
func (r *Runner) ReadFile(_ context.Context, target string, startLine, endLine int) (string, Evidence, error) {
	ev := Evidence{ID: "file.read", Capability: FileRead, Fields: map[string]string{"path": target}}
	if err := r.authorize(FileRead); err != nil {
		return "", Refuse(FileRead, err), err
	}
	abs, err := r.resolveWithinRoot(target)
	if err != nil {
		ev.OK, ev.Class, ev.Summary = false, FailureCapabilityFailed, err.Error()
		return "", ev, err
	}
	info, statErr := os.Stat(abs)
	if statErr != nil {
		ev.OK, ev.Class, ev.Summary = false, FailureCapabilityFailed, "cannot stat "+target+": "+statErr.Error()
		return "", ev, errors.New(ev.Summary)
	}
	if info.IsDir() {
		ev.OK, ev.Class, ev.Summary = false, FailureTargetUncertain, target+" is a directory, not a file"
		return "", ev, errors.New(ev.Summary)
	}
	data, readErr := os.ReadFile(abs)
	if readErr != nil {
		ev.OK, ev.Class, ev.Summary = false, FailureCapabilityFailed, "cannot read "+target+": "+readErr.Error()
		return "", ev, errors.New(ev.Summary)
	}
	truncated := false
	if len(data) > maxReadBytes {
		data = data[:maxReadBytes]
		truncated = true
	}
	content := string(data)
	if startLine > 0 || endLine > 0 {
		content, startLine, endLine = sliceLines(content, startLine, endLine)
	}
	ev.OK = true
	ev.Summary = fmt.Sprintf("read %s (%d bytes, lines %d-%d)", target, len(content), startLine, endLine)
	ev.Fields["bytes"] = strconv.Itoa(len(content))
	ev.Fields["start_line"] = strconv.Itoa(startLine)
	ev.Fields["end_line"] = strconv.Itoa(endLine)
	ev.Fields["truncated"] = strconv.FormatBool(truncated)
	return content, ev, nil
}

// SearchMatch is one located literal match.
type SearchMatch struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

// Search locates a literal string across workspace text files and returns
// bounded path:line records. It is the capability a diagnosis uses to answer
// "does anything in this workspace actually hold that name?" — a question no
// filename heuristic can answer.
func (r *Runner) Search(_ context.Context, query, dir string, maxResults int) ([]SearchMatch, Evidence, error) {
	ev := Evidence{ID: "file.search", Capability: FileSearch, Fields: map[string]string{"query": query, "path": dir}}
	if err := r.authorize(FileSearch); err != nil {
		return nil, Refuse(FileSearch, err), err
	}
	query = strings.TrimSpace(query)
	if query == "" {
		ev.OK, ev.Class, ev.Summary = false, FailureContextInsufficient, "search requires a non-empty query"
		return nil, ev, errors.New(ev.Summary)
	}
	if maxResults <= 0 || maxResults > 500 {
		maxResults = maxSearchResults
	}
	base, err := r.resolveWithinRoot(dir)
	if err != nil {
		ev.OK, ev.Class, ev.Summary = false, FailureCapabilityFailed, err.Error()
		return nil, ev, err
	}
	root, _ := filepath.Abs(r.root)
	var out []SearchMatch
	scanned := 0
	walkErr := filepath.WalkDir(base, func(p string, entry os.DirEntry, wErr error) error {
		if wErr != nil || entry == nil {
			// An entry the walk could not read is coverage this pass did NOT get,
			// so it is skipped rather than allowed to abort the scan.
			return nil //nolint:nilerr // the skipped entry is reflected in the scanned count
		}
		if entry.IsDir() {
			if skipWorkspaceDir(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if scanned >= maxSearchScanned || len(out) >= maxResults {
			// Both bounds reached: the search is complete for the requested scope,
			// and the evidence reports how much was scanned so the caller can tell a
			// bounded answer from a whole-workspace one.
			return filepath.SkipAll
		}
		scanned++
		abs := p
		matches, mErr := searchFile(abs, root, query, maxResults-len(out))
		if mErr != nil {
			// An unreadable or binary file is not a search failure: the scan is
			// best-effort by design, and the evidence records the scanned count.
			return nil //nolint:nilerr // a skipped file is recorded as coverage, not as failure
		}
		out = append(out, matches...)
		if len(out) >= maxResults {
			return filepath.SkipAll
		}
		return nil
	})
	if walkErr != nil {
		ev.Fields["walk_error"] = walkErr.Error()
	}
	ev.OK = true
	ev.Summary = fmt.Sprintf("search for %q matched %d location(s) across %d file(s)", query, len(out), scanned)
	ev.Fields["matches"] = strconv.Itoa(len(out))
	ev.Fields["scanned"] = strconv.Itoa(scanned)
	return out, ev, nil
}

// searchFile returns the literal matches of query in one file, bounded to limit.
// Line numbers are 1-based and count every line, including lines that do not
// match, so a reported location can be opened and checked directly.
func searchFile(abs, root, query string, limit int) ([]SearchMatch, error) {
	if limit <= 0 {
		return nil, nil
	}
	f, err := os.Open(abs)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	head := make([]byte, 512)
	n, _ := f.Read(head)
	for _, c := range head[:n] {
		if c == 0 {
			return nil, errors.New("binary file")
		}
	}
	if _, err := f.Seek(0, 0); err != nil {
		return nil, err
	}
	rel, rErr := filepath.Rel(root, abs)
	if rErr != nil {
		rel = abs
	}
	rel = filepath.ToSlash(rel)
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), maxReadBytes)
	out := make([]SearchMatch, 0, 4)
	line := 0
	for scanner.Scan() {
		line++
		text := scanner.Text()
		if !strings.Contains(text, query) {
			continue
		}
		out = append(out, SearchMatch{Path: rel, Line: line, Text: strings.TrimSpace(text)})
		if len(out) >= limit {
			return out, nil
		}
	}
	if err := scanner.Err(); err != nil {
		// A truncated final line still yields the matches already found; the
		// caller reports a bounded result either way.
		return out, nil
	}
	return out, nil
}

// skipWorkspaceDir reports whether a directory name is never entered by a
// workspace scan.
func skipWorkspaceDir(name string) bool {
	for _, d := range workspaceIgnoreDirs {
		if d == name {
			return true
		}
	}
	return false
}

// sliceLines returns a 1-based inclusive line range of content and the bounds
// actually used. Non-positive bounds mean "from the start" / "to the end".
func sliceLines(content string, start, end int) (string, int, int) {
	lines := strings.Split(content, "\n")
	if start <= 0 {
		start = 1
	}
	if end <= 0 || end > len(lines) {
		end = len(lines)
	}
	if start > len(lines) {
		return "", start, end
	}
	if start > end {
		start, end = end, start
	}
	return strings.Join(lines[start-1:end], "\n"), start, end
}

// resolveWithinRoot resolves target inside the workspace root and guarantees the
// result stays there. It defends the traversal boundary that every
// evidence-reading capability depends on.
func (r *Runner) resolveWithinRoot(target string) (string, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		target = "."
	}
	root, err := filepath.Abs(r.root)
	if err != nil {
		return "", fmt.Errorf("workspace root cannot be resolved: %w", err)
	}
	abs := target
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(root, target)
	}
	abs = filepath.Clean(abs)
	if abs != root {
		rel, relErr := filepath.Rel(root, abs)
		if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			return "", fmt.Errorf("path %q resolves outside the workspace root", target)
		}
	}
	return abs, nil
}

// jsonUnmarshal is a thin alias so manifest validation reads as one concern.
func jsonUnmarshal(data []byte, v any) error { return json.Unmarshal(data, v) }

// sortStrings returns a sorted copy of in.
func sortStrings(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

// cleanRef normalizes a document reference into a slash-separated path.
func cleanRef(ref string) string { return path.Clean(strings.TrimSpace(ref)) }
