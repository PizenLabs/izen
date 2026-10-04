package capability

import (
	"fmt"
	"sort"
	"strings"
)

// subresourceAttr pairs an attribute that carries a resource reference with the
// tag it belongs to. The list is a STRUCTURAL vocabulary of document
// references — it maps a tag to the ATTRIBUTE THAT HOLDS A REFERENCE, which is a
// property of the document format, not a guess about which file should exist.
//
// It is deliberately not a file-type-to-filename map. Nothing here decides that
// an HTML document needs "styles.css"; that claim can only come from reading
// the document and probing the runtime, which is what Inspect does.
var subresourceAttr = map[string][]string{
	"link":   {"href"},
	"script": {"src"},
	"img":    {"src"},
	"source": {"src"},
	"audio":  {"src"},
	"video":  {"src"},
	"iframe": {"src"},
	"embed":  {"src"},
	"track":  {"src"},
	"input":  {"src"},
	"use":    {"href"},
	"object": {"data"},
}

// Subresource is one reference extracted from a served document.
type Subresource struct {
	// Tag is the element that carried the reference.
	Tag string `json:"tag"`
	// Attr is the attribute the reference was read from.
	Attr string `json:"attr"`
	// Ref is the reference value exactly as the document stated it.
	Ref string `json:"ref"`
}

// ExtractSubresourceRefs returns every local-or-external resource reference a
// document states, in deterministic order and de-duplicated by reference.
//
// Extraction is content-driven and attribute-driven only. It reports what the
// document ASKS FOR; whether that ask is satisfied is a separate, observed
// question answered by probing.
func ExtractSubresourceRefs(body string) []Subresource {
	lower := strings.ToLower(body)
	seen := map[string]bool{}
	out := make([]Subresource, 0, 8)
	cursor := 0
	for cursor < len(lower) {
		lt := strings.Index(lower[cursor:], "<")
		if lt < 0 {
			break
		}
		start := cursor + lt
		gt := strings.IndexByte(lower[start:], '>')
		if gt < 0 {
			break
		}
		tag := lower[start+1 : start+gt]
		cursor = start + gt + 1
		if strings.HasPrefix(tag, "!") || strings.HasPrefix(tag, "?") {
			continue
		}
		tagName := tag
		if i := strings.IndexAny(tagName, " \t\r\n/"); i >= 0 {
			tagName = tagName[:i]
		}
		attrs, ok := subresourceAttr[tagName]
		if !ok {
			continue
		}
		for _, attr := range attrs {
			value, found := attrValue(tag, attr)
			if !found {
				continue
			}
			value = strings.TrimSpace(value)
			if value == "" {
				continue
			}
			// De-duplicate by REFERENCE VALUE, not by (tag, attribute): the
			// runtime resolves references to URLs, so a stylesheet linked from
			// <link> and re-declared on <img> is ONE thing to fetch and probe
			// twice would only inflate the evidence with a duplicate observation.
			if seen[value] {
				continue
			}
			seen[value] = true
			out = append(out, Subresource{Tag: tagName, Attr: attr, Ref: value})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Ref < out[j].Ref })
	return out
}

// extractSubresourceRefs is the package-internal projection used by Inspect.
func extractSubresourceRefs(body string) []string {
	subs := ExtractSubresourceRefs(body)
	out := make([]string, 0, len(subs))
	for _, s := range subs {
		out = append(out, s.Ref)
	}
	return out
}

// countSubresourceRefs counts how many distinct references a body states. It is
// the structural signal used to rank entry-document candidates: an entry
// document is the one a runtime serves AND that pulls in more.
func countSubresourceRefs(body string) int {
	return len(extractSubresourceRefs(body))
}

// isAttrBoundary reports whether prev may precede an attribute name.
func isAttrBoundary(prev byte) bool {
	switch prev {
	case ' ', '\t', '\n', '\r', '/':
		return true
	default:
		return false
	}
}

