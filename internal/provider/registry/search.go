package registry

import (
	"strconv"
	"strings"
)

// searchTokens splits a raw query into deterministic lowercase tokens.
// Tokenization is whitespace-first, then splits each chunk on common model-ID
// separators (/, -, _, :, ., +, ,) so "deepseek/r1" matches
// "deepseek/deepseek-r1" via AND-across-tokens. Empty query yields nil.
func searchTokens(query string) []string {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil
	}
	var out []string
	for _, chunk := range strings.Fields(q) {
		for _, tok := range strings.FieldsFunc(chunk, isQuerySep) {
			tok = strings.TrimSpace(tok)
			// Drop a lone "$" artifact from price queries like "$0.55".
			tok = strings.TrimPrefix(tok, "$")
			if tok != "" {
				out = append(out, tok)
			}
		}
	}
	return out
}

// isQuerySep reports whether r separates query tokens (model-ID punctuation).
func isQuerySep(r rune) bool {
	switch r {
	case '/', '-', '_', ':', '.', '+', ',', '|':
		return true
	}
	return false
}

// matchToken reports whether a single lowercase token matches any field of m:
// ID, Name, Provider (substring or subsequence-fuzzy fallback), context
// window (128000 / 128k / 128), price (0.55 / $0.55 / free), or capabilities
// (thinking / tools / vision).
func matchToken(m ModelDescriptor, tok string) bool {
	if tok == "" {
		return true
	}
	id := strings.ToLower(m.ID)
	name := strings.ToLower(m.Name)
	prov := strings.ToLower(m.Provider)
	if strings.Contains(id, tok) || strings.Contains(name, tok) || strings.Contains(prov, tok) {
		return true
	}
	// Synthetic pricing tag: "free" matches zero-priced models so queries
	// like "groq free" or "free" surface $0.00 models even when "free"
	// is absent from the ID/name. Also match the formatted price pair
	// (e.g. "free" or "$0.04/$0.15") as synthetic search buffer.
	if tok == "free" {
		if m.InputCostPerM == 0 && m.OutputCostPerM == 0 {
			return true
		}
		priceTag := strings.ToLower(formatPricePair(m.InputCostPerM, m.OutputCostPerM))
		return strings.Contains(priceTag, tok)
	}
	// Also inject synthetic priceTag for numeric price fragments so
	// queries like "0.04" match via the "$0.04/$0.15" buffer even when
	// individual cost formatting differs.
	if isNumericToken(tok) {
		priceTag := strings.ToLower(formatPricePair(m.InputCostPerM, m.OutputCostPerM))
		if strings.Contains(priceTag, tok) {
			return true
		}
	}
	// Capability tokens.
	switch tok {
	case "thinking", "think", "reasoning", "reasoner", "r1":
		if m.IsThinking {
			return true
		}
		for _, c := range m.Capabilities {
			if c == CapThinking {
				return true
			}
		}
		// Fall through to fuzzy ID check below (e.g. "r1" in deepseek-r1
		// already matched via substring; bare "thinking" in the ID matches
		// here too).
		if strings.Contains(id, "r1") || strings.Contains(id, "thinking") || strings.Contains(id, "reasoner") {
			return true
		}
		return false
	case "vision":
		for _, c := range m.Capabilities {
			if c == CapVision {
				return true
			}
		}
		return false
	case "tools", "tool":
		for _, c := range m.Capabilities {
			if c == CapTools {
				return true
			}
		}
		// Modern LLMs default to tools; empty capabilities still match
		// "tools" via the classifier fallback used by the picker.
		return true
	}
	// Context/price tokens only when the token looks numeric (digits, $, ., k).
	// Pure-alpha tokens skip number formatting for the <5ms/2000-model budget.
	if isNumericToken(tok) {
		// Context-window tokens: "128k", "128", "128000".
		if matchContextToken(m.ContextWindow, tok) {
			return true
		}
		// Price tokens: match formatted $/M input/output costs.
		if matchPriceToken(m.InputCostPerM, tok) || matchPriceToken(m.OutputCostPerM, tok) {
			return true
		}
	}
	// Deterministic fuzzy fallback: every char of tok appears in order in
	// the ID or Name (preserves the Phase 1 "cld" -> "claude" contract).
	if isSubsequence(tok, id) || isSubsequence(tok, name) {
		return true
	}
	return false
}

// matchContextToken matches context-window tokens: raw ("128000"),
// kilo ("128k"), or bare kilo digits ("128" matches 128000).
func matchContextToken(ctx int, tok string) bool {
	if ctx <= 0 {
		return false
	}
	t := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(tok)), "$")
	if t == "" {
		return false
	}
	raw := strings.ToLower(formatContextRaw(ctx))
	if strings.Contains(raw, t) {
		return true
	}
	// "128k" form.
	if strings.HasSuffix(t, "k") {
		num := strings.TrimSuffix(t, "k")
		if num == strings.ToLower(formatContextK(ctx)) {
			return true
		}
		// Also allow substring on the kilo digits.
		if strings.Contains(strings.ToLower(formatContextK(ctx)), num) {
			return true
		}
		return false
	}
	// Bare digits: "128" matches 128000 via kilo prefix.
	if isDigits(t) && strings.HasPrefix(strings.ToLower(formatContextK(ctx)), t) {
		return true
	}
	return false
}

