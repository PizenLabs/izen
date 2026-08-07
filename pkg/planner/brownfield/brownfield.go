// Package brownfield implements the BrownfieldPlanner: a closed-loop agentic
// planner for refactoring an existing workspace. It builds an initial
// graph.ExecutionGraph of edit and verify operations and, when execution
// fails, analyzes the failure output, injects repair operations via
// graph.ExecutionGraph.InjectRepairOps, and re-runs the graph until it goes
// green or the repair budget is exhausted. Execution itself is delegated
// exclusively to the Phase A kernel.Engine.
package brownfield

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/PizenLabs/izen/pkg/graph"
	"github.com/PizenLabs/izen/pkg/ir"
	"github.com/PizenLabs/izen/pkg/kernel"
	"github.com/PizenLabs/izen/pkg/op"
	"github.com/PizenLabs/izen/pkg/planner"
	"github.com/PizenLabs/izen/pkg/resource/file"
	"github.com/PizenLabs/izen/pkg/resource/terminal"
)

const (
	defaultNodePrefix               = "bf-write"
	defaultVerifyPrefix             = "bf-verify"
	defaultRepairPrefix             = "bf-repair"
	defaultFileMode     fs.FileMode = 0o644
)

// DefaultVerifyCommand builds the default compile-and-test command run after
// every edit batch. Callers override it with WithVerifyCommand for other
// toolchains.
func DefaultVerifyCommand(string) string { return "go build ./... && go test ./..." }

// Errors returned by BrownfieldPlanner.
var (
	// ErrEmptyWorkspaceRoot is returned when the planner was built without a
	// workspace root.
	ErrEmptyWorkspaceRoot = errors.New("brownfield: workspace root is required")
	// ErrNilEngine is returned by ExecuteAndRepair when no kernel engine is
	// provided.
	ErrNilEngine = errors.New("brownfield: nil kernel engine")
	// ErrNilGraph is returned by ExecuteAndRepair when no graph is provided.
	ErrNilGraph = errors.New("brownfield: nil execution graph")
	// ErrNoRepair is returned when a failure produced no repair operations.
	ErrNoRepair = errors.New("brownfield: no repair operations produced")
)

// Compile-time assertion that BrownfieldPlanner satisfies planner.Planner.
var _ planner.Planner = (*BrownfieldPlanner)(nil)

// FailureSymptom classifies the cause of a failed command node so a repair
// strategy can choose a fix.
type FailureSymptom string

// Supported failure symptoms.
const (
	// SymptomNone means the failure carried no analyzable signal.
	SymptomNone FailureSymptom = ""
	// SymptomMissingFile means a referenced file was not found.
	SymptomMissingFile FailureSymptom = "missing_file"
	// SymptomCompileError means compilation failed.
	SymptomCompileError FailureSymptom = "compile_error"
	// SymptomTestFailure means the verification command reported a test
	// failure.
	SymptomTestFailure FailureSymptom = "test_failure"
	// SymptomUnknown means a failure occurred with no recognized signature.
	SymptomUnknown FailureSymptom = "unknown"
)

// FailureReport is the deterministic analysis of a failed node's terminal
// result.
type FailureReport struct {
	// Symptom classifies the failure.
	Symptom FailureSymptom
	// Command is the failed command string when the node carried one.
	Command string
	// Output is the combined failure output (stdout and stderr).
	Output string
	// Cause is the underlying kernel error.
	Cause error
	// MissingPath names the file the failure could not find, when known.
	MissingPath string
}

// RepairFunc decides the repair operations for a failed node from the failure
// report. It returns nil when it cannot propose a fix; the planner then stops
// rather than guessing. attempt is the zero-based repair cycle index.
type RepairFunc func(ctx context.Context, failure *graph.ExecutionFailure, report FailureReport, attempt int) ([]op.Operation, error)

// BrownfieldPlanner builds and repairs refactoring graphs for an existing
// workspace. It is the agentic, closed-loop counterpart to GreenfieldPlanner.
type BrownfieldPlanner struct {
	workspaceRoot string
	mode          fs.FileMode
	timeout       time.Duration
	nodePrefix    string
	verifyPrefix  string
	repairPrefix  string

	terminal *terminal.TerminalResource
	verify   func(intent string) string
	repair   RepairFunc

	artifacts []ir.Artifact
	repairSeq atomic.Uint64
}

// Option configures a BrownfieldPlanner.
type Option func(*BrownfieldPlanner)

// WithTerminal overrides the terminal resource commands run on.
func WithTerminal(t *terminal.TerminalResource) Option {
	return func(p *BrownfieldPlanner) {
		if t != nil {
			p.terminal = t
		}
	}
}

// WithVerifyCommand overrides the compile/test command builder. The function
// receives the planning intent and returns the shell command to run after an
// edit batch.
func WithVerifyCommand(cmd func(intent string) string) Option {
	return func(p *BrownfieldPlanner) {
		if cmd != nil {
			p.verify = cmd
		}
	}
}

