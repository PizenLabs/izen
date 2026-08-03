package output

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Persistent tee log management.
const (
	// LogDirName is the directory, relative to a workspace root, where the tee
	// writes its persistent log files.
	LogDirName = ".logs"
	// LastLogLink is the symlink that always points at the newest log file.
	LastLogLink = "last.log"
	// DefaultLogRetention is how long log files are kept before pruning.
	DefaultLogRetention = 7 * 24 * time.Hour
)

// logFileRE is the shape of a managed log file name:
// YYYYMMDD-HHMMSS-<tool-type>.log (optionally suffixed -N on same-second
// collisions).
var logFileRE = regexp.MustCompile(`^\d{8}-\d{6}-[A-Z0-9_]+(?:-\d+)?\.log$`)

// Tee records the uncompressed, normalized output of every pipeline execution
// to a persistent log directory (default <workspace>/.logs/). It is safe for
// concurrent use: writes, symlink rotation, and pruning are serialized. The
// newest log is always reachable via the last.log symlink and logs older than
// the retention window are pruned automatically after each write.
type Tee struct {
	dir    string
	maxAge time.Duration
	now    func() time.Time

	mu sync.Mutex
}

// NewTee returns a Tee that writes logs under <workspace>/.logs/.
func NewTee(workspace string) *Tee {
	return NewTeeDir(filepath.Join(workspace, LogDirName))
}

// NewTeeDir returns a Tee that writes logs directly into dir.
func NewTeeDir(dir string) *Tee {
	return &Tee{
		dir:    dir,
		maxAge: DefaultLogRetention,
		now:    time.Now,
	}
}

// WithMaxAge overrides the retention window. Values <= 0 keep the default.
func (t *Tee) WithMaxAge(d time.Duration) *Tee {
	if t != nil && d > 0 {
		t.maxAge = d
	}
	return t
}

// Dir returns the log directory this tee writes into.
func (t *Tee) Dir() string {
	if t == nil {
		return ""
	}
	return t.dir
}

// Write records content under a YYYYMMDD-HHMMSS-<tool-type>.log file, rotates
// the last.log symlink to it, and prunes logs older than the retention window.
// It returns the absolute path of the written log. When symlink rotation or
// pruning fails, the log file is still written and the error is returned
// alongside the path.
func (t *Tee) Write(typ ToolType, content []byte) (string, error) {
	if t == nil {
		return "", errors.New("output: nil tee")
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.dir == "" {
		return "", errors.New("output: tee log directory is empty")
	}
	if err := os.MkdirAll(t.dir, 0o755); err != nil {
		return "", fmt.Errorf("output: create log directory: %w", err)
	}

	name := t.nextName(typ)
	path := filepath.Join(t.dir, name)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		return "", fmt.Errorf("output: write log %s: %w", name, err)
	}
	if err := t.linkLocked(path); err != nil {
		return path, fmt.Errorf("output: rotate %s: %w", LastLogLink, err)
	}
	t.pruneLocked()
	return path, nil
}

// nextName returns a collision-free log file name for the current timestamp,
// appending -N when another write already used the same second.
func (t *Tee) nextName(typ ToolType) string {
	base := t.now().Format("20060102-150405") + "-" + safeType(typ)
	name := base + ".log"
	for n := 1; fileExists(filepath.Join(t.dir, name)); n++ {
		name = fmt.Sprintf("%s-%d.log", base, n)
	}
	return name
}

// linkLocked (re)creates the last.log symlink so it points at the newest log.
// The link target is the base file name (a relative link), keeping the symlink
// valid if the log directory moves.
func (t *Tee) linkLocked(path string) error {
	target := filepath.Join(t.dir, LastLogLink)
	tmp := target + ".tmp"
	_ = os.Remove(tmp)
	if err := os.Symlink(filepath.Base(path), tmp); err != nil {
		return err
	}
	return os.Rename(tmp, target)
}

