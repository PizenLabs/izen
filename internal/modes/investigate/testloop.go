package investigate

import (
	"fmt"
	"strings"
)

type TestResultSummary struct {
	Package    string   `json:"package"`
	Passed     bool     `json:"passed"`
	Total      int      `json:"total"`
	PassedN    int      `json:"passed_n"`
	FailedN    int      `json:"failed_n"`
	Skipped    int      `json:"skipped"`
	Failed     []string `json:"failed,omitempty"`
	Output     string   `json:"output,omitempty"`
	Frames     []StackFrame `json:"frames,omitempty"`
}

type TestLoop struct {
	maxIterations int
}

func NewTestLoop(maxIterations int) *TestLoop {
	if maxIterations <= 0 {
		maxIterations = 3
	}
	return &TestLoop{maxIterations: maxIterations}
}

type TestExecutor interface {
	RunAllTests() (*TestResultSummary, error)
	RunPackageTests(pkg string) (*TestResultSummary, error)
	RunSpecificTest(pkg, test string) (*TestResultSummary, error)
}

type testLoopConfig struct {
	Strategy string
	Package  string
	TestName string
}

func (tl *TestLoop) Run(exec TestExecutor, cfg testLoopConfig) (*TestResultSummary, error) {
	var result *TestResultSummary
	var err error

	switch cfg.Strategy {
	case "all":
		result, err = exec.RunAllTests()
	case "package":
		result, err = exec.RunPackageTests(cfg.Package)
	case "specific":
		result, err = exec.RunSpecificTest(cfg.Package, cfg.TestName)
	default:
		result, err = exec.RunAllTests()
	}

	if err != nil {
		return result, fmt.Errorf("test execution: %w", err)
	}

	return result, nil
}

func (tl *TestLoop) NarrowIteration(prev *TestResultSummary, frames []StackFrame) []string {
	var candidates []string
	for _, failed := range prev.Failed {
		candidates = append(candidates, failed)
	}
	for _, frame := range frames {
		pkg := extractPackageFromFile(frame.File)
		if pkg != "" {
			candidates = append(candidates, pkg)
		}
	}
	return unique(candidates)
}

func extractPackageFromFile(file string) string {
	parts := strings.Split(file, string(filepathSeparator))
	for i, part := range parts {
		if part == "internal" || part == "pkg" || part == "cmd" {
			if i+1 < len(parts) {
				return strings.Join(parts[:i+2], "/")
			}
		}
	}
	return ""
}

var filepathSeparator = "/"

func unique(s []string) []string {
	seen := make(map[string]bool)
	var result []string
	for _, v := range s {
		if !seen[v] {
			seen[v] = true
			result = append(result, v)
		}
	}
	return result
}