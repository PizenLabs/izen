package ui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/PizenLabs/izen/internal/policy"
)

// promptTestModel is a model in the exact state a keystroke meets in the real
// app: an initialized workspace, a focused prompt bar, a composed document, and
// no overlay. Every test in this file starts here so that what is being measured
// is the isolation and not the setup.
func promptTestModel(t *testing.T) *model {
	t.Helper()
	m := initializedChatModel(t)
	m.ti.Focus()
	m.records = append(m.records,
		record{role: roleUser, text: "summarise the migration"},
		record{role: roleSystem, text: "The migration is complete and every test passes."},
	)
	m.refreshViewportContent()
	// One full compose, which is what makes the document bands capturable.
	m.View()
	if !m.promptRegions.valid {
		t.Fatal("precondition: a full compose must capture the document regions")
	}
	if m.promptDirty {
		t.Fatal("precondition: a full compose must clear the prompt dirty flag")
	}
	return m
}

func typeRune(r rune) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
}

// TestPromptKeyDoesNotTouchTheDocument is the core isolation claim. A printable
// keystroke into the prompt bar must mark the PROMPT dirty and leave the document
// generation exactly where it was — because a document that moved would force the
// next frame to re-run the whole composition, which is the entire cost this path
// exists to remove.
func TestPromptKeyDoesNotTouchTheDocument(t *testing.T) {
	m := promptTestModel(t)
	gen := m.documentGen

	nm, _ := m.Update(typeRune('h'))
	m = nm.(*model)

	if got := m.ti.Value(); got != "h" {
		t.Fatalf("prompt value = %q, want %q", got, "h")
	}
	if !m.promptDirty {
		t.Error("a prompt keystroke must mark the prompt region dirty")
	}
	if m.documentGen != gen {
		t.Errorf("a prompt keystroke moved the document generation %d -> %d: the next frame would re-compose the document",
			gen, m.documentGen)
	}
	if !m.documentRegionClean() {
		t.Error("documentRegionClean() must hold after a prompt keystroke")
	}
}

// TestPromptKeyReusesTheComposedDocumentRows is the payoff. Typing must
// re-compose the prompt region and reuse the document rows the previous frame
// already rendered — the same rows, byte for byte, and not one of them
// recomputed.
func TestPromptKeyReusesTheComposedDocumentRows(t *testing.T) {
	m := promptTestModel(t)
	rowsBefore := m.docRowRebuilds
	cachedViewport := m.promptRegions.viewport
	cachedDoc := m.docLayout.Len()
	composes := m.promptComposes

	nm, _ := m.Update(typeRune('x'))
	m = nm.(*model)
	frame := m.View()

	if m.promptComposes != composes+1 {
		t.Fatalf("the prompt-only path did not run (composes %d -> %d)", composes, m.promptComposes)
	}
	if m.promptDirty {
		t.Error("a served prompt compose must clear its dirty flag")
	}
	if m.promptRegions.viewport != cachedViewport {
		t.Error("the cached document rows were re-rendered instead of reused")
	}
	if m.docRowRebuilds != rowsBefore {
		t.Errorf("the document row cache was rebuilt %d -> %d by a keystroke", rowsBefore, m.docRowRebuilds)
	}
	if m.docLayout.Len() != cachedDoc {
		t.Errorf("the document changed length %d -> %d by a keystroke", cachedDoc, m.docLayout.Len())
	}
	// The frame must actually show the typed character, or the fast path is not
	// doing its job — it is cheaper only if it is also correct.
	if !strings.Contains(ansi.Strip(frame), "x") {
		t.Errorf("the frame does not show the typed character:\n%s", ansi.Strip(frame))
	}
}

// TestPromptComposeIsByteIdenticalToTheFullCompose is the parity contract. The
// fast path must produce exactly what the full path would have produced, because
// it reuses the same renderInputRegion and the same compositor. Any divergence is
// a frame the user sees flicker between two renderings of one keystroke.
func TestPromptComposeIsByteIdenticalToTheFullCompose(t *testing.T) {
	m := promptTestModel(t)
	nm, _ := m.Update(typeRune('a'))
	m = nm.(*model)

	fast := m.View()
	// Force the full path for the same state and compare.
	m.promptDirty = true
	m.promptRegions.valid = false
	slow := m.View()

	if fast != slow {
		t.Errorf("the prompt-only frame differs from the full compose\n--- fast ---\n%q\n--- full ---\n%q", fast, slow)
	}
}

