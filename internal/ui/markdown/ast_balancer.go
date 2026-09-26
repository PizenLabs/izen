package markdown

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// ── TRANSIENT AST BALANCER ─────────────────────────────────────────────────────
//
// # THE DEFECT
//
// A stream delivers the trailing line of an answer a few bytes at a time, so the
// renderer only ever sees PREFIXES of it. The block holdback (see Incomplete)
// answers that correctly for BLOCK grammar — a table row is diverted wholesale —
// but inline grammar has no equivalent: the moment a `**` has arrived and its
// closer has not, the bytes on screen are two literal asterisks. The reader sees
//
//	**The migration is complete
//
// and then, one frame later,
//
//	The migration is complete          (bold)
//
// That is the "raw markup flashes in while the answer streams" report, and it is
// the single most visible streaming artefact there is, because it repeats on
// every emphasised phrase in the answer.
//
// # THE FIX: CLOSE IT TRANSIENTLY
//
// BalanceTrailingLine takes the trailing line and returns a COPY with the
// matching closing delimiters appended, so the frame's AST pass sees a
// syntactically complete line on the very first tick:
//
//	"**The migration is complete"  ->  "**The migration is complete**"
//
// Three properties make this safe rather than a clever bug:
//
//  1. THE COPY IS TRANSIENT AND LOCAL. It exists for one frame's parse and is
//     then thrown away. The next frame re-balances the new prefix from scratch.
//     The caller's own buffer is never touched — Go strings are immutable, and
//     the returned value is either the input string itself (nothing was open) or
//     a freshly allocated copy. Nothing is ever written back through a pointer
//     the message buffer owns.
//
//  2. A BALANCED LINE IS RETURNED UNTOUCHED. When nothing is open the function
//     returns `line` itself, byte for byte, with no allocation. So the
//     overwhelmingly common case (a line with no inline markup, or one whose
//     markers have all closed) costs one linear scan and nothing else, and it is
//     impossible for the balancer to alter a line that did not need altering.
//
//  3. ONLY DELIMITERS THAT CAN ACTUALLY OPEN ARE OPENED. A run surrounded by
//     whitespace cannot open emphasis in CommonMark, so `2 * 3`, `* item` and
//     `a * b` are left completely alone. That is what keeps the balancer from
//     "styling" arithmetic and list bullets mid-stream.
//
// # WHAT IS DELIBERATELY NOT BALANCED
//
// `_` is excluded. It is a legal emphasis delimiter, but a lone `_` is far more
// often a snake_case identifier or a leading-underscore symbol than an emphasis
// opener, and the mistake is invisible: `_private` would be transiently
// italicised and then snap back to literal text at commit. That is a worse
// artefact than the one this file exists to remove, so the asymmetry is the
// point. Callers that want it can pair the run themselves.
//
// RESIDUAL, AND STATED PLAINLY: the trailing line is a prefix of a line that has
// not been written yet, so a model that opens emphasis and never closes it
// (`see the * wildcard`) gets one frame of provisional styling. That is inherent
// to showing the style at all — the alternative is showing the raw marker — and
// it is bounded by the line, self-heals at the next newline, and never touches a
// committed line.

// balancerStackDepth bounds the inline delimiter stack. The stack lives in a
// fixed-size array, so the scan allocates nothing at any depth: a line with more
// open delimiters than this simply stops tracking the excess and is balanced only
// as far as the stack reached. Sixteen is far beyond any real prose line — a
// pathological line of nothing but `**` is a model bug, not a rendering case —
// and a fixed bound is what makes the function O(1) in memory rather than
// O(delimiters).
const balancerStackDepth = 16

// maxMarkerRun is the longest inline marker run CommonMark gives meaning to.
// A run of one, two or three `*` is emphasis, strong, or emphasis+strong; a
// longer run is a thematic break and is not an inline delimiter at all.
const maxMarkerRun = 3

// delimKind is an inline delimiter family. The run length is carried alongside
// it because identity is per-family AND per-length: `*` does not close `**`, and
// a one-backtick code span does not close a two-backtick one.
type delimKind uint8

const (
	delimStar delimKind = iota
	delimTilde
	delimCode
)

// openDelim is one entry of the inline delimiter stack.
type openDelim struct {
	kind delimKind
	run  uint8
}

// marker returns the single character this delimiter is built from.
func (d openDelim) marker() string {
	if d.kind == delimCode {
		return "`"
	}
	if d.kind == delimTilde {
		return "~"
	}
	return "*"
}

// closer returns the transient closing delimiter for this opener.
func (d openDelim) closer() string {
	return strings.Repeat(d.marker(), int(d.run))
}

// trailingRunOf returns the length of the run of d's marker character at the
// very end of s, so a closer that is already half-typed can be finished rather
// than doubled.
func trailingRunOf(s string, d openDelim) int {
	m := d.marker()
	run := 0
	for i := len(s) - 1; i >= 0 && s[i] == m[0]; i-- {
		run++
	}
	return run
}

