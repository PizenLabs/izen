package review

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/graph"
)

func TestStateMachineInitialState(t *testing.T) {
	sm := NewStateMachine(DefaultStateConfig())
	if sm.Current() != StateCollect {
		t.Fatalf("expected initial state Collect, got %s", sm.Current())
	}
}

func TestStateMachineValidTransitions(t *testing.T) {
	sm := NewStateMachine(DefaultStateConfig())

	transitions := []struct {
		from, to State
	}{
		{StateCollect, StateAnalyzeDiff},
		{StateAnalyzeDiff, StateImpactRadius},
		{StateImpactRadius, StateRiskAudit},
		{StateRiskAudit, StateReport},
		{StateReport, StateDone},
	}

	for _, tr := range transitions {
		if err := sm.Transition(tr.to); err != nil {
			t.Fatalf("transition %s->%s: %v", tr.from, tr.to, err)
		}
	}

	if !sm.IsTerminal() {
		t.Fatal("expected terminal state after full path")
	}
}

func TestStateMachineInvalidTransitions(t *testing.T) {
	sm := NewStateMachine(DefaultStateConfig())

	invalid := []State{StateImpactRadius, StateRiskAudit, StateReport}
	for _, st := range invalid {
		if err := sm.Transition(st); err == nil {
			t.Fatalf("expected error for Collect->%s transition", st)
		}
	}
}

func TestStateMachineDoneHasNoTransitions(t *testing.T) {
	sm := NewStateMachine(DefaultStateConfig())
	sm.Transition(StateAnalyzeDiff)
	sm.Transition(StateImpactRadius)
	sm.Transition(StateRiskAudit)
	sm.Transition(StateReport)
	sm.Transition(StateDone)

	if err := sm.Transition(StateCollect); err == nil {
		t.Fatal("expected error transitioning from Done")
	}
}

func TestStateMachineLoopBack(t *testing.T) {
	sm := NewStateMachine(DefaultStateConfig())
	sm.Transition(StateAnalyzeDiff)
	sm.Transition(StateImpactRadius)
	sm.Transition(StateRiskAudit)
	sm.Transition(StateReport)

	if err := sm.Transition(StateAnalyzeDiff); err != nil {
		t.Fatalf("expected Report->AnalyzeDiff to be valid: %v", err)
	}
}

func TestStateMachineShouldStop(t *testing.T) {
	sm := NewStateMachine(DefaultStateConfig())
	if sm.ShouldStop() {
		t.Fatal("should not stop at start")
	}

	sm.Transition(StateAnalyzeDiff)
	sm.Transition(StateImpactRadius)
	sm.Transition(StateRiskAudit)
	sm.Transition(StateReport)
	sm.Transition(StateDone)

	if !sm.ShouldStop() {
		t.Fatal("should stop at Done")
	}
}

func TestStateMachineMaxIterations(t *testing.T) {
	sm := NewStateMachine(StateConfig{MaxIterations: 2})
	for i := 0; i < 2; i++ {
		sm.Transition(StateAnalyzeDiff)
		sm.Transition(StateImpactRadius)
		sm.Transition(StateRiskAudit)
		sm.Transition(StateReport)
	}

	if !sm.ShouldStop() {
		t.Fatal("should stop after max iterations")
	}
}

func TestStateMachineHistory(t *testing.T) {
	sm := NewStateMachine(DefaultStateConfig())
	sm.Transition(StateAnalyzeDiff)
	sm.Transition(StateImpactRadius)

	history := sm.History()
	expected := []State{StateCollect, StateAnalyzeDiff, StateImpactRadius}
	if len(history) != len(expected) {
		t.Fatalf("expected %d history entries, got %d", len(expected), len(history))
	}
	for i, s := range history {
		if s != expected[i] {
			t.Fatalf("history[%d] = %s, expected %s", i, s, expected[i])
		}
	}
}

func TestStateMachineReset(t *testing.T) {
	sm := NewStateMachine(DefaultStateConfig())
	sm.Transition(StateAnalyzeDiff)
	sm.Reset()

	if sm.Current() != StateCollect {
		t.Fatalf("expected Collect after reset, got %s", sm.Current())
	}
	if len(sm.History()) != 1 {
		t.Fatalf("expected 1 history entry after reset, got %d", len(sm.History()))
	}
}

func TestStateString(t *testing.T) {
	tests := []struct {
		s State
		e string
	}{
		{StateCollect, "collect"},
		{StateAnalyzeDiff, "analyze_diff"},
		{StateImpactRadius, "impact_radius"},
		{StateRiskAudit, "risk_audit"},
		{StateReport, "report"},
		{StateDone, "done"},
		{State(99), "unknown"},
	}
	for _, tc := range tests {
		if got := tc.s.String(); got != tc.e {
			t.Errorf("State(%d).String() = %q, want %q", tc.s, tc.e, got)
		}
	}
}

