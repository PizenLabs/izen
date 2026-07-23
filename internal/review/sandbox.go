package review

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const SandboxBase = "/tmp/izen/review"

type Sandbox struct {
	ReviewID    string
	Workspace   string
	ProjectRoot string
	created     bool
}

func NewSandbox(reviewID, projectRoot string) *Sandbox {
	return &Sandbox{
		ReviewID:    reviewID,
		Workspace:   filepath.Join(SandboxBase, sanitizeID(reviewID)),
		ProjectRoot: projectRoot,
	}
}

func sanitizeID(id string) string {
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, id)
}

func (s *Sandbox) Create() error {
	if err := os.MkdirAll(s.Workspace, 0755); err != nil {
		return fmt.Errorf("create sandbox: %w", err)
	}
	s.created = true
	return nil
}

func (s *Sandbox) Cleanup() error {
	if !s.created {
		return nil
	}
	if err := os.RemoveAll(s.Workspace); err != nil {
		return fmt.Errorf("cleanup sandbox: %w", err)
	}
	s.created = false
	return nil
}

func (s *Sandbox) WriteTestFile(name, content string) error {
	path := filepath.Join(s.Workspace, name)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("write test file mkdir: %w", err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return fmt.Errorf("write test file: %w", err)
	}
	return nil
}

type TestResult struct {
	Passed   bool
	Output   string
	Panicked bool
}

func (s *Sandbox) RunTest(testFile string) TestResult {
	if !s.created {
		return TestResult{Passed: false, Output: "sandbox not created", Panicked: false}
	}

	cwd := s.Workspace

	pkg := filepath.Dir(testFile)
	if pkg == "." {
		pkg = ""
	}

	target := "./" + pkg
	if pkg == "" {
		target = "."
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "go", "test", "-v", "-count=1", target)
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(),
		"GOFLAGS=-mod=mod",
	)

	output, err := cmd.CombinedOutput()
	outStr := string(output)

	if err != nil {
		if strings.Contains(outStr, "panic") {
			return TestResult{
				Passed:   false,
				Output:   truncateOutput(outStr, 2000),
				Panicked: true,
			}
		}
		return TestResult{
			Passed:   false,
			Output:   truncateOutput(outStr, 2000),
			Panicked: false,
		}
	}

	return TestResult{
		Passed:   true,
		Output:   truncateOutput(outStr, 1000),
		Panicked: false,
	}
}

func (s *Sandbox) RunGoTestInProject(pkg string) TestResult {
	if !s.created {
		return TestResult{Passed: false, Output: "sandbox not created", Panicked: false}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "go", "test", "-v", "-count=1", pkg)
	cmd.Dir = s.ProjectRoot

	output, err := cmd.CombinedOutput()
	outStr := string(output)

	if err != nil {
		if strings.Contains(outStr, "panic") {
			return TestResult{Passed: false, Output: truncateOutput(outStr, 2000), Panicked: true}
		}
		return TestResult{Passed: false, Output: truncateOutput(outStr, 2000)}
	}

	return TestResult{Passed: true, Output: truncateOutput(outStr, 1000)}
}

func truncateOutput(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + fmt.Sprintf("\n... [truncated %d bytes]", len(s)-max)
}

type SandboxRunFn func(sb *Sandbox) (EvidenceStatus, EvidenceConfidence, string, string)

func RunWithSandbox(reviewID, projectRoot string, fn SandboxRunFn) (EvidenceRecord, error) {
	sb := NewSandbox(reviewID, projectRoot)
	if err := sb.Create(); err != nil {
		return EvidenceRecord{}, fmt.Errorf("sandbox create: %w", err)
	}

	status, confidence, artifactRef, output := fn(sb)

	if err := sb.Cleanup(); err != nil {
		return EvidenceRecord{}, fmt.Errorf("sandbox cleanup: %w", err)
	}

	rec := EvidenceRecord{
		ID:          "E-ephemeral",
		Type:        EvTypeEphemeralTest,
		Status:      status,
		Confidence:  confidence,
		ArtifactRef: artifactRef,
		Output:      output,
	}

	return rec, nil
}

type WaitForResult struct {
	Command  string
	ExitCode int
	TimedOut bool
	Duration time.Duration
	Stdout   string
	Stderr   string
}
