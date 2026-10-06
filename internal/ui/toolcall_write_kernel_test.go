package ui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/kernelbridge"
	"github.com/PizenLabs/izen/runtime/kernel"
)

// This file is the strangler proof for the write and read slices.
//
// It drives the REAL user-facing entry point: a native `write_file` tool call
// arriving from the model, buffered for review, and then approved by the human
// pressing "a" (accept) or "l" (allow all) in the tool-call approval prompt —
// the keys.go branch whose only action is to call applyToolCallBuffer. Nothing
// here constructs a kernel engine, names a capability, or reads the filesystem to
// decide what the outcome was.
//
// The assertions walk the chain link by link rather than checking the final
// status line. Asserting only the outcome would let the execution-start event,
// the grant, the evidence record, the mutation axis and the verification all be
// deleted and the test would still pass — which is the difference between "the
// write happened" and "the write was proven".

// TestToolCallWrite_IsProvenEndToEndThroughTheKernel is the black-box contract
// test for file.write.
//
// The links it proves, each asserted on its own:
//
//  1. the real user-facing entry point ran (the approval command, not a helper);
//  2. the execution started and was admitted under an explicit grant;
//  3. authorized capabilities were invoked — file.exists then file.write, each
//     naming the exact destination;
//  4. the bytes reached disk, re-read here with the test's own syscall;
//  5. evidence was produced, attributed to a step the program declared;
//  6. the mutation axis moved to APPLIED;
//  7. an independent verifier ran and passed;
//  8. adjudication reached PROVEN;
//  9. the file on disk holds exactly the requested content.
func TestToolCallWrite_IsProvenEndToEndThroughTheKernel(t *testing.T) {
	root := t.TempDir()
	const target = "src/app.go"
	const content = "package app\n\nfunc Hello() string { return \"hi\" }\n"

	m := &model{workspaceRoot: root, toolCallBuffer: execution.NewToolCallBuffer(root)}
	if err := m.toolCallBuffer.Buffer(writeFileCall("call-1", target, content)); err != nil {
		t.Fatalf("buffering the tool call failed: %v", err)
	}
	if err := m.toolCallBuffer.Approve(0); err != nil {
		t.Fatalf("approving the tool call failed: %v", err)
	}

	// ── 1. the real user-facing entry point ──────────────────────────────
	//
	// This is the exact command the "a"/"l" key handler runs. Calling
	// toolCallBuffer.ApplyApproved directly would prove the seam and not the
	// product.
	cmd := m.applyToolCallBuffer()
	if cmd == nil {
		t.Fatal("applyToolCallBuffer returned no command; the approval key would do nothing")
	}
	raw := cmd()
	msg, ok := raw.(applyAllResultMsg)
	if !ok {
		t.Fatalf("approval produced %T; want applyAllResultMsg", raw)
	}

	// ── 2. execution started, under an explicit grant ────────────────────
	applied := msg.execution
	if applied.ExecutionID == "" {
		t.Fatal("the approval command reported no kernel execution; the write did not go through the kernel")
	}
	if applied.State.Status != kernel.StatusSettled {
		t.Fatalf("state status = %s; want %s", applied.State.Status, kernel.StatusSettled)
	}
	if applied.State.Grant.ID == "" {
		t.Error("the execution ran with no grant; a mutation must never be authorized by anything else")
	}
	if !applied.State.Grant.Permits(kernel.FileWrite) {
		t.Errorf("grant %q does not permit file.write; the mutation was not authorized to do what it did",
			applied.State.Grant.ID)
	}
	if got := countEvents(applied.Events, kernel.EventExecutionStarted); got != 1 {
		t.Errorf("execution.started appears %d times; want exactly 1", got)
	}
	if got := countEvents(applied.Events, kernel.EventStepStarted); got == 0 {
		t.Fatal("no step.started events; nothing was dispatched")
	}

	// ── 3. authorized capability invocations, each naming the destination ─
	assertInvokedFor(t, applied, kernel.FileExists, target)
	assertInvokedFor(t, applied, kernel.FileWrite, target)

	// A mutation boundary is only meaningful if the grant names the destination,
	// so check it covers exactly what was written and nothing else.
	for _, covered := range applied.State.Grant.Targets {
		if covered != target {
			t.Errorf("grant covers %q; a write set's grant must name exactly the requested destinations", covered)
		}
	}

	// ── 4. the bytes reached disk, observed by this test's own syscall ───
	abs := filepath.Join(root, filepath.FromSlash(target))
	onDisk, err := os.ReadFile(abs)
	if err != nil {
		t.Fatalf("reading %s back from disk: %v\n"+
			"The approval reported a write over a file that is not there.", abs, err)
	}
	if string(onDisk) != content {
		t.Errorf("content on disk = %q; want %q", string(onDisk), content)
	}

	// ── 5. evidence, attributed to a step the program actually declared ──
	wrote := evidenceFor(applied, kernel.EvidenceFileWritten, target)
	if wrote == nil {
		t.Fatalf("no FILE_WRITTEN evidence for %s:\n%s", target, renderApplied(applied))
	}
	if wrote.Step == "" {
		t.Error("FILE_WRITTEN evidence names no step; unattributed evidence cannot be audited")
	}
	if wrote.Capability != kernel.FileWrite {
		t.Errorf("FILE_WRITTEN evidence claims capability %q; want %q", wrote.Capability, kernel.FileWrite)
	}
	if wrote.Verdict != kernel.VerdictPass {
		t.Errorf("FILE_WRITTEN evidence carries verdict %q; want %q", wrote.Verdict, kernel.VerdictPass)
	}
	if wrote.Bytes != len(content) {
		t.Errorf("FILE_WRITTEN evidence records %d bytes; want %d", wrote.Bytes, len(content))
	}
	// The write capability re-stats after committing, so presence is observed
	// twice, from two different capabilities. Requiring the second observation is
	// what makes the existence obligation satisfied by evidence rather than by the
	// write's own word.
	if evidenceFor(applied, kernel.EvidenceFilePresent, target) == nil {
		t.Errorf("no FILE_PRESENT evidence for %s; the write never confirmed the target exists:\n%s",
			target, renderApplied(applied))
	}
	// Presence must also have been observed BEFORE the write. That ordering is the
	// only thing that makes "created" derivable rather than a guess.
	assertPreWriteObservation(t, applied, target)

	// ── 6. the mutation axis moved ───────────────────────────────────────
	if applied.State.Mutation != kernel.MutationApplied {
		t.Errorf("mutation axis = %s; want %s. Bytes reached disk, so the axis must say so.",
			applied.State.Mutation, kernel.MutationApplied)
	}
	if got := countEvents(applied.Events, kernel.EventMutationApplied); got != 1 {
		t.Errorf("mutation.applied appears %d times; want exactly 1", got)
	}

	// ── 7. independent verification ──────────────────────────────────────
	if applied.Verify != kernel.VerifyPassed {
		t.Errorf("verify axis = %s; want %s (a required check must not settle as a skip)",
			applied.Verify, kernel.VerifyPassed)
	}
	assertEventSpans(t, applied.Events,
		kernel.EventMutationApplied, kernel.EventVerificationStarted)
	assertEventSpans(t, applied.Events,
		kernel.EventVerificationStarted, kernel.EventVerificationPassed)

	// ── 8. adjudication, not assertion ───────────────────────────────────
	if !applied.Proven() {
		t.Fatalf("outcome = %s (%s): %s; want PROVEN",
			applied.Outcome, applied.Class, applied.Reason)
	}
	if applied.Outcome != kernel.OutcomeProven {
		t.Errorf("Outcome = %s; want %s", applied.Outcome, kernel.OutcomeProven)
	}
	if !applied.Landed(target) {
		t.Error("Landed reported false on a PROVEN execution carrying write evidence for the target")
	}
	if !applied.Created(target) {
		t.Error("Created reported false; the kernel observed the destination absent before writing it")
	}
	// The contract is PATCH: "the named destination ends up holding this
	// content". It is stated as the one obligation that is true whether the write
	// creates or replaces, so it can never assert a creation that did not happen.
	if applied.Contract != kernel.ContractPatch {
		t.Errorf("contract = %s; want %s", applied.Contract, kernel.ContractPatch)
	}
	if applied.State.Spec.Contract.Kind.ForbidsMutation() {
		t.Error("the contract forbids mutation, so it cannot authorize a write")
	}

	// ── the chain is replayable ──────────────────────────────────────────
	//
	// A log that cannot be folded is a log that cannot be audited, and an audit
	// trail that cannot be reconstructed is decoration. This also proves the log
	// and the reported state are the same record rather than two.
	folded, err := kernel.Fold(kernel.Initial(applied.State.Spec, applied.State.Grant), applied.Events)
	if err != nil {
		t.Fatalf("the event log does not fold back into the state that reported PROVEN: %v", err)
	}
	if folded.Terminal.Outcome != applied.State.Terminal.Outcome {
		t.Errorf("folded outcome = %s; want %s. The log and the state disagree, so one of them is decoration.",
			folded.Terminal.Outcome, applied.State.Terminal.Outcome)
	}

	// The reported status line is derived from the same execution, not asserted
	// beside it.
	if len(msg.results) != 1 {
		t.Fatalf("reported %d results; want 1", len(msg.results))
	}
	if msg.results[0].file != target {
		t.Errorf("reported file = %q; want %q", msg.results[0].file, target)
	}
	if msg.results[0].status != "created" {
		t.Errorf("reported status = %q; want %q — the kernel observed the destination absent before writing it",
			msg.results[0].status, "created")
	}
}

