package audit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/PizenLabs/izen/internal/state"
)

type MutationEntry struct {
	Timestamp string `json:"timestamp"`
	File      string `json:"file"`
	Action    string `json:"action"`
	PatchID   string `json:"patch_id,omitempty"`
	Content   string `json:"content,omitempty"`
}

type ShellEntry struct {
	Timestamp  string `json:"timestamp"`
	Command    string `json:"command"`
	ExitCode   int    `json:"exit_code"`
	WorkingDir string `json:"working_dir"`
	StdoutPref string `json:"stdout_preview,omitempty"`
}

type Logger struct {
	root string
}

func NewLogger(root string) *Logger {
	return &Logger{root: root}
}

func (l *Logger) LogMutation(entry MutationEntry) error {
	entry.Timestamp = time.Now().UTC().Format(time.RFC3339)
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return l.append(state.LocalPath(l.root, state.AuditDir, state.MutationsLogFile), data)
}

func (l *Logger) LogShell(entry ShellEntry) error {
	entry.Timestamp = time.Now().UTC().Format(time.RFC3339)
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return l.append(state.LocalPath(l.root, state.AuditDir, state.ShellLogFile), data)
}

func (l *Logger) append(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("audit mkdir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("audit open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("audit write: %w", err)
	}
	return nil
}

func (l *Logger) ReadMutations() ([]MutationEntry, error) {
	return readEntries[MutationEntry](state.LocalPath(l.root, state.AuditDir, state.MutationsLogFile))
}

func (l *Logger) ReadShell() ([]ShellEntry, error) {
	return readEntries[ShellEntry](state.LocalPath(l.root, state.AuditDir, state.ShellLogFile))
}

func readEntries[T any](path string) ([]T, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var entries []T
	lines := splitLines(string(data))
	for _, line := range lines {
		if line == "" {
			continue
		}
		var entry T
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}
