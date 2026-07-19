package investigate

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/PizenLabs/izen/internal/session"
)

// stripANSI removes ANSI escape sequences from a string.
// These are display artifacts that add no semantic value and bloat tokens.
func stripANSI(s string) string {
	if s == "" {
		return s
	}
	ansiRE := regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)
	clean := ansiRE.ReplaceAllString(s, "")
	clean = regexp.MustCompile(`\n{3,}`).ReplaceAllString(clean, "\n\n")
	return strings.TrimSpace(clean)
}

// collapseBlankLines reduces multiple consecutive blank lines to at most two.
// This preserves structural readability while eliminating token bloat from
// excessive whitespace that carries no semantic meaning.
func collapseBlankLines(s string) string {
	if s == "" {
		return s
	}
	return regexp.MustCompile(`\n{3,}`).ReplaceAllString(s, "\n\n")
}

type Target struct {
	File    string `json:"file"`
	Line    int    `json:"line"`
	Node    string `json:"node"`
	Kind    string `json:"kind"`
	Snippet string `json:"snippet"`
}

// ContextLedger bridges investigation findings to downstream modes (/plan).
// The ledger MUST NOT generate or mutate task definitions — it only records
// root cause targets, evidence, and a conclusion. Atomic task synthesis is
// exclusively owned by /plan/planner.go.
type ContextLedger struct {
	Source      string     `json:"source"`
	Problem     string     `json:"problem"`
	RootCause   string     `json:"root_cause,omitempty"`
	Targets     []Target   `json:"targets"`
	Evidence    []Evidence `json:"evidence,omitempty"`
	Conclusion  string     `json:"conclusion,omitempty"`
	Resolved    bool       `json:"resolved"`
	Diagnostics string     `json:"diagnostics,omitempty"`
}

func NewContextLedger() *ContextLedger {
	return &ContextLedger{
		Source:  "investigate",
		Targets: []Target{},
	}
}

// SetRootCause stores the isolated root cause description.
// This is the ONLY structural mutation /investigate performs —
// atomic task generation is strictly forbidden in this mode.
func (cl *ContextLedger) SetRootCause(cause string) {
	cl.RootCause = cause
}

var funcOrTypePattern = regexp.MustCompile(`(?:^|\n)\s*(func\s+\w+|type\s+\w+\s+(?:struct|interface))\s*[{(]?`)

type TargetIsolator struct {
	root string
}

func NewTargetIsolator(root string) *TargetIsolator {
	return &TargetIsolator{root: root}
}

func (ti *TargetIsolator) IsolateFromEvidence(evidence []Evidence, frames []StackFrame) []Target {
	var targets []Target
	seen := make(map[string]bool)

	for _, frame := range frames {
		if frame.File == "" {
			continue
		}
		key := fmt.Sprintf("%s:%d", frame.File, frame.Line)
		if seen[key] {
			continue
		}
		seen[key] = true

		node, kind := ti.locateNode(frame.File, frame.Line)
		snippet := ti.readSnippet(frame.File, frame.Line)
		targets = append(targets, Target{
			File:    frame.File,
			Line:    frame.Line,
			Node:    node,
			Kind:    kind,
			Snippet: snippet,
		})
	}

	for _, ev := range evidence {
		if ev.File == "" || ev.Line <= 0 {
			continue
		}
		key := fmt.Sprintf("%s:%d", ev.File, ev.Line)
		if seen[key] {
			continue
		}
		seen[key] = true

		node, kind := ti.locateNode(ev.File, ev.Line)
		snippet := ti.readSnippet(ev.File, ev.Line)
		targets = append(targets, Target{
			File:    ev.File,
			Line:    ev.Line,
			Node:    node,
			Kind:    kind,
			Snippet: snippet,
		})
	}

	return targets
}