// TestToolCallWrite_CreatesATargetAndSaysSo proves the creation case, including
// the one the old implementation got wrong.
//
// The old path decided "created" from `orig == ""` at buffering time. An existing
// zero-byte file therefore reported as newly created, and any file created between
// buffering and approval reported as a modification. Here the target is created
// AFTER buffering, so only an observation taken immediately before the write can
// establish that this is an overwrite.
func TestToolCallWrite_CreatesATargetAndSaysSo(t *testing.T) {
	root := t.TempDir()
	const target = "docs/new.md"

	m := &model{workspaceRoot: root, toolCallBuffer: execution.NewToolCallBuffer(root)}
	if err := m.toolCallBuffer.Buffer(writeFileCall("call-1", target, "# new\n")); err != nil {
		t.Fatalf("buffering failed: %v", err)
	}
	// Seed the target before approval, so a buffer-time answer would now be stale.
	seedFile(t, root, target, "existing\n")
	if err := m.toolCallBuffer.Approve(0); err != nil {
		t.Fatalf("approving failed: %v", err)
	}

	msg := mustApplyAll(t, m)
	if !msg.execution.Proven() {
		t.Fatalf("outcome = %s: %s; want PROVEN", msg.execution.Outcome, msg.execution.Reason)
	}
	if msg.execution.Created(target) {
		t.Error("Created reported true; the target existed before this execution, so it was overwritten, not created")
	}
	if got := msg.results[0].status; got != "modified" {
		t.Errorf("reported status = %q; want %q. The runtime observed the target present before writing it.",
			got, "modified")
	}
	if body := readFile(t, root, target); body != "# new\n" {
		t.Errorf("content = %q; want the requested replacement", body)
	}
}

