package compact

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// EstimateTokens approximates the token count of text using a ~4
// characters-per-token heuristic (the classic byte→token ratio).
func EstimateTokens(text string) int {
	n := len([]rune(text))
	if n == 0 {
		return 0
	}
	return (n + 3) / 4
}

// Stats reports the outcome of an Optimize pass.
type Stats struct {
	OriginalBytes       int // input size in bytes
	NewBytes            int // optimized size in bytes
	OriginalTokens      int // estimated input tokens
	NewTokens           int // estimated output tokens
	LinesRemoved        int // prose/comment/duplicate lines dropped
	CodeBlocksPreserved int // fenced code blocks kept byte-for-byte
}

// SavingsPercent returns the relative size reduction (0..100).
func (s Stats) SavingsPercent() float64 {
	if s.OriginalBytes <= 0 {
		return 0
	}
	return 100 * (1 - float64(s.NewBytes)/float64(s.OriginalBytes))
}

// TargetFileNames are the prompt-overhead files scanned when no explicit paths
// are given to the workspace context optimizer.
var TargetFileNames = []string{"AGENTS.md", "RULES.md", "CLAUDE.md", "GEMINI.md", "README.md"}

// DiscoverFiles returns the prompt-overhead files under root: the known
// top-level target names (AGENTS.md, RULES.md, CLAUDE.md, GEMINI.md,
// README.md) plus every *.md file under root/docs. Only existing files are
// returned, in deterministic order.
func DiscoverFiles(root string) ([]string, error) {
	var found []string
	for _, name := range TargetFileNames {
		p := filepath.Join(root, name)
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			found = append(found, p)
		}
	}

	docs := filepath.Join(root, "docs")
	if info, err := os.Stat(docs); err == nil && info.IsDir() {
		err := filepath.WalkDir(docs, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			if strings.HasSuffix(strings.ToLower(d.Name()), ".md") {
				found = append(found, path)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}

	return found, nil
}

// Protect regex. Everything matched here is preserved byte-for-byte: inline
// code spans, file paths, and command-line flags/variables.
var protectRe = regexp.MustCompile("`[^`]*`|\\b[A-Za-z0-9_.-]+(?:/[A-Za-z0-9_.-]+)+|\\B--?[A-Za-z][A-Za-z0-9-]*")

// Line/paragraph classification regexes.
var (
	commentLineRe = regexp.MustCompile(`^\s*(<!--.*|\[(?://|comment)\]:\s*#.*)$`)

	boilerplateRe = regexp.MustCompile(`(?i)^\s*(?:this (?:file|document|section|guide|page) (?:contains|provides|lists|describes|serves as|acts as)|this (?:file|document) (?:is|was|has been)|this is (?:the )?(?:main|primary|canonical|definitive) (?:file|document|source|reference)|this (?:file|document) is (?:a|an)|please read this|read this (?:file|document) first)\b[\s\S]*$`)

	conversationalRe = regexp.MustCompile(`(?i)^\s*(?:hello|hi|hey|hey there|greetings|welcome|thanks|thank you|thanks for reading|thank you for reading|good luck|you're welcome|enjoy|happy to help|glad to help|hope this helps|i hope this helps|any questions\??|cheers|best regards|kind regards|regards|sincerely)(?:[.!,\s]*)$`)

	greetingLeadRe = regexp.MustCompile(`(?i)^\s*(?:hello|hi|hey|hey there|greetings|welcome|howdy)\b`)

	// metaTailRe matches unambiguous closing/meta phrases that add no technical
	// content and may appear anywhere in a line.
	metaTailRe = regexp.MustCompile(`(?i)(?:please don't hesitate|don't hesitate to|feel free to reach out|if you have any questions|let me know if you need|happy coding|happy to help|glad to help|thanks for reading|thank you for reading|hope this helps|i hope this helps|any questions\??|good luck)`)
)

// Sentence-level transforms applied after reflowing a prose paragraph.
var (
	discourseLeadRe = regexp.MustCompile(`(?i)^\s*(?:obviously|clearly|surely|naturally|needless to say|as we all know|of course|in fact|indeed|after all|at the end of the day|all things considered|to be honest|to tell you the truth|as it happens|as you can see|as you know|as we can see|in other words|simply put|to put it simply|basically|essentially|in a nutshell|to summarize|in summary|to recap|as mentioned above|as described above|that said|having said that|all in all|in general),?\s+`)

	simplicitySentenceRe = regexp.MustCompile(`(?i)^\s*(?:the|this|that|it|running|writing|using|building|testing|setting up|getting started|deploying|installing|using the)\b[^.!?]{0,50}\s+(?:is|are|was|were)\s+(?:very|really|quite|pretty|extremely|so|super|totally|truly|rather)(?:\s+(?:very|really|quite|pretty|extremely|so|super|totally|truly|rather))?\s+(?:easy|simple|straightforward|quick|fast|trivial|painless)\s*[.!?]*$`)

	danglingWhichRe = regexp.MustCompile(`(?i)^\s*(?:which|that)\s+(?:is|was|are|were|seems|looks)\s+(?:really|quite|very|simply|extremely|pretty|truly|super|totally|rather|so)(?:\s+(?:really|quite|very|simply|extremely|pretty|truly|super|totally|rather|so))*\s+(?:simple|easy|straightforward|standard|common|basic|trivial|obvious|normal|typical)\b[^.!?]*[.!?]*$`)

	fragmentTailRe = regexp.MustCompile(`(?i)^\s*(?:for|in|of|about|with|to|at|like|such as)\b[^.!?]{0,40}\s*(?:this|these|that|here|there|it|them|those|one|all|both)\s*\.?$`)

	tailMetaRe = regexp.MustCompile(`(?i)(?:\s*[—\-–,]\s*)?(?:please\s+)?(?:keep this in mind|remember this|take note of this)(?:\s+at all times)?[.!?]*$`)

	sentenceRe = regexp.MustCompile(`[^.!?]+[.!?]*`)
)

// Line-level prose transforms applied outside protected spans.
var (
	redundantLeadRe = regexp.MustCompile(`(?i)^(\s*)(?:so,|now,|first,|firstly,|next,|finally,|lastly,|additionally,|moreover,|furthermore,|however,|therefore,|thus,|meanwhile,|alright,|okay,|ok,|right,|hey,|hi,|hello,|well,|in other words,|in a nutshell,|long story short,|simply put,|to summarize,|to sum up,|in summary,|as mentioned above,|as described above,|in conclusion,|to recap,|as you can see,|as we can see,|that being said,|with that said,|basically,|essentially,|literally,|note that,|please note that,|it is important to note that,|remember that,|keep in mind that,|just to clarify,|as always,|of course,|obviously,|at the end of the day,|all things considered,)(\s+)`)

	phraseRe = regexp.MustCompile(`(?i)\b(in order to|due to the fact that|at this point in time|in the event that|for the purpose of|a large number of|with regard to|in regard to)\b`)

	fillerRe = regexp.MustCompile(`(?i)\b(?:it is important to note that|it's worth noting that|its worth noting that|please note that|note that|keep in mind that|as a matter of fact|feel free to|let me know|if you have any questions|of course|simply|just|very|really|extremely|quite|truly|honestly|obviously|importantly|fundamentally|basically|essentially|actually|literally|kind of|sort of|pretty much)\b`)

	spacesRe   = regexp.MustCompile(`[ \t]{2,}`)
	keyStripRe = regexp.MustCompile(`[^a-z0-9]+`)
)

const wrapWidth = 100

// Optimize compresses prose and removes conversational filler from markdown
// config/memory files while preserving every code block, inline code span,
// file path, and command-line flag byte-for-byte.
func Optimize(content string) (string, Stats) {
	stats := Stats{OriginalBytes: len(content)}
	blocks := splitBlocks(content)
	seen := make(map[string]bool)

	var out []string
	for _, block := range blocks {
		emitted := processBlock(block, seen, &stats)
		if len(emitted) > 0 {
			if len(out) > 0 {
				out = append(out, "")
			}
			out = append(out, emitted...)
		}
	}

	result := strings.Join(out, "\n")
	if result != "" {
		result += "\n"
	}
	stats.NewBytes = len(result)
	stats.OriginalTokens = EstimateTokens(content)
	stats.NewTokens = EstimateTokens(result)
	return result, stats
}

// splitBlocks groups the content into blocks separated by blank lines, never
// splitting inside fenced code blocks or multi-line HTML comments.
func splitBlocks(content string) [][]string {
	var blocks [][]string
	var cur []string
	inFence := false
	inComment := false
	flush := func() {
		if len(cur) > 0 {
			blocks = append(blocks, cur)
			cur = nil
		}
	}
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if inFence {
			cur = append(cur, line)
			if strings.HasPrefix(trimmed, "```") {
				inFence = false
			}
			continue
		}
		if inComment {
			cur = append(cur, line)
			if strings.Contains(line, "-->") {
				inComment = false
			}
			continue
		}
		if strings.HasPrefix(trimmed, "```") {
			inFence = true
			cur = append(cur, line)
			continue
		}
		if strings.HasPrefix(trimmed, "<!--") {
			inComment = !strings.Contains(line, "-->")
			cur = append(cur, line)
			continue
		}
		if trimmed == "" {
			flush()
			continue
		}
		cur = append(cur, line)
	}
	flush()
	return blocks
}

