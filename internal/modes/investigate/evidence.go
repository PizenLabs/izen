package investigate

import (
	"fmt"
	"strings"
	"time"
	"unicode"
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

const (
	maxLogInputBytes = 256 * 1024 // 256KB ceiling before truncation
	maxStackFrames   = 30         // max frames to keep after pre-processing
	maxLineLength    = 2000       // single line truncation threshold
	maxOutputLines   = 500        // total output lines cap
)

// BoundedLogPreprocessor intercepts raw terminal/CI failure output and
// extracts only the diagnostic signal — stack traces, error messages, and
// test failure markers — while stripping high-volume noise (ANSI codes,
// build cache logs, repetitive progress bars, Go module downloads, etc.).
// Returns a token-safe condensed payload.
func BoundedLogPreprocessor(raw string) string {
	if len(raw) > maxLogInputBytes {
		raw = raw[:maxLogInputBytes]
	}

	lines := strings.Split(raw, "\n")
	if len(lines) > maxOutputLines {
		lines = lines[:maxOutputLines]
	}

	var out []string
	var stackFrameCount int
	inNoiseBlock := false
	noiseBlockLines := 0

	for _, line := range lines {
		trimmed := strings.TrimRightFunc(line, unicode.IsSpace)
		clean := stripANSICodes(trimmed)

		if clean == "" {
			if inNoiseBlock {
				noiseBlockLines++
				if noiseBlockLines > 3 {
					continue
				}
			}
			out = append(out, "")
			continue
		}

		if isNoiseLine(clean) {
			if !inNoiseBlock {
				inNoiseBlock = true
				noiseBlockLines = 0
			}
			noiseBlockLines++
			if noiseBlockLines > 2 {
				continue
			}
			continue
		}
		inNoiseBlock = false
		noiseBlockLines = 0

		if isStackFrameLine(clean) {
			stackFrameCount++
			if stackFrameCount > maxStackFrames {
				continue
			}
		}

		if len(clean) > maxLineLength {
			clean = clean[:maxLineLength] + "..."
		}

		out = append(out, clean)
	}

	condensed := strings.Join(out, "\n")
	condensed = strings.TrimSpace(condensed)
	if condensed == "" {
		return raw
	}
	return condensed
}

func stripANSICodes(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	i := 0
	for i < len(s) {
		if s[i] == '\x1b' && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && (s[j] < 'A' || s[j] > 'Z') && (s[j] < 'a' || s[j] > 'z') {
				j++
			}
			if j < len(s) {
				i = j + 1
				continue
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

func isNoiseLine(s string) bool {
	lower := strings.ToLower(s)
	switch {
	case strings.HasPrefix(lower, "ok  ") || strings.HasPrefix(lower, "?   "):
		return false
	case strings.Contains(lower, "download") && strings.Contains(lower, "go"):
		return true
	case strings.Contains(lower, "cache") && strings.Contains(lower, "generated"):
		return true
	case strings.HasPrefix(lower, "progress"):
		return true
	case strings.HasPrefix(lower, "#") && strings.Contains(lower, "downloading"):
		return true
	case strings.Contains(lower, "go: finding module"):
		return true
	case strings.Contains(lower, "go: downloading"):
		return true
	case strings.Contains(lower, "go: extracting"):
		return true
	case strings.Count(lower, ".") > 20 && len(lower) > 100:
		return true
	default:
		return false
	}
}

func isStackFrameLine(s string) bool {
	switch {
	case strings.Contains(s, ".go:"):
		return true
	case strings.Contains(s, "File ") && strings.Contains(s, "line "):
		return true
	case strings.HasPrefix(s, "at ") && strings.Contains(s, ".go:"):
		return true
	case strings.HasPrefix(s, "\tat "):
		return true
	default:
		return false
	}
}

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