// WithRepairFunc overrides the repair decision strategy.
func WithRepairFunc(fn RepairFunc) Option {
	return func(p *BrownfieldPlanner) {
		if fn != nil {
			p.repair = fn
		}
	}
}

// WithFileMode overrides the permission bits applied to written files.
func WithFileMode(mode fs.FileMode) Option {
	return func(p *BrownfieldPlanner) { p.mode = mode }
}

// WithTimeout bounds the execution of every operation in the graph.
func WithTimeout(timeout time.Duration) Option {
	return func(p *BrownfieldPlanner) { p.timeout = timeout }
}

// WithNodePrefix overrides the node ID prefix for write nodes.
func WithNodePrefix(prefix string) Option {
	return func(p *BrownfieldPlanner) {
		if prefix != "" {
			p.nodePrefix = prefix
		}
	}
}

// NewBrownfieldPlanner returns a planner bound to workspaceRoot. The default
// verify command compiles and tests a Go module; override it with
// WithVerifyCommand for other toolchains. The default repair strategy writes
// artifacts from the plan whose path matches a missing-file failure.
func NewBrownfieldPlanner(workspaceRoot string, opts ...Option) (*BrownfieldPlanner, error) {
	if workspaceRoot == "" {
		return nil, ErrEmptyWorkspaceRoot
	}
	p := &BrownfieldPlanner{
		workspaceRoot: workspaceRoot,
		mode:          defaultFileMode,
		nodePrefix:    defaultNodePrefix,
		verifyPrefix:  defaultVerifyPrefix,
		repairPrefix:  defaultRepairPrefix,
		verify:        DefaultVerifyCommand,
	}
	for _, opt := range opts {
		opt(p)
	}
	if p.terminal == nil {
		t, err := terminal.NewTerminalResource(workspaceRoot, nil, "")
		if err != nil {
			return nil, err
		}
		p.terminal = t
	}
	if p.repair == nil {
		p.repair = p.defaultRepair
	}
	return p, nil
}

// Plan implements planner.Planner. It lowers every file artifact into an
// op.OpWriteFile edit node and appends a verification command node that
// depends on every edit. Non-file artifacts are retained in the result. An
// empty artifact list still yields a verification-only graph, so pure command
// refactorings are supported.
func (p *BrownfieldPlanner) Plan(ctx context.Context, intent string, artifacts []ir.Artifact) (*planner.PlanResult, error) {
	if p == nil {
		return nil, errors.New("brownfield: nil receiver")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	g := graph.NewExecutionGraph()
	written := make([]string, 0, len(artifacts))
	for _, a := range artifacts {
		if a.Kind != ir.ArtifactFile {
			continue
		}
		res, err := file.NewFileResource(p.workspaceRoot, a.Path, p.mode)
		if err != nil {
			return nil, fmt.Errorf("brownfield: target %q: %w", a.Path, err)
		}
		nodeID := p.nodePrefix + ":" + a.Path
		operation, err := op.NewOperation(nodeID, op.OpWriteFile, res, a, nil, p.timeout)
		if err != nil {
			return nil, fmt.Errorf("brownfield: operation for %q: %w", a.Path, err)
		}
		node, err := graph.NewOpNode(operation)
		if err != nil {
			return nil, fmt.Errorf("brownfield: node for %q: %w", a.Path, err)
		}
		if err := g.AddNode(node); err != nil {
			return nil, fmt.Errorf("brownfield: add node for %q: %w", a.Path, err)
		}
		written = append(written, nodeID)
	}

	verifyCommand := p.verify(intent)
	vop, err := op.NewOperation(p.verifyPrefix, op.OpRunCommand, p.terminal, verifyCommand, written, p.timeout)
	if err != nil {
		return nil, fmt.Errorf("brownfield: verify operation: %w", err)
	}
	vnode, err := graph.NewOpNode(vop)
	if err != nil {
		return nil, fmt.Errorf("brownfield: verify node: %w", err)
	}
	if err := g.AddNode(vnode); err != nil {
		return nil, fmt.Errorf("brownfield: add verify node: %w", err)
	}

	p.artifacts = append([]ir.Artifact(nil), artifacts...)

	return &planner.PlanResult{
		Graph:     g,
		Artifacts: append([]ir.Artifact(nil), artifacts...),
		Metadata: map[string]string{
			"planner":        "brownfield",
			"intent":         intent,
			"strategy":       "agentic-repair",
			"verify_command": verifyCommand,
			"node_count":     strconv.Itoa(len(written) + 1),
			"artifact_count": strconv.Itoa(len(artifacts)),
		},
	}, nil
}

// ExecuteAndRepair drives g on engine, injecting repair operations whenever a
// node fails, until the graph completes or the repair budget (maxRetries
// cycles) is exhausted. A green graph returns nil.
func (p *BrownfieldPlanner) ExecuteAndRepair(ctx context.Context, engine *kernel.Engine, g *graph.ExecutionGraph, maxRetries int) error {
	if p == nil {
		return errors.New("brownfield: nil receiver")
	}
	if engine == nil {
		return ErrNilEngine
	}
	if g == nil {
		return ErrNilGraph
	}

	repairs := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := g.Execute(ctx, engine); err == nil {
			return nil
		} else {
			var failure *graph.ExecutionFailure
			if !errors.As(err, &failure) {
				return err
			}
			if repairs >= maxRetries {
				return fmt.Errorf("brownfield: repair budget exhausted after %d repair(s): %w", repairs, err)
			}
			report := AnalyzeFailure(failure.Result, p.commandFor(g, failure.NodeID))
			ops, rerr := p.repair(ctx, failure, report, repairs)
			if rerr != nil {
				return fmt.Errorf("brownfield: repair analysis failed: %w", rerr)
			}
			if len(ops) == 0 {
				return fmt.Errorf("%w for node %q (%s): %w", ErrNoRepair, failure.NodeID, report.Symptom, failure)
			}
			if ierr := g.InjectRepairOps(failure.NodeID, ops); ierr != nil {
				return fmt.Errorf("brownfield: inject repair ops: %w", ierr)
			}
			repairs++
		}
	}
}

