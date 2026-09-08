// Package diff provides terminal geometry calculation and viewport render
// budget planning for diff projection. Diff rendering is strictly a projection
// of MutationEvidence: the renderer never re-computes or alters the diff, it
// only plans which lines fit within the terminal viewport budget.
package diff

// MutationType classifies a single patch line within mutation evidence.
type MutationType int

const (
	// MutationAdd marks a line that is inserted by the mutation.
	MutationAdd MutationType = iota
	// MutationModify marks a line that is altered by the mutation.
	MutationModify
	// MutationDelete marks a line that is removed by the mutation.
	MutationDelete
)

// PatchLine is a single diff line carrying its mutation type and content.
type PatchLine struct {
	Type    MutationType
	Content string
}

// MutationEvidence describes the diff of a mutation applied to a target file.
type MutationEvidence struct {
	TargetFile string
	Lines      []PatchLine
	Added      int
	Deleted    int
}

// RenderMode selects how a diff is projected into the viewport.
type RenderMode int

const (
	// RenderModeFullInline projects every line of the evidence in full.
	RenderModeFullInline RenderMode = iota
	// RenderModeTruncatedHeadTail projects symmetric head/tail slices with the
	// overflowing middle lines omitted.
	RenderModeTruncatedHeadTail
)

// ViewportConfig describes the terminal display geometry and the render
// budget. BudgetRatio defaults to 0.40 and TabWidth to 4 when left zero.
type ViewportConfig struct {
	TermWidth   int     // Total terminal columns
	TermHeight  int     // Total terminal rows
	GutterWidth int     // Width reserved for line numbers/status
	PrefixWidth int     // Width reserved for diff markers (+ / -)
	BudgetRatio float64 // Max vertical height ratio allowed (default: 0.40)
	TabWidth    int     // Spaces per tab character (default: 4)
}

// RenderPlan is the output of the viewport engine: the projected lines and
// the budget accounting that produced them.
type RenderPlan struct {
	Mode         RenderMode
	VisibleLines []PatchLine
	TotalVisual  int
	AllowedRows  int
	TruncatedAt  int // Number of hidden lines in middle when truncated
}
