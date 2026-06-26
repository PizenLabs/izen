package execution

import (
	"path/filepath"
	"strings"
)

type StreamMonitor struct {
	buf         strings.Builder
	cursor      int
	queue       *PatchQueue
	inBlock     bool
	currentFile string
	currentBody strings.Builder
}

func NewStreamMonitor(queue *PatchQueue) *StreamMonitor {
	return &StreamMonitor{queue: queue}
}

func (sm *StreamMonitor) Feed(chunk string) {
	sm.buf.WriteString(chunk)
	content := sm.buf.String()

	for {
		idx := strings.Index(content[sm.cursor:], "\n")
		if idx < 0 {
			break
		}
		line := content[sm.cursor : sm.cursor+idx]
		sm.cursor += idx + 1
		sm.processLine(line)
	}
}

func (sm *StreamMonitor) Flush() {
	if sm.inBlock {
		sm.closeBlock()
	}
}

func (sm *StreamMonitor) Reset() {
	sm.buf.Reset()
	sm.cursor = 0
	sm.inBlock = false
	sm.currentFile = ""
	sm.currentBody.Reset()
}

func (sm *StreamMonitor) processLine(line string) {
	trimmed := strings.TrimSpace(line)

	if !sm.inBlock {
		if strings.HasPrefix(trimmed, "```") {
			sm.inBlock = true
			lang := strings.TrimPrefix(trimmed, "```")
			if strings.Contains(lang, ":") {
				parts := strings.SplitN(lang, ":", 2)
				sm.currentFile = strings.TrimSpace(parts[1])
			} else if lang == "diff" {
				sm.currentFile = ""
			}
			return
		}

		lo := strings.ToLower(trimmed)
		if strings.HasPrefix(lo, "edit file") {
			raw := strings.TrimSpace(trimmed[9:])
			if raw != "" {
				sm.currentFile = filepath.Clean(raw)
			}
		} else if strings.HasPrefix(lo, "file:") {
			raw := strings.TrimSpace(trimmed[5:])
			if raw != "" {
				sm.currentFile = filepath.Clean(raw)
			}
		}
		return
	}

	if strings.HasPrefix(trimmed, "```") {
		sm.closeBlock()
		return
	}

	if strings.HasPrefix(line, "+++ b/") && sm.currentFile == "" {
		sm.currentFile = strings.TrimSpace(strings.TrimPrefix(line, "+++ b/"))
	}

	if sm.currentFile != "" {
		sm.currentBody.WriteString(line)
		sm.currentBody.WriteString("\n")
	}
}

func (sm *StreamMonitor) closeBlock() {
	body := strings.TrimSpace(sm.currentBody.String())
	if sm.currentFile != "" && body != "" {
		sm.queue.Stage(sm.currentFile, body)
	}
	sm.inBlock = false
	sm.currentFile = ""
	sm.currentBody.Reset()
}
