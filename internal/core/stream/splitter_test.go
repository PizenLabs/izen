package stream

import (
	"strings"
	"testing"
)

// collect runs the splitter over chunks and flushes, returning the frames.
func collect(t *testing.T, chunks ...string) []Frame {
	t.Helper()
	s := NewSplitter()
	var frames []Frame
	emit := func(f Frame) { frames = append(frames, f) }
	for _, c := range chunks {
		s.Write(c, emit)
	}
	s.Flush(emit)
	return frames
}

func framesText(frames []Frame, kind ChunkKind) string {
	var sb strings.Builder
	for _, f := range frames {
		if f.Kind == kind {
			sb.WriteString(f.Text)
		}
	}
	return sb.String()
}

func TestSplitterSimpleThoughtBlock(t *testing.T) {
	frames := collect(t, "Before. <thought>step one</thought> After.")
	if got := framesText(frames, ChunkContent); got != "Before.  After." {
		t.Errorf("content = %q", got)
	}
	if got := framesText(frames, ChunkReasoning); got != "step one" {
		t.Errorf("reasoning = %q", got)
	}
}

func TestSplitterThoughtTagSplitAcrossChunks(t *testing.T) {
	// The tag itself is split across chunks.
	frames := collect(t,
		"Hello <thou",
		"ght>internal chain</",
		"thought> world",
	)
	if got := framesText(frames, ChunkContent); got != "Hello  world" {
		t.Errorf("content = %q", got)
	}
	if got := framesText(frames, ChunkReasoning); got != "internal chain" {
		t.Errorf("reasoning = %q", got)
	}
}

func TestSplitterThoughtContentSplitAtChunkBoundary(t *testing.T) {
	// Reasoning body straddles a chunk boundary.
	frames := collect(t, "<thought>part one ", "part two</thought> done")
	if got := framesText(frames, ChunkReasoning); got != "part one part two" {
		t.Errorf("reasoning = %q", got)
	}
	if got := framesText(frames, ChunkContent); got != " done" {
		t.Errorf("content = %q", got)
	}
}

func TestSplitterNestedLikeAnglesStayVerbatim(t *testing.T) {
	// Angle-bracket comparisons that are NOT thought tags must survive intact.
	frames := collect(t, "a < b && c > d")
	if got := framesText(frames, ChunkContent); got != "a < b && c > d" {
		t.Errorf("content = %q, want verbatim", got)
	}
	if got := framesText(frames, ChunkReasoning); got != "" {
		t.Errorf("reasoning = %q, want empty", got)
	}
}

func TestSplitterUnclosedThoughtAtEOF(t *testing.T) {
	s := NewSplitter()
	var frames []Frame
	s.Write("<thought>never closed", func(f Frame) { frames = append(frames, f) })
	if !s.InThought() {
		t.Error("splitter should be inside a thought block")
	}
	s.Flush(func(f Frame) { frames = append(frames, f) })
	if s.InThought() {
		t.Error("splitter should reset after Flush")
	}
	if got := framesText(frames, ChunkReasoning); got != "never closed" {
		t.Errorf("reasoning = %q", got)
	}
}

func TestSplitterStrayPartialTagAtEOF(t *testing.T) {
	// A chunk ending mid-<thought is a partial marker that Flush must surface
	// as literal content — never silently dropped.
	s := NewSplitter()
	var frames []Frame
	s.Write("result: <tho", func(f Frame) { frames = append(frames, f) })
	s.Flush(func(f Frame) { frames = append(frames, f) })
	if got := framesText(frames, ChunkContent); got != "result: <tho" {
		t.Errorf("content = %q, want literal 'result: <tho'", got)
	}
}

func TestSplitterSentinelSynonym(t *testing.T) {
	// The reasoning_content sentinel (NUL-wrapped) is recognized like a tag.
	frames := collect(t, "Answer\x00RSNG\x00deep think\x00RSNG\x00 tail")
	if got := framesText(frames, ChunkContent); got != "Answer tail" {
		t.Errorf("content = %q", got)
	}
	if got := framesText(frames, ChunkReasoning); got != "deep think" {
		t.Errorf("reasoning = %q", got)
	}
}

func TestSplitterPreservesEscapesVerbatim(t *testing.T) {
	// Backslashes, backticks, markdown emphasis and JSON escapes must survive
	// the splitter untouched — no double-unescaping, no word stripping.
	input := "Use `fmt.Printf(\"x=%d\", 1)` and \\n literal plus *bold* and _em_."
	frames := collect(t, input)
	if got := framesText(frames, ChunkContent); got != input {
		t.Errorf("escapes corrupted:\n got  %q\n want %q", got, input)
	}
}

func TestSplitterPreservesEscapesAcrossBoundaries(t *testing.T) {
	// Same content delivered across arbitrary chunk boundaries.
	full := "func main() {\n\tfmt.Println(\"\\n\")\n\t// `code` *star* _under_\n}"
	var frames []Frame
	s := NewSplitter()
	// Feed 3 bytes at a time (ASCII-safe splitting point chosen per run).
	emit := func(f Frame) { frames = append(frames, f) }
	for i := 0; i < len(full); i += 3 {
		end := i + 3
		if end > len(full) {
			end = len(full)
		}
		s.Write(full[i:end], emit)
	}
	s.Flush(emit)
	if got := framesText(frames, ChunkContent); got != full {
		t.Errorf("escapes corrupted across boundaries:\n got  %q\n want %q", got, full)
	}
}

func TestSplitterEmptyInput(t *testing.T) {
	frames := collect(t, "")
	if len(frames) != 0 {
		t.Errorf("got %d frames for empty input", len(frames))
	}
}

func TestSplitterMixedTagsAndSentinel(t *testing.T) {
	// Both marker forms in one stream, interleaved with content.
	frames := collect(t,
		"<thought>reason A</thought>content 1",
		"\x00RSNG\x00reason B\x00RSNG\x00content 2",
		"<thought>reason C</thought>content 3",
	)
	if got := framesText(frames, ChunkReasoning); got != "reason Areason Breason C" {
		t.Errorf("reasoning = %q", got)
	}
	if got := framesText(frames, ChunkContent); got != "content 1content 2content 3" {
		t.Errorf("content = %q", got)
	}
}

func TestReasonBlock(t *testing.T) {
	content, reasoning := ReasonBlock("<thought>plan it</thought>The answer has `code`.")
	if reasoning != "plan it" {
		t.Errorf("reasoning = %q", reasoning)
	}
	if content != "The answer has `code`." {
		t.Errorf("content = %q", content)
	}
}

func TestReasonBlockNoReasoning(t *testing.T) {
	content, reasoning := ReasonBlock("plain text only")
	if reasoning != "" {
		t.Errorf("reasoning = %q, want empty", reasoning)
	}
	if content != "plain text only" {
		t.Errorf("content = %q", content)
	}
}

func TestSplitEmptyFramesFiltered(t *testing.T) {
	frames := Split([]string{"", "<thought>", "x", "</thought>", ""})
	if len(frames) != 1 {
		t.Fatalf("got %d frames, want 1", len(frames))
	}
	if frames[0].Kind != ChunkReasoning || frames[0].Text != "x" {
		t.Errorf("frame = %+v", frames[0])
	}
}
