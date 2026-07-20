package build

import (
	"context"
	"fmt"
	"strings"
	"sync"

	izenctx "github.com/PizenLabs/izen/internal/context"
)

type BuildRecoveryState int

const (
	RecoveryNone BuildRecoveryState = iota
	RecoveryFirstPatch
	RecoveryForceRewrite
)

// ErrHumanValidationRequired is returned when a build mutation requires explicit
// human approval before it can be applied. This enforces the Human-in-the-Loop
// guardrail: no bash execution or code mutation is permitted without explicit
// user sign-off via the Proposal UI chip.
var ErrHumanValidationRequired = fmt.Errorf("human validation required: mutation must be approved via Proposal UI before execution")

type Engine struct {
	recoveryCount int
	recoveryState BuildRecoveryState
	files         map[string]int
	maxRecovery   int

	// ledger bridges /build execution to /plan checklist state.
	ledger *izenctx.TaskLedger
	// contextID scopes mutations for the guardrail audit log.
	contextID string

	// pendingProposals holds mutations awaiting human approval. The build
	// engine MUST NOT apply any mutation that is not first registered here and
	// explicitly accepted by the human operator.
	pendingProposals []Proposal

	mu          sync.Mutex
	applied     int
	lastSummary string
}

// Proposal represents a single build mutation awaiting human validation.
type Proposal struct {
	ID       string
	File     string
	Content  string
	Mode     MutationMode
	TaskID   int
	Strategy string
	Approved bool
}

func NewEngine() *Engine {
	return &Engine{
		recoveryCount: 0,
		recoveryState: RecoveryNone,
		files:         make(map[string]int),
		maxRecovery:   1,
	}
}

func (e *Engine) RecoveryCount() int {
	return e.recoveryCount
}

func (e *Engine) RecoveryState() BuildRecoveryState {
	return e.recoveryState
}

func (e *Engine) RecordCompilationFailure(file string) {
	e.recoveryCount++
	e.files[file]++
	if e.recoveryCount > e.maxRecovery {
		e.recoveryState = RecoveryForceRewrite
	}
}

func (e *Engine) RecordCompilationSuccess() {
	e.recoveryCount = 0
	e.recoveryState = RecoveryNone
}

func (e *Engine) FileFailureCount(file string) int {
	return e.files[file]
}

func (e *Engine) MustRewriteEntireFile(file string) bool {
	return e.recoveryState == RecoveryForceRewrite || e.files[file] > e.maxRecovery
}

func (e *Engine) Reset() {
	e.recoveryCount = 0
	e.recoveryState = RecoveryNone
	e.files = make(map[string]int)
}

func (e *Engine) ValidateFirstToken(output string) error {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return fmt.Errorf("build output is empty")
	}
	if !strings.HasPrefix(trimmed, "```") &&
		!strings.HasPrefix(trimmed, "FILE:") &&
		!strings.HasPrefix(trimmed, "--- a/") {
		return fmt.Errorf("first token must be code fence (```), FILE:, or --- a/, got: %.40s", trimmed)
	}
	return nil
}

// QueueProposal registers a mutation for human validation. It MUST be called
// before any file mutation or bash execution under /build. The returned ID is
// used to track approval state. No mutation is applied until Approved==true.
func (e *Engine) QueueProposal(p Proposal) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if p.ID == "" {
		p.ID = fmt.Sprintf("build-proposal-%d", len(e.pendingProposals)+1)
	}
	e.pendingProposals = append(e.pendingProposals, p)
	return p.ID
}

// ApproveProposal marks a previously queued proposal as approved by the human.
func (e *Engine) ApproveProposal(id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i := range e.pendingProposals {
		if e.pendingProposals[i].ID == id {
			e.pendingProposals[i].Approved = true
			return nil
		}
	}
	return fmt.Errorf("proposal %s not found", id)
}

// IsApproved reports whether the given proposal ID has been human-approved.
func (e *Engine) IsApproved(id string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, p := range e.pendingProposals {
		if p.ID == id {
			return p.Approved
		}
	}
	return false
}

// IsApprovedByFile checks if there's an approved proposal for a specific file and task.
// This is used by the executor to verify human approval before applying mutations.
func (e *Engine) IsApprovedByFile(file string, taskID int) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, p := range e.pendingProposals {
		if p.File == file && p.TaskID == taskID && p.Approved {
			return true
		}
	}
	return false
}

// ExecuteFileMutation enforces the 7-Layer Guardrail: it refuses to apply any
// mutation that has not been explicitly validated by a human operator. The
// actual file write is delegated to the UI layer after approval is confirmed.
func (e *Engine) ExecuteFileMutation(ctx context.Context, file string, content string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	// Scan pending proposals for a matching file; if found and not approved,
	// block the mutation. This makes the guardrail fail-closed.
	for _, p := range e.pendingProposals {
		if p.File == file && !p.Approved {
			return ErrHumanValidationRequired
		}
	}
	// If no proposal exists for this file, it was never queued for validation
	// — also block, because /build MUST route every mutation through a Proposal.
	return ErrHumanValidationRequired
}
