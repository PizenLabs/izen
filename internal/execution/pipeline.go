package execution

import (
	"fmt"
	"strings"
	"time"

	"github.com/PizenLabs/izen/internal/audit"
)

type PipelineStage struct {
	Name   string
	Status string
	Detail string
}

type PipelineResult struct {
	Stages    []PipelineStage `json:"stages"`
	Passed    bool            `json:"passed"`
	RiskLevel RiskLevel       `json:"risk_level"`
	Report    string          `json:"report"`
	Duration  time.Duration   `json:"duration"`
}

type PipelineRunner struct {
	engine      *Engine
	auditLogger *audit.Logger
}

func NewPipelineRunner(engine *Engine) *PipelineRunner {
	return &PipelineRunner{
		engine:      engine,
		auditLogger: audit.NewLogger(engine.Root()),
	}
}

func (pr *PipelineRunner) ExecuteBuild(staged []StagedPatch) PipelineResult {
	start := time.Now()
	var stages []PipelineStage
	passed := true

	stage := func(name string) *PipelineStage {
		s := PipelineStage{Name: name, Status: "pending"}
		stages = append(stages, s)
		return &stages[len(stages)-1]
	}

	record := func(s *PipelineStage, status, detail string) {
		s.Status = status
		s.Detail = detail
	}

	// Stage 1: Capability & Policy Engine
	s1 := stage("Capability & Policy Engine")
	if pr.engine.Policy != nil {
		if err := pr.engine.Policy.Must(CapWorkspaceWrite); err != nil {
			record(s1, "blocked", err.Error())
			passed = false
		} else {
			record(s1, "passed", "write capability granted")
		}
	} else {
		record(s1, "skipped", "no policy engine configured")
	}
	pr.logPipeline("policy", s1, "")

	if !passed {
		return pr.finalize(start, stages, passed, 0, "")
	}

	// Stage 2: Git Snapshot
	s2 := stage("Git Snapshot")
	if pr.engine.Git != nil && pr.engine.Git.IsRepo() {
		hash, err := pr.engine.Git.Checkpoint("pre-execution-snapshot")
		if err != nil {
			record(s2, "warning", fmt.Sprintf("snapshot attempted but non-fatal: %v", err))
		} else {
			record(s2, "passed", fmt.Sprintf("snapshot at %s", strings.TrimSpace(hash)))
		}
	} else {
		record(s2, "skipped", "not a git repository")
	}
	pr.logPipeline("snapshot", s2, "")

	// Stage 3: Structural Diff Analysis
	s3 := stage("Structural Diff Analysis")
	if pr.engine.Diff != nil && len(staged) > 0 {
		report := pr.engine.Diff.AnalyzePatches(staged)
		record(s3, "completed", report.String())
	} else {
		record(s3, "skipped", "no patches to analyze")
	}
	pr.logPipeline("diff", s3, "")

	// Stage 6: Risk Classification (before verification, determines sandbox)
	s6 := stage("Risk Classification")
	if pr.engine.Risk != nil && len(staged) > 0 {
		highestRisk := RiskLow
		for _, sp := range staged {
			result := pr.engine.Risk.ClassifyFileOp(sp.File, true)
			if result.Level > highestRisk {
				highestRisk = result.Level
			}
		}
		record(s6, "classified", fmt.Sprintf("highest risk: %s", highestRisk))
	} else {
		record(s6, "skipped", "no risk classifier or no patches")
	}
	pr.logPipeline("risk", s6, "")
	riskLevel := s6.detailToRisk()

	// Stage 7: Sandbox Check
	s7 := stage("Sandbox")
	if pr.engine.Runner.sandbox {
		if riskLevel >= RiskHigh {
			record(s7, "isolated", "high-risk operation requires sandbox")
		} else {
			record(s7, "direct", "low-risk operation, executing directly")
		}
	} else {
		record(s7, "disabled", "sandbox not configured")
	}
	pr.logPipeline("sandbox", s7, "")

	// Stage 4: Build, Test & Security Verification
	s4 := stage("Build, Test & Security Verification")
	if pr.engine.Verifier != nil {
		report := pr.engine.Verifier.RunAll()
		if report.Passed {
			record(s4, "passed", fmt.Sprintf("all %d verification steps passed", len(report.Results)))
		} else {
			var failed []string
			for _, r := range report.Results {
				if !r.Passed && !r.Step.Optional {
					failed = append(failed, r.Step.Name)
				}
			}
			record(s4, "failed", fmt.Sprintf("verification failed: %s", strings.Join(failed, ", ")))
			passed = false
		}
	} else {
		record(s4, "skipped", "no verifier configured")
	}
	pr.logPipeline("verification", s4, "")

	if !passed {
		// Rollback transaction: restore all modified files to original state
		if errs := pr.engine.RollbackTransaction(); len(errs) > 0 {
			if globalActivityLog != nil {
				for _, e := range errs {
					globalActivityLog("[ROLLBACK] transaction rollback error: %v", e)
				}
			}
		} else if globalActivityLog != nil {
			globalActivityLog("[ROLLBACK] transaction rolled back — workspace restored to pristine state")
		}
		return pr.finalize(start, stages, passed, riskLevel, "verification failed, changes blocked")
	}

	// Stage 5: Immutable Audit Log (final recording)
	s5 := stage("Immutable Audit Log")
	if pr.auditLogger != nil {
		for _, sp := range staged {
			riskResult := pr.engine.Risk.ClassifyFileOp(sp.File, true)
			entry := audit.MutationEntry{
				File:       sp.File,
				Action:     "patch",
				Capability: string(CapWorkspaceWrite),
				RiskLevel:  riskResult.Label,
				Decision:   audit.DecisionApproved,
				ContextID:  pr.engine.Patches.ActiveContextID(),
			}
			_ = pr.auditLogger.LogMutation(entry)
		}
		record(s5, "recorded", fmt.Sprintf("%d mutation(s) logged", len(staged)))
	} else {
		record(s5, "skipped", "no audit logger configured")
	}
	pr.logPipeline("audit", s5, "")

	// Transaction committed: all mutations are final
	pr.engine.CommitTransaction()
	if globalActivityLog != nil {
		globalActivityLog("[COMMIT] transaction committed — all mutations finalized")
	}

	return pr.finalize(start, stages, passed, riskLevel, "")
}