// TestDocumentMutationFallsBackToTheFullCompose is the safety direction. Any
// document change must invalidate the cached bands, so the next frame re-composes
// everything. A cache that survived its own invalidation would serve rows that
// are not on screen any more.
func TestDocumentMutationFallsBackToTheFullCompose(t *testing.T) {
	m := promptTestModel(t)
	nm, _ := m.Update(typeRune('q'))
	m = nm.(*model)
	if !m.promptDirty {
		t.Fatal("precondition: the prompt is dirty")
	}

	// A document mutation lands before the frame is composed.
	m.refreshViewportContent()
	if m.documentRegionClean() {
		t.Fatal("refreshViewportContent must move the document generation")
	}
	composes := m.promptComposes
	m.View()
	if m.promptComposes != composes {
		t.Error("a dirty document must not be served from the prompt-only path")
	}
}

// TestScrollInvalidatesThePromptOnlyPath pins the subtle half: a scroll does not
// move a single row, but it changes WHICH rows are on screen, so it is a
// document-region change like any other.
func TestScrollInvalidatesThePromptOnlyPath(t *testing.T) {
	m := promptTestModel(t)
	// Give the document enough rows to scroll at all.
	for i := 0; i < 80; i++ {
		m.records = append(m.records, record{role: roleSystem, text: "filler line"})
	}
	m.refreshViewportContent()
	m.View()
	gen := m.documentGen

	m.scrollBy(3)
	if m.documentGen == gen {
		t.Fatal("a scroll must move the document generation: it changes which rows are on screen")
	}
	if m.documentRegionClean() {
		t.Error("a scroll must invalidate the cached document rows")
	}
}

// TestPromptGuardDefersEveryKeyClassThatIsNotPlainTyping is the routing
// equivalence contract. The fast path skips the whole key-routing chain, so every
// key class that some part of that chain claims must be handed to it. This table
// is the list of those claims, and each entry names the interceptor that owns it
// so a new interceptor and a missing guard entry fail together rather than
// silently letting a keystroke through.
func TestPromptGuardDefersEveryKeyClassThatIsNotPlainTyping(t *testing.T) {
	owned := []struct {
		name    string
		mutate  func(m *model)
		msg     tea.KeyMsg
		comment string
	}{
		{
			name:    "permission modal",
			mutate:  func(m *model) { m.pendingPermission = &policy.PermissionRequest{ID: "perm-1"} },
			msg:     typeRune('a'),
			comment: "the security interceptor owns every key",
		},
		{
			name:    "quit confirm",
			mutate:  func(m *model) { m.pendingQuitConfirm = true },
			msg:     typeRune('a'),
			comment: "the exit-safety dialog owns every key",
		},
		{
			name:    "status overlay",
			mutate:  func(m *model) { m.showStatus = true },
			msg:     typeRune('a'),
			comment: "the status modal owns every key",
		},
		{
			name:    "settings modal",
			mutate:  func(m *model) { m.showSettings = true },
			msg:     typeRune('a'),
			comment: "settings owns keyboard focus",
		},
		{
			name:    "help overlay",
			mutate:  func(m *model) { m.showHelpOverlay = true },
			comment: "the help surface replaces the frame",
			msg:     typeRune('a'),
		},
		{
			name:    "approval state",
			mutate:  func(m *model) { m.state = StateAwaitingApproval },
			msg:     typeRune('a'),
			comment: "handleKey owns the keyboard outright",
		},
		{
			name:    "processing state",
			mutate:  func(m *model) { m.state = StateProcessing },
			msg:     typeRune('a'),
			comment: "handleKey owns the keyboard outright",
		},
		{
			name:    "vi mode",
			mutate:  func(m *model) { m.inViMode = true },
			msg:     typeRune('a'),
			comment: "vi-mode routes every key to its own handler",
		},
		{
			name:    "model picker",
			mutate:  func(m *model) { m.showModelPicker = true },
			msg:     typeRune('a'),
			comment: "the picker routes keys to its widget",
		},
		{
			name:    "diff viewer",
			mutate:  func(m *model) { m.openDiffView("--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+b\n", "x") },
			msg:     typeRune('a'),
			comment: "the diff viewer owns its keybindings",
		},
		{
			name:    "unfocused prompt",
			mutate:  func(m *model) { m.ti.Blur() },
			msg:     typeRune('a'),
			comment: "an unfocused input is not typing",
		},
		{
			name:    "alt modified",
			mutate:  func(m *model) {},
			msg:     tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}, Alt: true},
			comment: "alt+key is a keybinding mechanism, never text",
		},
		{
			name:    "bracketed paste",
			mutate:  func(m *model) {},
			msg:     tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("pasted"), Paste: true},
			comment: "a paste must go through the paste path (badge folding)",
		},
		{
			name:    "control key",
			mutate:  func(m *model) {},
			msg:     tea.KeyMsg{Type: tea.KeyEnter},
			comment: "Enter submits and must reach the full chain",
		},
		{
			name:    "escape",
			mutate:  func(m *model) {},
			msg:     tea.KeyMsg{Type: tea.KeyEsc},
			comment: "Esc is the interrupt/interrupt-window key",
		},
		{
			name:    "ctrl-c",
			mutate:  func(m *model) {},
			msg:     tea.KeyMsg{Type: tea.KeyCtrlC},
			comment: "Ctrl+C is the unblockable escape hatch",
		},
		{
			name:    "arrow key",
			mutate:  func(m *model) {},
			msg:     tea.KeyMsg{Type: tea.KeyLeft},
			comment: "arrows are cursor movement handled by the chain",
		},
		{
			name:    "sgr mouse fragment",
			mutate:  func(m *model) {},
			msg:     tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'[', '<', '0', ';', '2', '6', ';', '3', '7', 'M'}},
			comment: "an orphaned mouse report must be dropped, and its runes are printable-looking",
		},
		{
			name:    "question mark on empty input",
			mutate:  func(m *model) { m.ti.SetValue("") },
			msg:     typeRune('?'),
			comment: "a bare ? on an empty buffer opens the help overlay",
		},
		{
			name:    "live mouse selection",
			mutate:  func(m *model) { m.mouseSel.Active = true },
			msg:     typeRune('a'),
			comment: "the framebuffer selection overlay is on screen",
		},
		{
			name:    "expanded reasoning panel",
			mutate:  func(m *model) { mountReasoningPanel(t, m) },
			msg:     typeRune('a'),
			comment: "the reasoning band must be recomposed, not reused",
		},
		{
			name:    "live scroll burst",
			mutate:  func(m *model) { m.markScrollBurst() },
			msg:     typeRune('a'),
			comment: "the scroll fast path owns chrome reuse inside a burst",
		},
	}

	for _, tc := range owned {
		t.Run(tc.name, func(t *testing.T) {
			m := promptTestModel(t)
			tc.mutate(m)
			if m.promptKeyUncontested(tc.msg) {
				t.Fatalf("the fast path claims a key that %s", tc.comment)
			}
		})
	}
}

