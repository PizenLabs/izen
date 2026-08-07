package app

import (
	"fmt"
	"strings"

	"github.com/PizenLabs/izen/pkg/dag"
	txfs "github.com/PizenLabs/izen/pkg/fs"
	"github.com/PizenLabs/izen/pkg/ir"
	"github.com/PizenLabs/izen/pkg/planner/greenfield"
)

// planExecutionOrder builds a dag.Graph over the plan's file artifacts, wires
// the inter-file depends_on edges, and returns the nodes in deterministic
// topological order. A circular dependency is rejected as a
// *dag.CyclicDependencyError before any file is touched, so a broken plan can
// never reach the workspace.
func planExecutionOrder(artifacts []ir.Artifact) ([]*dag.Node, error) {
	g := dag.NewGraph()
	for _, a := range artifacts {
		if a.Kind != ir.ArtifactFile {
			continue
		}
		if err := g.AddNodeWithPayload(a.ID, a); err != nil {
			return nil, fmt.Errorf("app: add execution node %q: %w", a.ID, err)
		}
	}
	for _, a := range artifacts {
		if a.Kind != ir.ArtifactFile {
			continue
		}
		for _, dep := range strings.Split(a.Metadata[greenfield.DependsOnKey], ",") {
			dep = strings.TrimSpace(dep)
			if dep == "" {
				continue
			}
			if !g.Has(dep) {
				continue
			}
			if err := g.AddEdge(dep, a.ID); err != nil {
				return nil, fmt.Errorf("app: add dependency edge %q -> %q: %w", dep, a.ID, err)
			}
		}
	}
	order, err := g.TopoSortNodes()
	if err != nil {
		return nil, err
	}
	return order, nil
}

// restartTx discards any staged state and reopens the transaction so a repair
// round always resumes with a clean, active transaction. It is invoked
// immediately before re-entering the generation/validation repair loop so a
// rejected output can never leak into the committed workspace.
func (p *Pipeline) restartTx() error {
	if p.tx == nil {
		p.tx = txfs.NewTxFS(p.root)
	}
	_ = p.tx.Rollback()
	if err := p.tx.Begin(); err != nil {
		return fmt.Errorf("app: restart transaction: %w", err)
	}
	return nil
}