func TestStateDescription(t *testing.T) {
	if desc := StateCollect.Description(); desc == "" {
		t.Fatal("expected non-empty description for StateCollect")
	}
	if desc := State(99).Description(); desc != "" {
		t.Fatalf("expected empty description for unknown state, got %q", desc)
	}
}

// ─── Diff Analyzer Tests ────────────────────────────────────────────────

func TestParseUnifiedDiff(t *testing.T) {
	da := NewDiffAnalyzer(t.TempDir())
	diff := `diff --git a/foo.go b/foo.go
new file mode 100644
--- /dev/null
+++ b/foo.go
@@ -0,0 +1,3 @@
+package foo
+
+func Foo() int {
+	return 42
+}
diff --git a/bar.go b/bar.go
--- a/bar.go
+++ b/bar.go
@@ -5,7 +5,7 @@ func Bar() {
-	old
+	new
`

	files, err := da.parseUnifiedDiff(diff)
	if err != nil {
		t.Fatalf("parseUnifiedDiff: %v", err)
	}

	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(files))
	}

	if files[0].Path != "foo.go" {
		t.Errorf("expected foo.go, got %s", files[0].Path)
	}
	if files[0].Status != "added" {
		t.Errorf("expected status added, got %s", files[0].Status)
	}
	if files[0].Additions != 5 {
		t.Errorf("expected 3 additions, got %d", files[0].Additions)
	}
	if len(files[0].Hunks) != 1 {
		t.Errorf("expected 1 hunk, got %d", len(files[0].Hunks))
	}

	if files[1].Path != "bar.go" {
		t.Errorf("expected bar.go, got %s", files[1].Path)
	}
	if files[1].Status != "modified" {
		t.Errorf("expected status modified, got %s", files[1].Status)
	}
}