// attrValue reads one attribute value out of a single lowercased tag source.
//
// The scan is anchored on a name BOUNDARY: matching `href=` inside `xhref=`
// would read an attribute the document never stated. Only whitespace, `/` or
// the start of the tag may precede the name.
func attrValue(tag, attr string) (string, bool) {
	for i := 0; i < len(tag); {
		idx := strings.Index(tag[i:], attr)
		if idx < 0 {
			return "", false
		}
		at := i + idx
		end := at + len(attr)
		if end >= len(tag) || tag[end] != '=' {
			i = at + len(attr)
			continue
		}
		if at > 0 {
			prev := tag[at-1]
			if !isAttrBoundary(prev) {
				i = at + len(attr)
				continue
			}
		}
		rest := tag[end+1:]
		if rest == "" {
			return "", false
		}
		if quote := rest[0]; quote == '"' || quote == '\'' {
			closing := strings.IndexByte(rest[1:], quote)
			if closing < 0 {
				return "", false
			}
			return rest[1 : 1+closing], true
		}
		if end2 := strings.IndexAny(rest, " \t\r\n"); end2 >= 0 {
			return rest[:end2], true
		}
		return rest, true
	}
	return "", false
}

// voidElements are the elements that never take a closing tag. They are part of
// the document format, so knowing them is structure knowledge rather than a
// project assumption.
var voidElements = map[string]bool{
	"area": true, "base": true, "br": true, "col": true, "embed": true,
	"hr": true, "img": true, "input": true, "link": true, "meta": true,
	"param": true, "source": true, "track": true, "wbr": true,
}

// optionalEndTags are elements whose closing tag the format permits to be
// omitted. Treating their absence as a structural fault would report a valid
// document as broken, so they are exempted.
var optionalEndTags = map[string]bool{
	"li": true, "dt": true, "dd": true, "p": true, "rt": true, "rp": true,
	"optgroup": true, "option": true, "colgroup": true, "tbody": true,
	"thead": true, "tfoot": true, "tr": true, "td": true, "th": true,
}

// structureFault is the first structural defect found in a document, with the
// line it was found on.
type structureFault struct {
	line   int
	detail string
}

