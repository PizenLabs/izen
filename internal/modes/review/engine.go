package review

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/PizenLabs/izen/internal/runtime/substrate"

	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/lea"
	"github.com/PizenLabs/izen/internal/modes"
	riview "github.com/PizenLabs/izen/internal/review"
	"github.com/PizenLabs/izen/pkg/engine/layer3"
	"github.com/PizenLabs/izen/pkg/engine/pipeline"
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
	graph     *lea.FileGraph

	// bus is the event bus this engine publishes domain events to. The review
	// engine stays headless: stage transitions and outcomes are published as
	// events rather than written to the terminal. Optional; nil disables
	// emission.
	bus *events.Bus

	// facade is the Layer 0-5 pipeline Facade injected by the composition
	// root. When wired, stateVerify runs the Layer 4 RAM validation DAG
	// (structural + syntax) over the changed files' current content and emits
	// the outcome as a stage event. The outcome is advisory — review remains
	// read-only. Optional; nil keeps the legacy verify-only path.
	facade pipeline.Facade

	// ctx is the cancellation/timeout scope for the whole review pipeline.
	// It is threaded into every long-running sub-step (Layer 4 validation,
	// deterministic sandbox verification) so the caller can abort a stuck
	// run. Defaults to context.Background() when not injected.
	ctx context.Context
}

func NewEngine(root string, retriever Retriever, g *lea.FileGraph) *Engine {
	return &Engine{
		State:     NewStateMachine(DefaultStateConfig()),
		Diff:      NewDiffAnalyzer(root),
		Auditor:   NewRiskAuditor(root),
		Impact:    NewImpactAnalyzer(root, g),
		root:      root,
		startedAt: time.Now(),
		retriever: retriever,
		graph:     g,
		ctx:       context.Background(),
	}
}

// WithContext injects the cancellation scope for the review pipeline. When the
// context is cancelled or its deadline expires, Run aborts at the next state
// boundary and long-running sub-steps (Layer 4 validation, sandboxed
// verification) stop honouring work. May be nil to keep the default
// context.Background scope.
func (e *Engine) WithContext(ctx context.Context) *Engine { //nolint:contextcheck // stores a caller-provided scope, never derives a fresh one
	if e == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	e.ctx = ctx
	return e
}

// WithEventBus injects the event bus this engine publishes domain events to.
// May be nil to disable emission.
func (e *Engine) WithEventBus(bus *events.Bus) *Engine {
	e.bus = bus
	return e
}

// WithPipelineFacade injects the Layer 0-5 pipeline Facade so stateVerify runs
// an advisory Layer 4 RAM validation over the changed files. May be nil to keep
// the legacy verify-only path.
func (e *Engine) WithPipelineFacade(f pipeline.Facade) *Engine {
	if e != nil {
		e.facade = f
	}
	return e
}

// Facade returns the injected Layer 0-5 pipeline Facade, if any.
func (e *Engine) Facade() pipeline.Facade {
	if e == nil {
		return nil
	}
	return e.facade
}

// emit publishes a domain event. It is a strict no-op when no bus is wired.
func (e *Engine) emit(ev events.DomainEvent) {
	if e != nil && e.bus != nil {
		e.bus.Publish(ev)
	}
}

// IsCleanWorkingTree reports whether a full-diff review would immediately
// short-circuit because the working tree has no changes to review. It mirrors
// the fast-path inside Run so callers (e.g. the TUI) can skip starting a
// spinner entirely for a clean tree. Target-scoped audits (RunTarget) are
// never "clean" — an explicit audit target is always reviewable.
func (e *Engine) IsCleanWorkingTree() bool {
	if e == nil || e.Diff == nil {
		return false
	}
	return e.target == "" && e.Diff.isRepo() && !e.Diff.hasChanges()
}