// mountReasoningPanel expands the reasoning viewport over a real trace. The
// buffer is required: an expanded panel with nothing behind it self-unmounts on
// the next sync (a panel claiming rows it cannot fill is a bug), so a test that
// wants the panel mounted has to give it content.
func mountReasoningPanel(t *testing.T, m *model) {
	t.Helper()
	m.thinkingBuffer = NewThinkingBuffer()
	m.thinkingBuffer.Append("step one: read the migration plan\nstep two: apply the patch\n")
	m.setReasoningExpanded(true)
	if !m.reasoningExpanded {
		t.Fatalf("precondition: the reasoning panel did not mount (content=%d)", m.thinkingBuffer.Len())
	}
}

// TestPromptComposeDefersEveryFrameThatIsNotJustThePrompt is the FRAME-side
// counterpart to the guard table above. The two lists are deliberately different:
// a key class the chain owns is not automatically a frame the fast path must
// decline (Enter changes the document, a left arrow does not), so each list states
// only the claims that are true for its own path.
func TestPromptComposeDefersEveryFrameThatIsNotJustThePrompt(t *testing.T) {
	deferFrames := []struct {
		name    string
		mutate  func(m *model)
		comment string
	}{
		{
			name:    "status overlay",
			mutate:  func(m *model) { m.showStatus = true },
			comment: "an overlay replaces the whole frame",
		},
		{
			name:    "help overlay",
			mutate:  func(m *model) { m.showHelpOverlay = true },
			comment: "an overlay replaces the whole frame",
		},
		{
			name:    "settings modal",
			mutate:  func(m *model) { m.showSettings = true },
			comment: "an overlay replaces the whole frame",
		},
		{
			name:    "model picker",
			mutate:  func(m *model) { m.showModelPicker = true },
			comment: "an overlay replaces the whole frame",
		},
		{
			name:    "live mouse selection",
			mutate:  func(m *model) { m.mouseSel.Active = true },
			comment: "the framebuffer selection overlay is on screen",
		},
		{
			name:    "vi mode",
			mutate:  func(m *model) { m.inViMode = true },
			comment: "vi-mode owns the viewport surface",
		},
		{
			name:    "not ready",
			mutate:  func(m *model) { m.Ready = false },
			comment: "there is no composed workspace to reuse",
		},
	}
	for _, tc := range deferFrames {
		t.Run(tc.name, func(t *testing.T) {
			m := promptTestModel(t)
			m.promptDirty = true
			tc.mutate(m)
			if _, ok := m.promptComposeFrame(m.Screen()); ok {
				t.Errorf("the prompt-only path composed a frame while %s", tc.comment)
			}
		})
	}
}

