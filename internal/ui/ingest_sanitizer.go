package ui

import (
	"regexp"
	"strings"
)

// ingestHardwareRE targets HARDWARE cursor-movement / line-clear / screen-reset
// sequences that force terminal emulator repositioning when leaked into chat
// history. It deliberately NEVER matches SGR color/style sequences (\x1b[...m)
// which are required for lipgloss styling.
//
// Groups:
//   - Visibility: \x1b[?25h / \x1b[?25l
//   - Positioning: \x1b[H, \x1b[<row>;<col>H, \x1b[<row>;<col>f
//   - Movement:  \x1b[<n>A/B/C/D/E/F/G
//   - Erase:     \x1b[2J (screen), \x1b[2K (line)
//   - Save/restore: \x1b[s, \x1b[u, \x1b7, \x1b8
//
// SGR (\x1b[...m) is excluded because its final byte is 'm'.
var ingestVisibilityRe = regexp.MustCompile(`\x1b\[\?25[hl]`)
var ingestCSICursorRe = regexp.MustCompile(`\x1b\[[0-9;]*[A-HJKsuf]`)
var ingestCSIFLowerRe = regexp.MustCompile(`\x1b\[[0-9;]*f`)
var ingestEscSaveRestoreRe = regexp.MustCompile(`\x1b[78]`)

// SanitizeForIngest is the DEDICATED ingestion-time sanitizer. It MUST be
// called at every ingress seam BEFORE text is committed to state memory or
// viewport buffers. It strips hardware cursor control sequences and normalizes
// carriage returns, while preserving SGR color/style sequences.
//
// Invariants:
//   - ZERO render-path regex: View() operates on pre-sanitized slices; no regex
//     is evaluated during scroll/render.
//   - INGESTION-TIME PURGING: caller sanitizes BEFORE storing.
//   - COLOR PRESERVATION: SGR \x1b[...m remains intact.
func SanitizeForIngest(s string) string {
	if s == "" {
		return s
	}
	// Normalize line breaks strictly to \n before regex so \r never leaks.
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")

	// Strip hardware cursor sequences. Order: visibility first, then CSI
	// cursor/erase/position, then f-variant, then ESC 7/8. Each pass is
	// pre-compiled and shared globally — zero per-frame compilation.
	s = ingestVisibilityRe.ReplaceAllString(s, "")
	s = ingestCSICursorRe.ReplaceAllString(s, "")
	s = ingestCSIFLowerRe.ReplaceAllString(s, "")
	s = ingestEscSaveRestoreRe.ReplaceAllString(s, "")

	// Also strip orphaned CURSOR hide/show fragments that survived an earlier
	// ESC-strip in sanitizeIngressANSI's rune-path (e.g. "[?25h" left after
	// \x1b was stripped elsewhere). They match without the leading ESC.
	// We handle them inline to avoid an extra regex: "[?25h"/"[?25l".
	// This is a rare stale fragment; a strings.ReplaceAll is cheaper than regex.
	s = strings.ReplaceAll(s, "[?25h", "")
	s = strings.ReplaceAll(s, "[?25l", "")

	return s
}
