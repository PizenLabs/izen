package hotfix

import (
	"fmt"
	"strings"

	"golang.org/x/net/html"
)

// RedundantKind classifies a deterministic redundant-content region.
type RedundantKind string

const (
	// RedundantOrphanText: meaningful text that is not nested inside any
	// semantic block container (e.g. stray text directly under <body>).
	RedundantOrphanText RedundantKind = "orphan_text"
	// RedundantDuplicate: a semantic block whose normalized content is
	// identical to an earlier block (the later copy is redundant).
	RedundantDuplicate RedundantKind = "duplicate_block"
	// RedundantEmptyBlock: a semantic block with no visible content (only
	// whitespace/comments).
	RedundantEmptyBlock RedundantKind = "empty_block"
)

// String returns the canonical redundancy kind label.
func (k RedundantKind) String() string { return string(k) }

// RedundantTarget is a deterministic redundant-content region in an HTML
// document. It is the "redundant content" equivalent of Target: a bounded
// region the runtime may remove, with the evidence that marks it redundant.
type RedundantTarget struct {
	StartLine int           // 1-based first line of the redundant region
	EndLine   int           // 1-based last line (inclusive)
	Block     string        // the exact on-disk text of lines [StartLine..EndLine]
	Kind      RedundantKind // what makes the region redundant
	Detail    string        // human/LLM-readable evidence
}

// Describe renders a human/LLM-readable removal instruction.
func (r RedundantTarget) Describe() string {
	switch r.Kind {
	case RedundantOrphanText:
		return fmt.Sprintf("lines %d-%d: orphan text not inside any container", r.StartLine, r.EndLine)
	case RedundantDuplicate:
		return fmt.Sprintf("lines %d-%d: %s", r.StartLine, r.EndLine, r.Detail)
	case RedundantEmptyBlock:
		return fmt.Sprintf("lines %d-%d: %s", r.StartLine, r.EndLine, r.Detail)
	default:
		return fmt.Sprintf("lines %d-%d", r.StartLine, r.EndLine)
	}
}

// maxRedundantTargets bounds the deterministic redundancy list so candidate
// inspection stays readable.
const maxRedundantTargets = 8

