package execution

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type TestResult struct {
	Package   string        `json:"package"`
	ContextID string        `json:"context_id,omitempty"`
	Passed    bool          `json:"passed"`
	Total     int           `json:"total"`
	PassedN   int           `json:"passed_n"`
	FailedN   int           `json:"failed_n"`
	Skipped   int           `json:"skipped"`
	Output    string        `json:"output"`
	Failed    []FailedTest  `json:"failed,omitempty"`
	Cover     string        `json:"coverage,omitempty"`
	Duration  time.Duration `json:"duration"`
}

type FailedTest struct {
	Name   string `json:"name"`
	Output string `json:"output"`
}

type TestRunner struct {
	root      string
	contextID string
}

func NewTestRunner(root string) *TestRunner {
	return &TestRunner{root: root}
}

func (tr *TestRunner) SetContextID(id string) {
	tr.contextID = id
}

func (tr *TestRunner) ActiveContextID() string {
	return tr.contextID
}

func (tr *TestRunner) RunAll() (*TestResult, error) {
	return tr.run(filepath.Join(tr.root, "..."), false)
}

func (tr *TestRunner) RunPackage(pkg string) (*TestResult, error) {
	return tr.run(pkg, false)
}

func (tr *TestRunner) RunWithCoverage(pkg string) (*TestResult, error) {
	return tr.run(pkg, true)
}

func (tr *TestRunner) RunFile(file string) (*TestResult, error) {
	return tr.run(file, false)
}

func (tr *TestRunner) run(target string, cover bool) (*TestResult, error) {
	runner := NewRunner(tr.root, false, false)
	runner.SetContextID(tr.contextID)

	args := []string{"go", "test"}
	if cover {
		args = append(args, "-cover")
	}
	args = append(args, "-v", target)

	result, err := runner.Run(strings.Join(args, " "))
	if err != nil {
		return nil, err
	}

	parsed := parseTestOutput(result.Stdout + "\n" + result.Stderr)
	parsed.ContextID = tr.contextID

	// Write test output to context-specific log file
	if tr.contextID != "" {
		_ = tr.writeTestRunLog(parsed)
	}

	return parsed, nil
}

// writeTestRunLog persists the full test output to .izen/history/test_runs/#ctx-<id>-r<seq>.log
// for isolated diagnostics retrieval.
func (tr *TestRunner) writeTestRunLog(result *TestResult) error {
	logDir := filepath.Join(tr.root, ".izen", "history", "test_runs")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return err
	}
	logName := sanitizeCtxFileName(tr.contextID) + ".log"
	logPath := filepath.Join(logDir, logName)
	return os.WriteFile(logPath, []byte(result.Output), 0644)
}

func sanitizeCtxFileName(id string) string {
	return strings.NewReplacer("#", "", "-", "_", "/", "_").Replace(id)
}

var (
	testResultRe = regexp.MustCompile(`^(ok|FAIL)\s+(\S+)\s+([\d.]+s)`)
	failLineRe   = regexp.MustCompile(`^--- FAIL:\s+(.+?)\s`)
	coverRe      = regexp.MustCompile(`coverage:\s+([\d.]+%)`)
)

func parseTestOutput(output string) *TestResult {
	result := &TestResult{}
	scanner := bufio.NewScanner(strings.NewReader(output))

	var currentFailed string
	var currentOutput strings.Builder

	for scanner.Scan() {
		line := scanner.Text()

		if m := failLineRe.FindStringSubmatch(line); m != nil {
			if currentFailed != "" {
				result.Failed = append(result.Failed, FailedTest{
					Name:   currentFailed,
					Output: currentOutput.String(),
				})
				currentOutput.Reset()
			}
			currentFailed = m[1]
			result.FailedN++
			continue
		}

		if currentFailed != "" {
			currentOutput.WriteString(line)
			currentOutput.WriteString("\n")
		}

		if m := testResultRe.FindStringSubmatch(line); m != nil {
			if currentFailed != "" {
				result.Failed = append(result.Failed, FailedTest{
					Name:   currentFailed,
					Output: currentOutput.String(),
				})
				currentFailed = ""
				currentOutput.Reset()
			}

			result.Passed = m[1] == "ok"
			result.Package = m[2]

			d, err := time.ParseDuration(m[3])
			if err == nil {
				result.Duration = d
			}
		}

		if m := coverRe.FindStringSubmatch(line); m != nil {
			result.Cover = m[1]
		}

		if strings.Contains(line, "--- PASS:") {
			result.PassedN++
		}
		if strings.Contains(line, "--- SKIP:") {
			result.Skipped++
		}
	}

	result.Total = result.PassedN + result.FailedN + result.Skipped
	result.Output = output

	if currentFailed != "" {
		result.Failed = append(result.Failed, FailedTest{
			Name:   currentFailed,
			Output: currentOutput.String(),
		})
	}

	if result.Package == "" {
		if strings.Contains(output, "FAIL") {
			result.Passed = false
		} else {
			result.Passed = true
		}
		parts := strings.Fields(output)
		for _, p := range parts {
			if strings.Contains(p, "/") && strings.Contains(p, ".") {
				result.Package = p
				break
			}
		}
	}

	return result
}

func (tr *TestRunner) RunTests(dir, pattern string) (*TestResult, error) {
	target := filepath.Join(tr.root, dir)
	if pattern != "" {
		target = fmt.Sprintf("%s -run %s", target, pattern)
	}
	return tr.run(target, false)
}
