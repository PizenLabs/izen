package review

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/PizenLabs/izen/internal/runtime/output"
)

const SandboxBase = "/tmp/izen/review"

type Sandbox struct {
	ReviewID    string
	Workspace   string
	ProjectRoot string
	created     bool
	// pipeline is the Phase 1 Tool Output Intelligence pipeline. Ephemeral
	// `go test` verification output is normalized, classified, semantically
	// compressed and tee-logged to `<projectRoot>/.logs/`. Nil disables
	// processing.
	pipeline *output.Pipeline
}

func NewSandbox(reviewID, projectRoot string) *Sandbox {
	return &Sandbox{
		ReviewID:    reviewID,
		Workspace:   filepath.Join(SandboxBase, sanitizeID(reviewID)),
		ProjectRoot: projectRoot,
		pipeline:    output.New().WithWorkspace(projectRoot),
	}
}

// WithPipeline overrides the output pipeline used for verification output. Nil
// disables normalization/compression/tee-logging.
func (s *Sandbox) WithPipeline(p *output.Pipeline) *Sandbox {
	s.pipeline = p
	return s
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

	// ── TOOL OUTPUT PIPELINE (PHASE 1) ──────────────────────────────────
	// The `go test` output is normalized, classified as GO_TEST, semantically
	// compressed and tee-logged to `.logs/`. The raw output still drives the
	// Passed/Panicked classification below.
	if s.pipeline != nil {
		s.pipeline.Process("go test -v -count=1 "+target, output)
	}

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

	return s.RunGoTestInProjectContext(ctx, pkg)
}

// RunGoTestInProjectContext runs `go test` against the project root, bounded by
// the caller's context (its deadline or cancellation) instead of a fixed local
// timeout. Cancellation surfaces as a non-passed result with a timeout marker.
func (s *Sandbox) RunGoTestInProjectContext(ctx context.Context, pkg string) TestResult { //nolint:contextcheck // threads a caller-provided scope into exec.CommandContext below
	if !s.created {
		return TestResult{Passed: false, Output: "sandbox not created", Panicked: false}
	}

	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "go", "test", "-v", "-count=1", pkg)
	cmd.Dir = s.ProjectRoot

	output, err := cmd.CombinedOutput()
	outStr := string(output)

	// ── TOOL OUTPUT PIPELINE (PHASE 1) ──────────────────────────────────
	// Same normalization/classification/compression/tee-logging as RunTest.
	if s.pipeline != nil {
		s.pipeline.Process("go test -v -count=1 "+pkg, output)
	}

	if err != nil {
		if ctx.Err() != nil {
			return TestResult{Passed: false, Output: truncateOutput("verification timed out or was cancelled: "+ctx.Err().Error()+"\n"+outStr, 2000), Panicked: false}
		}
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
	return RunWithSandboxContext(context.Background(), reviewID, projectRoot, fn)
}

// RunWithSandboxContext is RunWithSandbox with an external cancellation scope.
// The sandbox lifecycle honours ctx: when ctx is cancelled before the sandbox
// callback runs, the run is skipped and reported as cancelled rather than
// executing an abandoned verification.
func RunWithSandboxContext(ctx context.Context, reviewID, projectRoot string, fn SandboxRunFn) (EvidenceRecord, error) {
	sb := NewSandbox(reviewID, projectRoot)
	if err := sb.Create(); err != nil {
		return EvidenceRecord{}, fmt.Errorf("sandbox create: %w", err)
	}

	// Cleanup is guaranteed on every exit path (including a cancelled ctx).
	defer func() { _ = sb.Cleanup() }()

	if ctx != nil && ctx.Err() != nil {
		rec := EvidenceRecord{
			ID:          "E-ephemeral",
			Type:        EvTypeEphemeralTest,
			Status:      EvStatusSkipped,
			Confidence:  ConfLow,
			ArtifactRef: "",
			Output:      "Verification skipped — review context cancelled: " + ctx.Err().Error(),
		}
		return rec, nil
	}

	status, confidence, artifactRef, output := fn(sb)

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
