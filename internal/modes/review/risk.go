package review

import (
	"bufio"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type RiskAuditor struct {
	root  string
	rules []RiskRule
}

type RiskRule struct {
	ID          string
	Severity    RiskSeverity
	Category    string
	Description string
	Suggestion  string
	Check       func(path string, line int, content string) *RiskFinding
}

func NewRiskAuditor(root string) *RiskAuditor {
	ra := &RiskAuditor{root: root}
	ra.rules = ra.defaultRules()
	return ra
}

func (ra *RiskAuditor) Audit(files []DiffFile) []RiskFinding {
	var findings []RiskFinding

	for _, df := range files {
		if df.Status == "deleted" {
			continue
		}

		fullPath := filepath.Join(ra.root, df.Path)

		switch filepath.Ext(df.Path) {
		case ".go":
			findings = append(findings, ra.auditGoFile(fullPath, df)...)
		case ".py":
			findings = append(findings, ra.auditGenericFile(fullPath, df)...)
		case ".rs":
			findings = append(findings, ra.auditGenericFile(fullPath, df)...)
		default:
			findings = append(findings, ra.auditGenericFile(fullPath, df)...)
		}
	}

	return findings
}

func (ra *RiskAuditor) auditGoFile(path string, df DiffFile) []RiskFinding {
	var findings []RiskFinding

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		return findings
	}

	ast.Inspect(f, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.CallExpr:
			findings = append(findings, ra.checkGoCallExpr(fset, node, df.Path)...)
		case *ast.AssignStmt:
			findings = append(findings, ra.checkGoAssignStmt(fset, node, df.Path)...)
		case *ast.FuncDecl:
			findings = append(findings, ra.checkGoFuncDecl(fset, node, df.Path)...)
		case *ast.GoStmt:
			pos := fset.Position(node.Go)
			findings = append(findings, RiskFinding{
				File:        df.Path,
				Line:        pos.Line,
				Severity:    RiskMedium,
				Category:    "goroutine",
				Code:        "go ...",
				Description: "Goroutine launched without error handling. May cause silent failures.",
				Suggestion:  "Wrap goroutine body with error handling or use errgroup.Group.",
				RuleID:      "GO-GOROUTINE-001",
			})
		case *ast.DeferStmt:
			if call, ok := node.Call.Fun.(*ast.Ident); ok && (call.Name == "Close" || call.Name == "Unlock") {
				pos := fset.Position(node.Defer)
				findings = append(findings, RiskFinding{
					File:        df.Path,
					Line:        pos.Line,
					Severity:    RiskLow,
					Category:    "defer",
					Code:        fmt.Sprintf("defer %s(...)", call.Name),
					Description: fmt.Sprintf("defer %s() without error check", call.Name),
					Suggestion:  "Consider checking the error from deferred call.",
					RuleID:      "GO-DEFER-001",
				})
			}
		case *ast.ReturnStmt:
			findings = append(findings, ra.checkGoReturnStmt(path, fset, node, df.Path)...)
		}

		return true
	})

	content, err := os.ReadFile(path)
	if err != nil {
		return findings
	}

	lines := strings.Split(string(content), "\n")
	for _, hunk := range df.Hunks {
		for i := hunk.StartNew - 1; i < hunk.StartNew+hunk.CountNew-1 && i < len(lines); i++ {
			line := lines[i]
			for _, rule := range ra.rules {
				if finding := rule.Check(df.Path, i+1, line); finding != nil {
					findings = append(findings, *finding)
				}
			}
		}
	}

	return findings
}