func (pr *PipelineRunner) finalize(start time.Time, stages []PipelineStage, passed bool, riskLevel RiskLevel, failReason string) PipelineResult {
	duration := time.Since(start)
	var b strings.Builder

	b.WriteString("=== Izen Execution Pipeline ===\n")
	for _, s := range stages {
		icon := "✓"
		switch s.Status {
		case "blocked", "failed":
			icon = "✗"
		case "warning", "skipped":
			icon = "⚠"
		}
		fmt.Fprintf(&b, "  %s %s: %s\n", icon, s.Name, s.Status)
		if s.Detail != "" {
			for _, line := range strings.Split(s.Detail, "\n") {
				if strings.TrimSpace(line) != "" {
					fmt.Fprintf(&b, "      %s\n", line)
				}
			}
		}
	}

	switch {
	case failReason != "":
		fmt.Fprintf(&b, "  Result: BLOCKED — %s\n", failReason)
	case passed:
		b.WriteString("  Result: ALL STAGES PASSED\n")
	default:
		b.WriteString("  Result: PIPELINE FAILED\n")
	}

	fmt.Fprintf(&b, "  Duration: %v\n", duration.Round(time.Millisecond))

	return PipelineResult{
		Stages:    stages,
		Passed:    passed,
		RiskLevel: riskLevel,
		Report:    b.String(),
		Duration:  duration,
	}
}

func (pr *PipelineRunner) logPipeline(stage string, s *PipelineStage, dur string) {
	if pr.auditLogger == nil {
		return
	}
	entry := audit.PipelineEntry{
		Stage:  stage,
		Status: s.Status,
		Detail: s.Detail,
	}
	if len(s.Detail) > 200 {
		entry.Detail = s.Detail[:200] + "..."
	}
	_ = pr.auditLogger.LogPipeline(entry)
}

func (s *PipelineStage) detailToRisk() RiskLevel {
	if strings.Contains(s.Detail, "Critical") {
		return RiskCritical
	}
	if strings.Contains(s.Detail, "High") {
		return RiskHigh
	}
	if strings.Contains(s.Detail, "Medium") {
		return RiskMedium
	}
	return RiskLow
}
