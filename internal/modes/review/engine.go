package review

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/PizenLabs/izen/internal/graph"
	"github.com/PizenLabs/izen/internal/modes"
)

var ErrWriteForbidden = errors.New("review mode: write operations are forbidden")
var ErrShellForbidden = errors.New("review mode: shell execution is forbidden")

// VerifyModeCapability asserts that the given mode can perform review-level
// read-only operations. Returns an error if the mode lacks CapRead.
func VerifyModeCapability(m modes.Mode) error {
	if !m.CanRead() {
		return fmt.Errorf("mode %s lacks read capability", m)
	}
	if m.CanWrite() {
		return fmt.Errorf("mode %s has write capability — not allowed in review context", m)
	}
	return nil
}

type Engine struct {
	State   *StateMachine
	Diff    *DiffAnalyzer
	Auditor *RiskAuditor
	Impact  *ImpactAnalyzer

	root      string
	target    string
	startedAt time.Time
	Result    *ReviewResult

	retriever Retriever
	graph     *graph.Graph
}

func NewEngine(root string, retriever Retriever, g *graph.Graph) *Engine {
	return &Engine{
		State:     NewStateMachine(DefaultStateConfig()),
		Diff:      NewDiffAnalyzer(root),
		Auditor:   NewRiskAuditor(root),
		Impact:    NewImpactAnalyzer(root, g),
		root:      root,
		startedAt: time.Now(),
		retriever: retriever,
		graph:     g,
	}
}

func (e *Engine) Run() (*ReviewResult, error) {
	result := &ReviewResult{
		CreatedAt: time.Now(),
	}

	if err := VerifyModeCapability(modes.ModeReview); err != nil {
		result.Error = err.Error()
		e.Result = result
		return result, err
	}

	if e.target == "" && e.Diff.isRepo() && !e.Diff.hasChanges() {
		result.Error = "no changes to review — working tree is clean"
		e.Result = result
		return result, nil
	}

	for !e.State.ShouldStop() {
		if err := e.executeCurrentState(result); err != nil {
			result.Error = err.Error()
			break
		}
	}

	result.States = e.State.History()
	result.Duration = time.Since(e.startedAt).Round(time.Millisecond).String()
	e.Result = result

	if result.Error == "" {
		e.generateSummary(result)
	}

	return result, nil
}

func (e *Engine) RunTarget(target string) (*ReviewResult, error) {
	e.target = target
	return e.Run()
}

func (e *Engine) executeCurrentState(result *ReviewResult) error {
	switch e.State.Current() {
	case StateCollect:
		return e.stateCollect(result)
	case StateAnalyzeDiff:
		return e.stateAnalyzeDiff(result)
	case StateImpactRadius:
		return e.stateImpactRadius(result)
	case StateRiskAudit:
		return e.stateRiskAudit(result)
	case StateReport:
		return e.stateReport(result)
	case StateDone:
		return nil
	default:
		return e.State.Transition(StateDone)
	}
}

func (e *Engine) stateCollect(result *ReviewResult) error {
	if e.target != "" {
		result.Branch, _ = e.Diff.getBranch()
		result.BaseBranch = e.Diff.getBaseBranch()
		result.CommitHash, _ = e.Diff.getHash()
		result.Commits = 1
		result.FilesChanged = []DiffFile{{
			Path:     e.target,
			Status:   "audit",
			Language: strings.TrimPrefix(filepath.Ext(e.target), "."),
		}}
		return e.State.Transition(StateImpactRadius)
	}

	analysis, err := e.Diff.Analyze()
	if err != nil {
		return fmt.Errorf("collect changes: %w", err)
	}

	result.Branch = analysis.Branch
	result.BaseBranch = e.Diff.getBaseBranch()
	result.CommitHash = analysis.Hash
	result.Commits = analysis.Commits

	if len(analysis.Files) == 0 {
		return e.State.Transition(StateDone)
	}

	return e.State.Transition(StateAnalyzeDiff)
}

func (e *Engine) stateAnalyzeDiff(result *ReviewResult) error {
	if e.target != "" {
		return e.State.Transition(StateImpactRadius)
	}

	analysis, err := e.Diff.Analyze()
	if err != nil {
		return fmt.Errorf("analyze diff: %w", err)
	}

	result.FilesChanged = analysis.Files

	if len(result.FilesChanged) == 0 {
		return e.State.Transition(StateDone)
	}

	return e.State.Transition(StateImpactRadius)
}

func (e *Engine) stateImpactRadius(result *ReviewResult) error {
	if e.Impact == nil {
		return e.State.Transition(StateRiskAudit)
	}

	impact, err := e.Impact.Analyze(result.FilesChanged)
	if err == nil {
		result.ImpactRadius = *impact
	}

	return e.State.Transition(StateRiskAudit)
}

func (e *Engine) stateRiskAudit(result *ReviewResult) error {
	findings := e.Auditor.Audit(result.FilesChanged)
	result.RiskFindings = findings
	result.ImpactRadius.RiskScore = e.Auditor.calculateRiskScore(findings)

	return e.State.Transition(StateReport)
}

func (e *Engine) stateReport(result *ReviewResult) error {
	e.generateSummary(result)
	return e.State.Transition(StateDone)
}

