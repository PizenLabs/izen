package review

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

type RecordType string

const (
	RecordChange       RecordType = "C"
	RecordRisk         RecordType = "R"
	RecordHypothesis   RecordType = "H"
	RecordVerification RecordType = "V"
	RecordEvidence     RecordType = "E"
)

type EvidenceStatus string

const (
	EvStatusPassed     EvidenceStatus = "Passed"
	EvStatusFailed     EvidenceStatus = "Failed"
	EvStatusPanicked   EvidenceStatus = "Panicked"
	EvStatusSkipped    EvidenceStatus = "Skipped"
	EvStatusUnresolved EvidenceStatus = "Unresolved"
)

type EvidenceConfidence string

const (
	ConfVerified    EvidenceConfidence = "Verified"
	ConfHigh        EvidenceConfidence = "High"
	ConfMedium      EvidenceConfidence = "Medium"
	ConfLow         EvidenceConfidence = "Low"
	ConfSpeculative EvidenceConfidence = "Speculative"
)

type EvidenceType string

const (
	EvTypeExistingTest   EvidenceType = "Existing Test"
	EvTypeEphemeralTest  EvidenceType = "Ephemeral Sandbox"
	EvTypeStaticAnalysis EvidenceType = "Static Analysis"
	EvTypeManualCheck    EvidenceType = "Manual Check"
)

type ReviewLedger struct {
	mu            sync.Mutex
	Changes       []ChangeRecord       `json:"changes"`
	Risks         []RiskRecord         `json:"risks"`
	Hypotheses    []HypothesisRecord   `json:"hypotheses"`
	Verifications []VerificationRecord `json:"verifications"`
	Evidences     []EvidenceRecord     `json:"evidences"`
	Status        ReviewStatus         `json:"status"`
	ReviewID      string               `json:"review_id"`
	CreatedAt     time.Time            `json:"created_at"`
}

type ReviewStatus string

const (
	StatusConditional ReviewStatus = "Conditional"
	StatusUnresolved  ReviewStatus = "Unresolved"
	StatusVerified    ReviewStatus = "Verified"
)

type ChangeRecord struct {
	ID      string `json:"id"`
	File    string `json:"file"`
	Snippet string `json:"snippet"`
	Actor   string `json:"actor"`
	Seq     int    `json:"seq"`
}

type RiskRecord struct {
	ID       string `json:"id"`
	Category string `json:"category"`
	File     string `json:"file"`
	Line     int    `json:"line"`
	Desc     string `json:"desc"`
	Seq      int    `json:"seq"`
}

type HypothesisRecord struct {
	ID                string `json:"id"`
	RiskID            string `json:"risk_id"`
	Hypothesis        string `json:"hypothesis"`
	ExpectedInvariant string `json:"expected_invariant"`
	Seq               int    `json:"seq"`
}

type VerificationRecord struct {
	ID           string `json:"id"`
	HypothesisID string `json:"hypothesis_id"`
	Plan         string `json:"plan"`
	Seq          int    `json:"seq"`
}

type EvidenceRecord struct {
	ID             string             `json:"id"`
	VerificationID string             `json:"verification_id"`
	Type           EvidenceType       `json:"type"`
	Status         EvidenceStatus     `json:"status"`
	Confidence     EvidenceConfidence `json:"confidence"`
	ArtifactRef    string             `json:"artifact_ref,omitempty"`
	Output         string             `json:"output,omitempty"`
	Seq            int                `json:"seq"`
}

func NewReviewLedger(reviewID string) *ReviewLedger {
	return &ReviewLedger{
		ReviewID:  reviewID,
		CreatedAt: time.Now(),
		Status:    StatusUnresolved,
	}
}

func (l *ReviewLedger) AddChange(file, snippet, actor string) ChangeRecord {
	l.mu.Lock()
	defer l.mu.Unlock()
	seq := len(l.Changes) + 1
	rec := ChangeRecord{
		ID:      fmt.Sprintf("C-%03d", seq),
		File:    file,
		Snippet: snippet,
		Actor:   actor,
		Seq:     seq,
	}
	l.Changes = append(l.Changes, rec)
	return rec
}

func (l *ReviewLedger) AddRisk(category, file string, line int, desc string) RiskRecord {
	l.mu.Lock()
	defer l.mu.Unlock()
	seq := len(l.Risks) + 1
	rec := RiskRecord{
		ID:       fmt.Sprintf("R-%03d", seq),
		Category: category,
		File:     file,
		Line:     line,
		Desc:     desc,
		Seq:      seq,
	}
	l.Risks = append(l.Risks, rec)
	return rec
}