// processBlock emits the optimized lines for one block. Fenced code blocks and
// indented code are preserved verbatim; prose is reflowed, filtered, and
// rewrapped; comments/decorative/conversational content is dropped.
func processBlock(block []string, seen map[string]bool, stats *Stats) []string {
	var out []string
	var prose []string

	flushProse := func() {
		if len(prose) == 0 {
			return
		}
		first := prose[0]
		switch {
		case gateProseBlock(first):
			stats.LinesRemoved += len(prose)
		case isStructured(prose):
			for _, line := range prose {
				if t := transformStructuredLine(line, seen, stats); t != "" {
					out = append(out, t)
				}
			}
		default:
			text := processProseParagraph(prose)
			if text == "" {
				stats.LinesRemoved += len(prose)
				break
			}
			key := normalizeKey(text)
			if seen[key] {
				stats.LinesRemoved += len(prose)
				break
			}
			seen[key] = true
			out = append(out, wrapLines(text, wrapWidth)...)
		}
		prose = nil
	}

	inFence := false
	inComment := false
	for _, line := range block {
		trimmed := strings.TrimSpace(line)

		if inComment {
			stats.LinesRemoved++
			if strings.Contains(line, "-->") {
				inComment = false
			}
			continue
		}
		if inFence {
			out = append(out, line)
			if strings.HasPrefix(trimmed, "```") {
				inFence = false
			}
			continue
		}
		if strings.HasPrefix(trimmed, "```") {
			stats.CodeBlocksPreserved++
			flushProse()
			out = append(out, line)
			inFence = true
			continue
		}
		if strings.Contains(line, "<!--") {
			stats.LinesRemoved++
			if !strings.Contains(line, "-->") {
				inComment = true
			}
			continue
		}
		// indented code (4 spaces or tab) preserved verbatim
		if strings.HasPrefix(line, "    ") || strings.HasPrefix(line, "\t") {
			flushProse()
			out = append(out, line)
			continue
		}

		switch {
		case commentLineRe.MatchString(line):
			stats.LinesRemoved++
		case isDecorative(line):
			stats.LinesRemoved++
		case conversationalRe.MatchString(line) || metaTailRe.MatchString(line):
			stats.LinesRemoved++
		default:
			prose = append(prose, line)
		}
	}
	flushProse()
	return out
}

