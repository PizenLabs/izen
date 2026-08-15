package strategy

import (
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
)

// ── PHASE 11 — EXECUTION GRAPH ─────────────────────────────────────────────
//
// ExecutionGraph is the compiled, explicit description of WHAT Izen intends
// to execute for one operation. It is produced by Compile from an
// ExecutionStrategyProfile BEFORE any model invocation: the model never
// invents graph topology. It is a pure, deterministic structure over a bounded
// set of typed execution nodes (resolve_target, read_target, gather_evidence,
// reason, propose, approve, mutate, verify, clarify).
//
// Properties:
//
//   - explicit: every node is a first-class, typed execution step.
//   - inspectable: String() renders the full node sequence + states.
//   - deterministic in ordering: nodes are appended in compile-time order and
//     never reordered by map iteration.
//   - terminal-aware: every node and the graph have explicit terminal states.
//   - cancellation-aware: the graph can be driven to a terminal cancelled
//     state; a terminal graph never re-enters a non-terminal state.
//   - evidence-producing: every completion/failure carries the evidence it
//     produced (a located block, an artifact, an apply result).
//   - MutationSet-compatible: the graph carries the deduplicated target set
//     the owning MutationSet records, one mutation per node.
//   - ExecutionProof-compatible: the graph's terminal state and node evidence
//     fold directly into the operation proof.
//
// The graph performs no I/O and no provider work itself. The runtime drives
// node states through the graph lifecycle; the owning MutationSet remains the
// single transaction boundary.

// NodeKind is the bounded set of typed execution nodes. Adding a kind is an
// architectural decision, not a patch.
type NodeKind string

const (
	// NodeResolveTarget deterministically resolves the requested target set.
	NodeResolveTarget NodeKind = "resolve_target"
	// NodeReadTarget reads the bounded target content required for the node.
	NodeReadTarget NodeKind = "read_target"
	// NodeGatherEvidence discovers repository evidence (structural,
	// dependency) — the ONLY node allowed to touch repository scope.
	NodeGatherEvidence NodeKind = "gather_evidence"
	// NodeReason is the bounded model-reasoning step (when required). It is
	// the only node that legitimately invokes the model for a mutation.
	NodeReason NodeKind = "reason"
	// NodePropose deterministically compiles the model artifact into a
	// validated proposal.
	NodePropose NodeKind = "propose"
	// NodeApprove is the human approval boundary. Approval is a semantic
	// decision point; it is never silently converted into authorization.
	NodeApprove NodeKind = "approve"
	// NodeMutate applies one bounded mutation to one target inside the owning
	// MutationSet.
	NodeMutate NodeKind = "mutate"
	// NodeVerify runs the deterministic verification gate after mutation.
	NodeVerify NodeKind = "verify"
	// NodeClarify stops execution at a human clarification boundary. It is the
	// escalation outcome for an ambiguous/unresolved decision.
	NodeClarify NodeKind = "clarify"
)

// Label returns the compact machine label of the kind.
func (k NodeKind) Label() string { return string(k) }

// HumanBoundary reports whether the node is a human decision boundary.
func (k NodeKind) HumanBoundary() bool {
	return k == NodeApprove || k == NodeClarify
}

// EvidenceBoundary reports whether the node reads repository scope. Every other
// node is confined to explicit targets.
func (k NodeKind) EvidenceBoundary() bool { return k == NodeGatherEvidence }

// NodeState is the lifecycle state of one execution node.
type NodeState string

const (
	NodePending   NodeState = "pending"   // compiled, not started
	NodeRunning   NodeState = "running"   // executing
	NodeComplete  NodeState = "complete"  // produced its evidence
	NodeSkipped   NodeState = "skipped"   // cleanly unnecessary
	NodeWaiting   NodeState = "waiting"   // human checkpoint (approve/clarify)
	NodeFailed    NodeState = "failed"    // deterministic failure
	NodeCancelled NodeState = "cancelled" // cancelled by the runtime
)

