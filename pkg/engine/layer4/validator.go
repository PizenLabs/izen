package layer4

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/PizenLabs/izen/internal/runtime/substrate"
	"github.com/PizenLabs/izen/pkg/engine/layer1"
	"github.com/PizenLabs/izen/pkg/engine/layer3"
)

// Sentinel errors returned by the validators.
var (
	// ErrNoValidator is returned when a stage resolves to no validator.
	ErrNoValidator = errors.New("layer4: no validator configured")
	// ErrValidationFailed is returned when a validation stage reports a
	// failing ValidationResult.
	ErrValidationFailed = errors.New("layer4: validation failed")
	// ErrUnsupportedCapability is returned when a capability-gated validator
	// is requested for a workspace that lacks the capability.
	ErrUnsupportedCapability = errors.New("layer4: workspace capability not supported")
	// ErrEmptyCommand is returned when a command validator has no command.
	ErrEmptyCommand = errors.New("layer4: empty validation command")
	// ErrNoSor is returned when a structural validator has no SoR.
	ErrNoSor = errors.New("layer4: no system of record configured")
)

// Patch is a proposed file mutation validated by the engine. It aliases the
// Layer 3 patch type so validation operates directly on the patches the
// pipeline produces.
type Patch = layer3.FilePatch

// Validator validates a proposed mutation set. A Validator is a stateless,
// read-only check: it must never mutate workspace state. Implementations must
// be safe for concurrent use.
type Validator interface {
	// Name identifies the validator for telemetry and result attribution.
	Name() string
	// Validate checks the proposed patches. The returned result is never nil
	// when err is nil; a failing check is reported through the result, not
	// through the error.
	Validate(ctx context.Context, patches []Patch) (*ValidationResult, error)
}

// ValidationResult is the structured outcome of a single validation stage. It
// carries the error location, captured stdout/stderr and the exit status of
// the underlying check, mirroring a process-style result.
type ValidationResult struct {
	// OK reports whether the stage passed.
	OK bool
	// Stage is the validation stage that produced the result.
	Stage Stage
	// Location is the first error location "file:line:col", empty when the
	// stage is not location-bound or passed.
	Location string
	// Stdout is the captured standard output of the check.
	Stdout string
	// Stderr is the captured standard error of the check.
	Stderr string
	// ExitCode is the process exit status of the check; -1 when the check did
	// not execute an external process.
	ExitCode int
	// Summary is a concise human-readable outcome.
	Summary string
}

// WithStage returns a copy of the result attributed to the given stage.
func (r *ValidationResult) WithStage(s Stage) *ValidationResult {
	if r == nil {
		return nil
	}
	cp := *r
	cp.Stage = s
	return &cp
}

// SourceReader reads a file's content relative to the repository root.
type SourceReader func(root, path string) ([]byte, error)

// DefaultSourceReader reads via ReadScope abstraction relative to root.
// It uses substrate.FSReadScope to remain pure: no direct os access in
// semantic layer; substrate owns I/O.
func DefaultSourceReader(root, path string) ([]byte, error) {
	rs := substrate.NewFSReadScope(root)
	return rs.ReadFile(filepath.FromSlash(path))
}

// FuncValidator adapts a plain function to the Validator interface. It is
// primarily a test seam and a way to compose deterministic checks.
type FuncValidator struct {
	label string
	stage Stage
	fn    func(ctx context.Context, patches []Patch) (*ValidationResult, error)
}

// NewFuncValidator returns a validator delegating to fn.
func NewFuncValidator(label string, stage Stage, fn func(ctx context.Context, patches []Patch) (*ValidationResult, error)) *FuncValidator {
	return &FuncValidator{label: label, stage: stage, fn: fn}
}

// Name implements Validator.
func (f *FuncValidator) Name() string {
	if f == nil || f.label == "" {
		return "func-validator"
	}
	return f.label
}