func (l *ReviewLedger) AddHypothesis(riskID, hypothesis, invariant string) HypothesisRecord {
	l.mu.Lock()
	defer l.mu.Unlock()
	seq := len(l.Hypotheses) + 1
	rec := HypothesisRecord{
		ID:                fmt.Sprintf("H-%03d", seq),
		RiskID:            riskID,
		Hypothesis:        hypothesis,
		ExpectedInvariant: invariant,
		Seq:               seq,
	}
	l.Hypotheses = append(l.Hypotheses, rec)
	return rec
}

func (l *ReviewLedger) AddVerification(hypothesisID, plan string) VerificationRecord {
	l.mu.Lock()
	defer l.mu.Unlock()
	seq := len(l.Verifications) + 1
	rec := VerificationRecord{
		ID:           fmt.Sprintf("V-%03d", seq),
		HypothesisID: hypothesisID,
		Plan:         plan,
		Seq:          seq,
	}
	l.Verifications = append(l.Verifications, rec)
	return rec
}

func (l *ReviewLedger) AddEvidence(verificationID string, evType EvidenceType, status EvidenceStatus, confidence EvidenceConfidence, artifactRef, output string) EvidenceRecord {
	l.mu.Lock()
	defer l.mu.Unlock()
	seq := len(l.Evidences) + 1
	rec := EvidenceRecord{
		ID:             fmt.Sprintf("E-%03d", seq),
		VerificationID: verificationID,
		Type:           evType,
		Status:         status,
		Confidence:     confidence,
		ArtifactRef:    artifactRef,
		Output:         output,
		Seq:            seq,
	}
	l.Evidences = append(l.Evidences, rec)
	return rec
}

func (l *ReviewLedger) SetStatus(s ReviewStatus) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.Status = s
}

func (l *ReviewLedger) ActiveEvidenceIDs() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	ids := make([]string, 0, len(l.Evidences))
	for _, e := range l.Evidences {
		ids = append(ids, e.ID)
	}
	return strings.Join(ids, ", ")
}

func (l *ReviewLedger) FormatCompact() string {
	l.mu.Lock()
	defer l.mu.Unlock()

	var b strings.Builder
	b.WriteString("Review Ledger\n")
	b.WriteString("─────────────\n")

	for _, c := range l.Changes {
		snippet := c.Snippet
		if len(snippet) > 60 {
			snippet = snippet[:57] + "..."
		}
		fmt.Fprintf(&b, "%s  Change: %s (%s)\n", c.ID, c.File, snippet)
	}
	for _, r := range l.Risks {
		loc := r.File
		if r.Line > 0 {
			loc = fmt.Sprintf("%s:%d", r.File, r.Line)
		}
		fmt.Fprintf(&b, "%s  Risk [%s]: %s\n", r.ID, r.Category, loc)
	}
	for _, h := range l.Hypotheses {
		fmt.Fprintf(&b, "%s  Hypothesis: %s\n", h.ID, h.Hypothesis)
	}
	for _, v := range l.Verifications {
		fmt.Fprintf(&b, "%s  Plan: %s\n", v.ID, v.Plan)
	}
	for _, e := range l.Evidences {
		fmt.Fprintf(&b, "%s  Evidence [%s]: %s (Confidence: %s)\n", e.ID, string(e.Type), string(e.Status), string(e.Confidence))
	}

	fmt.Fprintf(&b, "\nReview Status: %s", string(l.Status))
	unsupported := l.countUnresolved()
	if unsupported > 0 {
		fmt.Fprintf(&b, " (%d risk(s) require manual check)", unsupported)
	}
	b.WriteString("\n")

	return b.String()
}

func (l *ReviewLedger) countUnresolved() int {
	count := 0
	for _, e := range l.Evidences {
		if e.Status == EvStatusUnresolved {
			count++
		}
	}
	return count
}

func (l *ReviewLedger) AllChangeIDs() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	ids := make([]string, 0, len(l.Changes))
	for _, c := range l.Changes {
		ids = append(ids, c.ID)
	}
	return strings.Join(ids, ", ")
}

func (l *ReviewLedger) AllRiskIDs() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	ids := make([]string, 0, len(l.Risks))
	for _, r := range l.Risks {
		ids = append(ids, r.ID)
	}
	return strings.Join(ids, ", ")
}
