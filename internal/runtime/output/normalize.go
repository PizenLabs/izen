package output

import (
	"regexp"
	"strings"
)

// ansiSequenceRE matches the ANSI escape families that pollute raw tool
// output: CSI sequences (colors, cursor moves, clear-line), OSC sequences
// (window titles), and the short two-character escapes. Anything the shell
// injected for a human terminal is stripped before the classifier sees it.
var ansiSequenceRE = regexp.MustCompile(`\x1b(?:\[[0-9;?]*[ -/]*[@-~]|\][^\x07]*(?:\x07|\x1b\\)|[@-Z\\-_])`)

// Normalize cleans raw terminal bytes into stable text for the classifier and
// compressor:
//
//  1. Strips ANSI escape codes (colors, cursor movement, OSC titles).
//  2. Unifies carriage returns: CRLF and lone CR both become LF.
//  3. Normalizes invalid UTF-8 to the Unicode replacement character.
//
// It is idempotent: Normalize(Normalize(s)) == Normalize(s).
func Normalize(raw []byte) string {
	s := ansiSequenceRE.ReplaceAllString(string(raw), "")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.ToValidUTF8(s, "\uFFFD")
}
