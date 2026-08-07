// Package greenfield implements the GreenfieldPlanner: a deterministic
// one-shot planner that synthesizes an entire workspace from ir.Artifact
// values. It lowers every file artifact into an op.OpWriteFile operation
// bound to a resource.FileResource and assembles them into a parallel (or
// topologically ordered) graph.ExecutionGraph. Planning performs zero tool
// calls: the whole workspace is emitted as a single batch graph and delegated
// to the Phase A kernel.Engine for execution.
package greenfield

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"

	txfs "github.com/PizenLabs/izen/pkg/fs"
	"github.com/PizenLabs/izen/pkg/graph"
	"github.com/PizenLabs/izen/pkg/ir"
	"github.com/PizenLabs/izen/pkg/op"
	"github.com/PizenLabs/izen/pkg/planner"
	"github.com/PizenLabs/izen/pkg/resource"
	"github.com/PizenLabs/izen/pkg/resource/file"
)

// DependsOnKey is the artifact metadata key holding a comma-separated list of
// artifact IDs that must be written before the artifact. Declaring a
// dependency orders the graph topologically; omitting it keeps the writes
// fully parallel. Dependencies on non-file artifacts are ignored.
const DependsOnKey = "depends_on"

// defaultNodePrefix is the prefix of every write node ID.
const defaultNodePrefix = "gf-write"

// defaultFileMode is applied to files written by the planner.
const defaultFileMode fs.FileMode = 0o644

// Errors returned by GreenfieldPlanner.
var (
	// ErrEmptyWorkspaceRoot is returned when the planner was built without a
	// workspace root.
	ErrEmptyWorkspaceRoot = errors.New("greenfield: workspace root is required")
	// ErrNoArtifacts is returned when Plan receives no file artifacts to
	// write.
	ErrNoArtifacts = errors.New("greenfield: no file artifacts to write")
	// ErrDuplicateArtifactID is returned when two artifacts resolve to the
	// same graph node ID.
	ErrDuplicateArtifactID = errors.New("greenfield: duplicate artifact ID")
)

// Compile-time assertion that GreenfieldPlanner satisfies planner.Planner.
var _ planner.Planner = (*GreenfieldPlanner)(nil)

// GreenfieldPlanner synthesizes an entire workspace in one shot. It is
// deterministic: the same artifact list always yields the same graph.
type GreenfieldPlanner struct {
	workspaceRoot string
	mode          fs.FileMode
	timeout       time.Duration
	nodePrefix    string
	tx            *txfs.TxFS
}

// Option configures a GreenfieldPlanner.
type Option func(*GreenfieldPlanner)

// WithFileMode overrides the permission bits applied to written files.
func WithFileMode(mode fs.FileMode) Option {
	return func(p *GreenfieldPlanner) { p.mode = mode }
}

// WithTimeout bounds the execution of every write operation.
func WithTimeout(timeout time.Duration) Option {
	return func(p *GreenfieldPlanner) { p.timeout = timeout }
}

// WithNodePrefix overrides the node ID prefix used for write nodes.
func WithNodePrefix(prefix string) Option {
	return func(p *GreenfieldPlanner) {
		if prefix != "" {
			p.nodePrefix = prefix
		}
	}
}

// WithTxFS binds an active transactional file system. When set, write
// operations stage through the transaction and reach the workspace atomically
// at Commit; Rollback restores it to a pristine state. The transaction must
// span the graph's execution (Begin before, Commit or Rollback after).
func WithTxFS(tx *txfs.TxFS) Option {
	return func(p *GreenfieldPlanner) {
		if tx != nil {
			p.tx = tx
		}
	}
}