// TestToolCallWrite_AnEmptyExistingFileIsNotACreation is the case the deleted
// `orig == ""` heuristic could not distinguish at all.
//
// A zero-byte file is present. It is not new. Reporting it as created is a claim
// about the filesystem that no observation supports, and it is exactly the kind of
// claim this migration exists to remove.
func TestToolCallWrite_AnEmptyExistingFileIsNotACreation(t *testing.T) {
	root := t.TempDir()
	const target = "empty.txt"
	seedFile(t, root, target, "")

	m := &model{workspaceRoot: root, toolCallBuffer: execution.NewToolCallBuffer(root)}
	if err := m.toolCallBuffer.Buffer(writeFileCall("call-1", target, "now populated\n")); err != nil {
		t.Fatalf("buffering failed: %v", err)
	}
	if err := m.toolCallBuffer.Approve(0); err != nil {
		t.Fatalf("approving failed: %v", err)
	}

	msg := mustApplyAll(t, m)
	if !msg.execution.Proven() {
		t.Fatalf("outcome = %s: %s; want PROVEN", msg.execution.Outcome, msg.execution.Reason)
	}
	if msg.execution.Created(target) {
		t.Error("a zero-byte file was reported as created; it existed, so nothing was created")
	}
	if got := msg.results[0].status; got != "modified" {
		t.Errorf("reported status = %q; want %q", got, "modified")
	}
	if !msg.execution.Exists(target) {
		t.Error("the execution did not prove the target present after writing it")
	}
}

