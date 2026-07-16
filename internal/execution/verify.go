package execution

import (
	"fmt"
	"strings"
)

type VerificationStep struct {
	Name     string `json:"name"`
	Command  string `json:"command"`
	Optional bool   `json:"optional"`
}

type VerificationResult struct {
	Step   VerificationStep `json:"step"`
	Passed bool             `json:"passed"`
	Output string           `json:"output,omitempty"`
	Error  string           `json:"error,omitempty"`
}

type VerificationReport struct {
	Results []VerificationResult `json:"results"`
	Passed  bool                 `json:"passed"`
}

type Verifier struct {
	root  string
	steps []VerificationStep
}

var defaultVerificationSteps = []VerificationStep{
	{Name: "go fmt", Command: "go fmt ./...", Optional: false},
	{Name: "go vet", Command: "go vet ./...", Optional: false},
	{Name: "go test", Command: "go test ./...", Optional: false},
	{Name: "golangci-lint", Command: "golangci-lint run ./...", Optional: true},
	{Name: "govulncheck", Command: "govulncheck ./...", Optional: true},
}

func NewVerifier(root string) *Verifier {
	return &Verifier{
		root:  root,
		steps: make([]VerificationStep, len(defaultVerificationSteps)),
	}
}

func (v *Verifier) SetCustomSteps(steps []VerificationStep) {
	v.steps = steps
}

func (v *Verifier) RunAll() VerificationReport {
	if len(v.steps) == 0 {
		v.steps = make([]VerificationStep, len(defaultVerificationSteps))
		copy(v.steps, defaultVerificationSteps)
	}

	var report VerificationReport
	report.Passed = true

	for _, step := range v.steps {
		result := v.runStep(step)
		report.Results = append(report.Results, result)

		if !result.Passed && !step.Optional {
			report.Passed = false
		}
	}

	return report
}

func (v *Verifier) runStep(step VerificationStep) VerificationResult {
	runner := NewRunner(v.root, false, false)

	rawResult, err := runner.Run(step.Command)

	result := VerificationResult{Step: step}

	if err != nil {
		result.Error = err.Error()
		if rawResult != nil {
			result.Output = rawResult.Stderr
			if result.Output == "" {
				result.Output = rawResult.Stdout
			}
		}
		if rawResult != nil && rawResult.ExitCode == 0 {
			result.Passed = true
		}
		return result
	}

	result.Passed = rawResult.ExitCode == 0
	result.Output = rawResult.Stderr
	if result.Output == "" {
		result.Output = rawResult.Stdout
	}
	if rawResult.ExitCode != 0 && result.Output == "" {
		result.Output = fmt.Sprintf("exit code: %d", rawResult.ExitCode)
	}

	return result
}

func (r VerificationReport) String() string {
	var b strings.Builder
	b.WriteString("=== Verification Report ===\n")
	for _, res := range r.Results {
		status := "PASS"
		if !res.Passed {
			status = "FAIL"
		}
		opt := ""
		if res.Step.Optional {
			opt = " (optional)"
		}
		fmt.Fprintf(&b, "  %s: %s%s\n", res.Step.Name, status, opt)
		if !res.Passed && res.Output != "" {
			for _, line := range strings.Split(res.Output, "\n") {
				if strings.TrimSpace(line) != "" {
					fmt.Fprintf(&b, "    |> %s\n", line)
				}
			}
		}
	}
	overall := "PASSED"
	if !r.Passed {
		overall = "FAILED"
	}
	fmt.Fprintf(&b, "  Overall: %s\n", overall)
	return b.String()
}