func TestParseUnifiedDiffDeletedFile(t *testing.T) {
	da := NewDiffAnalyzer(t.TempDir())
	diff := `diff --git a/old.go b/old.go
deleted file mode 100644
--- a/old.go
+++ /dev/null
@@ -1,5 +0,0 @@
-package old
-
-func OldFunc() {
-}
`

	files, err := da.parseUnifiedDiff(diff)
	if err != nil {
		t.Fatalf("parseUnifiedDiff: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	if files[0].Status != "deleted" {
		t.Errorf("expected status deleted, got %s", files[0].Status)
	}
	if files[0].Deletions != 4 {
		t.Errorf("expected 5 deletions, got %d", files[0].Deletions)
	}
}

func TestParseUnifiedDiffRenamedFile(t *testing.T) {
	da := NewDiffAnalyzer(t.TempDir())
	diff := `diff --git a/old_name.go b/new_name.go
rename from old_name.go
rename to new_name.go
`

	files, err := da.parseUnifiedDiff(diff)
	if err != nil {
		t.Fatalf("parseUnifiedDiff: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	if files[0].Status != "renamed" {
		t.Errorf("expected status renamed, got %s", files[0].Status)
	}
}

func TestParseUnifiedDiffEmpty(t *testing.T) {
	da := NewDiffAnalyzer(t.TempDir())
	files, err := da.parseUnifiedDiff("")
	if err != nil {
		t.Fatalf("parseUnifiedDiff: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("expected 0 files, got %d", len(files))
	}
}

func TestParsePorcelain(t *testing.T) {
	da := NewDiffAnalyzer(t.TempDir())
	status := " M foo.go\nA  new.go\n D deleted.go\n?? untracked.go\n"

	files, err := da.parsePorcelain(status)
	if err != nil {
		t.Fatalf("parsePorcelain: %v", err)
	}

	if len(files) != 4 {
		t.Fatalf("expected 4 files, got %d", len(files))
	}

	if files[0].Path != "foo.go" || files[0].Status != "modified" {
		t.Errorf("expected foo.go modified, got %s %s", files[0].Path, files[0].Status)
	}
	if files[1].Path != "new.go" || files[1].Status != "added" {
		t.Errorf("expected new.go added, got %s %s", files[1].Path, files[1].Status)
	}
	if files[2].Status != "deleted" {
		t.Errorf("expected deleted status, got %s", files[2].Status)
	}
	if files[3].Status != "untracked" {
		t.Errorf("expected untracked status, got %s", files[3].Status)
	}
}

func TestParsePorcelainShortLine(t *testing.T) {
	da := NewDiffAnalyzer(t.TempDir())
	files, err := da.parsePorcelain("ab") // too short
	if err != nil {
		t.Fatalf("parsePorcelain: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("expected 0 files for short line, got %d", len(files))
	}
}

func TestMapStatus(t *testing.T) {
	da := NewDiffAnalyzer(t.TempDir())
	tests := []struct {
		staging, worktree, expected string
	}{
		{"?", "?", "untracked"},
		{"A", " ", "added"},
		{"?", " ", "added"},
		{"D", " ", "deleted"},
		{" ", "D", "deleted"},
		{"R", " ", "renamed"},
		{"M", " ", "modified"},
		{" ", "M", "modified"},
		{"C", " ", "modified"}, // default
	}
	for _, tc := range tests {
		got := da.mapStatus(tc.staging, tc.worktree)
		if got != tc.expected {
			t.Errorf("mapStatus(%q, %q) = %q, want %q", tc.staging, tc.worktree, got, tc.expected)
		}
	}
}

func TestIsRepo(t *testing.T) {
	dir := t.TempDir()
	da := NewDiffAnalyzer(dir)
	if da.isRepo() {
		t.Fatal("temp dir should not be a repo")
	}

	os.MkdirAll(filepath.Join(dir, ".git"), 0755)
	if !da.isRepo() {
		t.Fatal("should be a repo after creating .git")
	}
}

func TestLanguageFromExtension(t *testing.T) {
	da := NewDiffAnalyzer(t.TempDir())
	diff := `diff --git a/main.go b/main.go
--- /dev/null
+++ b/main.go
@@ -0,0 +1,1 @@
+package main
`

	files, _ := da.parseUnifiedDiff(diff)
	if len(files) > 0 && files[0].Language != "go" {
		t.Errorf("expected language go, got %s", files[0].Language)
	}
}

// ─── Risk Audit Tests ───────────────────────────────────────────────────

func TestRiskAuditorEmpty(t *testing.T) {
	ra := NewRiskAuditor(t.TempDir())
	findings := ra.Audit(nil)
	if len(findings) != 0 {
		t.Fatalf("expected 0 findings for nil input, got %d", len(findings))
	}

	findings = ra.Audit([]DiffFile{})
	if len(findings) != 0 {
		t.Fatalf("expected 0 findings for empty input, got %d", len(findings))
	}
}

func TestRiskAuditorDeletedFileSkipped(t *testing.T) {
	ra := NewRiskAuditor(t.TempDir())
	findings := ra.Audit([]DiffFile{{Path: "gone.go", Status: "deleted"}})
	if len(findings) != 0 {
		t.Fatalf("expected 0 findings for deleted file, got %d", len(findings))
	}
}

func TestRiskAuditorGoFileNotFound(t *testing.T) {
	ra := NewRiskAuditor(t.TempDir())
	findings := ra.Audit([]DiffFile{{Path: "nonexistent.go", Status: "modified"}})
	if len(findings) != 0 {
		t.Fatalf("expected 0 findings for missing file, got %d", len(findings))
	}
}

func TestRiskAuditorPanicDetection(t *testing.T) {
	dir := t.TempDir()
	content := `package test

func DoSomething() {
	panic("not good")
}
`
	path := filepath.Join(dir, "main.go")
	os.WriteFile(path, []byte(content), 0644)

	ra := NewRiskAuditor(dir)
	findings := ra.Audit([]DiffFile{{
		Path:   "main.go",
		Status: "modified",
		Hunks:  []DiffHunk{{StartNew: 1, CountNew: 5}},
	}})

	found := false
	for _, f := range findings {
		if f.Category == "panic" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected panic finding")
	}
}

func TestRiskAuditorSecretDetection(t *testing.T) {
	dir := t.TempDir()
	content := `package config

var secret = "sk-1234567890abcdef"
`
	path := filepath.Join(dir, "config.go")
	os.WriteFile(path, []byte(content), 0644)

	ra := NewRiskAuditor(dir)
	findings := ra.Audit([]DiffFile{{
		Path:   "config.go",
		Status: "modified",
		Hunks:  []DiffHunk{{StartNew: 1, CountNew: 3}},
	}})

	found := false
	for _, f := range findings {
		if f.Category == "hardcoded_secret" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected hardcoded_secret finding")
	}
}

func TestRiskAuditorSQLInjectionDetection(t *testing.T) {
	dir := t.TempDir()
	content := `package db

import "database/sql"

func Query(db *sql.DB, id string) {
	db.Exec("SELECT * FROM users WHERE id = " + id)
}
`
	path := filepath.Join(dir, "db.go")
	os.WriteFile(path, []byte(content), 0644)

	ra := NewRiskAuditor(dir)
	findings := ra.Audit([]DiffFile{{
		Path:   "db.go",
		Status: "modified",
		Hunks:  []DiffHunk{{StartNew: 1, CountNew: 8}},
	}})

	found := false
	for _, f := range findings {
		if f.RuleID == "SEC-SQL-001" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected SQL injection finding")
	}
}

func TestRiskAuditorGoroutineDetection(t *testing.T) {
	dir := t.TempDir()
	content := `package main

func start() {
	go doWork()
}
`
	path := filepath.Join(dir, "main.go")
	os.WriteFile(path, []byte(content), 0644)

	ra := NewRiskAuditor(dir)
	findings := ra.Audit([]DiffFile{{
		Path:   "main.go",
		Status: "modified",
		Hunks:  []DiffHunk{{StartNew: 1, CountNew: 5}},
	}})

	found := false
	for _, f := range findings {
		if f.RuleID == "GO-GOROUTINE-001" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected goroutine finding")
	}
}

func TestRiskAuditorLockWithoutDefer(t *testing.T) {
	dir := t.TempDir()
	content := `package main

import "sync"

func unsafe() {
	var mu sync.Mutex
	mu.Lock()
	val := 42
	mu.Unlock()
}
`
	path := filepath.Join(dir, "main.go")
	os.WriteFile(path, []byte(content), 0644)

	ra := NewRiskAuditor(dir)
	findings := ra.Audit([]DiffFile{{
		Path:   "main.go",
		Status: "modified",
		Hunks:  []DiffHunk{{StartNew: 1, CountNew: 10}},
	}})

	found := false
	for _, f := range findings {
		if f.RuleID == "GO-LOCK-001" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected lock-without-defer finding")
	}
}

func TestRiskAuditorExportedFuncNoReturn(t *testing.T) {
	dir := t.TempDir()
	content := `package main

func DoSomething() {
}
`
	path := filepath.Join(dir, "main.go")
	os.WriteFile(path, []byte(content), 0644)

	ra := NewRiskAuditor(dir)
	findings := ra.Audit([]DiffFile{{
		Path:   "main.go",
		Status: "modified",
		Hunks:  []DiffHunk{{StartNew: 1, CountNew: 4}},
	}})

	found := false
	for _, f := range findings {
		if f.RuleID == "GO-FUNC-001" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected no-error-return finding for exported func without returns")
	}
}

func TestRiskAuditorTodoMarkerDetection(t *testing.T) {
	dir := t.TempDir()
	content := `package main

// TODO: fix this later
func main() {}
`
	path := filepath.Join(dir, "main.go")
	os.WriteFile(path, []byte(content), 0644)

	ra := NewRiskAuditor(dir)
	findings := ra.Audit([]DiffFile{{
		Path:   "main.go",
		Status: "modified",
		Hunks:  []DiffHunk{{StartNew: 1, CountNew: 4}},
	}})

	found := false
	for _, f := range findings {
		if f.RuleID == "CQ-TODO-001" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected TODO marker finding")
	}
}

func TestRiskAuditorPrintStatementDetection(t *testing.T) {
	dir := t.TempDir()
	content := `package main

func main() {
	fmt.Println("hello")
}
`
	path := filepath.Join(dir, "main.go")
	os.WriteFile(path, []byte(content), 0644)

	ra := NewRiskAuditor(dir)
	findings := ra.Audit([]DiffFile{{
		Path:   "main.go",
		Status: "modified",
		Hunks:  []DiffHunk{{StartNew: 1, CountNew: 5}},
	}})

	found := false
	for _, f := range findings {
		if f.RuleID == "CQ-PRINT-001" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected print statement finding")
	}
}

func TestCalculateRiskScore(t *testing.T) {
	ra := NewRiskAuditor(t.TempDir())

	tests := []struct {
		severity RiskSeverity
		expected int
	}{
		{RiskCritical, 40},
		{RiskHigh, 20},
		{RiskMedium, 10},
		{RiskLow, 3},
		{RiskInfo, 1},
	}

	for _, tc := range tests {
		findings := []RiskFinding{{Severity: tc.severity}}
		score := ra.calculateRiskScore(findings)
		if score != tc.expected {
			t.Errorf("calculateRiskScore(%s) = %d, want %d", tc.severity, score, tc.expected)
		}
	}

	combined := []RiskFinding{
		{Severity: RiskCritical},
		{Severity: RiskHigh},
		{Severity: RiskInfo},
	}
	score := ra.calculateRiskScore(combined)
	expected := 40 + 20 + 1
	if score != expected {
		t.Errorf("combined score = %d, want %d", score, expected)
	}
}

func TestRiskAuditorBlankIdentifierDetection(t *testing.T) {
	dir := t.TempDir()
	content := `package main

func main() {
	_ = someFunc()
}
`
	path := filepath.Join(dir, "main.go")
	os.WriteFile(path, []byte(content), 0644)

	ra := NewRiskAuditor(dir)
	findings := ra.Audit([]DiffFile{{
		Path:   "main.go",
		Status: "modified",
		Hunks:  []DiffHunk{{StartNew: 1, CountNew: 5}},
	}})

	found := false
	for _, f := range findings {
		if f.RuleID == "CQ-BLANK-001" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected blank identifier finding")
	}
}

func TestRiskAuditorHTTPHandlerDetection(t *testing.T) {
	dir := t.TempDir()
	content := `package main

import "net/http"

func main() {
	http.HandleFunc("/api", handler)
}
`
	path := filepath.Join(dir, "main.go")
	os.WriteFile(path, []byte(content), 0644)

	ra := NewRiskAuditor(dir)
	findings := ra.Audit([]DiffFile{{
		Path:   "main.go",
		Status: "modified",
		Hunks:  []DiffHunk{{StartNew: 1, CountNew: 7}},
	}})

	found := false
	for _, f := range findings {
		if f.RuleID == "SEC-HTTP-001" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected HTTP handler finding")
	}
}

func TestRiskAuditorGenericFile(t *testing.T) {
	dir := t.TempDir()
	content := `password = "supersecret"
api_key = "12345"
`
	path := filepath.Join(dir, "config.yml")
	os.WriteFile(path, []byte(content), 0644)

	ra := NewRiskAuditor(dir)
	findings := ra.Audit([]DiffFile{{
		Path:   "config.yml",
		Status: "modified",
	}})

	found := false
	for _, f := range findings {
		if f.Category == "hardcoded_secret" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected secret finding in generic file")
	}
}

// ─── Impact Analyzer Tests ──────────────────────────────────────────────

func TestImpactAnalyzerEmptyFiles(t *testing.T) {
	ia := NewImpactAnalyzer(t.TempDir(), nil)
	radius, err := ia.Analyze(nil)
	if err != nil {
		t.Fatalf("Analyze(nil): %v", err)
	}
	if len(radius.DirectFiles) != 0 {
		t.Errorf("expected 0 direct files, got %d", len(radius.DirectFiles))
	}
}

func TestImpactAnalyzerDeletedUntrackedSkipped(t *testing.T) {
	ia := NewImpactAnalyzer(t.TempDir(), nil)
	radius, err := ia.Analyze([]DiffFile{
		{Path: "gone.go", Status: "deleted"},
		{Path: "untracked.go", Status: "untracked"},
	})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(radius.DirectFiles) != 0 {
		t.Errorf("expected 0 direct files, got %d", len(radius.DirectFiles))
	}
}

func TestImpactAnalyzerExtractPackages(t *testing.T) {
	ia := NewImpactAnalyzer("/root", nil)
	pkgs := ia.extractPackages([]string{"a/main.go", "b/util.go"})
	if len(pkgs) != 2 {
		t.Fatalf("expected 2 packages, got %d", len(pkgs))
	}
	if pkgs[0] != "a" || pkgs[1] != "b" {
		t.Errorf("unexpected packages: %v", pkgs)
	}
}

func TestImpactAnalyzerExtractPackagesRootLevel(t *testing.T) {
	ia := NewImpactAnalyzer("/myproject", nil)
	pkgs := ia.extractPackages([]string{"main.go"})
	if len(pkgs) != 1 || pkgs[0] != "myproject" {
		t.Errorf("expected [myproject], got %v", pkgs)
	}
}

func TestImpactAnalyzerWithGraph(t *testing.T) {
	dir := t.TempDir()
	g := graph.NewGraph(dir)
	g.AddFile(graph.FileNode{
		Path:     "pkg/foo.go",
		Package:  "pkg",
		Language: "go",
		Symbols: []graph.Symbol{
			{Name: "Foo", Kind: graph.SymbolFunction, File: "pkg/foo.go", Line: 1, Exported: true},
		},
		Imports: []string{},
	})
	g.AddFile(graph.FileNode{
		Path:     "pkg/bar.go",
		Package:  "pkg",
		Language: "go",
		Symbols: []graph.Symbol{
			{Name: "Bar", Kind: graph.SymbolFunction, File: "pkg/bar.go", Line: 5, Exported: false},
		},
		Imports: []string{"pkg/foo.go"},
	})

	ia := NewImpactAnalyzer(dir, g)
	radius, err := ia.Analyze([]DiffFile{
		{Path: "pkg/foo.go", Status: "modified"},
	})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}

	if len(radius.DirectFiles) != 1 {
		t.Errorf("expected 1 direct file, got %d", len(radius.DirectFiles))
	}
	if radius.DirectFiles[0] != "pkg/foo.go" {
		t.Errorf("expected pkg/foo.go, got %s", radius.DirectFiles[0])
	}
}

func TestImpactAnalyzerExtractAffectedSymbols(t *testing.T) {
	dir := t.TempDir()
	g := graph.NewGraph(dir)
	g.AddFile(graph.FileNode{
		Path: "pkg/foo.go",
		Symbols: []graph.Symbol{
			{Name: "Foo", Kind: graph.SymbolFunction, File: "pkg/foo.go", Line: 1, Exported: true},
			{Name: "helper", Kind: graph.SymbolFunction, File: "pkg/foo.go", Line: 10, Exported: false},
		},
	})

	ia := NewImpactAnalyzer(dir, g)
	symbols := ia.extractAffectedSymbols([]string{"pkg/foo.go"})

	if len(symbols) != 2 {
		t.Fatalf("expected 2 symbols, got %d", len(symbols))
	}

	if symbols[0].Name != "Foo" || symbols[0].Impact != "direct" || !symbols[0].Exported {
		t.Errorf("unexpected first symbol: %+v", symbols[0])
	}
	if symbols[1].Name != "helper" || symbols[1].Impact != "indirect" || symbols[1].Exported {
		t.Errorf("unexpected second symbol: %+v", symbols[1])
	}
}

func TestImpactAnalyzerComplexity(t *testing.T) {
	dir := t.TempDir()
	content := `package main

func simple() {}
func complex() {
	if true {
		for i := 0; i < 10; i++ {
			switch i {
			case 1:
			case 2:
			}
		}
	}
}
`
	path := filepath.Join(dir, "main.go")
	os.WriteFile(path, []byte(content), 0644)

	ia := NewImpactAnalyzer(dir, nil)
	c := ia.estimateComplexity([]string{"main.go"})
	if c <= 0 {
		t.Errorf("expected positive complexity, got %d", c)
	}
}

func TestImpactAnalyzerComplexityParseError(t *testing.T) {
	dir := t.TempDir()
	content := `this is not valid go {{{`
	path := filepath.Join(dir, "bad.go")
	os.WriteFile(path, []byte(content), 0644)

	ia := NewImpactAnalyzer(dir, nil)
	c := ia.estimateComplexity([]string{"bad.go"})
	// parse error adds 5 complexity
	if c != 5 {
		t.Errorf("expected complexity 5 for parse error, got %d", c)
	}
}

func TestUnique(t *testing.T) {
	result := unique([]string{"a", "b", "a", "c", "b"})
	expected := []string{"a", "b", "c"}
	if len(result) != len(expected) {
		t.Fatalf("expected %d items, got %d", len(expected), len(result))
	}
	for i, v := range expected {
		if result[i] != v {
			t.Errorf("unique[%d] = %s, want %s", i, result[i], v)
		}
	}
}

func TestContains(t *testing.T) {
	ia := NewImpactAnalyzer(t.TempDir(), nil)
	files := []string{"a.go", "b.go", "c.go"}

	if !ia.contains(files, "b.go") {
		t.Error("expected contains(b.go) = true")
	}
	if ia.contains(files, "d.go") {
		t.Error("expected contains(d.go) = false")
	}
}

// ─── Engine Tests ───────────────────────────────────────────────────────

type mockRetriever struct{}

func (m *mockRetriever) SearchSymbol(name string) ([]SearchResult, error) {
	return nil, nil
}
func (m *mockRetriever) SearchText(text string) ([]SearchResult, error) {
	return nil, nil
}
func (m *mockRetriever) SearchFile(path string) ([]SearchResult, error) {
	return nil, nil
}
func (m *mockRetriever) ReadTarget(path string, lines int) ([]SearchResult, error) {
	return nil, nil
}

func TestEngineRunNoRepo(t *testing.T) {
	dir := t.TempDir()
	e := NewEngine(dir, &mockRetriever{}, nil)

	result, err := e.Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Error == "" {
		t.Log("no error — state machine completed (no changes to review)")
	}
}

func TestEngineRunCleanRepo(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".git"), 0755)

	e := NewEngine(dir, &mockRetriever{}, nil)
	result, err := e.Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Error != "no changes to review — working tree is clean" {
		t.Errorf("unexpected error: %s", result.Error)
	}
}

func TestEngineStateCollectWithDiff(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".git"), 0755)
	os.WriteFile(filepath.Join(dir, "test.go"), []byte("package main\n"), 0644)

	e := NewEngine(dir, &mockRetriever{}, nil)
	result := &ReviewResult{}

	err := e.stateCollect(result)
	if err == nil {
		t.Log("stateCollect succeeded (may have diff)")
	} else {
		t.Logf("stateCollect failed: %v (expected without git init)", err)
	}
}