// TestToolCallWrite_RefusesToEscapeTheWorkspace proves the boundary the deleted
// bare os.WriteFile never had.
//
// resolvePath joined the working directory with whatever path the model asked
// for, so `../` walked straight out of the workspace and the write landed there
// with no grant, no evidence and no verification. Now the FIRST kernel boundary
// the path meets refuses it, which is strictly better than catching it at the
// write: the approval prompt is never even rendered, so there is nothing for a
// human to approve by mistake.
//
// The write boundary itself is proved at the seam, in
// internal/kernelbridge/apply_test.go, where the escape can be presented directly.
func TestToolCallWrite_RefusesToEscapeTheWorkspace(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(filepath.Dir(root), "escaped.txt")
	t.Cleanup(func() { _ = os.Remove(outside) })

	m := &model{workspaceRoot: root, toolCallBuffer: execution.NewToolCallBuffer(root)}
	err := m.toolCallBuffer.Buffer(writeFileCall("call-1", "../escaped.txt", "nope\n"))
	if err == nil {
		t.Fatal("a path outside the workspace was buffered for approval")
	}
	if !strings.Contains(err.Error(), "AUTHORIZATION") {
		t.Errorf("error = %q; want an AUTHORIZATION-classed refusal, not a generic failure", err)
	}
	if m.toolCallBuffer.HasPending() {
		t.Error("the escaping call reached the approval prompt anyway")
	}
	if _, statErr := os.Stat(outside); statErr == nil {
		t.Fatalf("%s exists; the workspace boundary was crossed", outside)
	}
}

// TestToolCallRead_AnAbsentTargetIsAnObservedFact is the file.read half of the
// slice, and the case the old code could not express.
//
// `os.ReadFile` on a missing file returns an error. The old buffer treated ANY
// read error as "the file is empty", so a permission failure, a directory and a
// genuinely absent file were indistinguishable — and the write that followed
// replaced the first two with nothing.
//
// Here FILE_ABSENT reaches PROVEN. It is a fact about the filesystem: not an
// error, and not empty content.
func TestToolCallRead_AnAbsentTargetIsAnObservedFact(t *testing.T) {
	root := t.TempDir()
	const target = "not/there.txt"

	buffer := execution.NewToolCallBuffer(root)
	if err := buffer.Buffer(writeFileCall("call-1", target, "content\n")); err != nil {
		t.Fatalf("buffering a write to an absent target failed: %v", err)
	}

	buffered := buffer.All()
	if len(buffered) != 1 {
		t.Fatalf("buffered calls = %d; want 1", len(buffered))
	}
	if buffered[0].Original != "" {
		t.Errorf("Original = %q; an absent target has no baseline content", buffered[0].Original)
	}
	if !strings.Contains(buffered[0].Diff, target) {
		t.Errorf("diff does not name the target:\n%s", buffered[0].Diff)
	}
	if !toolCallCreatesFile(buffered[0]) {
		t.Error("the diff of a write to an absent target does not present as a creation")
	}
}

// TestToolCallRead_APatchAgainstAnAbsentTargetIsRefused proves absence is
// reported, not substituted.
//
// A patch needs existing content to modify. Reading some other file's bytes to
// compute a baseline would be inventing the thing the mutation is supposed to
// change, so the runtime refuses instead.
func TestToolCallRead_APatchAgainstAnAbsentTargetIsRefused(t *testing.T) {
	root := t.TempDir()

	buffer := execution.NewToolCallBuffer(root)
	err := buffer.Buffer(applyPatchCall("call-1", "gone.txt", "old", "new"))
	if err == nil {
		t.Fatal("apply_patch against an absent target succeeded")
	}
	if !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("error = %q; want an explicit refusal naming the absence", err)
	}
}