func (ra *RiskAuditor) checkGoCallExpr(fset *token.FileSet, call *ast.CallExpr, path string) []RiskFinding {
	var findings []RiskFinding
	pos := fset.Position(call.Pos())

	switch fun := call.Fun.(type) {
	case *ast.SelectorExpr:
		switch fun.Sel.Name {
		case "Exec", "Query", "QueryRow":
			if _, ok := fun.X.(*ast.Ident); ok {
				if !ra.hasPreparedStatement(call) {
					findings = append(findings, RiskFinding{
						File:        path,
						Line:        pos.Line,
						Severity:    RiskHigh,
						Category:    "sql_injection",
						Code:        fset.Position(call.Pos()).String(),
						Description: "SQL query constructed without prepared statement",
						Suggestion:  "Use parameterized queries or prepared statements to prevent SQL injection.",
						RuleID:      "SEC-SQL-001",
					})
				}
			}
		case "Execute", "Run":
			findings = append(findings, RiskFinding{
				File:        path,
				Line:        pos.Line,
				Severity:    RiskLow,
				Category:    "side_effect",
				Code:        fun.Sel.Name + "(...)",
				Description: fmt.Sprintf("Side-effect call to %s() outside of execution engine", fun.Sel.Name),
				Suggestion:  "Consider wrapping in execution.Engine for sandbox safety checks.",
				RuleID:      "SEC-EXEC-001",
			})
		case "Marshal", "Unmarshal":
			findings = append(findings, RiskFinding{
				File:        path,
				Line:        pos.Line,
				Severity:    RiskLow,
				Category:    "serialization",
				Code:        fun.Sel.Name + "(...)",
				Description: "Serialization call without size limit",
				Suggestion:  "Consider adding input size validation before unmarshaling.",
				RuleID:      "SEC-SERIAL-001",
			})
		}
	case *ast.Ident:
		switch fun.Name {
		case "os_exec", "exec":
			findings = append(findings, RiskFinding{
				File:        path,
				Line:        pos.Line,
				Severity:    RiskHigh,
				Category:    "os_command",
				Code:        fun.Name + "(...)",
				Description: "Direct os/exec.Command call without sandbox",
				Suggestion:  "Use execution.Engine.Runner which has sandbox and dangerous-command detection.",
				RuleID:      "SEC-CMD-001",
			})
		case "panic":
			findings = append(findings, RiskFinding{
				File:        path,
				Line:        pos.Line,
				Severity:    RiskHigh,
				Category:    "panic",
				Code:        "panic(...)",
				Description: "panic() call in non-main package",
				Suggestion:  "Return errors instead of panicking. Panic should only be used in initialization.",
				RuleID:      "GO-PANIC-001",
			})
		case "log_Fatal":
			if fun.Name == "Fatal" || fun.Name == "Fatalf" {
				findings = append(findings, RiskFinding{
					File:        path,
					Line:        pos.Line,
					Severity:    RiskMedium,
					Category:    "fatal_log",
					Code:        fun.Name + "(...)",
					Description: "log.Fatal/Fatalf call exits the program",
					Suggestion:  "Return error to caller instead of exiting.",
					RuleID:      "GO-FATAL-001",
				})
			}
		case "exit", "os_Exit":
			if fun.Name == "Exit" || fun.Name == "OsExit" {
				findings = append(findings, RiskFinding{
					File:        path,
					Line:        pos.Line,
					Severity:    RiskHigh,
					Category:    "os_exit",
					Code:        fun.Name + "(...)",
					Description: "os.Exit call in library code",
					Suggestion:  "os.Exit should only be used in main(). Return errors otherwise.",
					RuleID:      "GO-EXIT-001",
				})
			}
		}
	}

	return findings
}

func (ra *RiskAuditor) checkGoAssignStmt(fset *token.FileSet, assign *ast.AssignStmt, path string) []RiskFinding {
	var findings []RiskFinding

	for _, rhs := range assign.Rhs {
		call, ok := rhs.(*ast.CallExpr)
		if !ok {
			continue
		}

		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			continue
		}

		switch sel.Sel.Name {
		case "ReadFile", "ReadAll", "ioutil_ReadAll":
			if len(call.Args) < 1 {
				continue
			}
			pos := fset.Position(call.Pos())
			findings = append(findings, RiskFinding{
				File:        path,
				Line:        pos.Line,
				Severity:    RiskMedium,
				Category:    "unsized_read",
				Code:        sel.Sel.Name + "(...)",
				Description: "File/reader read without size limit",
				Suggestion:  "Add size limit using io.LimitReader to prevent unbounded memory usage.",
				RuleID:      "SEC-READ-001",
			})
		}
	}

	return findings
}

func (ra *RiskAuditor) checkGoFuncDecl(fset *token.FileSet, fn *ast.FuncDecl, path string) []RiskFinding {
	var findings []RiskFinding
	pos := fset.Position(fn.Pos())

	if fn.Name.IsExported() && fn.Type.Results == nil {
		if fn.Recv == nil {
			findings = append(findings, RiskFinding{
				File:        path,
				Line:        pos.Line,
				Severity:    RiskInfo,
				Category:    "no_error_return",
				Code:        fmt.Sprintf("func %s(...)", fn.Name.Name),
				Description: fmt.Sprintf("Exported function %s has no return values", fn.Name.Name),
				Suggestion:  "Exported functions should typically return errors to allow callers to handle failures.",
				RuleID:      "GO-FUNC-001",
			})
		}
	}

	if fn.Body != nil {
		hasMutex := false
		hasDefer := false
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.CallExpr:
				if sel, ok := node.Fun.(*ast.SelectorExpr); ok {
					if sel.Sel.Name == "Lock" || sel.Sel.Name == "RLock" {
						hasMutex = true
					}
				}
			case *ast.DeferStmt:
				hasDefer = true
			}
			return true
		})

		if hasMutex && !hasDefer {
			findings = append(findings, RiskFinding{
				File:        path,
				Line:        pos.Line,
				Severity:    RiskMedium,
				Category:    "lock_without_defer",
				Code:        fmt.Sprintf("func %s(...)", fn.Name.Name),
				Description: fmt.Sprintf("Lock() called in %s without deferred Unlock", fn.Name.Name),
				Suggestion:  "Use defer to ensure mutex is always unlocked, preventing deadlocks.",
				RuleID:      "GO-LOCK-001",
			})
		}
	}

	return findings
}

