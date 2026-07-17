package context

import (
	"regexp"
	"strings"

	"github.com/PizenLabs/izen/internal/modes/plan"
	"github.com/PizenLabs/izen/internal/session"
)

// ── Ledger Handoff Sanitizer ──────────────────────────────────────────────────
//
// handoffLedgerContent (FormatLedgerForPlan output) can be large and repetitive:
// interleaved system stack traces, container/runtime setup logs, ANSI escapes,
// and duplicated diagnostic blocks. Feeding that raw into the plan engine
// inflates the prompt and risks stalling local LLM generation loops.
//
// SanitizeLedger collapses that noise into a compact, structured handoff:
// it strips ANSI, repeated stack frames, verbose runtime/container logs, and
// boilerplate, keeping only the signal the engine needs (the failure scope,
// the failing file/package, the offending dependency, and the actionable
// stack tail). It is intentionally file-agnostic — it does NOT hardcode any
// specific error or path.

var (
	ansiRE         = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)
	dupBlankLineRE = regexp.MustCompile(`\n{3,}`)
)

// SanitizeLedger returns a slimmed, de-duplicated version of the raw ledger
// handoff content. It applies ONLY structural noise reduction:
//   - strip ANSI escapes (display artifacts, never signal)
//   - collapse 3+ blank lines to a single blank line
//
// It deliberately does NOT drop per-line content. The previous implementation
// stripped runtime/container log lines and collapsed stack frames, which
// destroyed structured diagnostic signal (e.g. the offending dependency in a
// "no required module provides package" error, or "rootless docker not found"
// environment blockers) and broke state continuity between modes. Per the Izen
// Lifecycle Philosophy, the operational data structure must be preserved — only
// display noise is removed. Empty / already-clean input passes through unchanged.
func SanitizeLedger(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return raw
	}

	// 1. Strip ANSI escapes (display artifacts, never signal).
	clean := ansiRE.ReplaceAllString(raw, "")

	// 2. Normalize whitespace only — preserve every diagnostic line. Collapse
	//    3+ blank lines to a single blank line so the payload stays compact
	//    without losing structural content.
	clean = dupBlankLineRE.ReplaceAllString(clean, "\n\n")
	clean = strings.TrimSpace(clean)

	return clean
}

// SanitizeLedgerPreserve is the canonical sanitizer for the typed handoff
// ledger (session.ContextLedger). Unlike the legacy line-dropping approach, it
// PRESERVES the operational data structure — every injected packet, its
// sequential PacketID, its payload, and the raw diagnostics — so that state is
// not lost across the mode transition. It only strips ANSI display artifacts
// from string payloads and normalizes whitespace. The Developer retains full,
// deterministic control of the structured handoff.
func SanitizeLedgerPreserve(l *session.ContextLedger) *session.ContextLedger {
	if l == nil {
		return nil
	}
	out := *l
	out.Diagnostics = stripANSI(out.Diagnostics)
	if out.Packets != nil {
		pkts := make([]session.LedgerPacket, len(out.Packets))
		for i, p := range out.Packets {
			p.Payload = stripANSI(p.Payload)
			pkts[i] = p
		}
		out.Packets = pkts
	}
	return &out
}

func stripANSI(s string) string {
	if s == "" {
		return s
	}
	clean := ansiRE.ReplaceAllString(s, "")
	clean = dupBlankLineRE.ReplaceAllString(clean, "\n\n")
	return strings.TrimSpace(clean)
}

// ── Build Handoff Sanitizer (Plan → Build) ────────────────────────────────────
//
// SanitizeBuildHandoff creates a minimal, focused context payload for the
// /build mode. It strips ALL conversational history, raw chat logs, and
// long system diagnostics, keeping ONLY:
//  1. The exact target file path(s) from the staged task
//  2. The exact staged task description(s) from PendingTodos
//  3. The raw relevant symbol definition/context (if available via graph)
//
// This prevents cognitive drift where the LLM loses track of system constraints
// and formatting rules (patch/unified diff schemas) due to bloated context.
func SanitizeBuildHandoff(task *plan.Task, symbolContext string) string {
	if task == nil {
		return ""
	}

	var b strings.Builder
	b.WriteString("## BUILD HANDOFF — STRUCTURED EXECUTION\n\n")
	b.WriteString("Execute ONLY the following task. Output unified diff or FILE: block.\n")
	b.WriteString("Do NOT restate the plan, do NOT explain, do NOT list other tasks.\n\n")

	b.WriteString("### TASK\n")
	b.WriteString(task.Type + ": " + task.Target)
	if task.Description != "" {
		b.WriteString(" — " + task.Description)
	}
	b.WriteString("\n\n")

	if symbolContext != "" {
		b.WriteString("### SYMBOL CONTEXT\n")
		b.WriteString(symbolContext)
		b.WriteString("\n\n")
	}

	b.WriteString("### INSTRUCTION\n")
	b.WriteString("Produce the minimal code change to complete this task. ")
	b.WriteString("Use unified diff format (```diff) or FILE: block format. ")
	b.WriteString("No conversational text, no markdown outside code blocks.")

	return strings.TrimSpace(b.String())
}