// BalanceTrailingLine returns the copy of the still-growing trailing line that
// the frame's AST pass should parse: the line with a matching closer appended
// for every inline delimiter still open at its end.
//
// The input is never modified. When no delimiter is open the input string is
// returned unchanged and unallocated, so the balanced and raw forms are the same
// string in the overwhelmingly common case and the caller can compare them with
// a single equality check to decide whether anything was added.
//
// Trailing whitespace is excluded from the scan and re-appended AFTER the
// closers, because a closing delimiter that is separated from its content by a
// space is not a closer at all (`**bold **` does not close), and a token ending
// in a space is a normal thing for a stream to emit mid-phrase.
func BalanceTrailingLine(line string) string {
	if line == "" {
		return line
	}
	end := len(line)
	for end > 0 && isSpaceByte(line[end-1]) {
		end--
	}
	body := line[:end]
	if body == "" {
		return line
	}

	// ── FENCE MARKERS ARE NOT INLINE DELIMITERS ──────────────────────────
	// A leading run of backticks or tildes is BLOCK grammar, owned by the
	// holdback: it is either a complete fence or a fence still arriving. Either
	// way it must come back byte-identical, because a transient closer appended
	// to a fence marker would turn ``` into a fenced block boundary and hand the
	// rest of the line to a highlighter that does not exist yet.
	if isFenceLine(body) {
		return line
	}
	if kind, ok := fenceIncomplete(body); ok && kind == BlockFence {
		return line
	}

	// Fixed-size stack: no allocation, no growth, bounded depth.
	var stack [balancerStackDepth]openDelim
	depth := 0

	for i := 0; i < len(body); {
		c := body[i]
		if c == '\\' {
			// A backslash escapes the byte that follows it, so neither the
			// backslash nor an escaped marker can open or close anything.
			i += 2
			continue
		}
		if c != '*' && c != '~' && c != '`' {
			i++
			continue
		}
		run := 1
		for i+run < len(body) && body[i+run] == c {
			run++
		}
		i += run

		switch c {
		case '`':
			// A code span is delimited by backtick strings of EQUAL length and
			// has no flanking rules at all, so it is matched purely on run
			// length. A run longer than the meaningful maximum is left alone.
			if run > maxMarkerRun {
				continue
			}
			d := openDelim{kind: delimCode, run: uint8(run)}
			if closer := indexBacktickRun(body, i, run); closer >= 0 {
				// A closed span: everything up to the closer is code, and
				// nothing inside it is an emphasis delimiter. Skipping it is what
				// keeps `a*b` from acquiring a stray asterisk.
				i = closer + run
				continue
			}
			// An open span runs to the end of the line, so there is nothing
			// after it that could be a delimiter.
			depth = balanceStack(&stack, depth, d, true, false)
			i = len(body)

		case '~':
			// GFM strikethrough needs a run of two; a single `~` is ordinary
			// text. Longer runs are normalised to two so `~~~` still pairs.
			if run < 2 {
				continue
			}
			if run > maxMarkerRun {
				run = 2
			}
			left := leftFlanking(body, i, run)
			right := rightFlanking(body, i, run)
			depth = balanceStack(&stack, depth, openDelim{kind: delimTilde, run: uint8(run)}, left, right)

		default: // '*'
			// A single leading `* ` is a LIST BULLET, not emphasis. Reading it as
			// an opener would italicise every bullet in a streamed list.
			if run == 1 && onlySpaceBefore(body, i-run) && (i >= end || isSpaceByte(body[i])) {
				continue
			}
			if run > maxMarkerRun {
				run = maxMarkerRun
			}
			left := leftFlanking(body, i, run)
			right := rightFlanking(body, i, run)
			depth = balanceStack(&stack, depth, openDelim{kind: delimStar, run: uint8(run)}, left, right)
		}
	}

	if depth == 0 {
		return line
	}

	var b strings.Builder
	b.Grow(end + balancerStackDepth*maxMarkerRun)
	b.WriteString(body)
	// Innermost first: a closer must close the delimiter it matches, and a
	// nested span is always inside the span that contains it.
	//
	// A closer already PARTLY typed at the end of the line is completed in place
	// rather than appended next to it. This matters for a reason that is visible
	// on screen: `**bold conclusion*` is the last frame before the model finishes
	// typing the closer, and appending a fresh `**` beside the star already there
	// produces `***`, which is a different construct (emphasis + strong) rather
	// than a closed one — the phrase would go bold, then unbold, then bold again.
	// Completing the run turns it into the `**` the model is in the middle of
	// typing, so the frame shows the same bold it will show a tick later.
	for k := depth - 1; k >= 0; k-- {
		d := stack[k]
		if have := trailingRunOf(body, d); have > 0 && have < int(d.run) {
			b.WriteString(strings.Repeat(d.marker(), int(d.run)-have))
			continue
		}
		b.WriteString(d.closer())
	}
	b.WriteString(line[end:])
	balanced := b.String()

	// ── VERIFY, THEN COMMIT ───────────────────────────────────────────────
	// Closing the stack is not by itself proof of balance: a nested line whose
	// closers collide with the markers already on it can come out still
	// ambiguous (`**bold *italic` balances to `***italic***`, which is a
	// different reading, not a closed one). So the candidate is re-classified
	// with the same detector the holdback uses, and anything that is not now
	// structurally final is DISCARDED in favour of the raw line.
	//
	// The result is the property the whole mechanism rests on: this function
	// returns either the input string unchanged, or a line that needs no
	// holdback at all. It can never return a half-balanced line, so a caller can
	// never paint a marker the detector still considers open.
	if _, incomplete := Incomplete(balanced, false, false); incomplete {
		return line
	}
	return balanced
}