// Terminal reports whether the state is terminal. A terminal node is never
// re-entered.
func (s NodeState) Terminal() bool {
	switch s {
	case NodeComplete, NodeSkipped, NodeFailed, NodeCancelled:
		return true
	}
	return false
}

// Succeeded reports whether the node provably produced its intended outcome.
func (s NodeState) Succeeded() bool { return s == NodeComplete || s == NodeSkipped }

// GraphState is the lifecycle state of the whole execution graph.
type GraphState string

const (
	GraphPending       GraphState = "pending"        // compiled, not started
	GraphRunning       GraphState = "running"        // executing
	GraphComplete      GraphState = "complete"       // terminal: intended end reached
	GraphAwaitingHuman GraphState = "awaiting_human" // paused at a human boundary (approve/clarify)
	GraphFailed        GraphState = "failed"         // terminal: deterministic failure
	GraphCancelled     GraphState = "cancelled"      // terminal: cancelled
)

// Terminal reports whether the graph reached a terminal state. A human
// boundary (awaiting_human) is a PAUSE, not a terminal: the runtime resumes it
// with Complete (human approved / clarification delivered), Fail (rejected) or
// Cancel — it is never silently re-entered.
func (s GraphState) Terminal() bool {
	switch s {
	case GraphComplete, GraphFailed, GraphCancelled:
		return true
	}
	return false
}

// StrategyNode is one typed execution node of the graph.
type StrategyNode struct {
	// ID is the stable ordinal identity within the graph ("n1", "n2", ...).
	ID string
	// Kind is the typed execution step.
	Kind NodeKind
	// State is the node lifecycle state.
	State NodeState
	// Target is the resolved target this node is scoped to ("" for
	// whole-operation nodes).
	Target string
	// Evidence is what the node produced when it reached a terminal state
	// (a located block, an artifact marker, an apply outcome).
	Evidence string
	// RequiresModel reports whether this node legitimately invokes the model.
	RequiresModel bool
	// Invocation is the 1-based model-invocation ordinal of this node (0 when
	// the node performs no provider call).
	Invocation int
	// Contract is the compact InvocationContract of the node's model call
	// ("" for deterministic nodes).
	Contract string
}

// graphCounter produces monotonic graph IDs.
var graphCounter atomic.Uint64

// ExecutionGraph is the compiled execution description of one operation.
type ExecutionGraph struct {
	// ID is the stable operation identity of the graph.
	ID string
	// Strategy is the execution strategy the graph was compiled from.
	Strategy ExecutionStrategy
	// State is the graph lifecycle state.
	State GraphState
	// Nodes is the deterministic, compile-ordered node sequence.
	Nodes []*StrategyNode
	// Escalations is the evidence-driven escalation history of the graph.
	Escalations []EscalationRecord
	// ExpectedInvocations is the model-invocation count the strategy justifies
	// (0 = none; bounded strategies count their mandatory reasoning call).
	ExpectedInvocations int
	// MutationTargets is the deduplicated target set the owning MutationSet
	// records — one user mutation maps to one MutationSet boundary.
	MutationTargets []string
	// ContextKinds is the minimum-sufficient context channel set the operation
	// requires (the compiled profile's context decision).
	ContextKinds []ContextKind
}

// NewExecutionGraph constructs an empty graph with the given strategy and a
// fresh identity. Compile is the only sanctioned constructor for compiled
// graphs; this exists for tests and recorders that build graphs incrementally.
func NewExecutionGraph(id string, s ExecutionStrategy) *ExecutionGraph {
	if id == "" {
		id = fmt.Sprintf("eg-%d", graphCounter.Add(1))
	}
	return &ExecutionGraph{ID: id, Strategy: s, State: GraphPending}
}

// Add appends a typed node in compile order and returns it.
func (g *ExecutionGraph) Add(kind NodeKind, target string, requiresModel bool, invocation int, contract string) *StrategyNode {
	if g == nil {
		return nil
	}
	n := &StrategyNode{
		ID:            fmt.Sprintf("n%d", len(g.Nodes)+1),
		Kind:          kind,
		State:         NodePending,
		Target:        target,
		RequiresModel: requiresModel,
		Invocation:    invocation,
		Contract:      contract,
	}
	g.Nodes = append(g.Nodes, n)
	return n
}