// TestToolCallRead_ReadsExistingContentForTheDiff proves the read side returns
// real bytes, so the diff a human approves is computed from proven content rather
// than from a syscall that might have failed.
func TestToolCallRead_ReadsExistingContentForTheDiff(t *testing.T) {
	root := t.TempDir()
	const target = "src/app.go"
	seedFile(t, root, target, "package app\n\nfunc Old() {}\n")

	buffer := execution.NewToolCallBuffer(root)
	if err := buffer.Buffer(writeFileCall("call-1", target, "package app\n\nfunc New() {}\n")); err != nil {
		t.Fatalf("buffering failed: %v", err)
	}
	buffered := buffer.All()[0]
	if !strings.Contains(buffered.Original, "func Old()") {
		t.Errorf("Original = %q; want the file's real current content", buffered.Original)
	}
	if !strings.Contains(buffered.Diff, "-func Old()") || !strings.Contains(buffered.Diff, "+func New()") {
		t.Errorf("diff does not describe the real change:\n%s", buffered.Diff)
	}
	if toolCallCreatesFile(buffered) {
		t.Error("an overwrite is presented as a creation; the baseline was non-empty")
	}
}

// TestToolCallRead_AnUnreadableTargetIsRefusedNotSilentlyEmpty is the failure
// mode the old `err == nil` check turned into data loss.
//
// os.ReadFile on a directory fails with EISDIR. The old buffer swallowed that and
// treated the target as empty, so the approval diff showed an empty baseline and
// the write replaced whatever was there. Now the read is an adjudicated execution
// that cannot reach a verdict, and the buffer refuses.
func TestToolCallRead_AnUnreadableTargetIsRefusedNotSilentlyEmpty(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "pkg"), 0o755); err != nil {
		t.Fatalf("creating the directory target: %v", err)
	}

	buffer := execution.NewToolCallBuffer(root)
	err := buffer.Buffer(writeFileCall("call-1", "pkg", "content\n"))
	if err == nil {
		t.Fatal("writing over a directory target succeeded; the unreadable target was treated as empty")
	}
	if !strings.Contains(err.Error(), "could not answer") {
		t.Errorf("error = %q; want an explicit report that the runtime could not answer", err)
	}
}

// ── helpers ─────────────────────────────────────────────────────────────────

// mustApplyAll runs the real approval command and requires the success message.
func mustApplyAll(t *testing.T, m *model) applyAllResultMsg {
	t.Helper()
	cmd := m.applyToolCallBuffer()
	if cmd == nil {
		t.Fatal("applyToolCallBuffer returned no command")
	}
	raw := cmd()
	msg, ok := raw.(applyAllResultMsg)
	if !ok {
		t.Fatalf("approval produced %T: %+v; want applyAllResultMsg", raw, raw)
	}
	return msg
}

func writeFileCall(id, path, content string) ai.ToolCall {
	args, err := json.Marshal(ai.WriteFileParams{Path: path, Content: content})
	if err != nil {
		panic(err)
	}
	return ai.ToolCall{
		ID:       id,
		Type:     "function",
		Function: ai.ToolCallFunction{Name: ai.ToolWriteFile, Arguments: string(args)},
	}
}

func applyPatchCall(id, path, search, replace string) ai.ToolCall {
	args, err := json.Marshal(ai.ApplyPatchParams{Path: path, Search: search, Replace: replace})
	if err != nil {
		panic(err)
	}
	return ai.ToolCall{
		ID:       id,
		Type:     "function",
		Function: ai.ToolCallFunction{Name: ai.ToolApplyPatch, Arguments: string(args)},
	}
}