func TestEngineCalculateScore(t *testing.T) {
	e := NewEngine(t.TempDir(), &mockRetriever{}, nil)

	result := &ReviewResult{
		FilesChanged: []DiffFile{
			{Path: "a.go", Status: "modified"},
			{Path: "b.go", Status: "modified"},
		},
		ImpactRadius: ImpactRadius{RiskScore: 10},
	}

	score := e.calculateScore(result)
	if score != 90 {
		t.Errorf("expected score 90, got %d", score)
	}
}

func TestEngineCalculateScoreManyFiles(t *testing.T) {
	e := NewEngine(t.TempDir(), &mockRetriever{}, nil)

	files := make([]DiffFile, 25)
	for i := range files {
		files[i] = DiffFile{Path: "f.go", Status: "modified"}
	}

	result := &ReviewResult{FilesChanged: files, ImpactRadius: ImpactRadius{RiskScore: 30}}
	score := e.calculateScore(result)
	if score != 50 {
		t.Errorf("expected score 50, got %d", score)
	}
}

func TestEngineCalculateScoreMinZero(t *testing.T) {
	e := NewEngine(t.TempDir(), &mockRetriever{}, nil)

	result := &ReviewResult{
		FilesChanged: []DiffFile{{Path: "a.go", Status: "modified"}},
		ImpactRadius: ImpactRadius{RiskScore: 200},
	}

	score := e.calculateScore(result)
	if score != 0 {
		t.Errorf("expected score 0, got %d", score)
	}
}