func (ra *RiskAuditor) checkGoReturnStmt(path string, fset *token.FileSet, ret *ast.ReturnStmt, filePath string) []RiskFinding {
	return nil
}

func (ra *RiskAuditor) hasPreparedStatement(call *ast.CallExpr) bool {
	for _, arg := range call.Args {
		lit, ok := arg.(*ast.BasicLit)
		if !ok {
			continue
		}
		val := lit.Value
		if strings.Contains(val, "?") || strings.Contains(val, "$1") {
			return true
		}
	}
	return false
}

var dangerousPatterns = []struct {
	pattern     *regexp.Regexp
	severity    RiskSeverity
	category    string
	description string
	suggestion  string
	ruleID      string
}{
	{
		pattern:     regexp.MustCompile(`((api.?key|api.?secret|password|secret|token|credential)\s*[:=]\s*["'].+?)`),
		severity:    RiskCritical,
		category:    "hardcoded_secret",
		description: "Potential hardcoded secret or API key detected",
		suggestion:  "Move secrets to environment variables or a secrets manager. Never commit secrets.",
		ruleID:      "SEC-SECRET-001",
	},
	{
		pattern:     regexp.MustCompile(`TODO|FIXME|HACK|XXX|BUG`),
		severity:    RiskLow,
		category:    "code_quality",
		description: "Code contains TODO/FIXME/HACK marker",
		suggestion:  "Address before merging or track in issue tracker.",
		ruleID:      "CQ-TODO-001",
	},
	{
		pattern:     regexp.MustCompile(`fmt\.Printf|fmt\.Println|fmt\.Print`),
		severity:    RiskLow,
		category:    "debug_output",
		description: "Direct print to stdout/stderr",
		suggestion:  "Use structured logging instead of print statements.",
		ruleID:      "CQ-PRINT-001",
	},
	{
		pattern:     regexp.MustCompile(`_ = `),
		severity:    RiskInfo,
		category:    "unused_result",
		description: "Error or return value silently discarded",
		suggestion:  "Handle errors explicitly. Use named blank imports only when necessary.",
		ruleID:      "CQ-BLANK-001",
	},
	{
		pattern:     regexp.MustCompile(`\bpanic\(`),
		severity:    RiskHigh,
		category:    "panic",
		description: "panic() call in non-main code",
		suggestion:  "Return errors instead of panicking.",
		ruleID:      "GO-PANIC-002",
	},
	{
		pattern:     regexp.MustCompile(`http\.HandleFunc|http\.Handle`),
		severity:    RiskMedium,
		category:    "exposed_endpoint",
		description: "HTTP handler registered directly. Ensure proper auth and rate limiting.",
		suggestion:  "Review authentication and authorization for this endpoint.",
		ruleID:      "SEC-HTTP-001",
	},
}

func (ra *RiskAuditor) defaultRules() []RiskRule {
	rules := make([]RiskRule, 0, len(dangerousPatterns))
	for _, dp := range dangerousPatterns {
		p := dp
		rules = append(rules, RiskRule{
			ID:          p.ruleID,
			Severity:    p.severity,
			Category:    p.category,
			Description: p.description,
			Suggestion:  p.suggestion,
			Check: func(path string, line int, content string) *RiskFinding {
				if p.pattern.MatchString(content) {
					return &RiskFinding{
						File:        path,
						Line:        line,
						Severity:    p.severity,
						Category:    p.category,
						Code:        strings.TrimSpace(content),
						Description: p.description,
						Suggestion:  p.suggestion,
						RuleID:      p.ruleID,
					}
				}
				return nil
			},
		})
	}
	return rules
}

func (ra *RiskAuditor) auditGenericFile(path string, df DiffFile) []RiskFinding {
	var findings []RiskFinding

	data, err := os.ReadFile(path)
	if err != nil {
		return findings
	}

	lines := strings.Split(string(data), "\n")
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		line := scanner.Text()

		for _, rule := range ra.rules {
			if finding := rule.Check(df.Path, lineNum, line); finding != nil {
				findings = append(findings, *finding)
			}
		}
	}

	_ = lines
	return findings
}

func (ra *RiskAuditor) calculateRiskScore(findings []RiskFinding) int {
	score := 0
	for _, f := range findings {
		switch f.Severity {
		case RiskCritical:
			score += 40
		case RiskHigh:
			score += 20
		case RiskMedium:
			score += 10
		case RiskLow:
			score += 3
		case RiskInfo:
			score += 1
		}
	}
	return score
}
