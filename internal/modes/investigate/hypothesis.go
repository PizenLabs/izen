package investigate

import (
	"fmt"
	"time"
)

type HypothesisStatus int

const (
	HypothesisActive   HypothesisStatus = iota
	HypothesisConfirmed
	HypothesisRejected
	HypothesisPending
)

func (s HypothesisStatus) String() string {
	switch s {
	case HypothesisActive:
		return "active"
	case HypothesisConfirmed:
		return "confirmed"
	case HypothesisRejected:
		return "rejected"
	case HypothesisPending:
		return "pending"
	default:
		return "unknown"
	}
}

type Hypothesis struct {
	ID          string           `json:"id"`
	Theory      string           `json:"theory"`
	Status      HypothesisStatus `json:"status"`
	Confidence  float64          `json:"confidence"`
	EvidenceIDs []string         `json:"evidence_ids,omitempty"`
	CreatedAt   time.Time        `json:"created_at"`
	UpdatedAt   time.Time        `json:"updated_at"`
	Tags        []string         `json:"tags,omitempty"`
}

type HypothesisManager struct {
	hypotheses []Hypothesis
	nextID     int
}

func NewHypothesisManager() *HypothesisManager {
	return &HypothesisManager{}
}

func (hm *HypothesisManager) Add(theory string) *Hypothesis {
	hm.nextID++
	h := &Hypothesis{
		ID:         fmt.Sprintf("H%d", hm.nextID),
		Theory:     theory,
		Status:     HypothesisActive,
		Confidence: 0.5,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	hm.hypotheses = append(hm.hypotheses, *h)
	return &hm.hypotheses[len(hm.hypotheses)-1]
}

func (hm *HypothesisManager) Get(id string) *Hypothesis {
	for i := range hm.hypotheses {
		if hm.hypotheses[i].ID == id {
			return &hm.hypotheses[i]
		}
	}
	return nil
}

func (hm *HypothesisManager) All() []Hypothesis {
	return hm.hypotheses
}

func (hm *HypothesisManager) Active() []Hypothesis {
	var active []Hypothesis
	for _, h := range hm.hypotheses {
		if h.Status == HypothesisActive || h.Status == HypothesisPending {
			active = append(active, h)
		}
	}
	return active
}

func (hm *HypothesisManager) Confirmed() []Hypothesis {
	var confirmed []Hypothesis
	for _, h := range hm.hypotheses {
		if h.Status == HypothesisConfirmed {
			confirmed = append(confirmed, h)
		}
	}
	return confirmed
}

func (hm *HypothesisManager) UpdateStatus(id string, status HypothesisStatus) {
	h := hm.Get(id)
	if h != nil {
		h.Status = status
		h.UpdatedAt = time.Now()
	}
}

func (hm *HypothesisManager) UpdateConfidence(id string, confidence float64) {
	h := hm.Get(id)
	if h != nil {
		h.Confidence = confidence
		h.UpdatedAt = time.Now()
	}
}

func (hm *HypothesisManager) LinkEvidence(hypID, evID string) {
	h := hm.Get(hypID)
	if h != nil {
		h.EvidenceIDs = append(h.EvidenceIDs, evID)
		h.UpdatedAt = time.Now()
	}
}

func (hm *HypothesisManager) Count() int {
	return len(hm.hypotheses)
}

func (hm *HypothesisManager) Best() *Hypothesis {
	var best *Hypothesis
	for i := range hm.hypotheses {
		if hm.hypotheses[i].Status == HypothesisRejected {
			continue
		}
		if best == nil || hm.hypotheses[i].Confidence > best.Confidence {
			best = &hm.hypotheses[i]
		}
	}
	return best
}