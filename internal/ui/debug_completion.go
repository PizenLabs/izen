package ui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
)

// completionLogEntry is one line of .izen/debug/completions.log. It records
// the raw-vs-visible composition of an LLM completion so token loss / reasoning
// leakage can be audited after the fact: ContentLen and ReasoningLen are the
// rune-lengths split by the reasoning stripper, while TokenInput/TokenOutput
// are the provider-reported token counts.
type completionLogEntry struct {
	Time         string `json:"time"`
	FinishReason string `json:"finish_reason,omitempty"`
	ContentLen   int    `json:"content_len"`
	ReasoningLen int    `json:"reasoning_len"`
	TokenInput   int    `json:"token_input"`
	TokenOutput  int    `json:"token_output"`
	Truncated    bool   `json:"truncated"`
	Stage        string `json:"stage,omitempty"`
}

// debugEnabled reports whether IZEN_DEBUG=1 or IZEN_DEBUG=true is set.
func debugEnabled() bool {
	switch os.Getenv("IZEN_DEBUG") {
	case "1", "true":
		return true
	default:
		return false
	}
}

// debugLogCompletion appends one raw-completion composition record to
// .izen/debug/completions.log. It is purely diagnostic — it never affects the
// runtime path, and it is a strict no-op when the log cannot be written or
// when IZEN_DEBUG is not enabled.
func debugLogCompletion(raw string, tokIn, tokOut int, finishReason string, stage string) {
	if !debugEnabled() {
		return
	}
	dir := filepath.Join(".izen", "debug")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	stats := ai.CompletionStatsOf(raw)
	entry := completionLogEntry{
		Time:         time.Now().Format(time.RFC3339Nano),
		FinishReason: finishReason,
		ContentLen:   stats.ContentLen,
		ReasoningLen: stats.ReasoningLen,
		TokenInput:   tokIn,
		TokenOutput:  tokOut,
		Truncated:    finishReason == "length",
		Stage:        stage,
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return
	}
	data = append(data, '\n')
	f, err := os.OpenFile(filepath.Join(dir, "completions.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	_, _ = f.Write(data)
}