func (ti *TargetIsolator) locateNode(file string, line int) (string, string) {
	fullPath := filepath.Join(ti.root, file)
	data, err := os.ReadFile(fullPath)
	if err != nil {
		rel, err2 := findFile(ti.root, file)
		if err2 != nil {
			return file, "file"
		}
		data, err = os.ReadFile(rel)
		if err != nil {
			return file, "file"
		}
		file = rel
	}

	content := string(data)
	matches := funcOrTypePattern.FindAllStringSubmatchIndex(content, -1)

	var bestNode, bestKind string
	bestDist := -1

	for _, m := range matches {
		if len(m) < 4 {
			continue
		}
		matchStart := m[2]
		matchEnd := m[3]
		matchText := content[matchStart:matchEnd]

		lineOfMatch := 1 + strings.Count(content[:matchStart], "\n")
		dist := abs(line - lineOfMatch)

		if bestDist == -1 || dist < bestDist {
			bestDist = dist
			parts := strings.Fields(matchText)
			if len(parts) >= 2 {
				if parts[0] == "func" {
					bestKind = "function"
					bestNode = parts[1]
				} else if parts[0] == "type" && len(parts) >= 3 {
					bestKind = parts[2]
					bestNode = parts[1]
				}
			}
		}
	}

	if bestNode == "" {
		return file, "file"
	}
	return bestNode, bestKind
}

func (ti *TargetIsolator) readSnippet(file string, line int) string {
	fullPath := filepath.Join(ti.root, file)
	data, err := os.ReadFile(fullPath)
	if err != nil {
		rel, err2 := findFile(ti.root, file)
		if err2 != nil {
			return ""
		}
		data, err = os.ReadFile(rel)
		if err != nil {
			return ""
		}
	}

	lines := strings.Split(string(data), "\n")
	if line < 1 || line > len(lines) {
		return ""
	}

	start := line - 3
	if start < 0 {
		start = 0
	}
	end := line + 2
	if end > len(lines) {
		end = len(lines)
	}

	return strings.Join(lines[start:end], "\n")
}

func (cl *ContextLedger) AddTarget(t Target) {
	cl.Targets = append(cl.Targets, t)
}

func (cl *ContextLedger) SetDiagnostics(raw string) {
	cl.Diagnostics = raw
}

func (cl *ContextLedger) SetConclusion(conclusion string, resolved bool) {
	cl.Conclusion = conclusion
	cl.Resolved = resolved
}

// compilerLogPathRe matches Go/Rust/TypeScript compiler diagnostics of the form
// "cmd/api/main.go:7:5" (file:line:col) and extracts the offending file
// coordinates. This powers TASK 1 of the build-freeze fix: even when a
// dependency/compilation error short-circuits the agent, we still resolve the
// exact file:line targets from the raw logs instead of exiting empty-handed.
var compilerLogPathRe = regexp.MustCompile(`([^\s:]+\.(?:go|rs|ts|tsx|js|jsx|py|java|cpp|c|cc|h)):(\d+):(\d+)`)

// ParseCompilerTargets extracts file:line:col coordinates directly from raw
// compiler/test output and returns them as investigate Targets. It is a pure,
// read-only operation — no file I/O, no mutations. Any file that cannot be
// localized falls back to the raw path with line 0 so the coordinate is still
// captured for downstream /plan consumption.
func ParseCompilerTargets(output string) []Target {
	var targets []Target
	seen := make(map[string]bool)
	for _, m := range compilerLogPathRe.FindAllStringSubmatch(output, -1) {
		file := m[1]
		// Normalize compiler-style "./path" prefixes to a project-root relative
		// path so downstream /plan task targeting matches the repo layout.
		file = strings.TrimPrefix(file, "./")
		file = strings.TrimPrefix(file, "/")
		line, _ := strconv.Atoi(m[2])
		col, _ := strconv.Atoi(m[3])
		key := fmt.Sprintf("%s:%d:%d", file, line, col)
		if seen[key] {
			continue
		}
		seen[key] = true
		node, kind := "", "file"
		// Best-effort AST node localization without mutating workspace state.
		if ti := NewTargetIsolator(""); file != "" {
			node, kind = ti.locateNode(file, line)
		}
		targets = append(targets, Target{
			File:    file,
			Line:    line,
			Node:    node,
			Kind:    kind,
			Snippet: "",
		})
	}
	return targets
}

