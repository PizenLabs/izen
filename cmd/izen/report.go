package main

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/PizenLabs/izen/internal/execution"
)

// reportSchema is the stable schema identifier of the machine-readable run
// report. Automation should key off this string, not off prose.
const reportSchema = "izen.run.v1"

// runReport is the machine-readable projection of ONE authoritative run. Every
// field is derived from runtime state (the driver's terminal result, its
// objective report, the executor's applied mutations and the durable ledger);
// nothing is inferred from model prose and no unavailable evidence is
// fabricated. It distinguishes EXECUTION completion (a mutation happened) from
// OBJECTIVE proof (the runtime observed every completion condition hold).
type runReport struct {
	Schema    string   `json:"schema"`
	RunID     string   `json:"run_id,omitempty"`
	Surface   string   `json:"surface"`
	Provider  string   `json:"provider,omitempty"`
	Model     string   `json:"model,omitempty"`
	Workspace string   `json:"workspace"`
	Objective string   `json:"objective"`
	Mode      string   `json:"mode"`
	Targets   []string `json:"targets,omitempty"`
	// ResumedFrom is the durable task id this run continues, when the run was
	// started with -resume.
	ResumedFrom string `json:"resumed_from,omitempty"`

	// Outcome is the terminal classification (see exitCodeForOutcome).
	Outcome string `json:"outcome"`
	// Reason is the runtime's own terminal reason, verbatim.
	Reason string `json:"reason,omitempty"`
	// Error is the command-level error text when the run could not complete.
	Error string `json:"error,omitempty"`

	// Human-surface decisions.
	Approved        bool `json:"approved"`
	Inspected       bool `json:"inspected"`
	Rejected        bool `json:"rejected"`
	ClarifyRequired bool `json:"clarify_required"`

	// Objective is the runtime's own completion-contract projection.
	ObjectiveReport objectiveReport `json:"objective_contract"`
	// Artifacts is the reviewed candidate and the mutations actually applied.
	Artifacts artifactReport `json:"artifacts"`
	// Usage is the runtime's own aggregated token accounting.
	Usage usageReport `json:"usage"`

	// ElapsedMS is the command wall-clock in milliseconds.
	ElapsedMS int64 `json:"elapsed_ms"`
	// ExitCode is the process exit code this run maps to.
	ExitCode int `json:"exit_code"`
}

type objectiveReport struct {
	ObjectiveID      string            `json:"objective_id,omitempty"`
	TaskKind         string            `json:"task_kind,omitempty"`
	Progress         string            `json:"progress,omitempty"`
	Outcome          string            `json:"outcome,omitempty"`
	Continuation     string            `json:"continuation,omitempty"`
	Scope            []string          `json:"scope,omitempty"`
	Pending          []string          `json:"pending,omitempty"`
	Conditions       []conditionReport `json:"conditions,omitempty"`
	RequirementCount int               `json:"requirement_count"`
	DischargedCount  int               `json:"discharged_count"`
}

type conditionReport struct {
	ID                string   `json:"id"`
	Description       string   `json:"description,omitempty"`
	Obligation        string   `json:"obligation,omitempty"`
	Status            string   `json:"status"`
	VerificationState string   `json:"verification_state,omitempty"`
	EvidenceRefs      []string `json:"evidence_refs,omitempty"`
}

type artifactReport struct {
	// CandidateTargets is the reviewed candidate's target set.
	CandidateTargets []string `json:"candidate_targets,omitempty"`
	// CandidateID and CandidateDigest bind the review to the authorization.
	CandidateID     string `json:"candidate_id,omitempty"`
	CandidateDigest string `json:"candidate_digest,omitempty"`
	// Operation is the runtime-derived operation (CREATE/PATCH/...).
	Operation string `json:"operation,omitempty"`
	// AddedLines and RemovedLines are the reviewed candidate's metrics.
	AddedLines   int `json:"added_lines"`
	RemovedLines int `json:"removed_lines"`
	// Applied is the exact target set the executor durably mutated.
	Applied []string `json:"applied,omitempty"`
}

type usageReport struct {
	InputTokens  int  `json:"input_tokens"`
	OutputTokens int  `json:"output_tokens"`
	Known        bool `json:"known"`
}

// newObjectiveReport projects the runtime's ObjectiveProgressReport without
// re-deciding anything.
func newObjectiveReport(rep execution.ObjectiveProgressReport) objectiveReport {
	out := objectiveReport{
		ObjectiveID:  rep.ObjectiveID,
		TaskKind:     string(rep.TaskKind),
		Progress:     string(rep.Progress),
		Outcome:      string(rep.Outcome),
		Continuation: string(rep.Continuation),
		Scope:        append([]string(nil), rep.Scope...),
		Pending:      append([]string(nil), rep.Pending...),
	}
	out.RequirementCount = len(rep.Requirements)
	for _, r := range rep.Requirements {
		if r.Discharged {
			out.DischargedCount++
		}
	}
	for _, c := range rep.Conditions {
		out.Conditions = append(out.Conditions, conditionReport{
			ID:                c.ID,
			Description:       c.Description,
			Obligation:        string(c.Obligation),
			Status:            string(c.Status),
			VerificationState: string(c.VerificationState),
			EvidenceRefs:      append([]string(nil), c.EvidenceRefs...),
		})
	}
	return out
}

// writeJSON emits the report as one JSON object on w.
func (r *runReport) writeJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// writeHuman renders a compact, truthful terminal summary. It never claims
// proof the runtime did not report.
func (r *runReport) writeHuman(w io.Writer) {
	_, _ = fmt.Fprintf(w, "%s: run=%s outcome=%s", r.Surface, r.RunID, r.Outcome)
	if r.ObjectiveReport.Progress != "" {
		_, _ = fmt.Fprintf(w, " objective=%s", r.ObjectiveReport.Progress)
	}
	if len(r.Artifacts.Applied) > 0 {
		_, _ = fmt.Fprintf(w, " applied=%v", r.Artifacts.Applied)
	}
	if r.Reason != "" {
		_, _ = fmt.Fprintf(w, " reason=%q", r.Reason)
	}
	if r.Error != "" {
		_, _ = fmt.Fprintf(w, " error=%q", r.Error)
	}
	_, _ = fmt.Fprintln(w)
}