// auditDocumentStructure parses a served document and returns its first
// structural defect, or nil when the structure is well-formed.
//
// The check is a real open/close reconciliation over the served bytes. It is
// what lets the runtime say "the page is broken" about the thing a user would
// actually see, instead of guessing from a file extension.
func auditDocumentStructure(body string) *structureFault {
	type frame struct {
		tag  string
		line int
	}
	var stack []frame
	line := 1
	i := 0
	for i < len(body) {
		c := body[i]
		if c == '\n' {
			line++
			i++
			continue
		}
		if c != '<' {
			i++
			continue
		}
		// Comment, doctype, or CDATA: skip to the terminator.
		if strings.HasPrefix(body[i:], "<!--") {
			end := strings.Index(body[i:], "-->")
			if end < 0 {
				return &structureFault{line: line, detail: "unterminated comment"}
			}
			line += strings.Count(body[i:i+end+3], "\n")
			i += end + 3
			continue
		}
		if strings.HasPrefix(strings.ToLower(body[i:]), "<!") || strings.HasPrefix(body[i:], "<?") {
			gt := strings.IndexByte(body[i:], '>')
			if gt < 0 {
				return &structureFault{line: line, detail: "unterminated declaration"}
			}
			i += gt + 1
			continue
		}
		gt := strings.IndexByte(body[i:], '>')
		if gt < 0 {
			return &structureFault{line: line, detail: "unterminated tag"}
		}
		raw := body[i+1 : i+gt]
		startLine := line
		line += strings.Count(body[i:i+gt+1], "\n")
		i += gt + 1

		closing := strings.HasPrefix(raw, "/")
		raw = strings.TrimPrefix(raw, "/")
		selfClosing := strings.HasSuffix(raw, "/")
		raw = strings.TrimSuffix(raw, "/")
		name := raw
		if j := strings.IndexAny(name, " \t\r\n"); j >= 0 {
			name = name[:j]
		}
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" || !isElementName(name) {
			continue
		}
		if voidElements[name] || selfClosing {
			continue
		}
		if closing {
			if len(stack) == 0 {
				return &structureFault{line: startLine, detail: fmt.Sprintf("closing tag </%s> has no matching open tag", name)}
			}
			top := stack[len(stack)-1]
			if top.tag != name {
				// The format PERMITS omitting the end tag of some elements, so a
				// closing tag implicitly closes any still-open element whose end
				// tag is optional. `<ul><li>a<li>b</ul>` is valid; reporting it as
				// broken would make the audit report valid documents as faults.
				for len(stack) > 0 && stack[len(stack)-1].tag != name {
					top = stack[len(stack)-1]
					if !optionalEndTags[top.tag] {
						// A Mismatch on an element whose end tag is REQUIRED is a
						// real defect. Report what was open, because
						// "unclosed <main>" is the actionable fact.
						return &structureFault{
							line: startLine,
							detail: fmt.Sprintf("closing tag </%s> does not match the open <%s> from line %d",
								name, top.tag, top.line),
						}
					}
					stack = stack[:len(stack)-1]
				}
				if len(stack) == 0 {
					return &structureFault{
						line:   startLine,
						detail: fmt.Sprintf("closing tag </%s> has no matching open tag", name),
					}
				}
			}
			stack = stack[:len(stack)-1]
			continue
		}
		// Opening an element whose end tag is optional implicitly closes any
		// still-open element whose end tag is also optional, which is exactly how
		// a sequence of sibling <li> elements is written.
		for len(stack) > 0 && optionalEndTags[stack[len(stack)-1].tag] && stack[len(stack)-1].tag != name {
			stack = stack[:len(stack)-1]
		}
		stack = append(stack, frame{tag: name, line: startLine})
	}
	if len(stack) > 0 {
		top := stack[len(stack)-1]
		return &structureFault{
			line:   top.line,
			detail: fmt.Sprintf("<%s> opened at line %d is never closed", top.tag, top.line),
		}
	}
	return nil
}

// auditServedDocument runs every structural check that applies to a served
// document and returns the first defect, or nil when the served bytes are
// well-formed.
//
// It inspects the DELIVERED bytes. That is deliberate: the objective is about
// the runtime's behaviour, so the document is judged as the runtime serves it.
func auditServedDocument(entryPath, body string) *Defect {
	if !isMarkupDocument(entryPath, body) {
		return nil
	}
	if fault := auditDocumentStructure(body); fault != nil {
		return &Defect{
			Code:  CodeDocumentStructureInvalid,
			Class: FailureExecutionFailed,
			Summary: fmt.Sprintf("the served document has invalid structure at line %d: %s",
				fault.line, fault.detail),
			Evidence: []string{},
			Detail:   fmt.Sprintf("%s line %d: %s", entryPath, fault.line, fault.detail),
		}
	}
	return nil
}

// isMarkupDocument reports whether a served document is subject to the
// structural audit. It decides by CONTENT (does the body declare markup
// elements?), never by file extension, so a document served from an unexpected
// path is still audited.
func isMarkupDocument(entryPath, body string) bool {
	lower := strings.ToLower(body)
	if !strings.Contains(lower, "<") {
		return false
	}
	return strings.Contains(lower, "<html") ||
		strings.Contains(lower, "<!doctype html") ||
		strings.Contains(lower, "<body") ||
		strings.Contains(lower, "<head")
}

// isElementName reports whether a token can be an element name. It excludes
// anything containing characters that never appear in an element name, which
// keeps comparisons in the text body from being mistaken for tags.
func isElementName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == ':':
		default:
			return false
		}
	}
	first := rune(name[0])
	return (first >= 'a' && first <= 'z') || (first >= 'A' && first <= 'Z')
}