func (e *Engine) generateSummary(result *ReviewResult) {
	var b strings.Builder

	if e.target != "" {
		fmt.Fprintf(&b, "Auditing target: %s\n", e.target)
	} else {
		fmt.Fprintf(&b, "Reviewing %d changed files in branch %s\n",
			len(result.FilesChanged), result.Branch)
	}

	totalAdditions := 0
	totalDeletions := 0
	for _, f := range result.FilesChanged {
		totalAdditions += f.Additions
		totalDeletions += f.Deletions
	}
	fmt.Fprintf(&b, "Changes: +%d/-%d lines\n", totalAdditions, totalDeletions)

	if len(result.ImpactRadius.IndirectFiles) > 0 {
		fmt.Fprintf(&b, "Impact radius: %d direct files, %d indirect files across %d packages\n",
			len(result.ImpactRadius.DirectFiles),
			len(result.ImpactRadius.IndirectFiles),
			len(result.ImpactRadius.AffectedPkgs))
	}

	severityCounts := map[RiskSeverity]int{
		RiskCritical: 0,
		RiskHigh:     0,
		RiskMedium:   0,
		RiskLow:      0,
		RiskInfo:     0,
	}
	for _, f := range result.RiskFindings {
		severityCounts[f.Severity]++
	}

	fmt.Fprintf(&b, "Risk findings: %d critical, %d high, %d medium, %d low, %d info\n",
		severityCounts[RiskCritical],
		severityCounts[RiskHigh],
		severityCounts[RiskMedium],
		severityCounts[RiskLow],
		severityCounts[RiskInfo])

	fmt.Fprintf(&b, "Risk score: %d/100\n", result.ImpactRadius.RiskScore)

	result.Summary = b.String()

	result.Score = e.calculateScore(result)

	result.Recommendations = e.generateRecommendations(result)
}

func (e *Engine) calculateScore(result *ReviewResult) int {
	score := 100

	numFiles := len(result.FilesChanged)
	switch {
	case numFiles > 20:
		score -= 20
	case numFiles > 10:
		score -= 10
	case numFiles > 5:
		score -= 5
	}

	score -= result.ImpactRadius.RiskScore

	if score < 0 {
		score = 0
	}

	return score
}

func (e *Engine) generateRecommendations(result *ReviewResult) []string {
	var recs []string

	if result.ImpactRadius.RiskScore > 50 {
		recs = append(recs, "High risk score. Consider breaking this change into smaller, focused PRs.")
	}

	if len(result.ImpactRadius.IndirectFiles) > len(result.FilesChanged)*2 {
		recs = append(recs, "Wide impact radius detected. Verify all indirectly affected files are tested.")
	}

	criticalCount := 0
	for _, f := range result.RiskFindings {
		if f.Severity == RiskCritical {
			criticalCount++
		}
	}
	if criticalCount > 0 {
		recs = append(recs, fmt.Sprintf("%d critical risk findings must be resolved before merging.", criticalCount))
	}

	highCount := 0
	for _, f := range result.RiskFindings {
		if f.Severity == RiskHigh {
			highCount++
		}
	}
	if highCount > 0 {
		recs = append(recs, fmt.Sprintf("%d high-severity findings should be reviewed carefully.", highCount))
	}

	if len(result.FilesChanged) > 0 {
		hasTestChanges := false
		for _, f := range result.FilesChanged {
			if strings.HasSuffix(f.Path, "_test.go") || strings.Contains(f.Path, "test") {
				hasTestChanges = true
				break
			}
		}
		if !hasTestChanges {
			recs = append(recs, "No test files changed. Consider adding tests for the modified code.")
		}
	}

	for _, f := range result.RiskFindings {
		if f.Category == "hardcoded_secret" {
			recs = append(recs, "Potential secret detected. Verify no credentials are being committed.")
			break
		}
	}

	return recs
}

func SaveReport(result *ReviewResult, dir string) error {
	reportDir := filepath.Join(dir, ".izen", "reviews")
	if err := os.MkdirAll(reportDir, 0755); err != nil {
		return err
	}

	timestamp := time.Now().Format("2006-01-02_150405")
	path := filepath.Join(reportDir, fmt.Sprintf("review_%s.json", timestamp))

	data := marshalReport(result)
	return os.WriteFile(path, data, 0644)
}

func marshalReport(result *ReviewResult) []byte {
	var b strings.Builder

	b.WriteString("{\n")
	fmt.Fprintf(&b, "  \"branch\": %q,\n", result.Branch)
	fmt.Fprintf(&b, "  \"base_branch\": %q,\n", result.BaseBranch)
	fmt.Fprintf(&b, "  \"commit_hash\": %q,\n", result.CommitHash)
	fmt.Fprintf(&b, "  \"commits\": %d,\n", result.Commits)
	fmt.Fprintf(&b, "  \"score\": %d,\n", result.Score)
	fmt.Fprintf(&b, "  \"risk_score\": %d,\n", result.ImpactRadius.RiskScore)
	fmt.Fprintf(&b, "  \"files_changed\": %d,\n", len(result.FilesChanged))
	fmt.Fprintf(&b, "  \"risk_findings\": %d,\n", len(result.RiskFindings))
	fmt.Fprintf(&b, "  \"summary\": %q,\n", result.Summary)
	fmt.Fprintf(&b, "  \"duration\": %q,\n", result.Duration)
	fmt.Fprintf(&b, "  \"created_at\": %q\n", result.CreatedAt.Format(time.RFC3339))
	b.WriteString("}\n")

	return []byte(b.String())
}

func severityScore(s RiskSeverity) int {
	switch s {
	case RiskCritical:
		return 5
	case RiskHigh:
		return 4
	case RiskMedium:
		return 3
	case RiskLow:
		return 2
	case RiskInfo:
		return 1
	default:
		return 0
	}
}