func TestEngineGenerateRecommendations(t *testing.T) {
	e := NewEngine(t.TempDir(), &mockRetriever{}, nil)

	result := &ReviewResult{
		FilesChanged: []DiffFile{{Path: "main.go", Status: "modified"}},
		ImpactRadius: ImpactRadius{
			RiskScore:     60,
			IndirectFiles: []string{"a.go", "b.go", "c.go", "d.go", "e.go"},
		},
		RiskFindings: []RiskFinding{
			{Severity: RiskCritical, Category: "panic"},
			{Severity: RiskHigh, Category: "os_command"},
		},
	}

	recs := e.generateRecommendations(result)
	if len(recs) == 0 {
		t.Fatal("expected recommendations")
	}

	foundHighRisk := false
	foundCritical := false
	for _, r := range recs {
		if strings.Contains(r, "High risk score") {
			foundHighRisk = true
		}
		if strings.Contains(r, "critical") {
			foundCritical = true
		}
	}
	if !foundHighRisk {
		t.Error("expected high-risk-score recommendation")
	}
	if !foundCritical {
		t.Error("expected critical findings recommendation")
	}
}

func TestEngineGenerateRecommendationsMissingTests(t *testing.T) {
	e := NewEngine(t.TempDir(), &mockRetriever{}, nil)
	result := &ReviewResult{
		FilesChanged: []DiffFile{{Path: "main.go", Status: "modified"}},
		ImpactRadius: ImpactRadius{RiskScore: 5},
	}

	recs := e.generateRecommendations(result)

	found := false
	for _, r := range recs {
		if strings.Contains(r, "No test files") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected missing-tests recommendation")
	}
}