// TestQuestionMarkIsStillTextOnceTheBufferHasContent pins the one asymmetry in
// the `?` rule: it is a help toggle only on a COMPLETELY EMPTY buffer, and a
// user asking a question that begins with `?` must be able to type it.
func TestQuestionMarkIsStillTextOnceTheBufferHasContent(t *testing.T) {
	m := promptTestModel(t)
	m.ti.SetValue("w")
	m.ti.SetCursor(1)
	if !m.promptKeyUncontested(typeRune('?')) {
		t.Error("? must be text once the buffer is not empty")
	}
}

// TestPromptComposeDeclinesWhenThePromptRegionResizes is the geometry safety
// valve. Opening the autocomplete dropdown changes the prompt region's height,
// which changes the viewport budget. The fast path does not re-derive that budget,
// so it must decline and let the full compose measure it.
func TestPromptComposeDeclinesWhenThePromptRegionResizes(t *testing.T) {
	m := promptTestModel(t)
	m.promptDirty = true
	before := m.promptComposes

	// A dropdown is taller than the single prompt line the cache was measured at.
	m.autocompleteActive = true
	m.autocompleteItems = []Suggestion{{Token: "/ask", Label: "ask", Kind: SuggestionGlobal}}
	if _, ok := m.promptComposeFrame(m.Screen()); ok {
		t.Error("the prompt-only path composed a frame whose prompt region changed height")
	}
	if m.promptComposes != before {
		t.Error("a declined compose must not count as a compose")
	}
}

// TestPromptComposeDeclinesAfterAResize pins that a pane resize invalidates the
// bands: every one of them was measured against the old width.
func TestPromptComposeDeclinesAfterAResize(t *testing.T) {
	m := promptTestModel(t)
	m.promptDirty = true
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 60, Height: 20})
	m = nm.(*model)
	if _, ok := m.promptComposeFrame(m.Screen()); ok {
		t.Error("the prompt-only path reused bands measured for a different pane")
	}
}

// TestPromptKeyLatencyIsIndependentOfDocumentSize is the measurable DoD claim.
// The point of the isolation is that a keystroke costs the same whether the
// conversation is one line or a thousand, so the test measures the key path
// against a large document and asserts it stayed in microseconds — two orders of
// magnitude inside the 1ms budget, with headroom for a loaded CI box.
func TestPromptKeyLatencyIsIndependentOfDocumentSize(t *testing.T) {
	if testing.Short() {
		t.Skip("latency measurement skipped in -short mode")
	}
	small := promptTestModel(t)
	large := promptTestModel(t)
	for i := 0; i < 600; i++ {
		large.records = append(large.records, record{
			role: roleSystem,
			text: "A paragraph of streamed content that occupies a few physical rows in the document.",
		})
	}
	large.refreshViewportContent()
	large.View()

	measure := func(m *model) time.Duration {
		const iters = 300
		start := time.Now()
		for i := range iters {
			nm, _ := m.Update(typeRune(rune('a' + i%26)))
			m = nm.(*model)
		}
		return time.Since(start) / iters
	}
	smallCost := measure(small)
	largeCost := measure(large)

	const budget = time.Millisecond
	if largeCost > budget {
		t.Errorf("prompt key cost on a %d-row document = %v, over the %v budget",
			large.docLayout.Len(), largeCost, budget)
	}
	// The isolation claim: growing the document 100x must not move the key cost
	// proportionally. A generous 20x bound still fails loudly if a document
	// composition has crept back onto the key path.
	if largeCost > smallCost*20 && largeCost > 50*time.Microsecond {
		t.Errorf("prompt key cost scaled with the document: %v (small) -> %v (large, %d rows)",
			smallCost, largeCost, large.docLayout.Len())
	}
}

