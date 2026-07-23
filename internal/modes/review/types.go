package review

import (
	"time"

	riview "github.com/PizenLabs/izen/internal/review"
)

type State int

const (
	StateCollect State = iota
	StateAnalyzeDiff
	StateImpactRadius
	StateRiskAudit
	StateVerify
	StateReport
	StateDone
)

func (s State) String() string {
	switch s {
	case StateCollect:
		return "collect"
	case StateAnalyzeDiff:
		return "analyze_diff"
	case StateImpactRadius:
		return "impact_radius"
	case StateRiskAudit:
		return "risk_audit"
	case StateVerify:
		return "verify"
	case StateReport:
		return "report"
	case StateDone:
		return "done"
	default:
		return "unknown"
	}
}

func (s State) Description() string {
	switch s {
	case StateCollect:
		return "Collect git diff / status information"
	case StateAnalyzeDiff:
		return "Parse diff and identify changed files and symbols"
	case StateImpactRadius:
		return "Trace the impact radius of changes through the codebase"
	case StateRiskAudit:
		return "Run AST validation and risk detection on changed files"
	case StateVerify:
		return "Run evidence-driven verification on risk findings"
	case StateReport:
		return "Generate comprehensive review report"
	case StateDone:
		return "Review complete"
	default:
		return ""
	}
}

type DiffFile struct {
	Path      string     `json:"path"`
	Status    string     `json:"status"`
	Additions int        `json:"additions"`
	Deletions int        `json:"deletions"`
	Hunks     []DiffHunk `json:"hunks,omitempty"`
	Language  string     `json:"language,omitempty"`
}

type DiffHunk struct {
	StartOld int    `json:"start_old"`
	StartNew int    `json:"start_new"`
	CountOld int    `json:"count_old"`
	CountNew int    `json:"count_new"`
	Content  string `json:"content"`
}

type AffectedSymbol struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	File     string `json:"file"`
	Line     int    `json:"line"`
	Impact   string `json:"impact"`
	Exported bool   `json:"exported"`
}

type ImpactRadius struct {
	DirectFiles   []string         `json:"direct_files"`
	IndirectFiles []string         `json:"indirect_files"`
	AffectedPkgs  []string         `json:"affected_pkgs"`
	AffectedSyms  []AffectedSymbol `json:"affected_syms"`
	ImportChains  []ImportChain    `json:"import_chains,omitempty"`
	CallChains    []CallChain      `json:"call_chains,omitempty"`
	RiskScore     int              `json:"risk_score"`
	Complexity    int              `json:"complexity"`
}

type ImportChain struct {
	Source string   `json:"source"`
	Chain  []string `json:"chain"`
}

// CallChain represents a downstream caller trace from a modified file.
// Source is the modified file; Callers lists every file that directly or
// transitively depends on it, forming an explicit regression-risk trace.
type CallChain struct {
	Source  string   `json:"source"`
	Callers []string `json:"callers"`
}

type RiskSeverity string

const (
	RiskCritical RiskSeverity = "critical"
	RiskHigh     RiskSeverity = "high"
	RiskMedium   RiskSeverity = "medium"
	RiskLow      RiskSeverity = "low"
	RiskInfo     RiskSeverity = "info"
)

type RiskFinding struct {
	File        string       `json:"file"`
	Line        int          `json:"line"`
	Column      int          `json:"column"`
	Severity    RiskSeverity `json:"severity"`
	Category    string       `json:"category"`
	Code        string       `json:"code"`
	Description string       `json:"description"`
	Suggestion  string       `json:"suggestion,omitempty"`
	RuleID      string       `json:"rule_id,omitempty"`
}

type ReviewResult struct {
	Branch          string               `json:"branch"`
	BaseBranch      string               `json:"base_branch"`
	CommitHash      string               `json:"commit_hash"`
	Commits         int                  `json:"commits"`
	FilesChanged    []DiffFile           `json:"files_changed"`
	ImpactRadius    ImpactRadius         `json:"impact_radius"`
	RiskFindings    []RiskFinding        `json:"risk_findings"`
	Ledger          *riview.ReviewLedger `json:"ledger,omitempty"`
	Summary         string               `json:"summary"`
	Score           int                  `json:"score"`
	Recommendations []string             `json:"recommendations"`
	States          []State              `json:"states"`
	Duration        string               `json:"duration"`
	Error           string               `json:"error,omitempty"`
	CreatedAt       time.Time            `json:"created_at"`
}

type Retriever interface {
	SearchSymbol(name string) ([]SearchResult, error)
	SearchText(text string) ([]SearchResult, error)
	SearchFile(path string) ([]SearchResult, error)
	ReadTarget(path string, lines int) ([]SearchResult, error)
}

type SearchResult struct {
	File       string  `json:"file"`
	Line       int     `json:"line"`
	Content    string  `json:"content"`
	Confidence float64 `json:"confidence"`
	Strategy   string  `json:"strategy"`
}
