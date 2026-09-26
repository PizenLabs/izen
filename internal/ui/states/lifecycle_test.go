package states

import (
	"strings"
	"sync"
	"testing"
)

// TestIndicatorContract pins the five indicator strings. These are the
// user-visible contract of the pre-execution lifecycle: a change here is a
// visible change to the product, not a refactor.
func TestIndicatorContract(t *testing.T) {
	cases := []struct {
		state  State
		target string
		want   string
	}{
		{StateTablePending, "", "[struct] Constructing table view..."},
		{StateCodePending, "", "[code] Formatting code block..."},
		{StateWorkspacePatch, "internal/ui/model.go", "[mutation] Staging edit @internal/ui/model.go..."},
		{StateToolExecution, "", "[exec] Preparing tool execution..."},
		{StateAstIndexing, "", "[index] Mapping workspace context..."},
	}
	for _, c := range cases {
		if got := c.state.Describe(c.target); got != c.want {
			t.Errorf("State(%d).Describe(%q) = %q, want %q", c.state, c.target, got, c.want)
		}
	}
}

// TestIdleRendersNothing asserts the no-fabricated-work invariant: the resting
// state has no indicator at all, so a stalled process can never be mistaken for
// an active one.
func TestIdleRendersNothing(t *testing.T) {
	if got := StateIdle.Describe("anything"); got != "" {
		t.Errorf("StateIdle.Describe = %q, want empty", got)
	}
	if StateIdle.Pending() {
		t.Error("StateIdle must not be pending")
	}
	if StateIdle.Tag() != "" {
		t.Errorf("StateIdle.Tag = %q, want empty", StateIdle.Tag())
	}
}

// TestEveryStateIndicatorIsOneLine is the single-line invariant expressed as a
// test: no indicator may contain a newline no matter what target it is handed.
func TestEveryStateIndicatorIsOneLine(t *testing.T) {
	hostile := []string{
		"a.go",
		"a\nb.go",
		"a\rb.go",
		"a\tb.go",
		"dir/\n\n/etc/passwd",
		"   ",
		"",
	}
	for _, s := range All() {
		for _, target := range hostile {
			line := s.Describe(target)
			if strings.ContainsAny(line, "\n\r\t") {
				t.Errorf("State(%s).Describe(%q) = %q contains a line/tab break", s, target, line)
			}
			if line == "" {
				t.Errorf("State(%s).Describe(%q) is empty", s, target)
			}
		}
	}
}

// TestNonTargetedStatesIgnoreTarget keeps the four fixed indicator strings
// exactly as specified: only the workspace-patch state names a subject.
func TestNonTargetedStatesIgnoreTarget(t *testing.T) {
	for _, s := range All() {
		if s.Targeted() {
			continue
		}
		plain := s.Describe("")
		withTarget := s.Describe("some/deep/path.go")
		if plain != withTarget {
			t.Errorf("non-targeted State(%s) must ignore its target: %q vs %q", s, plain, withTarget)
		}
	}
}

// TestWorkspacePatchFallsBackToDefaultTarget asserts an unresolved edit still
// renders a complete claim rather than "[mutation] Staging edit @...".
func TestWorkspacePatchFallsBackToDefaultTarget(t *testing.T) {
	got := StateWorkspacePatch.Describe("")
	want := "[mutation] Staging edit @" + DefaultTarget + "..."
	if got != want {
		t.Errorf("Describe(\"\") = %q, want %q", got, want)
	}
}

// TestUnknownStateIsNotPending guards the extension seam: a state added to the
// enum but not to Pending/Describe must not mount an indicator that renders
// nothing.
func TestUnknownStateIsNotPending(t *testing.T) {
	unknown := State(200)
	if unknown.Pending() {
		t.Error("an unknown state must not be pending")
	}
	if got := unknown.Describe("x"); got != "" {
		t.Errorf("unknown state Describe = %q, want empty", got)
	}
	if unknown.String() != "unknown" {
		t.Errorf("unknown String = %q", unknown.String())
	}
}

// TestMachineEnterExit covers the single-slot mount lifecycle.
func TestMachineEnterExit(t *testing.T) {
	m := NewMachine()
	if m.Active() {
		t.Fatal("a fresh machine must be idle")
	}
	if m.Epoch() != 0 {
		t.Fatalf("fresh epoch = %d, want 0", m.Epoch())
	}
	snap, ok := m.Enter(StateWorkspacePatch, "main.go")
	if !ok {
		t.Fatal("Enter must accept a pending state")
	}
	if !snap.Active || snap.State != StateWorkspacePatch || snap.Target != "main.go" {
		t.Fatalf("snapshot = %+v", snap)
	}
	if snap.Indicator() != "[mutation] Staging edit @main.go..." {
		t.Fatalf("Indicator = %q", snap.Indicator())
	}
	if snap.Epoch != 1 {
		t.Fatalf("epoch after Enter = %d, want 1", snap.Epoch)
	}
	if !m.Active() {
		t.Fatal("machine must be active after Enter")
	}
	if _, ok := m.Exit(); !ok {
		t.Fatal("Exit must report that it unmounted something")
	}
	if m.Active() {
		t.Fatal("machine must be idle after Exit")
	}
	if _, ok := m.Exit(); ok {
		t.Fatal("a redundant Exit must report false")
	}
}

