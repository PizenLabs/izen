package autonomy

import (
	"fmt"
	"strconv"
	"strings"
)

// Region is a bounded, labeled span of an artifact the AI may reason over. The
// runtime hands the model regions — never whole files.
type Region struct {
	File  string `json:"file"`
	Start int    `json:"start"`
	End   int    `json:"end"`
	Label string `json:"label,omitempty"`
}

// RegionsFor derives the relevant code regions from the compiled affected
// scope. It is a pure projection: regions point into the source and carry the
// symbol label so the model knows WHAT the region is without re-discovering it.
func (a ArtifactContext) RegionsFor(content string) []Region {
	if a.Code == nil || len(a.Code.AffectedScope) == 0 {
		return nil
	}
	regions := make([]Region, 0, len(a.Code.AffectedScope))
	for _, scope := range a.Code.AffectedScope {
		idx := strings.LastIndex(scope, ":")
		if idx < 0 {
			continue
		}
		label := scope[:idx]
		start, end := parseScopeRange(scope[idx+1:])
		if start < 1 {
			continue
		}
		regions = append(regions, Region{File: a.Path, Start: start, End: end, Label: label})
	}
	return regions
}

func parseScopeRange(s string) (start, end int) {
	parts := strings.SplitN(s, "-", 2)
	start, _ = strconv.Atoi(parts[0])
	end = start
	if len(parts) == 2 {
		if e, err := strconv.Atoi(parts[1]); err == nil {
			end = e
		}
	}
	return start, end
}

// RegionFor returns a single bounded region around a target line.
func (a ArtifactContext) RegionFor(line, pad int) Region {
	if line < 1 {
		line = 1
	}
	if pad < 0 {
		pad = 0
	}
	start := line - pad
	if start < 1 {
		start = 1
	}
	return Region{File: a.Path, Start: start, End: line + pad, Label: "target"}
}

// RegionContent extracts the bounded slice of an artifact for a region, with
// line numbers, so the model always sees location-aware context.
func (a ArtifactContext) RegionContent(content string, r Region) string {
	lines := strings.Split(content, "\n")
	if r.Start < 1 {
		r.Start = 1
	}
	if r.End > len(lines) {
		r.End = len(lines)
	}
	if r.Start > r.End {
		return ""
	}
	var b strings.Builder
	for i := r.Start; i <= r.End; i++ {
		fmt.Fprintf(&b, "%d: %s\n", i, lines[i-1])
	}
	return strings.TrimRight(b.String(), "\n")
}

// FormatAIHandoff renders the deterministic handoff the AI may reason over:
// the user intent, the structural evidence ledger, and the bounded relevant
// regions. It NEVER sends the entire file blindly: content beyond the compiled
// regions is excluded and the total payload is capped by budget (bytes; ~4
// bytes per token).
func (a ArtifactContext) FormatAIHandoff(intent, content string, budget int) string {
	if budget <= 0 {
		budget = 4096
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Intent: %s\n", intent)
	b.WriteString(a.FormatEvidenceLedger())
	b.WriteString("\n")

	regions := a.Regions
	if len(regions) == 0 {
		regions = a.RegionsFor(content)
	}
	if len(regions) == 0 {
		b.WriteString("Relevant regions: none identified\n")
		return strings.TrimSpace(b.String())
	}
	b.WriteString("Relevant regions:\n")
	for _, r := range regions {
		blk := a.RegionContent(content, r)
		if blk == "" {
			continue
		}
		head := fmt.Sprintf("--- %s (%s:%d-%d) ---\n", r.Label, r.File, r.Start, r.End)
		if b.Len()+len(head)+len(blk)+1 > budget {
			b.WriteString("* ... further regions omitted by budget\n")
			break
		}
		b.WriteString(head)
		b.WriteString(blk)
		b.WriteString("\n")
	}
	out := strings.TrimSpace(b.String())
	if len(out) > budget {
		// Hard ceiling: the header/ledger alone exceeded the budget. Cap the
		// payload so the handoff NEVER violates its byte contract.
		return out[:budget-1] + "…"
	}
	return out
}