// Validate implements Validator.
func (f *FuncValidator) Validate(ctx context.Context, patches []Patch) (*ValidationResult, error) {
	if f == nil || f.fn == nil {
		return nil, ErrNoValidator
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	res, err := f.fn(ctx, patches)
	if res != nil {
		res = res.WithStage(f.stage)
	}
	return res, err
}

// SyntaxValidator is a native, in-process syntax check. It parses every
// proposed Go source in RAM with the standard library parser and reports the
// first parse error with its file:line:col location. It never shells out.
type SyntaxValidator struct {
	// Root is the workspace root used to resolve patch paths. It may be empty
	// for pure in-memory validation.
	Root string
	// Source overrides how file content is read (test seam).
	Source SourceReader
}

// NewSyntaxValidator returns a native syntax validator over the workspace at
// root.
func NewSyntaxValidator(root string) *SyntaxValidator {
	return &SyntaxValidator{Root: root}
}

// Name implements Validator.
func (v *SyntaxValidator) Name() string { return "syntax" }

// Validate implements Validator by parsing each proposed Go patch in RAM.
func (v *SyntaxValidator) Validate(ctx context.Context, patches []Patch) (*ValidationResult, error) {
	src := v.Source
	if src == nil {
		src = DefaultSourceReader
	}
	// New content of a patch is authoritative; files without a proposed
	// mutation fall back to disk.
	content := func(p Patch) ([]byte, error) {
		if p.New != "" || p.Path == "" {
			return []byte(p.New), nil
		}
		return src(v.Root, p.Path)
	}
	for _, p := range patches {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !isGoPath(p.Path) {
			continue
		}
		body, err := content(p)
		if err != nil {
			return nil, fmt.Errorf("layer4: syntax: %s: %w", p.Path, err)
		}
		if loc := goParseError(p.Path, body); loc != "" {
			return resultFail(StageSyntax, loc, "", "", "syntax error"), nil
		}
	}
	return resultPass(StageSyntax, fmt.Sprintf("%d file(s) parse cleanly", len(patches))), nil
}

// CommandValidator validates by executing a workspace capability command
// (lint, build or test). The proposed patches must already be applied to the
// on-disk workspace before Validate is called; the validator reports the
// command's exit status and captured output. A CommandValidator is immutable
// after construction and safe for concurrent use.
type CommandValidator struct {
	// Label identifies the validator in results and telemetry.
	Label string
	// Stage is the validation stage the command backs.
	Stage Stage
	// Root is the directory the command runs in. Empty runs in the current
	// directory.
	Root string
	// Cmd is the space-separated command line to execute.
	Cmd string
	// Env augments the command environment. Values are KEY=VALUE pairs.
	Env []string
}

// NewCommandValidator returns a command validator for stage backed by cmd.
func NewCommandValidator(stage Stage, cmd, root string) *CommandValidator {
	return &CommandValidator{Label: string(stage), Stage: stage, Cmd: cmd, Root: root}
}

// Name implements Validator.
func (v *CommandValidator) Name() string {
	if v == nil || v.Label == "" {
		return "command"
	}
	return v.Label
}

// Validate implements Validator via substrate helper; semantic layer never
// invokes exec directly.
func (v *CommandValidator) Validate(ctx context.Context, patches []Patch) (*ValidationResult, error) {
	if v == nil {
		return nil, ErrNoValidator
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fields := splitCommand(v.Cmd)
	if len(fields) == 0 {
		return nil, fmt.Errorf("%w: stage %s", ErrEmptyCommand, v.Stage)
	}
	res := substrate.ExecCommand(ctx, v.Root, v.Env, fields)
	if res.Err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// Missing binary or execution error surfaces as transport error per contract.
		if res.ExitCode == -1 {
			return nil, fmt.Errorf("%w: stage %s: %w", ErrValidationFailed, v.Stage, res.Err)
		}
	}
	if res.ExitCode != 0 {
		r := resultFail(v.Stage, "", res.Stdout, res.Stderr, "exit status "+itoa(res.ExitCode))
		r.ExitCode = res.ExitCode
		return r, nil
	}
	return resultPassWithOutput(v.Stage, res.Stdout, res.Stderr, 0, "command succeeded"), nil
}

// capabilityValidators resolve capability-gated command validators. Each
// constructor returns ErrUnsupportedCapability when the workspace lacks the
// backing capability, so plan construction never fabricates a stage.

// LintValidator returns a command validator for the workspace lint command,
// resolved from the Layer 1 capability graph.
func LintValidator(caps CapabilityReader, root string) (*CommandValidator, error) {
	return capabilityValidator(caps, root, StageLint, layer1.CapLint)
}

// BuildValidator returns a command validator for the workspace build command,
// resolved from the Layer 1 capability graph.
func BuildValidator(caps CapabilityReader, root string) (*CommandValidator, error) {
	return capabilityValidator(caps, root, StageBuild, layer1.CapBuild)
}

// TestValidator returns a command validator for the workspace test command,
// resolved from the Layer 1 capability graph.
func TestValidator(caps CapabilityReader, root string) (*CommandValidator, error) {
	return capabilityValidator(caps, root, StageTest, layer1.CapTest)
}

func capabilityValidator(caps CapabilityReader, root string, stage Stage, cap layer1.Capability) (*CommandValidator, error) {
	if caps == nil || !caps.Supports(cap) {
		return nil, fmt.Errorf("%w: stage %s", ErrUnsupportedCapability, stage)
	}
	cmd, _ := caps.Resolve(cap)
	if strings.TrimSpace(cmd) == "" {
		return nil, fmt.Errorf("%w: stage %s", ErrEmptyCommand, stage)
	}
	return &CommandValidator{Label: "capability-" + string(cap), Stage: stage, Root: root, Cmd: cmd}, nil
}

// resultPass returns a passing result for a stage.
func resultPass(stage Stage, summary string) *ValidationResult {
	return &ValidationResult{OK: true, Stage: stage, ExitCode: 0, Summary: summary}
}

func resultPassWithOutput(stage Stage, stdout, stderr string, code int, summary string) *ValidationResult {
	return &ValidationResult{OK: true, Stage: stage, Stdout: stdout, Stderr: stderr, ExitCode: code, Summary: summary}
}

// resultFail returns a failing result for a stage with an optional location.
func resultFail(stage Stage, location, stdout, stderr, summary string) *ValidationResult {
	return &ValidationResult{OK: false, Stage: stage, Location: location, Stdout: stdout, Stderr: stderr, ExitCode: -1, Summary: summary}
}

// isGoPath reports whether path is a Go source file.
func isGoPath(path string) bool {
	return strings.HasSuffix(path, ".go")
}

// splitCommand tokenizes a command line into argv entries, honoring single
// quotes, double quotes and backslash escapes. It mirrors the common shell
// word-splitting rules so workspace commands such as `go test ./...` and
// `docker compose -f "a b.yml" up` split deterministically.
func splitCommand(cmd string) []string {
	var args []string
	var cur strings.Builder
	inSingle, inDouble := false, false
	flush := func() {
		if cur.Len() > 0 {
			args = append(args, cur.String())
			cur.Reset()
		}
	}
	for i := 0; i < len(cmd); i++ {
		c := cmd[i]
		switch {
		case inSingle:
			if c == '\'' {
				inSingle = false
			} else {
				cur.WriteByte(c)
			}
		case inDouble:
			switch {
			case c == '"':
				inDouble = false
			case c == '\\' && i+1 < len(cmd):
				i++
				cur.WriteByte(cmd[i])
			default:
				cur.WriteByte(c)
			}
		case c == '\'':
			inSingle = true
		case c == '"':
			inDouble = true
		case c == '\\' && i+1 < len(cmd):
			i++
			cur.WriteByte(cmd[i])
		case c == ' ' || c == '\t' || c == '\n':
			flush()
		default:
			cur.WriteByte(c)
		}
	}
	flush()
	return args
}

// itoa is a minimal integer to string helper avoiding strconv import noise.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