// assertInvokedFor requires that the capability actually ran for the destination.
//
// It checks the invocation event rather than the mere presence of evidence,
// because evidence without an invocation describes something the engine never did.
func assertInvokedFor(t *testing.T, applied kernelbridge.Applied, capability kernel.CapabilityID, target string) {
	t.Helper()
	for _, e := range applied.Events {
		if e.Kind == kernel.EventCapabilityInvoked && e.Capability == capability && e.Target == target {
			return
		}
	}
	t.Errorf("no capability.invoked for %s on %s:\n%s", capability, target, renderApplied(applied))
}

// assertPreWriteObservation requires that presence was observed BEFORE the write,
// which is the only thing that makes "created" derivable rather than asserted.
func assertPreWriteObservation(t *testing.T, applied kernelbridge.Applied, target string) {
	t.Helper()
	wroteAt := -1
	for i, e := range applied.Events {
		if e.Kind == kernel.EventMutationApplied && e.Target == target {
			wroteAt = i
			break
		}
	}
	if wroteAt < 0 {
		t.Fatalf("no mutation.applied for %s:\n%s", target, renderApplied(applied))
	}
	for _, e := range applied.Events[:wroteAt] {
		if e.Kind == kernel.EventEvidenceProduced && e.Capability == kernel.FileExists && e.Target == target {
			return
		}
	}
	t.Errorf("nothing observed %s before it was written; creation cannot be established from this log", target)
}

// assertEventSpans requires that first appears before second, and that neither is
// missing. Order matters because a log that verifies before it mutates describes
// a different runtime than the one this test is proving.
func assertEventSpans(t *testing.T, events []kernel.Event, first, second kernel.EventKind) {
	t.Helper()
	firstAt, secondAt := -1, -1
	for i, e := range events {
		if e.Kind == first && firstAt < 0 {
			firstAt = i
		}
		if e.Kind == second && secondAt < 0 {
			secondAt = i
		}
	}
	if firstAt < 0 {
		t.Fatalf("event %s is missing from the chain", first)
	}
	if secondAt < 0 {
		t.Fatalf("event %s is missing from the chain", second)
	}
	if firstAt > secondAt {
		t.Errorf("%s at %d comes after %s at %d", first, firstAt, second, secondAt)
	}
}

func countEvents(events []kernel.Event, kind kernel.EventKind) int {
	n := 0
	for _, e := range events {
		if e.Kind == kind {
			n++
		}
	}
	return n
}

func evidenceFor(applied kernelbridge.Applied, kind kernel.EvidenceKind, target string) *kernel.Evidence {
	for i, e := range applied.Evidence {
		if e.Kind == kind && e.Target == target {
			return &applied.Evidence[i]
		}
	}
	return nil
}

// renderApplied renders the whole chain, so a failure message shows the reader
// what actually happened rather than only which assertion tripped.
func renderApplied(applied kernelbridge.Applied) string {
	var sb strings.Builder
	sb.WriteString("  execution " + applied.ExecutionID + " → " + string(applied.Outcome) + "\n")
	if applied.Reason != "" {
		sb.WriteString("  reason: " + applied.Reason + "\n")
	}
	if len(applied.Evidence) == 0 {
		sb.WriteString("  (no evidence recorded)\n")
	}
	for _, e := range applied.Evidence {
		sb.WriteString("  " + e.ID + " " + string(e.Kind) + " " + e.Target +
			" (" + string(e.Capability) + "/" + e.Step + ", verdict=" + string(e.Verdict) + ")\n")
	}
	for _, e := range applied.Events {
		sb.WriteString("  seq=" + strconv.FormatUint(e.Seq, 10) + " " + string(e.Kind))
		if e.Step != "" {
			sb.WriteString(" step=" + e.Step)
		}
		if e.Capability != "" {
			sb.WriteString(" cap=" + string(e.Capability))
		}
		if e.Block != nil {
			sb.WriteString(" block=" + e.Block.Error())
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

func seedFile(t *testing.T, root, rel, content string) {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", rel, err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatalf("seeding %s: %v", rel, err)
	}
}

func readFile(t *testing.T, root, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("reading %s back from disk: %v", rel, err)
	}
	return string(data)
}
