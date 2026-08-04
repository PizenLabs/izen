package registry

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"sync"
)

// Validator is the plugin interface of the validation pipeline. Each
// validator checks one path (a file, package or directory) and returns a
// single report.
type Validator interface {
	// Name returns the validator identifier shown in reports.
	Name() string
	// Validate checks path and returns the verdict.
	Validate(ctx context.Context, path string) (*ValidationReport, error)
}

// ValidationReport is one plugin's verdict for a single path.
type ValidationReport struct {
	Name   string
	Path   string
	OK     bool
	Output string
	Err    error
}

// ValidationResult is the aggregate outcome of one validation pipeline run.
type ValidationResult struct {
	OK      bool
	Reports []ValidationReport
	Err     error
}

// ValidationRegistry holds the ordered validation pipeline. It is safe for
// concurrent use.
type ValidationRegistry struct {
	mu       sync.RWMutex
	pipeline []Validator
}

// NewValidationRegistry returns an empty validation registry.
func NewValidationRegistry() *ValidationRegistry {
	return &ValidationRegistry{}
}

// Add appends a validator to the pipeline. Nil validators are ignored.
func (r *ValidationRegistry) Add(v Validator) {
	if v == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pipeline = append(r.pipeline, v)
}

// Pipeline returns a snapshot of the registered validators in order.
func (r *ValidationRegistry) Pipeline() []Validator {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]Validator(nil), r.pipeline...)
}

// Run executes every registered validator against every target path and
// aggregates the reports. A run is OK only when every report is OK.
func (r *ValidationRegistry) Run(ctx context.Context, targets []string) *ValidationResult {
	res := &ValidationResult{OK: true}
	if len(targets) == 0 {
		return res
	}
	for _, v := range r.Pipeline() {
		for _, target := range targets {
			if err := ctx.Err(); err != nil {
				res.OK = false
				res.Err = err
				return res
			}
			report, err := v.Validate(ctx, target)
			if err != nil {
				report = &ValidationReport{Name: v.Name(), Path: target, OK: false, Err: err}
			}
			if report == nil {
				continue
			}
			if !report.OK {
				res.OK = false
			}
			res.Reports = append(res.Reports, *report)
		}
	}
	return res
}

// ── Built-in validators ────────────────────────────────────────────────────

// GofmtValidator runs `gofmt -l <path>` and fails when the formatter lists
// any file. When gofmt is not installed the validator is skipped, not failed.
type GofmtValidator struct {
	Root string
}

// Name implements Validator.
func (v GofmtValidator) Name() string { return "gofmt" }

// Validate implements Validator.
func (v GofmtValidator) Validate(ctx context.Context, path string) (*ValidationReport, error) {
	if !strings.HasSuffix(path, ".go") {
		return &ValidationReport{Name: v.Name(), Path: path, OK: true, Output: "non-Go path skipped"}, nil
	}
	out, err := runToolWithExit(ctx, "", "gofmt", "-l", path)
	if err != nil {
		if errors.Is(err, errToolMissing) {
			return &ValidationReport{Name: v.Name(), Path: path, OK: true, Output: "gofmt not found; skipped"}, nil
		}
		return &ValidationReport{Name: v.Name(), Path: path, OK: false, Output: out, Err: err}, nil
	}
	unformatted := strings.TrimSpace(out)
	if unformatted == "" {
		return &ValidationReport{Name: v.Name(), Path: path, OK: true, Output: "gofmt clean"}, nil
	}
	return &ValidationReport{
		Name: v.Name(), Path: path, OK: false,
		Output: "unformatted files: " + unformatted,
	}, nil
}

// GolangciLintValidator runs `golangci-lint run ./...` in the workspace root.
// When the linter is not installed the validator is skipped, not failed.
type GolangciLintValidator struct {
	Root string
}

// Name implements Validator.
func (v GolangciLintValidator) Name() string { return "golangci-lint" }

// Validate implements Validator.
func (v GolangciLintValidator) Validate(ctx context.Context, path string) (*ValidationReport, error) {
	dir := v.Root
	if dir == "" {
		dir = path
	}
	out, err := runToolWithExit(ctx, dir, "golangci-lint", "run", "./...")
	if err != nil {
		if errors.Is(err, errToolMissing) {
			return &ValidationReport{Name: v.Name(), Path: path, OK: true, Output: "golangci-lint not found; skipped"}, nil
		}
		return &ValidationReport{Name: v.Name(), Path: path, OK: false, Output: out, Err: err}, nil
	}
	return &ValidationReport{Name: v.Name(), Path: path, OK: true, Output: "golangci-lint clean"}, nil
}

// GoTestValidator runs `go test ./...` in the workspace root. When go is not
// installed the validator is skipped, not failed.
type GoTestValidator struct {
	Root string
}

// Name implements Validator.
func (v GoTestValidator) Name() string { return "go-test" }

// Validate implements Validator.
func (v GoTestValidator) Validate(ctx context.Context, path string) (*ValidationReport, error) {
	dir := v.Root
	if dir == "" {
		dir = path
	}
	out, err := runToolWithExit(ctx, dir, "go", "test", "./...")
	if err != nil {
		if errors.Is(err, errToolMissing) {
			return &ValidationReport{Name: v.Name(), Path: path, OK: true, Output: "go not found; skipped"}, nil
		}
		return &ValidationReport{Name: v.Name(), Path: path, OK: false, Output: out, Err: err}, nil
	}
	return &ValidationReport{Name: v.Name(), Path: path, OK: true, Output: "go test passed"}, nil
}

// errToolMissing signals that the tool backing a validator is not installed.
var errToolMissing = errors.New("registry: tool not found")

// runToolWithExit executes a tool and returns an error when it either fails
// or is unavailable. The caller distinguishes the two via errToolMissing.
func runToolWithExit(ctx context.Context, dir, name string, args ...string) (string, error) {
	path, err := exec.LookPath(name)
	if err != nil {
		return "", errToolMissing
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return strings.TrimSpace(string(out)), err
	}
	return strings.TrimSpace(string(out)), nil
}