// NewGreenfieldPlanner returns a planner that writes artifacts into
// workspaceRoot.
func NewGreenfieldPlanner(workspaceRoot string, opts ...Option) *GreenfieldPlanner {
	p := &GreenfieldPlanner{
		workspaceRoot: workspaceRoot,
		mode:          defaultFileMode,
		nodePrefix:    defaultNodePrefix,
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// Plan implements planner.Planner. It lowers every writable file artifact
// into an op.OpWriteFile node and returns a graph with zero tool-call
// overhead. Every write is a Direct Full-File Overwrite of the artifact's
// content: the planner never parses or applies SEARCH/REPLACE diffs against
// existing files, so obsolete workspace code cannot anchor the model or
// trigger patch-thrashing repair loops. Non-file artifacts are retained in
// the result but produce no node.
func (p *GreenfieldPlanner) Plan(ctx context.Context, intent string, artifacts []ir.Artifact) (*planner.PlanResult, error) {
	if p == nil {
		return nil, errors.New("greenfield: nil receiver")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p.workspaceRoot == "" {
		return nil, ErrEmptyWorkspaceRoot
	}
	return p.build(intent, artifacts)
}

// build assigns a deterministic node ID to every artifact, then lowers each
// writable file artifact into an op.OpWriteFile node. Declared depends_on
// metadata is mapped onto graph preconditions so the graph stays topological;
// independent artifacts remain fully parallel.
func (p *GreenfieldPlanner) build(intent string, artifacts []ir.Artifact) (*planner.PlanResult, error) {
	nodeIDs := make(map[string]string, len(artifacts))
	writable := make(map[string]string, len(artifacts))
	for i, a := range artifacts {
		id := artifactID(a)
		if _, dup := nodeIDs[id]; dup {
			return nil, fmt.Errorf("%w: %q", ErrDuplicateArtifactID, id)
		}
		nodeID := p.nodeID(id, i)
		nodeIDs[id] = nodeID
		if a.Kind == ir.ArtifactFile {
			writable[id] = nodeID
		}
	}

	g := graph.NewExecutionGraph()
	planned := 0
	for _, a := range artifacts {
		if a.Kind != ir.ArtifactFile {
			continue
		}
		res, err := p.fileResource(a)
		if err != nil {
			return nil, fmt.Errorf("greenfield: target %q: %w", a.Path, err)
		}
		operation, err := op.NewOperation(
			nodeIDs[artifactID(a)],
			op.OpWriteFile,
			res,
			a,
			p.preconditions(a, writable),
			p.timeout,
		)
		if err != nil {
			return nil, fmt.Errorf("greenfield: operation for %q: %w", a.Path, err)
		}
		node, err := graph.NewOpNode(operation)
		if err != nil {
			return nil, fmt.Errorf("greenfield: node for %q: %w", a.Path, err)
		}
		if err := g.AddNode(node); err != nil {
			return nil, fmt.Errorf("greenfield: add node for %q: %w", a.Path, err)
		}
		planned++
	}
	if planned == 0 {
		return nil, ErrNoArtifacts
	}

	return &planner.PlanResult{
		Graph:     g,
		Artifacts: append([]ir.Artifact(nil), artifacts...),
		Metadata: map[string]string{
			"planner":        "greenfield",
			"intent":         intent,
			"strategy":       "one-shot",
			"overwrite":      "full-content",
			"diff":           "none",
			"roundtrips":     "0",
			"node_count":     strconv.Itoa(planned),
			"artifact_count": strconv.Itoa(len(artifacts)),
		},
	}, nil
}

// fileResource lowers an artifact into a resource.Resource. When a TxFS is
// bound, the file write is staged transactionally so the workspace stays
// pristine until Commit.
func (p *GreenfieldPlanner) fileResource(a ir.Artifact) (resource.Resource, error) {
	base, err := file.NewFileResource(p.workspaceRoot, a.Path, p.mode)
	if err != nil {
		return nil, err
	}
	if p.tx == nil {
		return base, nil
	}
	return txfs.NewTxResource(base, p.tx)
}

// preconditions resolves the artifact's depends_on metadata to writable graph
// node IDs. The result is sorted for determinism.
func (p *GreenfieldPlanner) preconditions(a ir.Artifact, writable map[string]string) []string {
	deps := strings.Split(a.Metadata[DependsOnKey], ",")
	seen := make(map[string]bool, len(deps))
	out := make([]string, 0, len(deps))
	for _, dep := range deps {
		dep = strings.TrimSpace(dep)
		if dep == "" {
			continue
		}
		nodeID, ok := writable[dep]
		if !ok || seen[nodeID] {
			continue
		}
		seen[nodeID] = true
		out = append(out, nodeID)
	}
	sort.Strings(out)
	return out
}

// nodeID derives the deterministic graph node ID for an artifact. The ID falls
// back to the artifact position when an artifact carries neither ID nor path.
func (p *GreenfieldPlanner) nodeID(id string, index int) string {
	if id != "" {
		return p.nodePrefix + ":" + id
	}
	return p.nodePrefix + ":" + strconv.Itoa(index)
}

// artifactID resolves the canonical identity of an artifact, defaulting to its
// path.
func artifactID(a ir.Artifact) string {
	if a.ID != "" {
		return a.ID
	}
	return a.Path
}
