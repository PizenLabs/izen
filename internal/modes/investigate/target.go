package investigate

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type Target struct {
	File    string `json:"file"`
	Line    int    `json:"line"`
	Node    string `json:"node"`
	Kind    string `json:"kind"`
	Snippet string `json:"snippet"`
}

type ContextLedger struct {
	Source     string     `json:"source"`
	Problem    string     `json:"problem"`
	Targets    []Target   `json:"targets"`
	Evidence   []Evidence `json:"evidence,omitempty"`
	Conclusion string     `json:"conclusion,omitempty"`
	Resolved   bool       `json:"resolved"`
}

func NewContextLedger() *ContextLedger {
	return &ContextLedger{
		Source:  "investigate",
		Targets: []Target{},
	}
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

func (cl *ContextLedger) SetConclusion(conclusion string, resolved bool) {
	cl.Conclusion = conclusion
	cl.Resolved = resolved
}

func (cl *ContextLedger) FormatForPlan() string {
	var b strings.Builder
	fmt.Fprintf(&b, "### CONTEXT LEDGER (from /investigate)\n")
	fmt.Fprintf(&b, "Source: %s\n", cl.Source)
	fmt.Fprintf(&b, "Problem: %s\n\n", cl.Problem)

	if len(cl.Targets) > 0 {
		fmt.Fprintf(&b, "### ISOLATED TARGETS\n")
		for _, t := range cl.Targets {
			fmt.Fprintf(&b, "  File: %s\n", t.File)
			if t.Line > 0 {
				fmt.Fprintf(&b, "  Line: %d\n", t.Line)
			}
			if t.Node != "" {
				fmt.Fprintf(&b, "  Node: %s (%s)\n", t.Node, t.Kind)
			}
			if t.Snippet != "" {
				fmt.Fprintf(&b, "  Snippet:\n%s\n", t.Snippet)
			}
			fmt.Fprintf(&b, "\n")
		}
	}

	if cl.Conclusion != "" {
		fmt.Fprintf(&b, "### CONCLUSION\n%s\n", cl.Conclusion)
	}

	fmt.Fprintf(&b, "### ACTION REQUIRED\n")
	fmt.Fprintf(&b, "Hand off to /plan for remediation. Do NOT fix in /investigate.\n")

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