// indexBacktickRun returns the byte offset of the next backtick run of exactly
// run bytes at or after from, or -1 when there is none. Only an exactly equal
// length closes a code span, so ``` is not closed by a single `.
func indexBacktickRun(s string, from, run int) int {
	for i := from; i < len(s); i++ {
		if s[i] != '`' {
			continue
		}
		n := 1
		for i+n < len(s) && s[i+n] == '`' {
			n++
		}
		if n == run {
			return i
		}
		i += n - 1
	}
	return -1
}

// balanceStack resolves one marker run against the delimiter stack and returns
// the new depth.
//
// The precedence is CommonMark's: a run that can CLOSE and has a matching opener
// closes it; otherwise a run that can OPEN is pushed as a new opener. Closing
// first is what makes `**a**` balanced rather than nested, and opening second is
// what makes the scan a single left-to-right pass with no backtracking.
//
// A run that can do neither (surrounded by whitespace, say) is not a delimiter
// at all and is dropped. A push at maximum depth is also dropped: the excess is
// left untracked, which under-balances a pathological line rather than
// mis-styling a normal one.
func balanceStack(stack *[balancerStackDepth]openDelim, depth int, d openDelim, canOpen, canClose bool) int {
	if canClose {
		for k := depth - 1; k >= 0; k-- {
			if stack[k] == d {
				return k
			}
		}
	}
	if canOpen && depth < balancerStackDepth {
		stack[depth] = d
		return depth + 1
	}
	return depth
}

// leftFlanking reports whether the marker run that starts at i and is run bytes
// long can OPEN emphasis under CommonMark: it must not be followed by
// whitespace, and when it IS followed by punctuation it must be preceded by
// whitespace, punctuation, or the start of the line.
func leftFlanking(s string, i, run int) bool {
	next, _ := decodeRuneAt(s, i)
	if next == 0 || unicode.IsSpace(next) {
		return false
	}
	if !isPunctRune(next) {
		return true
	}
	prev, _ := decodeRuneBefore(s, i)
	return prev == 0 || unicode.IsSpace(prev) || isPunctRune(prev)
}

// rightFlanking reports whether the marker run that ends at i-1 and started at
// i-run can CLOSE emphasis: it must not be preceded by whitespace, and when it
// IS preceded by punctuation it must be followed by whitespace, punctuation, or
// the end of the line.
func rightFlanking(s string, i, run int) bool {
	prev, _ := decodeRuneBefore(s, i-run)
	if prev == 0 || unicode.IsSpace(prev) {
		return false
	}
	next, _ := decodeRuneAt(s, i)
	if next == 0 || !isPunctRune(next) {
		return true
	}
	return unicode.IsSpace(next) || isPunctRune(next)
}

// onlySpaceBefore reports whether s[:i] is nothing but whitespace, i.e. the
// marker run at i sits at the start of the line (ignoring indentation).
func onlySpaceBefore(s string, i int) bool {
	for k := 0; k < i; k++ {
		if !isSpaceByte(s[k]) {
			return false
		}
	}
	return true
}

func isSpaceByte(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

// isPunctRune reports whether r counts as punctuation for CommonMark's
// left/right-flanking rules. An ASCII fast path keeps the scan on bytes for the
// overwhelming majority of real prose; everything else defers to the Unicode
// classes, which is what makes flanking correct around CJK and accented text.
func isPunctRune(r rune) bool {
	if r == utf8.RuneError {
		return false
	}
	if r < utf8.RuneSelf {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return false
		}
		return true
	}
	return unicode.IsPunct(r) || unicode.IsSymbol(r)
}

// decodeRuneAt decodes the rune starting at byte offset i. It returns (0, 0) at
// or past the end of the string, which the flanking tests read as "no
// character" — the same as CommonMark's end-of-line.
func decodeRuneAt(s string, i int) (rune, int) {
	if i < 0 || i >= len(s) {
		return 0, 0
	}
	return utf8.DecodeRuneInString(s[i:])
}

// decodeRuneBefore decodes the rune that ENDS at byte offset i. It returns
// (0, 0) at or before the start of the string, which the flanking tests read as
// "no character" — the same as CommonMark's beginning-of-line.
func decodeRuneBefore(s string, i int) (rune, int) {
	if i <= 0 || i > len(s) {
		return 0, 0
	}
	return utf8.DecodeLastRuneInString(s[:i])
}
