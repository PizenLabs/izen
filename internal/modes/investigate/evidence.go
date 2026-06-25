package investigate

import (
	"fmt"
	"time"
)

type EvidenceSource string

const (
	EvSourceGraph     EvidenceSource = "graph"
	EvSourceSemantic  EvidenceSource = "semantic"
	EvSourceRipgrep   EvidenceSource = "ripgrep"
	EvSourceRead      EvidenceSource = "read"
	EvSourceTest      EvidenceSource = "test"
	EvSourceStack     EvidenceSource = "stacktrace"
	EvSourceUser      EvidenceSource = "user"
	EvSourceLog       EvidenceSource = "log"
	EvSourceExecution EvidenceSource = "execution"
)

type Evidence struct {
	ID         string         `json:"id"`
	Source     EvidenceSource `json:"source"`
	Content    string         `json:"content"`
	File       string         `json:"file,omitempty"`
	Line       int            `json:"line,omitempty"`
	Confidence float64        `json:"confidence"`
	Strategy   string         `json:"strategy,omitempty"`
	CreatedAt  time.Time      `json:"created_at"`
	Label      string         `json:"label,omitempty"`
}

type EvidenceStore struct {
	evidence []Evidence
	nextID   int
}

func NewEvidenceStore() *EvidenceStore {
	return &EvidenceStore{}
}

func (es *EvidenceStore) Add(source EvidenceSource, content, file string, line int, confidence float64) *Evidence {
	es.nextID++
	ev := &Evidence{
		ID:         fmt.Sprintf("EV%d", es.nextID),
		Source:     source,
		Content:    content,
		File:       file,
		Line:       line,
		Confidence: confidence,
		CreatedAt:  time.Now(),
	}
	es.evidence = append(es.evidence, *ev)
	return &es.evidence[len(es.evidence)-1]
}

func (es *EvidenceStore) AddWithStrategy(source EvidenceSource, content, file string, line int, confidence float64, strategy string) *Evidence {
	ev := es.Add(source, content, file, line, confidence)
	ev.Strategy = strategy
	return ev
}

func (es *EvidenceStore) Get(id string) *Evidence {
	for i := range es.evidence {
		if es.evidence[i].ID == id {
			return &es.evidence[i]
		}
	}
	return nil
}

func (es *EvidenceStore) All() []Evidence {
	return es.evidence
}

func (es *EvidenceStore) BySource(source EvidenceSource) []Evidence {
	var filtered []Evidence
	for _, ev := range es.evidence {
		if ev.Source == source {
			filtered = append(filtered, ev)
		}
	}
	return filtered
}

func (es *EvidenceStore) ByFile(file string) []Evidence {
	var filtered []Evidence
	for _, ev := range es.evidence {
		if ev.File == file {
			filtered = append(filtered, ev)
		}
	}
	return filtered
}

func (es *EvidenceStore) HighConfidence(threshold float64) []Evidence {
	var filtered []Evidence
	for _, ev := range es.evidence {
		if ev.Confidence >= threshold {
			filtered = append(filtered, ev)
		}
	}
	return filtered
}

func (es *EvidenceStore) Count() int {
	return len(es.evidence)
}

func (es *EvidenceStore) Clear() {
	es.evidence = nil
	es.nextID = 0
}

func (es *EvidenceStore) Summary() string {
	sources := make(map[EvidenceSource]int)
	for _, ev := range es.evidence {
		sources[ev.Source]++
	}
	return fmt.Sprintf("Total evidence: %d", len(es.evidence))
}