package execution

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"
)

const (
	// DefaultGuardrailWindow is the look-back window for counting mutations.
	DefaultGuardrailWindow = 60 * time.Second
	// DefaultGuardrailThreshold is the maximum number of apply attempts allowed
	// for a single file within the window before autofix is halted.
	DefaultGuardrailThreshold = 3
)

// mutationLogRe parses an appendMutationLog line:
//
//	[<ts>] context=<ctx> file=<file> patch=<id> action=apply
var mutationLogRe = regexp.MustCompile(`^\[([^\]]+)\] context=(\S+) file=(\S+) patch=(\S+) action=apply$`)

// GuardrailDecision is the result of a MutationGuardrail.Check.
type GuardrailDecision struct {
	Halt    bool
	File    string
	Context string
	Count   int
	Window  time.Duration
	Limit   int
}

// Message returns the terminal message to display when the guardrail halts.
func (d GuardrailDecision) Message() string {
	return fmt.Sprintf("⚠️ GUARDRAIL TRIGGERED: Infinite mutation loop detected for %s. "+
		"History shows %d attempts in the last 60 seconds. "+
		"Autofix halted to prevent structural corruption. "+
		"Manual review requested.", d.File, d.Count)
}

// MutationGuardrail prevents infinite mutation loops by counting how many times
// a single file has been patched (action=apply) within a recent time window,
// scoped to the active transaction context. It is read-only against the
// append-only audit log written by PatchManager.AppendMutationLog, so it never
// mutates state itself.
//
// Scoping by BOTH file and context is what keeps legitimate multi-file
// refactors flowing: a refactor that touches many distinct files only ever
// records one apply per file, so no single file crosses the threshold. The
// context scope additionally means one transaction's history cannot trip the
// guardrail for an unrelated concurrent or subsequent transaction.
type MutationGuardrail struct {
	root   string
	window time.Duration
	limit  int
	now    func() time.Time
	mu     sync.Mutex
}

// NewMutationGuardrail creates a guardrail for the repository rooted at root.
func NewMutationGuardrail(root string) *MutationGuardrail {
	return &MutationGuardrail{
		root:   root,
		window: DefaultGuardrailWindow,
		limit:  DefaultGuardrailThreshold,
		now:    time.Now,
	}
}

// auditLogPath returns the path to mutations.log for this root.
func (g *MutationGuardrail) auditLogPath() string {
	return filepath.Join(g.root, ".izen", "audit", "mutations.log")
}

// Check scans the audit log for applies to file within the recent window,
// scoped to the supplied transaction context, and decides whether to halt.
// The returned decision is Halt when the prior apply count reaches the limit.
func (g *MutationGuardrail) Check(file, contextID string) GuardrailDecision {
	decision := GuardrailDecision{
		File:    file,
		Context: contextID,
		Window:  g.window,
		Limit:   g.limit,
	}

	g.mu.Lock()
	now := g.now()
	g.mu.Unlock()

	cutoff := now.Add(-g.window)

	decision.Count = g.countApplies(file, contextID, cutoff)
	if decision.Count >= g.limit {
		decision.Halt = true
	}
	return decision
}

// countApplies returns the number of action=apply entries for file + context
// whose timestamp is strictly after cutoff. It is tolerant of a missing or
// partially-written log and never returns an error.
func (g *MutationGuardrail) countApplies(file, contextID string, cutoff time.Time) int {
	f, err := os.Open(g.auditLogPath())
	if err != nil {
		return 0
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	count := 0
	for scanner.Scan() {
		m := mutationLogRe.FindStringSubmatch(scanner.Text())
		if m == nil {
			continue
		}
		// Scope by transaction context so unrelated work is ignored.
		if m[2] != contextID {
			continue
		}
		// Scope by file so multi-file refactors are not penalised.
		if m[3] != file {
			continue
		}
		ts, err := time.Parse(time.RFC3339, m[1])
		if err != nil {
			continue
		}
		if ts.After(cutoff) {
			count++
		}
	}
	return count
}