// pruneLocked removes managed log files older than the retention window and
// refreshes the last.log symlink when a pruned file was its target.
func (t *Tee) pruneLocked() {
	cutoff := t.now().Add(-t.maxAge)
	removed := 0
	for _, e := range listLogEntries(t.dir) {
		if e.IsDir() || e.Name() == LastLogLink {
			continue
		}
		if !logFileRE.MatchString(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(t.dir, e.Name()))
			removed++
		}
	}
	if removed > 0 {
		t.refreshLinkLocked()
	}
}

// Prune removes managed log files older than the retention window and returns
// how many were removed. It is exposed for explicit housekeeping; Write already
// prunes automatically.
func (t *Tee) Prune() int {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.pruneCountLocked()
}

func (t *Tee) pruneCountLocked() int {
	cutoff := t.now().Add(-t.maxAge)
	removed := 0
	for _, e := range listLogEntries(t.dir) {
		if e.IsDir() || e.Name() == LastLogLink {
			continue
		}
		if !logFileRE.MatchString(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(t.dir, e.Name()))
			removed++
		}
	}
	if removed > 0 {
		t.refreshLinkLocked()
	}
	return removed
}

// refreshLinkLocked repoints last.log at the newest surviving log, or removes
// the symlink entirely when no logs remain.
func (t *Tee) refreshLinkLocked() {
	newest := t.newestLogLocked()
	if newest == "" {
		_ = os.Remove(filepath.Join(t.dir, LastLogLink))
		return
	}
	_ = t.linkLocked(newest)
}

// newestLogLocked returns the newest managed log file (by modification time),
// or "" when the directory holds none.
func (t *Tee) newestLogLocked() string {
	var best string
	var bestTime time.Time
	for _, e := range listLogEntries(t.dir) {
		if e.IsDir() || e.Name() == LastLogLink {
			continue
		}
		if !logFileRE.MatchString(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if best == "" || info.ModTime().After(bestTime) {
			best = filepath.Join(t.dir, e.Name())
			bestTime = info.ModTime()
		}
	}
	return best
}

// Logs returns the absolute paths of every managed log file, newest first.
func (t *Tee) Logs() []string {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	all := make([]logEntry, 0)
	for _, e := range listLogEntries(t.dir) {
		if e.IsDir() || e.Name() == LastLogLink {
			continue
		}
		if !logFileRE.MatchString(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		all = append(all, logEntry{name: e.Name(), mod: info.ModTime()})
	}
	sortByModDesc(all)
	out := make([]string, 0, len(all))
	for _, e := range all {
		out = append(out, filepath.Join(t.dir, e.name))
	}
	return out
}

// logEntry pairs a managed log file name with its modification time.
type logEntry struct {
	name string
	mod  time.Time
}

// LastLog resolves the last.log symlink to the absolute path of the newest log
// file.
func (t *Tee) LastLog() (string, error) {
	if t == nil {
		return "", errors.New("output: nil tee")
	}
	target, err := os.Readlink(filepath.Join(t.dir, LastLogLink))
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(t.dir, target)
	}
	return target, nil
}

// listLogEntries lists the entries of the log directory, tolerating a missing
// directory.
func listLogEntries(dir string) []os.DirEntry {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	return entries
}

// sortByModDesc sorts log entries by modification time, newest first.
func sortByModDesc(all []logEntry) {
	for i := 1; i < len(all); i++ {
		for j := i; j > 0 && all[j].mod.After(all[j-1].mod); j-- {
			all[j], all[j-1] = all[j-1], all[j]
		}
	}
}

// safeType sanitizes a tool type into a log-file-safe token.
func safeType(t ToolType) string {
	s := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			return r
		}
		return '_'
	}, string(t))
	if s == "" {
		return "GENERIC"
	}
	return s
}

// fileExists reports whether path exists as a file or symlink.
func fileExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}