// Node returns the node with the given ordinal ID, or nil.
func (g *ExecutionGraph) Node(id string) *StrategyNode {
	if g == nil {
		return nil
	}
	for _, n := range g.Nodes {
		if n.ID == id {
			return n
		}
	}
	return nil
}

// First returns the first node of the given kind, or nil.
func (g *ExecutionGraph) First(kind NodeKind) *StrategyNode {
	if g == nil {
		return nil
	}
	for _, n := range g.Nodes {
		if n.Kind == kind {
			return n
		}
	}
	return nil
}

// All returns every node of the given kind in compile order.
func (g *ExecutionGraph) All(kind NodeKind) []*StrategyNode {
	if g == nil {
		return nil
	}
	var out []*StrategyNode
	for _, n := range g.Nodes {
		if n.Kind == kind {
			out = append(out, n)
		}
	}
	return out
}

// Has reports whether the graph contains a node of the given kind.
func (g *ExecutionGraph) Has(kind NodeKind) bool { return g != nil && g.First(kind) != nil }

// NodeCount returns the number of compiled nodes.
func (g *ExecutionGraph) NodeCount() int {
	if g == nil {
		return 0
	}
	return len(g.Nodes)
}

// ModelNodeCount returns the number of nodes that legitimately invoke the
// model. This is the maximum provider-invocation count the strategy justifies.
func (g *ExecutionGraph) ModelNodeCount() int {
	if g == nil {
		return 0
	}
	n := 0
	for _, node := range g.Nodes {
		if node.RequiresModel {
			n++
		}
	}
	return n
}

// MutationNodeCount returns the number of mutate nodes.
func (g *ExecutionGraph) MutationNodeCount() int {
	if g == nil {
		return 0
	}
	return len(g.All(NodeMutate))
}