// gateProseBlock reports whether a whole prose block should be dropped because
// it opens with greeting/boilerplate/comment content.
func gateProseBlock(first string) bool {
	trimmed := strings.TrimSpace(first)
	if trimmed == "" {
		return false
	}
	return greetingLeadRe.MatchString(trimmed) ||
		boilerplateRe.MatchString(trimmed) ||
		conversationalRe.MatchString(trimmed) ||
		metaTailRe.MatchString(trimmed)
}

// isStructured reports whether a block is a list/heading/table paragraph that
// must be processed line-by-line (never reflowed).
func isStructured(lines []string) bool {
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		switch {
		case strings.HasPrefix(t, "#"),
			strings.HasPrefix(t, "- "),
			strings.HasPrefix(t, "* "),
			strings.HasPrefix(t, "+ "),
			strings.HasPrefix(t, "> "),
			strings.HasPrefix(t, "| "):
			return true
		}
		return orderedListRe.MatchString(t)
	}
	return false
}

var orderedListRe = regexp.MustCompile(`^\d+[.)]\s`)

// transformStructuredLine applies line-level transforms to a single
// list/heading/table line, returning "" when the line should be dropped.
func transformStructuredLine(line string, seen map[string]bool, stats *Stats) string {
	if strings.TrimSpace(line) == "" {
		return ""
	}
	t := transformProse(line)
	if strings.TrimSpace(t) == "" {
		stats.LinesRemoved++
		return ""
	}
	key := normalizeKey(t)
	if seen[key] {
		stats.LinesRemoved++
		return ""
	}
	seen[key] = true
	return t
}

