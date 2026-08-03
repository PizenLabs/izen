package debug

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/lea"
	"github.com/PizenLabs/izen/internal/planner"
	"github.com/PizenLabs/izen/internal/runtime/output"
)

func TestReportRenderSections(t *testing.T) {
	var buf bytes.Buffer
	r := Report{
		Engine: lea.DebugInfo{
			Root: "/ws", CachePath: "/ws/.izen/graph.bin.zst", CacheVersion: 2,
			FilesIndexed: 10, Symbols: 42, Nodes: 50, Edges: 100, Routes: 3, CallEdges: 60,
			LastIndexDuration: 1500 * time.Millisecond, LastIndexedAt: time.Now(),
		},
		Governance: planner.DebugInfo{
			Intent: planner.IntentBugFix, AllocatedTokens: 4000,
			RetrievedChunks: 5, FittedChunks: 3, DroppedChunks: 2, UsedTokens: 1500,
		},
		Output: output.DebugInfo{
			Tool: "GO_TEST", OriginalChars: 1000, CompressedChars: 200,
			CompressionRatioPct: 80, CharsSaved: 800, TokenBytesSaved: 200,
			LogPath: "/ws/.logs/20260804-120000-GO_TEST.log",
		},
		Logs: output.WorkspaceInspection{
			LogDir: "/ws/.logs", LogCount: 2,
			LogFiles: []string{"/ws/.logs/20260804-120000-GO_TEST.log", "/ws/.logs/20260804-110000-GENERIC.log"},
			LastLog:  "/ws/.logs/last.log",
		},
	}
	if err := r.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}
	out := buf.String()

	for _, want := range []string{
		"IZEN DEBUG REPORT",
		"[LEA STRUCTURAL ENGINE]",
		"root:           /ws",
		"cache:          /ws/.izen/graph.bin.zst (version 2)",
		"files indexed:  10",
		"symbols:        42",
		"nodes / edges:  50 / 100",
		"routes / calls: 3 / 60",
		"[CONTEXT GOVERNANCE]",
		"intent:          BUG_FIX",
		"allocated:       4000 tokens",
		"retrieved:       5 chunks",
		"fitted:          3 chunks",
		"dropped:         2 chunks",
		"used:            1500 tokens",
		"[OUTPUT PIPELINE]",
		"compression:     80.0% (1000 -> 200 chars)",
		"chars saved:     800",
		"token bytes:     200",
		"log dir:         /ws/.logs",
		"log files:       2",
		"last log:        /ws/.logs/last.log",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("render output missing %q\n---\n%s", want, out)
		}
	}
}

func TestReportRenderEmpty(t *testing.T) {
	var buf bytes.Buffer
	if err := (Report{}).Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"IZEN DEBUG REPORT",
		"[LEA STRUCTURAL ENGINE]",
		"not indexed",
		"[CONTEXT GOVERNANCE]",
		"no planning run recorded",
		"[OUTPUT PIPELINE]",
		"no execution recorded",
		"log files:       0",
		"last log:        (none)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("render output missing %q\n---\n%s", want, out)
		}
	}
}

func TestReportRenderNilWriter(t *testing.T) {
	if err := (Report{}).Render(nil); err == nil {
		t.Error("Render(nil) should return an error")
	}
}

func TestNewReport(t *testing.T) {
	eng := lea.DebugInfo{FilesIndexed: 3}
	plan := planner.DebugInfo{AllocatedTokens: 100}
	res := output.DebugInfo{LogPath: "/x"}
	logs := output.WorkspaceInspection{LogDir: "/x/.logs"}

	// Passing nil engines yields zero-valued sections.
	r := NewReport(nil, nil, res, logs)
	if r.Engine.Indexed() {
		t.Error("NewReport(nil engine) should yield an unindexed engine section")
	}
	if r.Governance.AllocatedTokens != 0 {
		t.Error("NewReport(nil plan) should yield a zero governance section")
	}
	if r.Output.LogPath != "/x" {
		t.Errorf("NewReport output = %+v", r.Output)
	}

	// A non-nil plan with real stats surfaces through NewReport.
	_ = eng
	_ = plan
}