// TestMachineIgnoresNonPendingEnter is the no-fabricated-work invariant at the
// machine boundary: StateIdle can never mount an indicator.
func TestMachineIgnoresNonPendingEnter(t *testing.T) {
	m := NewMachine()
	if _, ok := m.Enter(StateIdle, ""); ok {
		t.Fatal("Enter(StateIdle) must be rejected")
	}
	if m.Active() {
		t.Fatal("rejected Enter must leave the machine idle")
	}
	if _, ok := m.Enter(State(99), ""); ok {
		t.Fatal("Enter(unknown) must be rejected")
	}
	if m.Epoch() != 0 {
		t.Fatalf("rejected Enter must not bump the epoch, got %d", m.Epoch())
	}
}

// TestMachineSupersedesWithoutStacking is invariant #1: a second pre-execution
// state replaces the first, because the viewport holds exactly one line.
func TestMachineSupersedesWithoutStacking(t *testing.T) {
	m := NewMachine()
	m.Enter(StateCodePending, "")
	first := m.Snapshot()
	snap, ok := m.Enter(StateToolExecution, "")
	if !ok {
		t.Fatal("second Enter must be accepted")
	}
	if snap.Epoch <= first.Epoch {
		t.Fatalf("supersession must advance the epoch: %d -> %d", first.Epoch, snap.Epoch)
	}
	if m.Snapshot().State != StateToolExecution {
		t.Fatalf("state = %v, want StateToolExecution", m.Snapshot().State)
	}
}

// TestAdvanceKeepsEpoch is the in-flight refinement seam: a state that learns
// its subject mid-mount must not invalidate a finalizer already in flight.
func TestAdvanceKeepsEpoch(t *testing.T) {
	m := NewMachine()
	m.Enter(StateWorkspacePatch, "")
	before := m.Epoch()
	snap := m.Advance(StateWorkspacePatch, "resolved/file.go")
	if snap.Epoch != before {
		t.Fatalf("Advance bumped the epoch: %d -> %d", before, snap.Epoch)
	}
	if snap.Target != "resolved/file.go" {
		t.Fatalf("Advance target = %q", snap.Target)
	}
	// An empty label must not blank an already-resolved subject.
	snap = m.Advance(StateWorkspacePatch, "")
	if snap.Target != "resolved/file.go" {
		t.Fatalf("empty Advance target blanked the subject: %q", snap.Target)
	}
}

// TestAdvanceOnRestingMachineIsAFirstMount covers the zero-value path.
func TestAdvanceOnRestingMachineIsAFirstMount(t *testing.T) {
	var m Machine
	snap := m.Advance(StateTablePending, "")
	if !snap.Active || snap.Epoch != 1 {
		t.Fatalf("zero-value machine Advance = %+v", snap)
	}
	// A non-pending Advance exits.
	snap = m.Advance(StateIdle, "")
	if snap.Active {
		t.Fatalf("Advance(StateIdle) must exit, got %+v", snap)
	}
}

// TestRelabelOnlyChangesTheSubject pins the "narrow seam" contract.
func TestRelabelOnlyChangesTheSubject(t *testing.T) {
	m := NewMachine()
	m.Enter(StateWorkspacePatch, "old.go")
	epoch := m.Epoch()
	snap := m.Relabel("new.go")
	if snap.State != StateWorkspacePatch || snap.Target != "new.go" || snap.Epoch != epoch {
		t.Fatalf("Relabel = %+v (epoch %d)", snap, epoch)
	}
	// Relabel on a resting machine must not mount anything.
	var idle Machine
	if got := idle.Relabel("x.go"); got.Active {
		t.Fatalf("Relabel mounted an indicator on a resting machine: %+v", got)
	}
}

// TestExitIgnoresNothingAndSupersessionIsObservable is the two halves of the
// staleness contract: a redundant Exit is reported, and a supersession is
// observable through the epoch so a caller can tell the mounts apart.
func TestExitIgnoresNothingAndSupersessionIsObservable(t *testing.T) {
	m := NewMachine()
	if _, ok := m.Exit(); ok {
		t.Fatal("Exit on a resting machine reported success")
	}
	first, _ := m.Enter(StateCodePending, "")
	second, _ := m.Enter(StateWorkspacePatch, "live.go")
	if second.Epoch == first.Epoch {
		t.Fatalf("supersession did not advance the epoch: %d", second.Epoch)
	}
	if m.Epoch() != second.Epoch {
		t.Fatalf("Epoch() = %d, want %d", m.Epoch(), second.Epoch)
	}
	if _, ok := m.Exit(); !ok {
		t.Fatal("Exit reported failure on a mounted machine")
	}
	if _, ok := m.Exit(); ok {
		t.Fatal("a redundant Exit reported success")
	}
}