// matchPriceToken matches a price token against one $/M cost. Zero costs
// (unknown pricing) never match a nonzero token; token "0" matches zero.
func matchPriceToken(cost float64, tok string) bool {
	t := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(tok)), "$")
	if t == "" {
		return false
	}
	formatted := strings.ToLower(formatPrice(cost))
	return strings.Contains(formatted, t)
}

// formatContextRaw renders a context window as raw digits ("128000").
func formatContextRaw(ctx int) string {
	return strconv.Itoa(ctx)
}

// formatContextK renders a context window kilo digits ("128" for 128000).
// Sub-kilo windows render raw.
func formatContextK(ctx int) string {
	if ctx >= 1000 {
		if ctx%1000 == 0 {
			return strconv.Itoa(ctx / 1000)
		}
		return strings.TrimRight(strings.TrimRight(strconv.FormatFloat(float64(ctx)/1000.0, 'f', 1, 64), "0"), ".")
	}
	return strconv.Itoa(ctx)
}

// formatPrice renders a $/M cost compactly ("0.55", "2.19", "0").
func formatPrice(cost float64) string {
	return strconv.FormatFloat(cost, 'f', -1, 64)
}

// formatPriceVal formats a single $/M value for the synthetic pair.
func formatPriceVal(v float64) string {
	if v == 0 {
		return "0"
	}
	if v < 0.01 {
		return strconv.FormatFloat(v, 'f', 3, 64)
	}
	return strconv.FormatFloat(v, 'f', 2, 64)
}

// formatPricePair renders "$in/$out" or "free" for synthetic indexing.
func formatPricePair(in, out float64) string {
	if in == 0 && out == 0 {
		return "free"
	}
	return "$" + formatPriceVal(in) + "/$" + formatPriceVal(out)
}

// isNumericToken reports whether tok could match a number field (context,
// price). Requires a digit, $, ., or k-suffix so pure-alpha ID fragments skip
// number formatting on the hot filter path.
func isNumericToken(tok string) bool {
	for _, r := range tok {
		if (r >= '0' && r <= '9') || r == '$' || r == '.' || r == 'k' {
			return true
		}
	}
	return false
}

// isDigits reports whether s is all ASCII digits.
func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// Filter returns descriptors matching query against ID and Name plus an
// optional exact provider filter. It runs strictly against the in-memory
// slice: zero disk I/O. Matching is case-insensitive substring with a
// subsequence-fuzzy fallback (all query chars appear in order).
//
// An empty query matches everything (subject to providerFilter); an empty
// providerFilter matches all providers. The result is a fresh slice; the
// registry is never mutated and callers may freely modify the output.
func (r *Registry) Filter(query, providerFilter string) []ModelDescriptor {
	// Lock-free read via the immutable atomic snapshot: zero disk I/O,
	// zero mutex contention with background Sync/UpdateProvider writers.
	return filterSlice(r.Load().Models, query, providerFilter)
}

// MatchesQuery reports whether m satisfies every token of query via the
// deterministic multi-field matcher (ID, Name, Provider, context, price,
// capabilities). Empty query matches everything. Pure, zero I/O; shared with
// the TUI picker so RAM filtering mirrors Registry.Filter exactly.
func MatchesQuery(m ModelDescriptor, query string) bool {
	tokens := searchTokens(query)
	if len(tokens) == 0 {
		return true
	}
	for _, tok := range tokens {
		if !matchToken(m, tok) {
			return false
		}
	}
	return true
}

// filterSlice is the lock-free core so benchmarks can isolate matching cost.
// It performs deterministic multi-field token matching: the query is split
// into tokens (searchTokens) and EVERY token must match at least one field
// (ID, Name, Provider, context window, price, capabilities). Zero I/O.
func filterSlice(models []ModelDescriptor, query, providerFilter string) []ModelDescriptor {
	pf := strings.ToLower(strings.TrimSpace(providerFilter))
	tokens := searchTokens(query)
	out := make([]ModelDescriptor, 0, len(models))
	for _, m := range models {
		if pf != "" && strings.ToLower(m.Provider) != pf {
			continue
		}
		if len(tokens) == 0 {
			out = append(out, m)
			continue
		}
		matched := true
		for _, tok := range tokens {
			if !matchToken(m, tok) {
				matched = false
				break
			}
		}
		if matched {
			out = append(out, m)
		}
	}
	return out
}

// isSubsequence reports whether every rune of q appears in s in order.
func isSubsequence(q, s string) bool {
	if q == "" {
		return true
	}
	qi := 0
	qRunes := []rune(q)
	for _, sr := range s {
		if sr == qRunes[qi] {
			qi++
			if qi == len(qRunes) {
				return true
			}
		}
	}
	return false
}
