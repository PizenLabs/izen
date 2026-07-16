package audit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/PizenLabs/izen/internal/state"
)

type OperationDecision string

const (
	DecisionApproved OperationDecision = "approved"
	DecisionRejected OperationDecision = "rejected"
	DecisionPending  OperationDecision = "pending"
)

type MutationEntry struct {
	Timestamp          string            `json:"timestamp"`
	File               string            `json:"file"`
	Action             string            `json:"action"`
	PatchID            string            `json:"patch_id,omitempty"`
	Content            string            `json:"content,omitempty"`
	Capability         string            `json:"capability,omitempty"`
	RiskLevel          string            `json:"risk_level,omitempty"`
	Decision           OperationDecision `json:"decision,omitempty"`
	VerificationPassed *bool             `json:"verification_passed,omitempty"`
	ContextID          string            `json:"context_id,omitempty"`
}

type ShellEntry struct {
	Timestamp  string            `json:"timestamp"`
	Command    string            `json:"command"`
	ExitCode   int               `json:"exit_code"`
	WorkingDir string            `json:"working_dir"`
	StdoutPref string            `json:"stdout_preview,omitempty"`
	Capability string            `json:"capability,omitempty"`
	RiskLevel  string            `json:"risk_level,omitempty"`
	Decision   OperationDecision `json:"decision,omitempty"`
}

type PipelineEntry struct {
	Timestamp          string            `json:"timestamp"`
	Stage              string            `json:"stage"`
	Status             string            `json:"status"`
	Capability         string            `json:"capability,omitempty"`
	RiskLevel          string            `json:"risk_level,omitempty"`
	Decision           OperationDecision `json:"decision,omitempty"`
	VerificationPassed *bool             `json:"verification_passed,omitempty"`
	Detail             string            `json:"detail,omitempty"`
	Duration           string            `json:"duration,omitempty"`
	ContextID          string            `json:"context_id,omitempty"`
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
	return l.append(state.LocalPath(l.root, state.AuditDir, state.MutationsLogFile), data)
}

func (l *Logger) LogShell(entry ShellEntry) error {
	entry.Timestamp = time.Now().UTC().Format(time.RFC3339)
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	return l.append(state.LocalPath(l.root, state.AuditDir, state.ShellLogFile), data)
}

func (l *Logger) LogPipeline(entry PipelineEntry) error {
	entry.Timestamp = time.Now().UTC().Format(time.RFC3339)
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	return l.append(state.LocalPath(l.root, state.AuditDir, "pipeline.log"), data)
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

func (l *Logger) ReadPipeline() ([]PipelineEntry, error) {
	return readEntries[PipelineEntry](state.LocalPath(l.root, state.AuditDir, "pipeline.log"))
}

func (l *Logger) ReadRecent(limit int) string {
	entries, err := l.ReadPipeline()
	if err != nil || len(entries) == 0 {
		return "no audit entries"
	}

	start := len(entries) - limit
	if start < 0 {
		start = 0
	}

	var b strings.Builder
	entries = entries[start:]
	for _, e := range entries {
		ts := e.Timestamp
		if len(ts) > 16 {
			ts = ts[11:19]
		}
		fmt.Fprintf(&b, "%s  ", ts)

		fmt.Fprintf(&b, "Stage: %s  Status: %s", e.Stage, e.Status)
		if e.Capability != "" {
			fmt.Fprintf(&b, "  Capability: %s", e.Capability)
		}
		if e.RiskLevel != "" {
			fmt.Fprintf(&b, "  Risk: %s", e.RiskLevel)
		}
		if e.Decision != "" {
			fmt.Fprintf(&b, "  Decision: %s", e.Decision)
		}
		if e.VerificationPassed != nil {
			v := "Passed"
			if !*e.VerificationPassed {
				v = "Failed"
			}
			fmt.Fprintf(&b, "  Verification: %s", v)
		}
		b.WriteString("\n")
	}
	return b.String()
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
