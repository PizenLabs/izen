package execution

import (
	"os"
	"path/filepath"
	"strings"
)

type StreamMonitor struct {
	buf          strings.Builder
	cursor       int
	queue        *PatchQueue
	inBlock      bool
	inDiffBlock  bool
	currentFile  string
	currentBody  strings.Builder
	rawDiffBuf   strings.Builder
	contextFiles []string
}

func NewStreamMonitor(queue *PatchQueue) *StreamMonitor {
	return &StreamMonitor{queue: queue}
}

func (sm *StreamMonitor) SetContextFiles(files []string) {
	sm.contextFiles = files
}

func (sm *StreamMonitor) resolvePath(path string) string {
	if path == "" {
		return ""
	}

	clean := filepath.Clean(path)

	if filepath.IsAbs(clean) {
		clean = filepath.Base(clean)
	}

	if len(sm.contextFiles) > 0 {
		if len(sm.contextFiles) == 1 {
			return sm.contextFiles[0]
		}
		for _, cf := range sm.contextFiles {
			cfBase := filepath.Base(cf)
			pathBase := filepath.Base(clean)
			if cfBase == pathBase || cf == clean || strings.HasSuffix(cf, string(filepath.Separator)+clean) {
				return cf
			}
		}
		return sm.contextFiles[0]
	}

	if _, err := os.Stat(clean); err == nil {
		return clean
	}

	return clean
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
	sm.inDiffBlock = false
	sm.currentFile = ""
	sm.currentBody.Reset()
	sm.rawDiffBuf.Reset()
}

func (sm *StreamMonitor) processLine(line string) {
	trimmed := strings.TrimSpace(line)

	if !sm.inBlock {
		if strings.HasPrefix(trimmed, "```") {
			sm.inBlock = true
			sm.inDiffBlock = false
			sm.rawDiffBuf.Reset()
			lang := strings.TrimPrefix(trimmed, "```")
			if strings.Contains(lang, ":") {
				parts := strings.SplitN(lang, ":", 2)
				sm.currentFile = sm.resolvePath(strings.TrimSpace(parts[1]))
			} else if lang == "diff" {
				sm.inDiffBlock = true
				sm.currentFile = ""
			} else {
				sm.currentFile = ""
			}
			return
		}

		lo := strings.ToLower(trimmed)
		if strings.HasPrefix(lo, "edit file") {
			raw := strings.TrimSpace(trimmed[9:])
			if raw != "" {
				sm.currentFile = sm.resolvePath(filepath.Clean(raw))
			}
		} else if strings.HasPrefix(lo, "file:") {
			raw := strings.TrimSpace(trimmed[5:])
			if raw != "" {
				sm.currentFile = sm.resolvePath(filepath.Clean(raw))
			}
		}
		return
	}

	if strings.HasPrefix(trimmed, "```") {
		sm.closeBlock()
		return
	}

	if strings.HasPrefix(line, "+++ b/") && sm.currentFile == "" {
		sm.currentFile = sm.resolvePath(strings.TrimSpace(strings.TrimPrefix(line, "+++ b/")))
	}

	if sm.currentFile != "" {
		sm.currentBody.WriteString(line)
		sm.currentBody.WriteString("\n")
		sm.rawDiffBuf.WriteString(line)
		sm.rawDiffBuf.WriteString("\n")
	} else if sm.inDiffBlock {
		sm.rawDiffBuf.WriteString(line)
		sm.rawDiffBuf.WriteString("\n")
	}
}

func (sm *StreamMonitor) closeBlock() {
	body := strings.TrimSpace(sm.currentBody.String())
	rawDiff := strings.TrimSpace(sm.rawDiffBuf.String())
	if sm.inDiffBlock && rawDiff != "" {
		sm.queue.Stage(sm.currentFile, body, rawDiff)
	} else if sm.currentFile != "" && body != "" {
		sm.queue.Stage(sm.currentFile, body, rawDiff)
	}
	sm.inBlock = false
	sm.inDiffBlock = false
	sm.currentFile = ""
	sm.currentBody.Reset()
	sm.rawDiffBuf.Reset()
}