// TestPromptComposeLeavesTheDocumentCacheIntact is the anti-side-effect check: the
// fast path must not, as a side effect of composing, warm or extend any document
// cache. A path that "helpfully" refreshed the rows would be O(document) again
// while looking isolated.
func TestPromptComposeLeavesTheDocumentCacheIntact(t *testing.T) {
	m := promptTestModel(t)
	nm, _ := m.Update(typeRune('z'))
	m = nm.(*model)

	rows := m.docRowRebuilds
	hitMap := m.hitMapRebuilds
	m.View()

	if m.docRowRebuilds != rows {
		t.Errorf("the prompt compose rebuilt the document row cache %d -> %d", rows, m.docRowRebuilds)
	}
	if m.hitMapRebuilds != hitMap {
		t.Errorf("the prompt compose rebuilt the hit map %d -> %d", hitMap, m.hitMapRebuilds)
	}
}

// TestPromptKeyIsEquivalentToTheChain pins the state-transition parity, and it is
// the claim the whole fast path rests on: for a key the guard accepts, running it
// through interceptPromptKey must leave the model in the same state the ordinary
// chain would have.
//
// The chain is reached by making the fast path DECLINE — a reasoning panel that is
// not yet mounted costs nothing to arrange and routes the key through the real
// PRIORITY 1 handler — and then the two models are compared on everything a
// keystroke can move. Anything the fast path forgets to write shows up here as a
// difference, which is the failure mode that would otherwise only appear as a
// cursor that does not blink or an Esc window that does not close.
func TestPromptKeyIsEquivalentToTheChain(t *testing.T) {
	// The fast-path model takes the shortcut.
	fast := promptTestModel(t)
	nm, fastCmd := fast.Update(typeRune('k'))
	fast = nm.(*model)

	// The chain model is identical except that the fast path declines, so the key
	// walks the whole routing chain and lands on the same handler.
	slow := promptTestModel(t)
	mountReasoningPanel(t, slow)
	// The panel is mounted for the FAST path too, so the only difference between
	// the two models is the guard's verdict, not their state.
	mountReasoningPanel(t, fast)
	nm, slowCmd := slow.Update(typeRune('k'))
	slow = nm.(*model)

	if fastCmd == nil || slowCmd == nil {
		t.Fatal("precondition: both paths must have handled the key")
	}
	if got, want := fast.ti.Value(), slow.ti.Value(); got != want {
		t.Errorf("prompt value: fast path %q, chain %q", got, want)
	}
	if got, want := fast.ti.Position(), slow.ti.Position(); got != want {
		t.Errorf("cursor position: fast path %d, chain %d", got, want)
	}
	if got, want := fast.input.String(), slow.input.String(); got != want {
		t.Errorf("mirrored input buffer: fast path %q, chain %q", got, want)
	}
	if fast.escCount != slow.escCount {
		t.Errorf("triple-Esc window: fast path %d, chain %d", fast.escCount, slow.escCount)
	}
	if !fast.scrollChromeDirty || !slow.scrollChromeDirty {
		t.Error("both paths must dirty the scroll chrome cache")
	}
	if !fast.promptDirty || !slow.promptDirty {
		t.Error("both paths must leave the prompt region dirty for the next frame")
	}
	if got, want := fast.documentGen, slow.documentGen; got != want {
		t.Errorf("document generation: fast path %d, chain %d — a keystroke must move neither", got, want)
	}
}

// TestPromptKeyDoesNotStealEnterOrEscape is the one behavioural claim the fast
// path must never make, stated as its own test because it is the one that would
// break the product rather than a frame: submission and the interrupt window must
// keep travelling the full chain, or the user cannot send what they typed or stop
// what is running.
func TestPromptKeyDoesNotStealEnterOrEscape(t *testing.T) {
	for _, tc := range []struct {
		name string
		msg  tea.KeyMsg
	}{
		{"enter", tea.KeyMsg{Type: tea.KeyEnter}},
		{"escape", tea.KeyMsg{Type: tea.KeyEsc}},
		{"ctrl-c", tea.KeyMsg{Type: tea.KeyCtrlC}},
		{"up", tea.KeyMsg{Type: tea.KeyUp}},
		{"pgup", tea.KeyMsg{Type: tea.KeyPgUp}},
		{"ctrl-o", tea.KeyMsg{Type: tea.KeyCtrlO}},
		{"tab", tea.KeyMsg{Type: tea.KeyTab}},
		{"backspace", tea.KeyMsg{Type: tea.KeyBackspace}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := promptTestModel(t)
			if cmd, handled := m.interceptPromptKey(tc.msg); handled {
				t.Fatalf("the fast path claimed %s (cmd %v)", tc.name, cmd != nil)
			}
		})
	}
}
