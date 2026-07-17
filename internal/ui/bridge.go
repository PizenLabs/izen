package ui

import (
	"fmt"
	"strings"

	"github.com/PizenLabs/izen/internal/modes"
	"github.com/PizenLabs/izen/internal/modes/plan"
	"github.com/PizenLabs/izen/internal/session"
)

// bridgeInvestigationToLedger projects the read-only forensic findings from
// /investigate into the canonical session.ContextLedger (the handoff SSOT).
// This is a pure projection: it performs no writes, patches, or shell
// execution. The structured ledger content (FormatLedgerForPlan) and any
// engine error are captured in the Diagnostics field; if nothing could be
// extracted the field is tagged "unknown" rather than fabricating a root cause.
func (m *model) bridgeInvestigationToLedger(ledgerContent string, engErr error) {
	if m.sess == nil {
		return
	}
	ledger := m.loadOrCreateLedger(modes.ModeInvestigate)

	var diag []string
	if ledgerContent != "" {
		diag = append(diag, ledgerContent)
	}
	if engErr != nil {
		diag = append(diag, engErr.Error())
	}
	if len(diag) == 0 {
		ledger.Diagnostics = "unknown"
	} else {
		ledger.Diagnostics = strings.Join(diag, "\n")
	}

	m.sess.SetContextLedger(ledger)
}

// bridgePlanToLedger projects the structured /plan task queue into the
// canonical session.ContextLedger as []AtomicTask. This is the single source
// of truth /build consumes; the session's CurrentTasks remains the live
// execution mirror used by the orchestrator.
func (m *model) bridgePlanToLedger(tasks []plan.Task) {
	if m.sess == nil || len(tasks) == 0 {
		return
	}
	ledger := m.loadOrCreateLedger(modes.ModePlan)

	atomic := make([]plan.AtomicTask, 0, len(tasks))
	for _, t := range tasks {
		atomic = append(atomic, plan.AtomicTask{
			TaskID:      t.StepNum,
			File:        t.Target,
			Strategy:    t.Type,
			Description: t.Description,
		})
		if t.Target != "" {
			ledger.TargetFile = t.Target
		}
	}
	ledger.Tasks = atomic

	m.sess.SetContextLedger(ledger)
}

// loadOrCreateLedger returns the persisted session.ContextLedger, or a fresh
// one keyed to the given source mode if none exists yet.
func (m *model) loadOrCreateLedger(source modes.Mode) *session.ContextLedger {
	if m.sess != nil && m.sess.ContextLedger != nil {
		return m.sess.ContextLedger
	}
	return session.NewContextLedger(source)
}

// bridgeBuildResultToLedger records the outcome of the active /build task into
// the canonical session.ContextLedger, implementing the fail-fast machine's
// state tracking. A failed verification marks the active task Failed = true and
// freezes the queue (no subsequent task is advanced); a successful verification
// marks the active task Completed = true. The updated ledger is persisted as
// the SSOT for the developer to inspect.
func (m *model) bridgeBuildResultToLedger(taskID int, passed bool, diagnostics string) {
	if m.sess == nil || taskID <= 0 {
		return
	}
	ledger := m.loadOrCreateLedger(modes.ModeBuild)

	changed := false
	for i := range ledger.Tasks {
		if ledger.Tasks[i].TaskID == taskID {
			if ledger.TaskStatus == nil {
				ledger.TaskStatus = make(map[int]string)
			}
			if passed {
				ledger.TaskStatus[taskID] = "completed"
			} else {
				ledger.TaskStatus[taskID] = "failed"
				if diagnostics != "" {
					ledger.Diagnostics = diagnostics
				}
			}
			changed = true
			break
		}
	}

	if changed {
		_ = ledger.Save()
		m.sess.SetContextLedger(ledger)
	}
}

// countCompletedLedgerTasks returns how many tasks in the canonical ledger are
// marked completed, for concise fail-fast progress reporting.
func (m *model) countCompletedLedgerTasks() int {
	if m.sess == nil || m.sess.ContextLedger == nil {
		return 0
	}
	n := 0
	for _, status := range m.sess.ContextLedger.TaskStatus {
		if status == "completed" {
			n++
		}
	}
	return n
}

// reloadContextLedger synchronously loads the on-disk session.ContextLedger
// into the active in-memory session state. It MUST be called after
// CleanContextTransitions (which purges transient in-memory handoff buffers)
// and before any target-mode LLM dispatch, so the new mode reads from the
// freshly reloaded structured handoff SSOT rather than stale or cleared memory.
func (m *model) reloadContextLedger() {
	if m.sess == nil {
		return
	}
	ledger, err := session.LoadContextLedger()
	if err != nil || ledger == nil {
		return
	}
	m.sess.ContextLedger = ledger
}

// primeHandoffFromLedger re-populates the transient in-memory handoff buffers
// (handoffLedgerContent / handoffCtx) from the freshly reloaded authoritative
// session.ContextLedger. This is the load-and-inject step of the blocking
// handoff: after CleanContextTransitions wipes transient state, the structural
// /plan and /build engines must still receive the forensic diagnostics/targets
// so they execute immediately instead of booting with a generic greeting.
func (m *model) primeHandoffFromLedger(mode modes.Mode) {
	if m.sess == nil || m.sess.ContextLedger == nil {
		return
	}
	l := m.sess.ContextLedger

	if len(l.Tasks) > 0 {
		var b strings.Builder
		b.WriteString("## INVESTIGATION HANDOFF\n\n")
		b.WriteString("Generate an atomic task list from the following forensic findings:\n\n")
		for _, t := range l.Tasks {
			fmt.Fprintf(&b, "- %s: %s — %s\n", t.Strategy, t.File, t.Description)
		}
		if l.Diagnostics != "" {
			fmt.Fprintf(&b, "\n### INJECTED INVESTIGATION FORENSICS\n")
			fmt.Fprintf(&b, "- Target File: %s\n", l.TargetFile)
			fmt.Fprintf(&b, "- Diagnostics Error Log:\n```\n%s\n```\n", l.Diagnostics)
		}
		m.handoffLedgerContent = b.String()
	} else if l.Diagnostics != "" && l.Diagnostics != "unknown" {
		var b strings.Builder
		b.WriteString("### INJECTED INVESTIGATION FORENSICS\n")
		if l.TargetFile != "" {
			fmt.Fprintf(&b, "- Target File: %s\n", l.TargetFile)
		}
		fmt.Fprintf(&b, "- Diagnostics Error Log:\n```\n%s\n```\n", l.Diagnostics)
		m.handoffLedgerContent = b.String()
	}

	// Mirror into the legacy handoff context so injectHandoffContext and the
	// /build trigger also see the structured payload.
	if l.Diagnostics != "" {
		m.handoffCtx.LastFailurePayload = l.Diagnostics
	}
	if m.handoffLedgerContent != "" && m.handoffCtx.ProposedFix == "" {
		m.handoffCtx.ProposedFix = m.handoffLedgerContent
	}
}
