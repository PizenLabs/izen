// Package debug is the on-demand inspection capability (Debug Capability) for
// the Phase 1-4 engines. It materializes diagnostic snapshots of the Lea
// structural engine, the Context Governance planner, and the Output Pipeline
// only when requested — none of the engines carry continuous runtime overhead
// for this feature. The package is consumed by CLI commands such as
// `izen debug`.
package debug

import (
	"fmt"
	"io"

	"github.com/PizenLabs/izen/internal/lea"
	"github.com/PizenLabs/izen/internal/planner"
	"github.com/PizenLabs/izen/internal/runtime/output"
)

// Report aggregates the on-demand diagnostic snapshots of the engines. A
// zero-valued section marks an engine that was not inspected (or not
// available in the current context).
type Report struct {
	Engine     lea.DebugInfo
	Governance planner.DebugInfo
	Output     output.DebugInfo
	Logs       output.WorkspaceInspection
}

// NewReport assembles a Report from the inspected engines. A nil engine or nil
// plan yields a zero-valued section; res and logs are taken as-is.
func NewReport(eng *lea.Engine, plan *planner.ContextPlan, res output.DebugInfo, logs output.WorkspaceInspection) Report {
	r := Report{Output: res, Logs: logs}
	if eng != nil {
		r.Engine = eng.Debug()
	}
	if plan != nil {
		r.Governance = plan.Debug()
	}
	return r
}

// Render writes the report as aligned, human-readable text.
func (r Report) Render(w io.Writer) error {
	if w == nil {
		return fmt.Errorf("debug: nil writer")
	}

	if _, err := fmt.Fprintln(w, "IZEN DEBUG REPORT"); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, "================="); err != nil {
		return err
	}

	if err := r.renderEngine(w); err != nil {
		return err
	}
	if err := r.renderGovernance(w); err != nil {
		return err
	}
	return r.renderOutput(w)
}

func (r Report) renderEngine(w io.Writer) error {
	e := r.Engine
	if _, err := fmt.Fprintln(w, "\n[LEA STRUCTURAL ENGINE]"); err != nil {
		return err
	}
	if !e.Indexed() {
		_, err := fmt.Fprintln(w, "  not indexed")
		return err
	}
	if _, err := fmt.Fprintf(w, "  root:           %s\n", e.Root); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "  cache:          %s (version %d)\n", e.CachePath, e.CacheVersion); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "  files indexed:  %d\n", e.FilesIndexed); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "  symbols:        %d\n", e.Symbols); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "  nodes / edges:  %d / %d\n", e.Nodes, e.Edges); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "  routes / calls: %d / %d\n", e.Routes, e.CallEdges); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "  last index:     %s (cache=%v incremental=%v)\n",
		e.LastIndexDuration.Round(0), e.FromCache, e.Incremental); err != nil {
		return err
	}
	if !e.LastIndexedAt.IsZero() {
		_, err := fmt.Fprintf(w, "  indexed at:     %s\n", e.LastIndexedAt.Format("2006-01-02 15:04:05"))
		return err
	}
	return nil
}

func (r Report) renderGovernance(w io.Writer) error {
	g := r.Governance
	if _, err := fmt.Fprintln(w, "\n[CONTEXT GOVERNANCE]"); err != nil {
		return err
	}
	if g.AllocatedTokens == 0 {
		_, err := fmt.Fprintln(w, "  no planning run recorded")
		return err
	}
	if _, err := fmt.Fprintf(w, "  intent:          %s\n", g.Intent); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "  allocated:       %d tokens\n", g.AllocatedTokens); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "  retrieved:       %d chunks\n", g.RetrievedChunks); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "  fitted:          %d chunks\n", g.FittedChunks); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "  dropped:         %d chunks\n", g.DroppedChunks); err != nil {
		return err
	}
	_, err := fmt.Fprintf(w, "  used:            %d tokens\n", g.UsedTokens)
	return err
}

func (r Report) renderOutput(w io.Writer) error {
	o := r.Output
	if _, err := fmt.Fprintln(w, "\n[OUTPUT PIPELINE]"); err != nil {
		return err
	}
	if o.OriginalChars > 0 {
		if _, err := fmt.Fprintf(w, "  compression:     %.1f%% (%d -> %d chars)\n",
			o.CompressionRatioPct, o.OriginalChars, o.CompressedChars); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "  chars saved:     %d\n", o.CharsSaved); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "  token bytes:     %d (~4 chars/token)\n", o.TokenBytesSaved); err != nil {
			return err
		}
		if o.Tool != "" {
			if _, err := fmt.Fprintf(w, "  tool type:       %s\n", o.Tool); err != nil {
				return err
			}
		}
	} else {
		if _, err := fmt.Fprintln(w, "  no execution recorded"); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(w, "  log dir:         %s\n", r.Logs.LogDir); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "  log files:       %d\n", r.Logs.LogCount); err != nil {
		return err
	}
	if r.Logs.LastLog != "" {
		_, err := fmt.Fprintf(w, "  last log:        %s\n", r.Logs.LastLog)
		return err
	}
	_, err := fmt.Fprintln(w, "  last log:        (none)")
	return err
}