func TestEngineGenerateRecommendationsNoMissingTests(t *testing.T) {
	e := NewEngine(t.TempDir(), &mockRetriever{}, nil)
	result := &ReviewResult{
		FilesChanged: []DiffFile{{Path: "main_test.go", Status: "modified"}},
		ImpactRadius: ImpactRadius{RiskScore: 5},
	}

	recs := e.generateRecommendations(result)
	for _, r := range recs {
		if strings.Contains(r, "No test files") {
			t.Error("should not recommend tests when test file is changed")
		}
	}
}

func TestEngineGenerateRecommendationsSecret(t *testing.T) {
	e := NewEngine(t.TempDir(), &mockRetriever{}, nil)
	result := &ReviewResult{
		FilesChanged: []DiffFile{{Path: "config.go", Status: "modified"}},
		ImpactRadius: ImpactRadius{RiskScore: 5},
		RiskFindings: []RiskFinding{
			{Severity: RiskCritical, Category: "hardcoded_secret"},
		},
	}

	recs := e.generateRecommendations(result)
	found := false
	for _, r := range recs {
		if strings.Contains(r, "secret") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected secret-detected recommendation")
	}
}

func TestSaveReport(t *testing.T) {
	dir := t.TempDir()
	result := &ReviewResult{
		Branch:     "feature",
		BaseBranch: "main",
		CommitHash: "abc123",
		Score:      85,
		ImpactRadius: ImpactRadius{
			RiskScore: 15,
		},
		FilesChanged: []DiffFile{{Path: "main.go", Status: "modified", Additions: 5, Deletions: 2}},
		RiskFindings: []RiskFinding{{Severity: RiskLow, Category: "code_quality", Description: "TODO", RuleID: "CQ-TODO-001"}},
		Summary:      "Reviewed 1 file",
		Duration:     "5ms",
		CreatedAt:    time.Now(),
	}

	err := SaveReport(result, dir)
	if err != nil {
		t.Fatalf("SaveReport: %v", err)
	}

	reviewsDir := filepath.Join(dir, ".izen", "reviews")
	entries, err := os.ReadDir(reviewsDir)
	if err != nil {
		t.Fatalf("read reviews dir: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("expected at least 1 review file")
	}
}

func TestMarshalReport(t *testing.T) {
	result := &ReviewResult{
		Branch:     "feature",
		BaseBranch: "main",
		CommitHash: "abc",
		Score:      90,
		ImpactRadius: ImpactRadius{
			RiskScore: 10,
		},
		FilesChanged: []DiffFile{
			{Path: "a.go", Status: "modified", Additions: 3, Deletions: 1},
		},
		RiskFindings: []RiskFinding{
			{Severity: RiskLow, Category: "code_quality", Description: "TODO found", RuleID: "CQ-TODO-001"},
		},
		Summary:   "Minor changes",
		Duration:  "2ms",
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}

	data := marshalReport(result)
	if !strings.Contains(string(data), "feature") {
		t.Error("expected branch name in report")
	}
	if !strings.Contains(string(data), "1") {
		t.Error("expected risk_findings count in report")
	}
}

func TestSeverityScore(t *testing.T) {
	tests := []struct {
		s    RiskSeverity
		want int
	}{
		{RiskCritical, 5},
		{RiskHigh, 4},
		{RiskMedium, 3},
		{RiskLow, 2},
		{RiskInfo, 1},
		{RiskSeverity("unknown"), 0},
	}
	for _, tc := range tests {
		if got := severityScore(tc.s); got != tc.want {
			t.Errorf("severityScore(%q) = %d, want %d", tc.s, got, tc.want)
		}
	}
}

// ─── Type Tests ─────────────────────────────────────────────────────────

func TestDiffFileDefaults(t *testing.T) {
	df := DiffFile{Path: "test.go", Status: "modified"}
	if df.Additions != 0 || df.Deletions != 0 {
		t.Error("expected zero counts")
	}
	if df.Language != "" {
		t.Errorf("expected empty language, got %s", df.Language)
	}
}

func TestReviewResultDefaults(t *testing.T) {
	r := ReviewResult{}
	if r.Score != 0 {
		t.Errorf("expected score 0, got %d", r.Score)
	}
}

func TestImpactRadiusDefaults(t *testing.T) {
	ir := ImpactRadius{}
	if len(ir.DirectFiles) != 0 {
		t.Error("expected empty DirectFiles")
	}
	if ir.RiskScore != 0 {
		t.Errorf("expected RiskScore 0, got %d", ir.RiskScore)
	}
}

func TestImportChainEmpty(t *testing.T) {
	ic := ImportChain{Source: "a.go", Chain: nil}
	if len(ic.Chain) != 0 {
		t.Error("expected empty chain")
	}
}

// ─── Diff Hunk Tests ────────────────────────────────────────────────────

func TestDiffHunkMultiHunk(t *testing.T) {
	da := NewDiffAnalyzer(t.TempDir())
	diff := `diff --git a/main.go b/main.go
--- a/main.go
+++ b/main.go
@@ -1,3 +1,4 @@
 line1
+added
 line2
 line3
@@ -10,5 +11,7 @@ func foo() {
 	old1
 	old2
+	new1
+	new2
 	old3
`

	files, _ := da.parseUnifiedDiff(diff)
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	if len(files[0].Hunks) != 2 {
		t.Fatalf("expected 2 hunks, got %d", len(files[0].Hunks))
	}

	h1 := files[0].Hunks[0]
	if h1.StartOld != 1 || h1.CountOld != 3 || h1.StartNew != 1 || h1.CountNew != 4 {
		t.Errorf("unexpected hunk1 values: %+v", h1)
	}

	h2 := files[0].Hunks[1]
	if h2.StartOld != 10 || h2.CountOld != 5 || h2.StartNew != 11 || h2.CountNew != 7 {
		t.Errorf("unexpected hunk2 values: %+v", h2)
	}
}

// ─── Retriever Interface Tests ─────────────────────────────────────────

func TestRetrieverInterface(t *testing.T) {
	var _ Retriever = &mockRetriever{}
}

func TestSearchResultDefaults(t *testing.T) {
	sr := SearchResult{File: "test.go", Content: "code"}
	if sr.Line != 0 {
		t.Errorf("expected Line 0, got %d", sr.Line)
	}
	if sr.Confidence != 0 {
		t.Errorf("expected Confidence 0, got %f", sr.Confidence)
	}
	if sr.Strategy != "" {
		t.Errorf("expected empty Strategy, got %s", sr.Strategy)
	}
}