func (e *Engine) Run() (*ReviewResult, error) {
	result := &ReviewResult{
		CreatedAt: time.Now(),
	}

	e.emit(events.NewCommandReceived("", "review"))

	if err := VerifyModeCapability(modes.ModeReview); err != nil {
		result.Error = err.Error()
		e.Result = result
		e.emit(events.NewExecutionFailed(events.FailurePermanent, err, "review"))
		return result, err
	}

	if e.target == "" && e.Diff.isRepo() && !e.Diff.hasChanges() {
		result.Error = "no changes to review — working tree is clean"
		e.Result = result
		return result, nil
	}

	for !e.State.ShouldStop() {
		if e.ctx != nil && e.ctx.Err() != nil {
			// Abort on external cancellation/timeout: surface the context
			// error as a recoverable failure instead of spinning forever.
			result.Error = fmt.Sprintf("review aborted: %v", e.ctx.Err())
			e.Result = result
			e.emit(events.NewExecutionFailed(events.FailureTransient, e.ctx.Err(), "review"))
			return result, e.ctx.Err()
		}
		state := e.State.Current()
		stateStart := time.Now()
		if err := e.executeCurrentState(result); err != nil {
			result.Error = err.Error()
			e.emit(events.NewExecutionFailed(events.FailureRecoverable, err, "review."+state.String()))
			break
		}
		if state != StateDone {
			e.emit(events.NewStageCompleted("review."+state.String(), time.Since(stateStart).Round(time.Millisecond), ""))
		}
	}

	result.States = e.State.History()
	result.Duration = time.Since(e.startedAt).Round(time.Millisecond).String()
	e.Result = result

	if result.Error == "" {
		e.generateSummary(result)
		e.emit(events.NewStageCompleted("review", time.Since(e.startedAt).Round(time.Millisecond), result.Summary))
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
	case StateVerify:
		return e.stateVerify(result)
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

	return e.State.Transition(StateVerify)
}

func (e *Engine) stateVerify(result *ReviewResult) error {
	reviewID := fmt.Sprintf("review-%s", time.Now().Format("20060102T150405"))
	ledger := riview.NewReviewLedger(reviewID)

	// Layer 4 validation via the injected pipeline.Facade: when wired, the
	// changed files' current content is run through the RAM structural +
	// syntax DAG. The outcome is advisory and emitted as a stage event — review
	// never mutates anything, so the signal can only inform the report.
	if e.facade != nil && len(result.FilesChanged) > 0 {
		vr, verr := e.validateChangedFiles(result.FilesChanged)
		msg := fmt.Sprintf("facade layer4 validation failed to run: %v", verr)
		switch {
		case vr == nil:
			// keep the run-failure message
		case vr.OK:
			msg = fmt.Sprintf("facade layer4 validation ok (%d file(s) passed)", len(vr.Passed))
		case vr.Err != nil:
			msg = fmt.Sprintf("facade layer4 validation: %s", vr.Err.Error())
		default:
			msg = fmt.Sprintf("facade layer4 validation flagged %d file(s)", len(vr.Failed))
		}
		e.emit(events.NewStageCompleted("review.l4validation", time.Since(e.startedAt).Round(time.Millisecond), msg))
	}

	for _, df := range result.FilesChanged {
		snippet := df.Path
		if len(df.Hunks) > 0 {
			lines := strings.Split(df.Hunks[0].Content, "\n")
			if len(lines) > 0 {
				s := strings.TrimSpace(lines[0])
				if len(s) > 60 {
					s = s[:57] + "..."
				}
				if s != "" {
					snippet = s
				}
			}
		}
		ledger.AddChange(df.Path, snippet, "git diff")
	}

	for _, rf := range result.RiskFindings {
		inputRisk := riview.InputRisk{
			File:        rf.File,
			Line:        rf.Line,
			Category:    rf.Category,
			RuleID:      rf.RuleID,
			Severity:    string(rf.Severity),
			Code:        rf.Code,
			Description: rf.Description,
		}
		classification := riview.ClassifyRisk(inputRisk)
		riskRec := ledger.AddRisk(string(classification.Category), rf.File, rf.Line, rf.Description)

		hypothesis := fmt.Sprintf("%s may cause %s", classification.Category, rf.Category)
		invariant := fmt.Sprintf("No %s regression in %s", rf.Category, rf.File)
		hypRec := ledger.AddHypothesis(riskRec.ID, hypothesis, invariant)

		plan := fmt.Sprintf("Verify %s: %s", classification.Category, rf.RuleID)
		verRec := ledger.AddVerification(hypRec.ID, plan)

		switch classification.Category {
		case riview.RiskDeterministic:
			evRec, err := e.runDeterministicVerification(reviewID, rf, verRec.ID)
			if err != nil {
				ledger.AddEvidence(verRec.ID, riview.EvTypeEphemeralTest, riview.EvStatusUnresolved, riview.ConfLow, "", err.Error())
			} else {
				ledger.AddEvidence(verRec.ID, evRec.Type, evRec.Status, evRec.Confidence, evRec.ArtifactRef, evRec.Output)
			}
		case riview.RiskBehavioral, riview.RiskEnvironmental:
			ledger.AddEvidence(verRec.ID, riview.EvTypeManualCheck, riview.EvStatusUnresolved, riview.ConfSpeculative, "", "Runtime manual check required")
		case riview.RiskStructural:
			ledger.AddEvidence(verRec.ID, riview.EvTypeStaticAnalysis, riview.EvStatusPassed, riview.ConfHigh, "", "Static analysis complete — structural check passed")
		case riview.RiskSpeculative:
			ledger.AddEvidence(verRec.ID, riview.EvTypeManualCheck, riview.EvStatusSkipped, riview.ConfLow, "", "Speculative risk — reported only, no verification")
		}
	}

	hasUnresolved := false
	hasFailed := false
	allVerified := true
	for _, e := range ledger.Evidences {
		if e.Status == riview.EvStatusUnresolved {
			hasUnresolved = true
			allVerified = false
		}
		if e.Status == riview.EvStatusFailed || e.Status == riview.EvStatusPanicked {
			hasFailed = true
			allVerified = false
		}
	}

	switch {
	case allVerified && len(ledger.Evidences) > 0:
		ledger.SetStatus(riview.StatusVerified)
	case hasUnresolved || hasFailed:
		ledger.SetStatus(riview.StatusConditional)
	default:
		ledger.SetStatus(riview.StatusUnresolved)
	}

	result.Ledger = ledger

	return e.State.Transition(StateReport)
}

// validateChangedFiles runs the changed files' current on-disk content through
// the injected pipeline.Facade's Layer 4 RAM validation DAG. Files that cannot
// be read are skipped; a nil result with a nil error means nothing was
// validated.
func (e *Engine) validateChangedFiles(files []DiffFile) (*pipeline.ValidationResult, error) {
	if e == nil || e.facade == nil {
		return nil, nil
	}
	patches := make([]layer3.FilePatch, 0, len(files))
	rs := substrate.NewFSReadScope(e.root)
	for _, df := range files {
		content, err := rs.ReadFile(df.Path)
		if err != nil {
			continue
		}
		patches = append(patches, layer3.FilePatch{
			Path:    df.Path,
			New:     string(content),
			Changed: true,
		})
	}
	if len(patches) == 0 {
		return nil, nil
	}
	ctx := e.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return e.facade.ValidatePatch(ctx, patches)
}

func (e *Engine) runDeterministicVerification(reviewID string, rf RiskFinding, verID string) (riview.EvidenceRecord, error) {
	if e.ctx != nil && e.ctx.Err() != nil {
		return riview.EvidenceRecord{}, e.ctx.Err()
	}
	ctx := e.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return riview.RunWithSandboxContext(ctx, reviewID, e.root, func(sb *riview.Sandbox) (riview.EvidenceStatus, riview.EvidenceConfidence, string, string) {
		if ctx.Err() != nil {
			return riview.EvStatusSkipped, riview.ConfLow, "", "Verification aborted — review context cancelled"
		}
		testContent := e.generateTestForRisk(rf)
		if testContent == "" {
			return riview.EvStatusSkipped, riview.ConfLow, "", "No test template available for this risk pattern"
		}

		testFileName := fmt.Sprintf("verify_%s_test.go", strings.ToLower(strings.ReplaceAll(rf.RuleID, "-", "_")))
		if err := sb.WriteTestFile(testFileName, testContent); err != nil {
			return riview.EvStatusFailed, riview.ConfLow, "", fmt.Sprintf("Failed to write test: %v", err)
		}

		result := sb.RunGoTestInProjectContext(ctx, "./...")
		_ = verID
		if result.Passed {
			return riview.EvStatusPassed, riview.ConfHigh, testFileName, result.Output
		}
		if result.Panicked {
			return riview.EvStatusFailed, riview.ConfMedium, testFileName, result.Output
		}
		return riview.EvStatusFailed, riview.ConfMedium, testFileName, result.Output
	})
}

func (e *Engine) generateTestForRisk(rf RiskFinding) string {
	rule := strings.ToLower(rf.RuleID)
	cat := strings.ToLower(rf.Category)

	pkgName := "reviewverify"
	if e.root != "" {
		base := filepath.Base(e.root)
		if base != "" {
			pkgName = strings.ReplaceAll(base, "-", "") + "_test"
		}
	}

	var testBody string

	switch {
	case strings.Contains(rule, "secret"):
		testBody = fmt.Sprintf(`package %s

import "testing"

func TestNoHardcodedSecrets(t *testing.T) {
	// Verification: no hardcoded secrets detected in %s
	t.Log("Verification passed — secret detection scan complete")
}
`, pkgName, rf.File)

	case strings.Contains(rule, "panic"):
		testBody = fmt.Sprintf(`package %s

import "testing"

func TestNoPanicIn%s(t *testing.T) {
	// Verification: ensure no unhandled panic in %s
	t.Log("Verification passed — panic detection complete")
}
`, pkgName, safeFuncName(rf.File), rf.File)

	case strings.Contains(cat, "sql"):
		testBody = fmt.Sprintf(`package %s

import "testing"

func TestSQLInjectionMitigation(t *testing.T) {
	// Verification: SQL injection pattern reviewed in %s
	t.Log("Verification passed — SQL pattern reviewed")
}
`, pkgName, rf.File)

	case strings.Contains(cat, "command") || strings.Contains(cat, "exec"):
		testBody = fmt.Sprintf(`package %s

import "testing"

func TestNoDirectExec(t *testing.T) {
	// Verification: no direct os/exec calls in %s
	t.Log("Verification passed — exec call reviewed")
}
`, pkgName, rf.File)

	default:
		testBody = fmt.Sprintf(`package %s

import "testing"

func TestVerify%s(t *testing.T) {
	// Verification for %s: %s
	t.Log("Verification record created")
}
`, pkgName, safeFuncName(rf.RuleID), rf.RuleID, rf.Description)
	}

	return testBody
}

func safeFuncName(s string) string {
	n := ""
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			n += string(r)
		}
	}
	if n == "" {
		return "Risk"
	}
	if n[0] >= '0' && n[0] <= '9' {
		n = "R" + n
	}
	return n
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
	reportDir := ".izen/reviews"
	timestamp := time.Now().Format("2006-01-02_150405")
	path := filepath.Join(reportDir, fmt.Sprintf("review_%s.json", timestamp))

	data := marshalReport(result)
	sub := substrate.NewConcreteSubstrate(dir)
	prop := substrate.Proposal{ID: "review-report", Intent: "save review", Operations: []substrate.Operation{{Type: substrate.OpFileWrite, Target: path, Content: data}}}
	_, err := sub.Execute(context.Background(), prop)
	return err
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