// processProseParagraph reflows a wrapped prose paragraph and applies
// sentence-level transforms. It returns "" when every sentence is dropped.
func processProseParagraph(lines []string) string {
	text := strings.Join(strings.Fields(strings.Join(lines, " ")), " ")

	var spans []string
	protected := protectRe.ReplaceAllStringFunc(text, func(m string) string {
		spans = append(spans, m)
		return fmt.Sprintf("\x00%d\x00", len(spans)-1)
	})

	var kept []string
	for _, sent := range splitSentences(protected) {
		if s := transformSentence(sent); s != "" {
			kept = append(kept, s)
		}
	}
	protected = strings.Join(kept, " ")

	protected = phraseRe.ReplaceAllStringFunc(protected, func(m string) string {
		switch strings.ToLower(m) {
		case "in order to", "for the purpose of":
			return "to"
		case "due to the fact that":
			return "because"
		case "at this point in time":
			return "now"
		case "in the event that":
			return "if"
		case "a large number of":
			return "many"
		case "with regard to", "in regard to":
			return "about"
		default:
			return m
		}
	})
	protected = fillerRe.ReplaceAllString(protected, " ")
	protected = spacesRe.ReplaceAllString(protected, " ")

	out := strings.TrimSpace(protected)
	for i, span := range spans {
		out = strings.ReplaceAll(out, fmt.Sprintf("\x00%d\x00", i), span)
	}
	return strings.TrimSpace(out)
}

// transformSentence filters a single sentence: discourse leads are stripped,
// purely evaluative/dangling sentences are dropped, and trailing meta clauses
// are trimmed.
func transformSentence(sent string) string {
	s := discourseLeadRe.ReplaceAllString(sent, "")
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	switch {
	case simplicitySentenceRe.MatchString(s),
		danglingWhichRe.MatchString(s),
		fragmentTailRe.MatchString(s):
		return ""
	}
	s = tailMetaRe.ReplaceAllString(s, "")
	return strings.TrimSpace(s)
}

func splitSentences(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	if matches := sentenceRe.FindAllString(s, -1); len(matches) > 0 {
		return matches
	}
	return []string{s}
}

// transformProse strips filler words/phrases from a single prose line while
// leaving protected tokens (code spans, paths, flags) untouched.
func transformProse(line string) string {
	var spans []string
	protected := protectRe.ReplaceAllStringFunc(line, func(m string) string {
		spans = append(spans, m)
		return fmt.Sprintf("\x00%d\x00", len(spans)-1)
	})

	protected = redundantLeadRe.ReplaceAllString(protected, "${1}")
	protected = phraseRe.ReplaceAllStringFunc(protected, func(m string) string {
		switch strings.ToLower(m) {
		case "in order to", "for the purpose of":
			return "to"
		case "due to the fact that":
			return "because"
		case "at this point in time":
			return "now"
		case "in the event that":
			return "if"
		case "a large number of":
			return "many"
		case "with regard to", "in regard to":
			return "about"
		default:
			return m
		}
	})
	protected = fillerRe.ReplaceAllString(protected, " ")
	protected = spacesRe.ReplaceAllString(protected, " ")

	out := protected
	for i, span := range spans {
		out = strings.ReplaceAll(out, fmt.Sprintf("\x00%d\x00", i), span)
	}
	return strings.TrimSpace(out)
}

// isDecorative reports whether a line is a pure separator (e.g. `---`,
// `***`, `===`) with no textual content.
func isDecorative(line string) bool {
	trimmed := strings.TrimSpace(line)
	if len(trimmed) < 3 {
		return false
	}
	first := trimmed[0]
	if first == '-' || first == '*' || first == '_' || first == '=' || first == '~' {
		for i := 1; i < len(trimmed); i++ {
			if trimmed[i] != first {
				return false
			}
		}
		return true
	}
	return false
}

// wrapLines reflows text to the given width on word boundaries.
func wrapLines(text string, width int) []string {
	var out []string
	for _, word := range strings.Fields(text) {
		if len(out) == 0 {
			out = append(out, word)
			continue
		}
		if len(out[len(out)-1])+1+len(word) <= width {
			out[len(out)-1] += " " + word
		} else {
			out = append(out, word)
		}
	}
	return out
}

// normalizeKey collapses a line to a canonical comparison form for duplicate
// detection.
func normalizeKey(line string) string {
	lower := strings.ToLower(line)
	return strings.TrimSpace(keyStripRe.ReplaceAllString(lower, " "))
}
