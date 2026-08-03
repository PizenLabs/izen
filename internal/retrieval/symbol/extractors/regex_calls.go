package extractors

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/PizenLabs/izen/internal/retrieval/symbol"
)

// regexFallbackCallRe captures a call target as written: "foo(", "obj.method("
// or "pkg.func(".
var regexFallbackCallRe = regexp.MustCompile(`([A-Za-z_]\w*(?:\.[A-Za-z_]\w*)*)\s*\(`)

// regexDeclPrefixRe matches a line prefix that introduces a declaration rather
// than an invocation, so those matches are skipped.
var regexDeclPrefixRe = regexp.MustCompile(`(?i)^(?:export\s+)?(?:default\s+)?(?:async\s+)?(?:public\s+|private\s+|protected\s+|static\s+|final\s+|abstract\s+)*?(?:func|function|fn|def|class|interface|type|enum|struct|trait|impl|declare|const|let|var|import|from|match|if|for|while|switch|case|catch|with|new)\b`)

// regexCallKeywordRe lists words that are never call targets.
var regexCallKeywordRe = regexp.MustCompile(`^(?:if|for|while|switch|case|catch|return|match|with|import|from|export|func|function|fn|def|class|interface|type|enum|struct|trait|impl|var|const|let|new|sizeof|typeof|assert|using|yield)$`)

// tsRouteRe matches Express/Fastify style registrations:
// app.get('/path', handler).
var tsRouteRe = regexp.MustCompile(`\b(?:app|router|server|r|api|express)\s*\.\s*(get|post|put|patch|delete|head|options|all|use)\s*\(\s*['"]([^'"]*)['"]\s*[,)]\s*([A-Za-z_]\w*(?:\.[A-Za-z_]\w*)*)?`)

// pyRouteRe matches Flask decorators: @app.route('/path').
var pyRouteRe = regexp.MustCompile(`^\s*@[a-zA-Z_]\w*\.\s*(route|get|post|put|patch|delete)\s*\(\s*['"]([^'"]*)['"]`)

// rustRouteRe matches axum registrations:
// Router::new().route("/path", get(handler)).
var rustRouteRe = regexp.MustCompile(`\.route\s*\(\s*"([^"]*)"\s*,\s*(?:get|post|put|delete|patch|any)\s*\(\s*([A-Za-z_]\w*(?:::[A-Za-z_]\w*)*)\s*\)\s*\)`)

// pyDefLineRe locates a Python function definition that follows a decorator.
var pyDefLineRe = regexp.MustCompile(`^\s*(?:async\s+)?def\s+([a-zA-Z_]\w*)`)

// extractRegexCalls is the regex-fallback call-site detector used by the
// non-Go extractors. It is intentionally approximate: it records the callee
// target as written so the graph layer can resolve it against in-repo
// definitions.
func extractRegexCalls(content []byte) []symbol.CallSite {
	lines := strings.Split(string(content), "\n")
	var calls []symbol.CallSite
	seen := make(map[string]bool)

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || regexDeclPrefixRe.MatchString(trimmed) {
			continue
		}
		idx := regexFallbackCallRe.FindAllStringSubmatchIndex(trimmed, -1)
		for _, m := range idx {
			name := trimmed[m[2]:m[3]]
			if isDeclContext(trimmed, m[0]) || regexCallKeywordRe.MatchString(name) {
				continue
			}
			key := fmt.Sprintf("%s:%d", name, i+1)
			if seen[key] {
				continue
			}
			seen[key] = true
			calls = append(calls, symbol.CallSite{Name: name, Line: i + 1})
		}
	}
	return calls
}

// scopeRegexCalls attributes each call site to the function that contains it,
// using the already-extracted function symbols sorted by start line. Calls
// without a containing function keep an empty InFunc.
func scopeRegexCalls(functions []symbol.SymbolNode, calls []symbol.CallSite) []symbol.CallSite {
	if len(functions) == 0 || len(calls) == 0 {
		return calls
	}
	sorted := make([]symbol.SymbolNode, len(functions))
	copy(sorted, functions)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].StartLine < sorted[j].StartLine
	})

	for ci := range calls {
		line := calls[ci].Line
		best := ""
		for _, fn := range sorted {
			if fn.StartLine <= line {
				best = fn.Name
			} else {
				break
			}
		}
		calls[ci].InFunc = best
	}
	return calls
}

// isDeclContext reports whether the call-like match at byte offset start is the
// callee of a declaration form such as "def foo(" or "const bar = (".
func isDeclContext(line string, start int) bool {
	prefix := strings.TrimSpace(line[:start])
	if prefix == "" {
		return false
	}
	last := prefix[strings.LastIndexAny(prefix, " \t(")+1:]
	if last == "" {
		return false
	}
	switch strings.ToLower(last) {
	case "func", "function", "fn", "def", "class", "interface", "type",
		"enum", "struct", "trait", "impl", "const", "let", "var", "new",
		"return", "match", "case":
		return true
	}
	return false
}

// extractTSRoutes is the regex-fallback HTTP route detector for JS/TS.
func extractTSRoutes(lines []string) []symbol.HTTPRoute {
	var routes []symbol.HTTPRoute
	for i, line := range lines {
		m := tsRouteRe.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		method := strings.ToUpper(m[1])
		if method == "ALL" || method == "USE" {
			method = "ANY"
		}
		routes = append(routes, symbol.HTTPRoute{
			Path:    m[2],
			Method:  method,
			Handler: m[3],
			Line:    i + 1,
		})
	}
	return routes
}

// extractPythonRoutes is the regex-fallback HTTP route detector for Python
// Flask-style decorators.
func extractPythonRoutes(lines []string) []symbol.HTTPRoute {
	var routes []symbol.HTTPRoute
	for i, line := range lines {
		m := pyRouteRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		method := strings.ToUpper(m[1])
		if method == "ROUTE" {
			method = "ANY"
		}
		handler := ""
		for j := i + 1; j < len(lines) && j <= i+3; j++ {
			if hm := pyDefLineRe.FindStringSubmatch(lines[j]); hm != nil {
				handler = hm[1]
				break
			}
		}
		routes = append(routes, symbol.HTTPRoute{
			Path:    m[2],
			Method:  method,
			Handler: handler,
			Line:    i + 1,
		})
	}
	return routes
}

// extractRustRoutes is the regex-fallback HTTP route detector for axum-style
// registrations.
func extractRustRoutes(lines []string) []symbol.HTTPRoute {
	var routes []symbol.HTTPRoute
	for i, line := range lines {
		m := rustRouteRe.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		routes = append(routes, symbol.HTTPRoute{
			Path:    m[1],
			Method:  "ANY",
			Handler: m[2],
			Line:    i + 1,
		})
	}
	return routes
}