func (cl *ContextLedger) FormatForPlan() string {
	var b strings.Builder
	fmt.Fprintf(&b, "### INVESTIGATION LEDGER (Root Cause Only — No Tasks)\n")
	fmt.Fprintf(&b, "Source: %s\n", cl.Source)
	fmt.Fprintf(&b, "Problem: %s\n", cl.Problem)

	if cl.Diagnostics != "" {
		cleanDiag := stripANSI(cl.Diagnostics)
		cleanDiag = collapseBlankLines(cleanDiag)
		fmt.Fprintf(&b, "\n### RAW DIAGNOSTICS\n")
		fmt.Fprintf(&b, "```\n%s\n```\n", cleanDiag)
	}

	if cl.RootCause != "" {
		fmt.Fprintf(&b, "\n### ROOT CAUSE\n%s\n", cl.RootCause)
	}

	if len(cl.Targets) > 0 {
		fmt.Fprintf(&b, "\n### AFFECTED SYMBOLS (Root Cause Evidence Only)\n")
		for _, t := range cl.Targets {
			fmt.Fprintf(&b, "  File: %s", t.File)
			if t.Line > 0 {
				fmt.Fprintf(&b, ":%d", t.Line)
			}
			if t.Node != "" {
				fmt.Fprintf(&b, " %s (%s)", t.Node, t.Kind)
			}
			fmt.Fprintf(&b, "\n")
		}
	}

	if cl.Conclusion != "" {
		fmt.Fprintf(&b, "\n### CONCLUSION\n%s\n", cl.Conclusion)
	}

	fmt.Fprintf(&b, "\n### BOUNDARY ENFORCEMENT\n")
	fmt.Fprintf(&b, "/investigate produced only root cause and evidence above.\n")
	fmt.Fprintf(&b, "Atomic task synthesis is delegated exclusively to /plan.\n")

	return b.String()
}

func (cl *ContextLedger) TargetPackages() []string {
	pkgSet := make(map[string]bool)
	for _, t := range cl.Targets {
		pkg := extractPackageFromFile(t.File)
		if pkg != "" {
			pkgSet[pkg] = true
		}
	}
	pkgs := make([]string, 0, len(pkgSet))
	for p := range pkgSet {
		pkgs = append(pkgs, p)
	}
	return pkgs
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// ToPackets projects the forensic findings of this investigate ledger into the
// canonical session.LedgerPacket type so they can be injected — sequentially and
// ID-addressed — into the session.ContextLedger handoff. Each finding becomes
// its own packet: the problem, every isolated target coordinate, high-signal
// evidence, and the final conclusion. The full payload is preserved verbatim;
// the caller (bridgeInvestigationToLedger) assigns the sequential PacketIDs.
func (cl *ContextLedger) ToPackets() []session.LedgerPacket {
	var pkts []session.LedgerPacket

	if cl.Problem != "" {
		pkts = append(pkts, session.LedgerPacket{
			Kind:    "problem",
			Title:   "Investigation problem statement",
			Payload: cl.Problem,
		})
	}

	for _, t := range cl.Targets {
		var b strings.Builder
		fmt.Fprintf(&b, "node=%s kind=%s", t.Node, t.Kind)
		if t.Snippet != "" {
			fmt.Fprintf(&b, "\nsnippet:\n%s", t.Snippet)
		}
		pkts = append(pkts, session.LedgerPacket{
			Kind:    "target",
			Title:   "Isolated code coordinate",
			Payload: b.String(),
			File:    t.File,
			Line:    t.Line,
		})
	}

	for _, ev := range cl.Evidence {
		if ev.Content == "" {
			continue
		}
		pkts = append(pkts, session.LedgerPacket{
			Kind:       "evidence",
			Title:      fmt.Sprintf("Evidence [%s]", ev.Source),
			Payload:    ev.Content,
			File:       ev.File,
			Line:       ev.Line,
			Confidence: ev.Confidence,
		})
	}

	if cl.RootCause != "" {
		pkts = append(pkts, session.LedgerPacket{
			Kind:    "root_cause",
			Title:   "Derived root cause",
			Payload: cl.RootCause,
		})
	}

	if cl.Conclusion != "" {
		pkts = append(pkts, session.LedgerPacket{
			Kind:    "conclusion",
			Title:   "Investigation conclusion",
			Payload: cl.Conclusion,
		})
	}

	return pkts
}