// Targets returns the MutationSet-compatible deduplicated target set in
// compile order.
func (g *ExecutionGraph) Targets() []string {
	if g == nil {
		return nil
	}
	out := make([]string, 0, len(g.MutationTargets))
	seen := map[string]bool{}
	for _, t := range g.MutationTargets {
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}

// Depth returns the number of nodes in the longest compile-order prefix —
// the maximum execution depth of the graph (all compiled topologies are
// linear chains, so this equals the node count).
func (g *ExecutionGraph) Depth() int {
	if g == nil {
		return 0
	}
	return len(g.Nodes)
}

// Start transitions the graph into running. A terminal graph never restarts.
func (g *ExecutionGraph) Start() {
	if g == nil || g.State.Terminal() {
		return
	}
	g.State = GraphRunning
}

// Terminal reports whether the graph reached a terminal state.
func (g *ExecutionGraph) Terminal() bool { return g != nil && g.State.Terminal() }

// Succeeded reports whether the graph reached its intended terminal end.
func (g *ExecutionGraph) Succeeded() bool { return g != nil && g.State == GraphComplete }

// AwaitingHuman reports whether the graph stopped at a human boundary.
func (g *ExecutionGraph) AwaitingHuman() bool {
	return g != nil && g.State == GraphAwaitingHuman
}

// Begin marks a node as running. A terminal node is never re-entered; a
// terminal graph never resumes.
func (g *ExecutionGraph) Begin(id string) {
	if g == nil || g.State.Terminal() {
		return
	}
	if n := g.Node(id); n != nil && !n.State.Terminal() {
		n.State = NodeRunning
	}
}

// Complete marks a node complete with the evidence it produced. The graph
// becomes complete only when every node is terminal and none failed.
func (g *ExecutionGraph) Complete(id, evidence string) {
	if g == nil {
		return
	}
	n := g.Node(id)
	if n == nil || n.State.Terminal() {
		return
	}
	n.State = NodeComplete
	n.Evidence = evidence
	g.reconcile()
}

// Skip marks a node as cleanly unnecessary.
func (g *ExecutionGraph) Skip(id, reason string) {
	if g == nil {
		return
	}
	n := g.Node(id)
	if n == nil || n.State.Terminal() {
		return
	}
	n.State = NodeSkipped
	n.Evidence = reason
	g.reconcile()
}

// Wait parks the graph at a human decision boundary (approve / clarify). The
// graph pauses in the awaiting_human state; the runtime resumes it with
// Complete (approved / clarification delivered), Fail (rejected) or Cancel —
// it is never silently re-entered and never restarts the operation.
func (g *ExecutionGraph) Wait(id, reason string) {
	if g == nil || g.State.Terminal() {
		return
	}
	if n := g.Node(id); n != nil && !n.State.Terminal() {
		n.State = NodeWaiting
		n.Evidence = reason
	}
	g.State = GraphAwaitingHuman
}

// Fail marks a node failed with the evidence of its failure. The graph reaches
// the terminal failed state; no later node may run.
func (g *ExecutionGraph) Fail(id, evidence string) {
	if g == nil || g.State.Terminal() {
		return
	}
	if n := g.Node(id); n != nil && !n.State.Terminal() {
		n.State = NodeFailed
		n.Evidence = evidence
	}
	g.State = GraphFailed
}

// Cancel drives the graph to the terminal cancelled state. It is a no-op on an
// already-terminal graph — a committed/completed execution can never be
// cancelled.
func (g *ExecutionGraph) Cancel(reason string) {
	if g == nil || g.State.Terminal() {
		return
	}
	for _, n := range g.Nodes {
		if !n.State.Terminal() {
			n.State = NodeCancelled
			if reason != "" {
				n.Evidence = reason
			}
		}
	}
	g.State = GraphCancelled
}

// reconcile advances the graph to complete when every node reached a terminal
// state and none failed.
func (g *ExecutionGraph) reconcile() {
	if g == nil || g.State.Terminal() {
		return
	}
	for _, n := range g.Nodes {
		if !n.State.Terminal() {
			return
		}
	}
	g.State = GraphComplete
}

// RecordEscalation appends an evidence-driven escalation record.
func (g *ExecutionGraph) RecordEscalation(r EscalationRecord) {
	if g == nil {
		return
	}
	g.Escalations = append(g.Escalations, r)
}

// EscalationCount returns the number of recorded escalations.
func (g *ExecutionGraph) EscalationCount() int {
	if g == nil {
		return 0
	}
	return len(g.Escalations)
}

// FailedNode returns the first failed node, or nil.
func (g *ExecutionGraph) FailedNode() *StrategyNode {
	if g == nil {
		return nil
	}
	for _, n := range g.Nodes {
		if n.State == NodeFailed {
			return n
		}
	}
	return nil
}

// AllTerminal reports whether every node reached a terminal state.
func (g *ExecutionGraph) AllTerminal() bool {
	if g == nil || len(g.Nodes) == 0 {
		return false
	}
	for _, n := range g.Nodes {
		if !n.State.Terminal() {
			return false
		}
	}
	return true
}

// Validate runs the deterministic structural validation the compiled graph
// must pass before execution: every mutate node maps to a MutationTarget and
// every model node carries an explicit InvocationContract.
func (g *ExecutionGraph) Validate() error {
	if g == nil {
		return fmt.Errorf("graph: nil graph")
	}
	if len(g.Nodes) == 0 {
		return fmt.Errorf("graph: no execution nodes")
	}
	targets := map[string]bool{}
	for _, t := range g.MutationTargets {
		targets[t] = true
	}
	for _, n := range g.Nodes {
		if n.Kind == NodeMutate && n.Target != "" && !targets[n.Target] {
			return fmt.Errorf("graph: mutate node %s targets %q which is not in the MutationSet target set", n.ID, n.Target)
		}
		if n.RequiresModel && n.Invocation <= 0 {
			return fmt.Errorf("graph: model node %s (%s) has no invocation ordinal", n.ID, n.Kind)
		}
		if n.RequiresModel && n.Contract == "" {
			return fmt.Errorf("graph: model node %s (%s) has no InvocationContract", n.ID, n.Kind)
		}
		if !n.RequiresModel && n.Invocation > 0 {
			return fmt.Errorf("graph: deterministic node %s (%s) claims invocation %d", n.ID, n.Kind, n.Invocation)
		}
	}
	return nil
}

// String renders the graph as a compact, deterministic, inspectable record:
// strategy, state, expected invocations, node sequence with states and
// evidence, and the escalation history. It carries execution facts only.
func (g *ExecutionGraph) String() string {
	if g == nil {
		return "execution-graph: <nil>"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "execution-graph: strategy=%s state=%s nodes=%d expected-invocations=%d",
		g.Strategy, g.State, len(g.Nodes), g.ExpectedInvocations)
	if len(g.MutationTargets) > 0 {
		fmt.Fprintf(&b, " mutation-targets=%s", strings.Join(g.MutationTargets, ","))
	}
	if len(g.ContextKinds) > 0 {
		b.WriteString(" context=" + contextKindsString(g.ContextKinds))
	}
	for _, n := range g.Nodes {
		fmt.Fprintf(&b, "\n  %s %s: %s", n.ID, n.Kind.Label(), n.State)
		if n.Target != "" {
			b.WriteString(" " + n.Target)
		}
		if n.RequiresModel {
			fmt.Fprintf(&b, " model=yes invocation#%d", n.Invocation)
		}
		if n.Kind.HumanBoundary() {
			b.WriteString(" human")
		}
		if n.Evidence != "" {
			b.WriteString(" — " + n.Evidence)
		}
	}
	if len(g.Escalations) > 0 {
		fmt.Fprintf(&b, "\n  escalations=%d", len(g.Escalations))
		for _, e := range g.Escalations {
			b.WriteString("\n    " + e.String())
		}
	}
	return b.String()
}

// Metrics is the compact observability summary of a compiled execution graph.
// It aggregates the section-22 signals the graph owns: strategy decision, node
// count, graph depth, model-invocation count, escalation count, human
// checkpoints, mutation count and verification count. It deliberately does NOT
// report token counts — provider-reported usage remains the only authoritative
// usage (see Telemetry).
type Metrics struct {
	Strategy            ExecutionStrategy
	NodeCount           int
	Depth               int
	ModelInvocations    int
	ExpectedInvocations int
	EscalationCount     int
	HumanCheckpoints    int
	MutationCount       int
	VerificationCount   int
}

// Metrics returns the observability summary of the graph.
func (g *ExecutionGraph) Metrics() Metrics {
	if g == nil {
		return Metrics{}
	}
	m := Metrics{
		Strategy:            g.Strategy,
		NodeCount:           g.NodeCount(),
		Depth:               g.Depth(),
		ModelInvocations:    g.ModelNodeCount(),
		ExpectedInvocations: g.ExpectedInvocations,
		EscalationCount:     g.EscalationCount(),
		VerificationCount:   len(g.All(NodeVerify)),
	}
	for _, n := range g.Nodes {
		if n.Kind.HumanBoundary() {
			m.HumanCheckpoints++
		}
		if n.Kind == NodeMutate {
			m.MutationCount++
		}
	}
	return m
}

// String renders the metrics compactly for $inspect.
func (m Metrics) String() string {
	return fmt.Sprintf("strategy=%s nodes=%d depth=%d model-invocations=%d expected=%d escalations=%d human-checkpoints=%d mutations=%d verifications=%d",
		m.Strategy, m.NodeCount, m.Depth, m.ModelInvocations, m.ExpectedInvocations,
		m.EscalationCount, m.HumanCheckpoints, m.MutationCount, m.VerificationCount)
}

// SortedKinds returns the distinct node kinds in the graph, sorted, for
// compact assertion rendering.
func (g *ExecutionGraph) SortedKinds() []string {
	if g == nil {
		return nil
	}
	seen := map[NodeKind]bool{}
	for _, n := range g.Nodes {
		seen[n.Kind] = true
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, string(k))
	}
	sort.Strings(out)
	return out
}
