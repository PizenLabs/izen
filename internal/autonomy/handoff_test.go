package autonomy

import (
	"strconv"
	"strings"
	"testing"
)

func TestRegionsForFromAffectedScope(t *testing.T) {
	content := "package x\n\nfunc A() {}\n\nfunc B() {}\n"
	ctx := CompileContext("x.go", content)
	if len(ctx.Regions) == 0 {
		t.Fatal("no regions compiled from affected scope")
	}
	for _, r := range ctx.Regions {
		if r.File != "x.go" {
			t.Errorf("region file = %q, want x.go", r.File)
		}
		if r.Start < 1 || r.End < r.Start {
			t.Errorf("region bounds invalid: %d-%d", r.Start, r.End)
		}
	}
}

func TestRegionContentBoundedAndNumbered(t *testing.T) {
	ctx := CompileContext("x.go", "l1\nl2\nl3\nl4\nl5\n")
	out := ctx.RegionContent("l1\nl2\nl3\nl4\nl5\n", Region{File: "x.go", Start: 2, End: 3})
	if !strings.Contains(out, "2: l2") || !strings.Contains(out, "3: l3") {
		t.Errorf("region content missing expected lines: %q", out)
	}
	if strings.Contains(out, "1:") || strings.Contains(out, "4:") {
		t.Errorf("region escaped its bounds: %q", out)
	}
}

func TestRegionForPadsAroundLine(t *testing.T) {
	ctx := CompileContext("x.go", "l1\nl2\nl3\nl4\nl5\n")
	r := ctx.RegionFor(3, 1)
	if r.Start != 2 || r.End != 4 {
		t.Errorf("padded region = %d-%d, want 2-4", r.Start, r.End)
	}
	// Bottom edge clamps to line 1.
	r = ctx.RegionFor(1, 3)
	if r.Start != 1 {
		t.Errorf("bottom edge start = %d, want 1", r.Start)
	}
}

func TestFormatAIHandoffNeverSendsWholeFile(t *testing.T) {
	content := `package x
import "fmt"
func A() { fmt.Println("a") }
func B() { fmt.Println("b") }
func C() { fmt.Println("c") }
`
	ctx := CompileContext("x.go", content)
	out := ctx.FormatAIHandoff("refactor the functions", content, 2048)

	if !strings.Contains(out, "Intent: refactor the functions") {
		t.Error("intent missing from handoff")
	}
	if !strings.Contains(out, "Context Evidence Ledger") {
		t.Error("evidence ledger missing from handoff")
	}
	if !strings.Contains(out, "Relevant regions:") {
		t.Error("relevant regions missing from handoff")
	}
	if len(out) > 2048 {
		t.Errorf("handoff over budget: %d bytes", len(out))
	}
	// The handoff embeds numbered region slices; the unmodified whole file must
	// never appear verbatim.
	if strings.Contains(out, content) {
		t.Error("handoff must never embed the entire file verbatim")
	}
}

func TestFormatAIHandoffBudgetCapsRegions(t *testing.T) {
	content := "package x\n"
	for i := 0; i < 50; i++ {
		content += "func F" + strconv.Itoa(i) + "() { return }\n"
	}
	ctx := CompileContext("x.go", content)
	out := ctx.FormatAIHandoff("x", content, 512)
	if len(out) > 512 {
		t.Errorf("budget exceeded: %d bytes > 512", len(out))
	}
	if len(out) >= len(content) {
		t.Errorf("handoff (%d bytes) must stay below full content (%d bytes)", len(out), len(content))
	}
}

func TestFormatAIHandoffEmptyRegions(t *testing.T) {
	ctx := CompileContext("README.md", "hello world")
	out := ctx.FormatAIHandoff("summarize", "hello world", 1024)
	if !strings.Contains(out, "Relevant regions: none identified") {
		t.Errorf("empty-region handoff must state no regions: %q", out)
	}
}