// commandFor returns the shell command carried by a graph node, if any.
func (p *BrownfieldPlanner) commandFor(g *graph.ExecutionGraph, nodeID string) string {
	node, ok := g.GetNode(nodeID)
	if !ok {
		return ""
	}
	command, _ := node.Operation().Payload.(string)
	return command
}

// defaultRepair writes the first artifact whose path matches the missing file
// reported by the failure. It proposes no repair for any other symptom, so
// the planner stops rather than guessing.
func (p *BrownfieldPlanner) defaultRepair(ctx context.Context, failure *graph.ExecutionFailure, report FailureReport, attempt int) ([]op.Operation, error) {
	if report.Symptom != SymptomMissingFile {
		return nil, nil
	}
	for _, a := range p.artifacts {
		if a.Kind != ir.ArtifactFile || !matchesMissingPath(a.Path, report.MissingPath) {
			continue
		}
		res, err := file.NewFileResource(p.workspaceRoot, a.Path, p.mode)
		if err != nil {
			return nil, err
		}
		operation, err := op.NewOperation(p.nextRepairID(), op.OpWriteFile, res, a, nil, p.timeout)
		if err != nil {
			return nil, err
		}
		return []op.Operation{operation}, nil
	}
	return nil, nil
}

// nextRepairID returns a monotonically increasing repair node ID, unique
// across every repair cycle of the planner's lifetime.
func (p *BrownfieldPlanner) nextRepairID() string {
	return p.repairPrefix + "-" + strconv.FormatUint(p.repairSeq.Add(1), 10)
}

// matchesMissingPath reports whether an artifact path names the missing file
// or is a relative suffix/prefix of it.
func matchesMissingPath(artifactPath, missing string) bool {
	if missing == "" {
		return false
	}
	if artifactPath == missing {
		return true
	}
	return strings.HasSuffix(artifactPath, "/"+missing) || strings.HasSuffix(missing, "/"+artifactPath)
}

// Failure signatures recognized by AnalyzeFailure.
var (
	missingFileRe = regexp.MustCompile(`(?m)(\S+):\s*(?:No such file or directory|not found)\s*$`)
	openFileRe    = regexp.MustCompile(`open\s+(\S+):\s*no such file or directory`)
	compilePkgRe  = regexp.MustCompile(`cannot find package\s+"?[\w./-]+"?`)
	undefinedRe   = regexp.MustCompile(`undefined(?:[:]\s*)?\w+`)
	testFailRe    = regexp.MustCompile(`(?im)^\s*(?:FAIL|--- FAIL|panic:|Error:|error:)`)
)

// AnalyzeFailure classifies a failed node's terminal result into a
// FailureReport. The combined output is assembled from the error cause and
// the result's data (the command's captured output). command is the failed
// command string when known.
func AnalyzeFailure(result kernel.TaskResult, command string) FailureReport {
	report := FailureReport{Command: command, Cause: result.Error}
	if result.Error != nil {
		report.Output = result.Error.Error()
	}
	if data, ok := result.Data.(string); ok && data != "" {
		report.Output = strings.TrimSpace(report.Output + "\n" + data)
	}

	switch {
	case missingFileRe.MatchString(report.Output):
		report.Symptom = SymptomMissingFile
		report.MissingPath = missingFileRe.FindStringSubmatch(report.Output)[1]
	case openFileRe.MatchString(report.Output):
		report.Symptom = SymptomMissingFile
		report.MissingPath = openFileRe.FindStringSubmatch(report.Output)[1]
	case compilePkgRe.MatchString(report.Output) || undefinedRe.MatchString(report.Output):
		report.Symptom = SymptomCompileError
	case testFailRe.MatchString(report.Output):
		report.Symptom = SymptomTestFailure
	case report.Cause != nil:
		report.Symptom = SymptomUnknown
	default:
		report.Symptom = SymptomNone
	}
	return report
}
