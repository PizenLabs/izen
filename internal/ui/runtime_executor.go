package ui

import (
	"context"
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/execution"
)

// ── RuntimeExecutor UI bridge (Steps 2-4 of the authority migration) ───────
//
// The UI submits execution requests to the RuntimeExecutor boundary and
// renders the returned results + the canonical execution events. It never
// calls a provider, a PatchManager or a MutationSet directly on these paths:
//
//	execute  → m.executor.Execute(req)   (runtime owns provider, context, patch)
//	approve  → m.executor.Approve(id)    (runtime owns apply, verify, commit)
//	reject   → m.executor.Reject(id)     (runtime rolls back, terminates)
//
// The executor emits the canonical events.Event* lifecycle stream on the
// shared bus; the UI projects it (see handleDomainEvent).

// executionResultMsg carries the terminal result of a RuntimeExecutor request
// back into the Bubble Tea event loop.
type executionResultMsg struct {
	res *execution.ExecutionResult
	err error
}

var _ tea.Msg = executionResultMsg{}

// runExecutorApproveCmd approves the held mutation through the RuntimeExecutor.
// The approval is a REAL human authorization (Alt+A): a fresh
// MutationAuthorization is issued through the production AuthorizationEngine
// over the held execution's target files and attached to the runtime BEFORE
// the apply, so the runtime's internal PatchManager + Verifier run under the
// same governance owner the legacy path used. Without a token the runtime
// denies deterministically.
func (m *model) runExecutorApproveCmd(patchID string) tea.Cmd {
	x := m.executor
	if x == nil {
		return func() tea.Msg {
			return executionResultMsg{err: fmt.Errorf("executor not wired")}
		}
	}
	if err := m.authorizeExecutorApproval(); err != nil {
		return func() tea.Msg {
			return executionResultMsg{err: err}
		}
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		res, err := x.Approve(ctx, patchID)
		if res == nil {
			res = &execution.ExecutionResult{}
		}
		return executionResultMsg{res: res, err: err}
	}
}

// authorizeExecutorApproval issues a MutationAuthorization through the
// production AuthorizationEngine and attaches it to the RuntimeExecutor. The
// token covers exactly the execution's held target files; the human approval
// flag is true (the user pressed Alt+A on the proposal). Nil-safe for
// harnesses without an AuthorizationEngine.
func (m *model) authorizeExecutorApproval() error {
	if m.executor == nil || m.authEngine == nil {
		return nil
	}
	targets := m.executorPendingTargets
	if len(targets) == 0 {
		if m.pendingHotfixPatch != nil {
			targets = []string{m.pendingHotfixPatch.File}
		}
	}
	auth, err := m.authEngine.AuthorizeBuild(
		targets,
		m.caps,
		m.mutationBudget,
		m.microBudget,
		false,
		true, // human-approved: the developer pressed Alt+A on the proposal
	)
	if err != nil {
		return fmt.Errorf("build authorization: %w", err)
	}
	m.executor.SetAuthorization(auth)
	return nil
}

// runExecutorRejectCmd rejects the held mutation through the RuntimeExecutor.
func (m *model) runExecutorRejectCmd(patchID, reason string) tea.Cmd {
	x := m.executor
	if x == nil {
		return func() tea.Msg {
			return executionResultMsg{err: fmt.Errorf("executor not wired")}
		}
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		res, err := x.Reject(ctx, patchID, reason)
		if res == nil {
			res = &execution.ExecutionResult{}
		}
		return executionResultMsg{res: res, err: err}
	}
}