// TestAdvanceIsAFirstMountOnARestingMachineWithNoEpochBump: advancing a resting
// machine mounts, but Exit reports the machine as having nothing to unmount —
// so the two are distinguishable.
func TestAdvanceIsNotIdempotentOnARestingMachine(t *testing.T) {
	var m Machine
	if m.Advance(StateTablePending, "").Epoch != 1 {
		t.Fatal("the first Advance did not bump the epoch")
	}
	if m.Advance(StateTablePending, "").Epoch != 1 {
		t.Fatal("a refining Advance must not bump the epoch")
	}
}

// TestNilMachineIsSafe keeps the render path total: a headless harness that
// never constructs a machine must not panic on a read.
func TestNilMachineIsSafe(t *testing.T) {
	var m *Machine
	if m.Active() || m.Epoch() != 0 {
		t.Fatal("nil machine must read as idle")
	}
	if _, ok := m.Enter(StateCodePending, ""); ok {
		t.Fatal("nil machine Enter must report false")
	}
	if _, ok := m.Exit(); ok {
		t.Fatal("nil machine Exit must report false")
	}
	if s := m.Snapshot(); s.Active {
		t.Fatal("nil machine snapshot must be inactive")
	}
	if s := m.Advance(StateCodePending, ""); s.Active {
		t.Fatal("nil machine Advance must be inactive")
	}
	if s := m.Relabel("x"); s.Active {
		t.Fatal("nil machine Relabel must be inactive")
	}
}

// TestConcurrentEnterSnapshot is the race guard: the runtime stages patches from
// worker goroutines while the UI goroutine renders.
func TestConcurrentEnterSnapshot(t *testing.T) {
	m := NewMachine()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				m.Enter(StateWorkspacePatch, "f.go")
				m.Snapshot()
				m.Active()
			}
		}(i)
	}
	wg.Wait()
	if !m.Active() {
		t.Fatal("machine must still be mounted after concurrent traffic")
	}
}

// TestSanitizeIsLossyByDesign documents the one place target fidelity is
// deliberately given up: the indicator is a status line, the finalized content
// that replaces it carries the real path.
func TestSanitizeIsLossyByDesign(t *testing.T) {
	cases := map[string]string{
		"  main.go  ":       "main.go",
		"a\nb":              "a b",
		"a\t\tb":            "a b",
		"\r\n":              "",
		"dir/sub/final.go":  "dir/sub/final.go",
		"a\x00b":            "a b",
		"":                  "",
		"   ":               "",
		"no  double spaces": "no double spaces",
	}
	for in, want := range cases {
		if got := Sanitize(in); got != want {
			t.Errorf("Sanitize(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestAllCoversEveryPendingState keeps All() and Pending() in agreement, so a
// caller enumerating the vocabulary can never miss a state.
func TestAllCoversEveryPendingState(t *testing.T) {
	all := All()
	if len(all) != 5 {
		t.Fatalf("All() returned %d states, want 5", len(all))
	}
	seen := map[State]bool{}
	for _, s := range all {
		if !s.Pending() {
			t.Errorf("All() contains non-pending %v", s)
		}
		if seen[s] {
			t.Errorf("All() repeats %v", s)
		}
		seen[s] = true
		if s.String() == "unknown" {
			t.Errorf("state %d has no String()", uint8(s))
		}
	}
}

// TestStringNamesAreStable pins the machine names used in trace output.
func TestStringNamesAreStable(t *testing.T) {
	want := map[State]string{
		StateIdle:           "idle",
		StateTablePending:   "table-pending",
		StateCodePending:    "code-pending",
		StateWorkspacePatch: "workspace-patch",
		StateToolExecution:  "tool-execution",
		StateAstIndexing:    "ast-indexing",
	}
	for s, name := range want {
		if got := s.String(); got != name {
			t.Errorf("State(%d).String() = %q, want %q", uint8(s), got, name)
		}
	}
}

// TestSanitizeDoesNotPanicOnInvalidUTF8 keeps the render path total for file
// names that are not valid UTF-8 (a legal thing for a path on disk).
func TestSanitizeDoesNotPanicOnInvalidUTF8(t *testing.T) {
	bad := string([]byte{0xff, 0xfe, 0x00, 'a', '\n', 'b'})
	got := Sanitize(bad)
	if strings.ContainsAny(got, "\n\r\x00") {
		t.Fatalf("Sanitize leaked a control byte: %q", got)
	}
}
