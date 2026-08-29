package ui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// roleHeader maps the internal record role to the canonical copy header.
// The headers are stable, human-readable, and presentation-independent.
// They deliberately match the spec's example vocabulary (USER, ASSISTANT, TOOL,
// ERROR, EXECUTION) while preserving every role that exists in the model.
func roleHeader(r role) string {
	switch r {
	case roleUser:
		return "USER"
	case roleAI:
		return "ASSISTANT"
	case roleError:
		return "ERROR"
	case roleSystem:
		return "TOOL"
	case roleStatus:
		return "EXECUTION"
	case roleActivity:
		return "ACTIVITY"
	case roleCode:
		return "CODE"
	default:
		return "SYSTEM"
	}
}

// SerializeTranscript renders the canonical conversation transcript as
// deterministic plain text for clipboard copy.
//
// Guarantees:
//   - No ANSI escape sequences (stripped via ansi.Strip).
//   - No dependence on viewport position, terminal width, wrapping, or styling.
//   - No spinner frames or transient UI artifacts (records never contain them).
//   - Code blocks and multiline tool output are preserved verbatim, line-by-line.
//   - Each logical record is rendered exactly once with a stable header boundary.
//   - Deterministic: same records always produce the same string.
func SerializeTranscript(records []record) string {
	if len(records) == 0 {
		return ""
	}
	var b strings.Builder
	first := true
	for _, rec := range records {
		header := roleHeader(rec.role)
		// Strip ANSI and normalize line endings.
		content := ansi.Strip(rec.text)
		content = strings.ReplaceAll(content, "\r\n", "\n")
		content = strings.ReplaceAll(content, "\r", "\n")
		// Trim a single trailing newline so the separator logic is deterministic.
		// Internal blank lines and indentation (code blocks) are preserved.
		content = strings.TrimRight(content, "\n")
		if strings.TrimSpace(content) == "" {
			continue
		}
		if !first {
			b.WriteString("\n\n")
		}
		first = false
		b.WriteString(header)
		b.WriteString("\n")
		b.WriteString(content)
	}
	return b.String()
}

// handleCopy executes the /copy command: serializes the complete canonical
// transcript and writes it to the system clipboard.
//
// It is safe to execute repeatedly, never mutates conversation state, and
// surfaces a transient footer notice on success or failure without inserting a
// permanent record (so the notice is never recursively copied).
func (m *model) handleCopy() {
	serialized := SerializeTranscript(m.records)
	if strings.TrimSpace(serialized) == "" {
		m.uiNotice = "Nothing to copy — conversation is empty"
		return
	}
	var err error
	if m.clipboard != nil {
		err = m.clipboard.WriteAll(serialized)
	} else {
		err = clipboardWriteAll(serialized)
	}
	if err != nil {
		m.uiNotice = "Failed to copy conversation: clipboard unavailable"
		return
	}
	m.uiNotice = "Copied conversation to clipboard"
}
