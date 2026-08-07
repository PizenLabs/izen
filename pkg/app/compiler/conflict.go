package compiler

import (
	"fmt"
	"sort"
	"strings"

	"github.com/PizenLabs/izen/pkg/knowledge"
)

// WorkspaceState summarises what the compiler can observe about the
// workspace before planning. It distinguishes application-level target types
// (todo_app, portfolio, ...) from technical archetype markers (vanilla web,
// Go, React) so the conflict logic reasons about intent-relevant types only.
type WorkspaceState struct {
	// Root is the workspace directory the state was derived from.
	Root string
	// Empty reports whether the workspace holds no recognisable content.
	Empty bool
	// FileCount is the number of files scanned.
	FileCount int
	// AppTypes maps every application-level target type detected on disk.
	AppTypes map[string]bool
	// Archetypes maps every technical marker detected on disk.
	Archetypes map[string]bool
	// Markers are the human-readable markers that fired, in discovery order.
	Markers []string
}

// Conflict records a detected mismatch between the resolved intent and the
// existing workspace state.
type Conflict struct {
	// Present reports whether a high-impact mismatch exists.
	Present bool
	// Reason is the machine-readable trigger.
	Reason string
	// Requested is the target type the prompt asks for.
	Requested string
	// Detected are the application-level target types found on disk.
	Detected []string
}

// ConflictDetector scans a workspace for target-type markers and compares
// them against the resolved intent.
type ConflictDetector struct {
	// knowledge is the optional shared RuntimeKnowledge graph. When set,
	// Detect serves the cached workspace state instead of re-walking the
	// disk, so repeated compiles over the same root cost no I/O.
	knowledge *knowledge.KnowledgeGraph
}

// NewConflictDetector builds a ConflictDetector. It scans the disk on demand;
// attach a shared knowledge.KnowledgeGraph via SetKnowledge to cache scans
// across calls.
func NewConflictDetector() *ConflictDetector {
	return &ConflictDetector{}
}

// SetKnowledge binds the detector to a shared RuntimeKnowledge graph. When
// set, Detect reads the graph's cached workspace state instead of performing
// a fresh disk sweep. A nil argument detaches the graph.
func (c *ConflictDetector) SetKnowledge(kg *knowledge.KnowledgeGraph) {
	c.knowledge = kg
}

// Detect returns the observable workspace state for root. With an attached
// KnowledgeGraph it is served from memory (scanning the root once on first
// use); without one, a transient graph is scanned on the fly so standalone
// use behaves identically.
func (c *ConflictDetector) Detect(root string) WorkspaceState {
	var snap knowledge.WorkspaceState
	if c.knowledge != nil {
		snap = c.knowledge.Ensure(root)
	} else {
		transient := knowledge.NewKnowledgeGraph()
		snap = transient.Ensure(root)
	}
	return WorkspaceState{
		Root:       snap.Root,
		Empty:      snap.Empty,
		FileCount:  snap.FileCount,
		AppTypes:   copyBoolSet(snap.AppTypes),
		Archetypes: copyBoolSet(snap.Archetypes),
		Markers:    append([]string(nil), snap.Markers...),
	}
}

// copyBoolSet returns a defensive copy of src.
func copyBoolSet(src map[string]bool) map[string]bool {
	if len(src) == 0 {
		return make(map[string]bool)
	}
	out := make(map[string]bool, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

// Process compares the resolved intent against the observed workspace state
// and reports a Conflict when a high-impact mismatch exists. Conflicts arise
// when (1) the prompt explicitly excludes a target that the workspace is
// currently built as, or (2) the prompt targets a type that differs from an
// established application type on disk.
func (c *ConflictDetector) Process(res *Resolution, ws WorkspaceState) Conflict {
	if ws.Empty || len(ws.AppTypes) == 0 {
		return Conflict{}
	}

	detected := SortedKeys(ws.AppTypes)
	conf := Conflict{Requested: res.TargetType, Detected: detected}

	// Explicit negation wins: the user said "not X" while the workspace is X.
	for negated := range res.Negated {
		if ws.AppTypes[negated] {
			conf.Present = true
			conf.Reason = fmt.Sprintf("intent explicitly excludes %s but the workspace is a %s workspace", negated, negated)
			return conf
		}
	}

	// Target mismatch: the requested type differs from every established one.
	if res.TargetType != "" && !ws.AppTypes[res.TargetType] {
		conf.Present = true
		conf.Reason = fmt.Sprintf("requested %s over an existing %s workspace", res.TargetType, strings.Join(detected, ", "))
		return conf
	}
	return conf
}

// SortedKeys returns the true-valued keys of set in stable sorted order.
func SortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k, on := range set {
		if on {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}