// SanitizeSourceForLLM strips non-essential inline comments and legacy
// developer notes from source code before feeding it to the LLM. This
// prevents stale TODO/FIXME comments, outdated documentation, and
// verbose explanatory comments from acting as prompt injections that
// distract the model from the actual task.
//
// It preserves:
//   - Function/method signatures and their doc comments (if short)
//   - Type definitions and their doc comments (if short)
//   - Export control comments (//go:build, // +build, etc.)
//
// It removes:
//   - Inline // comments longer than 80 chars (likely explanations)
//   - Block comments /* ... */ not directly attached to declarations
//   - Lines containing only TODO, FIXME, NOTE, HACK, BUG (case-insensitive)
//   - Stale copyright/license headers beyond the first 5 lines
func SanitizeSourceForLLM(source string, lang string) string {
	lines := strings.Split(source, "\n")
	var result []string
	inBlockComment := false
	consecutiveEmpty := 0

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Skip stale copyright/license headers (first 10 lines, detect by keywords)
		if i < 10 && (strings.Contains(strings.ToLower(trimmed), "copyright") ||
			strings.Contains(strings.ToLower(trimmed), "license") ||
			strings.Contains(strings.ToLower(trimmed), "author")) {
			continue
		}

		// Handle block comments
		if strings.Contains(line, "/*") && strings.Contains(line, "*/") {
			// Single-line block comment /* ... */
			cleaned := regexp.MustCompile(`/\*.*?\*/`).ReplaceAllString(line, "")
			if strings.TrimSpace(cleaned) != "" {
				result = append(result, cleaned)
			}
			continue
		}

		if strings.HasPrefix(trimmed, "/*") {
			inBlockComment = true
			// Check if it's a doc comment (starts with /** or /*!)
			if strings.HasPrefix(trimmed, "/**") || strings.HasPrefix(trimmed, "/*!") {
				// Keep short doc comments
				if len(trimmed) < 200 {
					result = append(result, line)
				}
			}
			continue
		}

		if inBlockComment {
			if strings.HasSuffix(trimmed, "*/") {
				inBlockComment = false
			}
			continue
		}

		// Strip inline // comments that are long explanatory text
		if idx := strings.Index(line, "//"); idx >= 0 {
			comment := line[idx:]
			code := line[:idx]

			// Preserve go:build, +build, line directives
			if strings.Contains(comment, "go:build") || strings.Contains(comment, "+build") ||
				strings.HasPrefix(strings.TrimSpace(comment), "//line") {
				result = append(result, line)
				continue
			}

			// Remove TODO, FIXME, NOTE, HACK, BUG comments
			upperComment := strings.ToUpper(comment)
			if strings.Contains(upperComment, "TODO") || strings.Contains(upperComment, "FIXME") ||
				strings.Contains(upperComment, "NOTE:") || strings.Contains(upperComment, "HACK") ||
				strings.Contains(upperComment, "BUG:") {
				// Keep only the code part
				if strings.TrimSpace(code) != "" {
					result = append(result, strings.TrimRight(code, " \t"))
				}
				continue
			}

			// Strip long inline comments (>80 chars) - likely explanations
			if len(comment) > 80 {
				if strings.TrimSpace(code) != "" {
					result = append(result, strings.TrimRight(code, " \t"))
				}
				continue
			}

			// Short inline comment - keep it
			result = append(result, line)
			continue
		}

		// Skip lines that are ONLY TODO/FIXME/NOTE/HACK/BUG
		upperLine := strings.ToUpper(trimmed)
		if strings.HasPrefix(upperLine, "// TODO") || strings.HasPrefix(upperLine, "// FIXME") ||
			strings.HasPrefix(upperLine, "// NOTE") || strings.HasPrefix(upperLine, "// HACK") ||
			strings.HasPrefix(upperLine, "// BUG") {
			continue
		}

		// Collapse multiple empty lines
		if trimmed == "" {
			consecutiveEmpty++
			if consecutiveEmpty <= 1 {
				result = append(result, "")
			}
			continue
		}
		consecutiveEmpty = 0
		result = append(result, line)
	}

	return strings.Join(result, "\n")
}
