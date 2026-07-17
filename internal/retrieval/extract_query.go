package retrieval

import (
	"regexp"
	"strings"

	"github.com/PizenLabs/izen/internal/lynx"
)

// extractionTokenRe matches structural search terms: filesystem paths
// (e.g. go-template/cmd/api), dotted/namespaced identifiers, Go/JS-style
// symbols (CamelCase, Initialisms), and plain identifiers of length >= 3.
// Raw, dynamic log fragments (timestamps, container ids, stack offsets) are
// intentionally excluded — a codebase search engine can only match stable
// structural tokens.
var extractionTokenRe = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_-]*(?:[./][A-Za-z_][A-Za-z0-9_-]*)+|[A-Za-z_][A-Za-z0-9_.]*|[A-Za-z_][A-Za-z0-9_]{2,}`)

// errorClassRe matches deterministic, human-authored error constants worth
// surfacing to a code search (e.g. ErrNotFound, panic:, nil pointer).
var errorClassRe = regexp.MustCompile(`\b(?:[A-Z][A-Za-z0-9]*(?:Error|Exception|NotFound|Failed|Timeout|Panic)|panic:|nil\s+pointer|segmentation\s+fault|invalid\s+memory\s+address)\b`)

// ExtractSearchTerms isolates ONLY structured, searchable keywords from an
// arbitrary input string. It is the single architectural boundary that keeps
// dynamic runtime log lines (full container output, stack offsets, ANSI noise)
// from being fired verbatim at the code search engine.
//
// If the input carries no clean structural keyword (pure prose or raw log
// sludge), it returns nil — signalling the caller to SKIP the search entirely
// rather than emitting a blind [FAIL].
func ExtractSearchTerms(input string) []string {
	cleaned := strings.ReplaceAll(input, "\x1b[", "")
	cleaned = strings.ToValidUTF8(cleaned, "")
	cleaned = strings.ReplaceAll(cleaned, "\r", " ")
	cleaned = strings.ReplaceAll(cleaned, "\n", " ")

	seen := make(map[string]struct{})
	var terms []string

	add := func(t string) {
		t = strings.TrimSpace(t)
		if t == "" || len(t) < 3 {
			return
		}
		if _, ok := seen[strings.ToLower(t)]; ok {
			return
		}
		seen[strings.ToLower(t)] = struct{}{}
		terms = append(terms, t)
	}

	// Deterministic error constants take priority: they are the most useful
	// search anchors and must not be crowded out by generic prose tokens.
	for _, m := range errorClassRe.FindAllString(cleaned, -1) {
		add(m)
	}
	for _, m := range extractionTokenRe.FindAllString(cleaned, -1) {
		add(m)
	}

	// Cap the term fan-out so a single handoff can never trigger an
	// unbounded bombardment of search calls.
	if len(terms) > 8 {
		terms = terms[:8]
	}
	return terms
}

// SearchWithExtraction performs a structural code search over the extracted
// terms of input. When no clean terms are found it returns nil,nil by design
// (safe-skip) instead of firing a raw query that would [FAIL].
func SearchWithExtraction(lc *lynx.Controller, input string) ([]lynx.SearchResult, error) {
	if lc == nil {
		return nil, nil
	}
	terms := ExtractSearchTerms(input)
	if len(terms) == 0 {
		return nil, nil
	}
	var merged []lynx.SearchResult
	var firstErr error
	for _, term := range terms {
		results, err := lc.SearchRaw(term)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		merged = append(merged, results...)
	}
	return merged, firstErr
}