// ResolveRedundantTargets scans HTML content for deterministic redundant
// regions: orphan text nodes, duplicate semantic blocks, and empty blocks.
// The scan is structural — it never invokes a model. It returns at most
// maxRedundantTargets candidates ordered by scan position, and ok=false when
// the content is not recognizable HTML or no redundant region is found.
func ResolveRedundantTargets(content string) ([]RedundantTarget, bool) {
	if strings.TrimSpace(content) == "" {
		return nil, false
	}
	lines := strings.Split(content, "\n")
	z := html.NewTokenizer(strings.NewReader(content))

	// elementFrame tracks one open semantic block while the tokenizer advances.
	type elementFrame struct {
		tag       string
		startLine int
		innerByte int // byte offset just past the opening tag
	}

	var (
		out      []RedundantTarget
		blocks   []elementFrame // open semantic block frames
		allTags  []string       // full element-name stack (every open element)
		seen     = make(map[string]int)
		pos      int // current token byte offset
		orphans  []RedundantTarget
		empty    []RedundantTarget
		blockRes []RedundantTarget
	)

	record := func(t RedundantTarget) {
		if t.StartLine < 1 {
			return
		}
		switch t.Kind {
		case RedundantOrphanText:
			orphans = append(orphans, t)
		case RedundantEmptyBlock:
			empty = append(empty, t)
		default:
			blockRes = append(blockRes, t)
		}
	}

	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			break
		}
		raw := z.Raw()
		tokStart := pos
		pos += len(raw)
		startLine := lineAt(content, tokStart)
		endLine := lineAt(content, pos)

		switch tt {
		case html.StartTagToken:
			tok := z.Token()
			name := strings.ToLower(tok.Data)
			selfClosing := strings.HasSuffix(strings.TrimSpace(string(raw)), "/>")
			if selfClosing || voidElements[name] {
				break
			}
			allTags = append(allTags, name)
			if semanticBlockTags[name] {
				blocks = append(blocks, elementFrame{
					tag:       name,
					startLine: startLine,
					innerByte: pos,
				})
			}
		case html.EndTagToken:
			tok := z.Token()
			name := strings.ToLower(tok.Data)
			// Pop the element stack.
			for i := len(allTags) - 1; i >= 0; i-- {
				if allTags[i] == name {
					allTags = allTags[:i]
					break
				}
			}
			if len(blocks) == 0 {
				break
			}
			top := blocks[len(blocks)-1]
			if top.tag != name {
				// Implicit-close tolerance: pop the innermost matching frame.
				found := false
				for i := len(blocks) - 1; i >= 0; i-- {
					if blocks[i].tag == name {
						top = blocks[i]
						blocks = blocks[:i]
						found = true
						break
					}
				}
				if !found {
					break
				}
			} else {
				blocks = blocks[:len(blocks)-1]
			}
			// The block spans [top.innerByte, tokStart].
			if tokStart <= top.innerByte {
				break
			}
			block := content[top.innerByte:tokStart]
			inner := normalizeText(block)
			if inner == "" {
				if !hasVisibleContent(block) {
					record(RedundantTarget{
						StartLine: top.startLine, EndLine: endLine,
						Block:  strings.Join(lines[top.startLine-1:endLine], "\n"),
						Kind:   RedundantEmptyBlock,
						Detail: fmt.Sprintf("<%s> block contains no visible content", top.tag),
					})
				}
			} else {
				// Duplicate detection is keyed by tag so a child block (e.g.
				// <p>) can never make its parent (<section>) look duplicate.
				sig := top.tag + "\x00" + inner
				if prevLine, ok := seen[sig]; ok {
					record(RedundantTarget{
						StartLine: top.startLine, EndLine: endLine,
						Block:  strings.Join(lines[top.startLine-1:endLine], "\n"),
						Kind:   RedundantDuplicate,
						Detail: fmt.Sprintf("<%s> block duplicates earlier <%s> block (line %d)", top.tag, top.tag, prevLine),
					})
				} else {
					seen[sig] = top.startLine
				}
			}
		case html.TextToken:
			text := strings.TrimSpace(string(z.Text()))
			if text == "" {
				break
			}
			// Orphan text: meaningful text directly inside <body>, <html>, or
			// the document root — never nested inside a container block.
			parent := ""
			if len(allTags) > 0 {
				parent = allTags[len(allTags)-1]
			}
			if parent == "" || parent == "body" || parent == "html" {
				record(RedundantTarget{
					StartLine: startLine, EndLine: endLine,
					Block:  strings.Join(lines[startLine-1:endLine], "\n"),
					Kind:   RedundantOrphanText,
					Detail: fmt.Sprintf("orphan text node: %q", truncate(text, 48)),
				})
			}
		}
	}

	// Deterministic ordering: orphan text first (highest-signal), then empty
	// blocks, then duplicates, capped at maxRedundantTargets.
	out = append(out, orphans...)
	out = append(out, empty...)
	out = append(out, blockRes...)
	if len(out) > maxRedundantTargets {
		out = out[:maxRedundantTargets]
	}
	return out, len(out) > 0
}

// semanticBlockTags are the container elements whose inner content is
// meaningful as a block (mirrors autonomy's semantic tags).
var semanticBlockTags = map[string]bool{
	"header": true, "nav": true, "main": true, "section": true,
	"article": true, "aside": true, "footer": true, "div": true,
	"form": true, "table": true, "ul": true, "ol": true, "p": true,
	"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
	"figure": true, "figcaption": true, "blockquote": true, "span": true,
	"li": true, "td": true, "th": true,
}

// hasVisibleContent reports whether a block's raw inner content carries any
// visible text (comments and whitespace do not count).
func hasVisibleContent(block string) bool {
	z := html.NewTokenizer(strings.NewReader(block))
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			return false
		}
		if tt == html.TextToken {
			if strings.TrimSpace(string(z.Text())) != "" {
				return true
			}
		}
	}
}

// normalizeText collapses whitespace for duplicate signature comparison.
func normalizeText(s string) string {
	// Keep only visible text so a comment inside a block does not change the
	// signature (a comment-only difference is still a duplicate).
	var b strings.Builder
	z := html.NewTokenizer(strings.NewReader(s))
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			break
		}
		if tt == html.TextToken {
			b.WriteString(" ")
			b.Write(z.Text())
		}
	}
	return strings.Join(strings.Fields(strings.ToLower(b.String())), " ")
}

// lineAt computes the 1-based line number of a byte offset in content.
func lineAt(content string, offset int) int {
	if offset < 0 {
		offset = 0
	}
	if offset > len(content) {
		offset = len(content)
	}
	return strings.Count(content[:offset], "\n") + 1
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
