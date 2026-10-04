package execution

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/kernelbridge"
)

// ── ToolCallBuffer ────────────────────────────────────────────────────────────
// ToolCallBuffer is the in-memory interceptor for native LLM tool calls.
// When the LLM responds with finish_reason: "tool_calls", the calls are buffered
// here and NOT applied to disk. A unified diff preview is generated for each call,
// and the user must explicitly approve before any disk mutation occurs.

// BufferedToolCall holds a single intercepted tool call with its computed diff.
//
// It no longer records whether the target is new. It used to, as `IsNew`, decided
// by comparing the baseline against the empty string — which reports an existing
// zero-byte file as new, and goes stale the moment anything touches the workspace
// between buffering and approval. Whether a write CREATED its destination is now
// established by the kernel observing the destination immediately before
// committing, and reported back on ToolCallResult.
type BufferedToolCall struct {
	ID       string
	Name     string
	Path     string
	Original string
	Modified string
	Diff     string
	Approved bool
}

// ToolCallBuffer intercepts and buffers tool calls, generating diff previews.
//
// It performs no filesystem mutation and no filesystem read of its own any more.
// Both directions are adjudicated kernel executions: reads while buffering, so
// the diff a human approves is computed from proven content, and the write on
// approval, so what reaches disk is written under an explicit grant and
// re-verified from disk afterwards.
type ToolCallBuffer struct {
	mu      sync.Mutex
	calls   []BufferedToolCall
	cwd     string
	applied bool
}

// NewToolCallBuffer creates a buffer for the given working directory.
func NewToolCallBuffer(cwd string) *ToolCallBuffer {
	return &ToolCallBuffer{cwd: cwd}
}

// Buffer parses tool call arguments, reads the destination's current content
// through the Runtime Kernel, computes the modified content and a unified diff,
// and stores everything in memory. Returns an error if the arguments cannot be
// parsed or if the runtime could not establish the baseline.
func (b *ToolCallBuffer) Buffer(tc ai.ToolCall) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	btc, err := bufferToolCall(tc, b.cwd)
	if err != nil {
		return fmt.Errorf("buffer tool call %q (%s): %w", tc.Function.Name, tc.ID, err)
	}
	b.calls = append(b.calls, *btc)
	return nil
}

// BufferAll buffers multiple tool calls in a single call.
func (b *ToolCallBuffer) BufferAll(tcs []ai.ToolCall) error {
	for _, tc := range tcs {
		if err := b.Buffer(tc); err != nil {
			return err
		}
	}
	return nil
}

// Pending returns all unapproved tool calls.
func (b *ToolCallBuffer) Pending() []BufferedToolCall {
	b.mu.Lock()
	defer b.mu.Unlock()
	var pending []BufferedToolCall
	for _, c := range b.calls {
		if !c.Approved {
			pending = append(pending, c)
		}
	}
	return pending
}

// All returns all buffered tool calls.
func (b *ToolCallBuffer) All() []BufferedToolCall {
	b.mu.Lock()
	defer b.mu.Unlock()
	result := make([]BufferedToolCall, len(b.calls))
	copy(result, b.calls)
	return result
}

// Count returns the total number of buffered calls.
func (b *ToolCallBuffer) Count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.calls)
}

// Approve marks a single buffered call as approved by index.
func (b *ToolCallBuffer) Approve(index int) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if index < 0 || index >= len(b.calls) {
		return fmt.Errorf("tool call index %d out of range (0-%d)", index, len(b.calls)-1)
	}
	b.calls[index].Approved = true
	return nil
}

// ApproveAll marks all buffered calls as approved.
func (b *ToolCallBuffer) ApproveAll() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i := range b.calls {
		b.calls[i].Approved = true
	}
}

// Reject removes all buffered calls without applying them.
func (b *ToolCallBuffer) Reject() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls = nil
}

// ApplyApproved writes all approved buffered calls to disk, through the Runtime
// Kernel, and returns the results describing what actually landed.
//
// The write does not happen here any more. It used to be a bare os.WriteFile per
// approved call: no event, no state, no evidence and no verification, written in
// place so a failure mid-write left a truncated target, unconfinement to the
// working directory, and a hardcoded 0644 that discarded the mode of the file it
// was replacing. It is now a single MUTATE execution — observed, authorized,
// written by the filesystem capability, and re-read from disk by a verifier that
// did not write it — whose verdict is adjudicated from evidence.
//
// A destination the kernel could not prove landed is reported as an error, never
// as a result. "The runtime could not prove this file was written" and "this file
// was written" must not read the same way to the human who approved it.
//
// Destinations that DID land are reported alongside the failure, because a
// partial write is a partial write and the caller has to be able to reconcile
// what reached disk.
func (b *ToolCallBuffer) ApplyApproved(ctx context.Context) (*ToolCallResults, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.applied {
		return &ToolCallResults{}, nil
	}

	approved := make([]int, 0, len(b.calls))
	for i := range b.calls {
		if b.calls[i].Approved {
			approved = append(approved, i)
		}
	}
	if len(approved) == 0 {
		b.applied = true
		return &ToolCallResults{}, nil
	}

	// The contract is DECLARED here, and it is always PATCH: "the named
	// destinations end up holding this content". That is the one obligation this
	// path can state truthfully for every call in the batch, whether it creates a
	// file or replaces one.
	//
	// The tempting alternative is to declare CREATE for calls that look like
	// creations, and it is wrong twice over. It cannot be decided here at all,
	// because the only honest answer is a filesystem observation and this function
	// runs before the kernel has observed anything. And CREATE's obligations are a
	// strict superset of PATCH's, so declaring it for a call that turns out to
	// overwrite an existing file would assert the creation of a file that was
	// already there.
	//
	// Whether a write CREATED its destination is not an obligation here — it is a
	// fact, and it is read back from the kernel's own pre-write observation below.
	writes := make([]kernelbridge.Write, 0, len(approved))
	for _, i := range approved {
		writes = append(writes, kernelbridge.Write{
			Target:   b.calls[i].Path,
			Content:  b.calls[i].Modified,
			Contract: kernelbridge.ContractPatch,
		})
	}

	applied := kernelbridge.Apply(ctx, b.cwd, writes)

	results := make([]ToolCallResult, 0, len(approved))
	for _, i := range approved {
		if !applied.Landed(b.calls[i].Path) {
			continue
		}
		results = append(results, ToolCallResult{
			File:     b.calls[i].Path,
			Original: b.calls[i].Original,
			Modified: b.calls[i].Modified,
			// IsNew is now evidence, not a guess carried over from buffering
			// time. The kernel observed the destination before and after the
			// write; "created" is what those two observations jointly establish.
			// A buffer-time guess gets this wrong for an existing empty file,
			// which it reports as newly created.
			IsNew: applied.Created(b.calls[i].Path),
		})
	}

	if !applied.Proven() {
		// applied is deliberately NOT set. Nothing here claims the remaining
		// calls were written, and a later attempt must stay free to try again
		// rather than being told the batch already ran.
		return &ToolCallResults{Results: results, Execution: applied}, fmt.Errorf(
			"tool call write under %s did not reach a proven terminal state: %s (%s), execution %s, %d/%d destination(s) proven written",
			applied.Contract, applied.Outcome, applied.Reason, applied.ExecutionID,
			len(results), len(approved))
	}

	b.applied = true
	return &ToolCallResults{Results: results, Execution: applied}, nil
}

// ApplyPending approves all pending calls and applies them in one step.
func (b *ToolCallBuffer) ApplyPending(ctx context.Context) (*ToolCallResults, error) {
	b.ApproveAll()
	return b.ApplyApproved(ctx)
}

// Reset clears the buffer for reuse.
func (b *ToolCallBuffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls = nil
	b.applied = false
}

// HasPending returns true if there are calls that have not been approved.
func (b *ToolCallBuffer) HasPending() bool {
	return len(b.Pending()) > 0
}

func bufferToolCall(tc ai.ToolCall, cwd string) (*BufferedToolCall, error) {
	switch tc.Function.Name {
	case ai.ToolWriteFile:
		return bufferWriteFile(tc, cwd)
	case ai.ToolApplyPatch:
		return bufferApplyPatch(tc, cwd)
	default:
		return nil, fmt.Errorf("unknown tool: %s", tc.Function.Name)
	}
}

func bufferWriteFile(tc ai.ToolCall, cwd string) (*BufferedToolCall, error) {
	var params ai.WriteFileParams
	if err := json.Unmarshal([]byte(tc.Function.Arguments), &params); err != nil {
		return nil, fmt.Errorf("parse write_file arguments: %w", err)
	}

	orig, _, err := readThroughKernel(cwd, params.Path)
	if err != nil {
		return nil, err
	}

	diff := buildDiff(orig, params.Content, params.Path)

	return &BufferedToolCall{
		ID:       tc.ID,
		Name:     tc.Function.Name,
		Path:     params.Path,
		Original: orig,
		Modified: params.Content,
		Diff:     diff,
	}, nil
}

func bufferApplyPatch(tc ai.ToolCall, cwd string) (*BufferedToolCall, error) {
	var params ai.ApplyPatchParams
	if err := json.Unmarshal([]byte(tc.Function.Arguments), &params); err != nil {
		return nil, fmt.Errorf("parse apply_patch arguments: %w", err)
	}

	orig, absent, err := readThroughKernel(cwd, params.Path)
	if err != nil {
		return nil, err
	}
	if absent {
		// A patch has nothing to patch. This is a refusal rather than a
		// substitution: reading some other file's content to compute a diff would
		// be inventing the baseline the mutation is supposed to change.
		return nil, fmt.Errorf("apply_patch target %s does not exist; a patch needs existing content to modify", params.Path)
	}

	// Use the whitespace/indentation-tolerant matcher shared with the LLM
	// SEARCH/REPLACE pipeline so a search block with minor whitespace drift
	// still applies cleanly instead of failing with a context mismatch.
	modified, ok := ApplySearchReplace(orig, params.Search, params.Replace)
	if !ok || modified == orig {
		return nil, fmt.Errorf("search text not found in %s", params.Path)
	}

	diff := buildDiff(orig, modified, params.Path)

	return &BufferedToolCall{
		ID:       tc.ID,
		Name:     tc.Function.Name,
		Path:     params.Path,
		Original: orig,
		Modified: modified,
		Diff:     diff,
	}, nil
}

// readThroughKernel returns one workspace file's content, read as adjudicated
// kernel truth rather than as a syscall answer.
//
// Absence is a reported FACT, not a failure and not an empty string. The caller
// receives a genuinely proven-absent signal and decides what that means for its
// own tool — write_file treats it as a creation, apply_patch refuses — because
// only the caller knows which of the two it is asking for.
//
// A read that could not reach a verdict is an error. Returning "" for it would
// make an unreadable file indistinguishable from an empty one, which is how a
// permission error becomes a silent overwrite.
func readThroughKernel(root, target string) (content string, absent bool, err error) {
	read := kernelbridge.ReadFiles(context.Background(), root, []string{target})
	switch {
	case read.Absent(target):
		return "", true, nil
	case read.Found(target):
		return read.Content(target), false, nil
	default:
		return "", false, fmt.Errorf(
			"read %s under %s: the runtime could not answer (%s): %s",
			target, read.Outcome, read.Class, read.Reason)
	}
}

// buildDiff generates a minimal unified diff between old and new content.
func buildDiff(oldContent, newContent, filePath string) string {
	if oldContent == "" && newContent != "" {
		return fmt.Sprintf("--- a/%s\n+++ b/%s\n@@ -0,0 +1,%d @@\n%s",
			filePath, filePath, len(strings.Split(newContent, "\n")),
			addPlusPrefix(newContent))
	}
	if oldContent != "" && newContent == "" {
		return fmt.Sprintf("--- a/%s\n+++ b/%s\n@@ -1,%d +0,0 @@\n",
			filePath, filePath, len(strings.Split(oldContent, "\n")))
	}
	if oldContent == newContent {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "--- a/%s\n+++ b/%s\n", filePath, filePath)
	oldLines := strings.Split(oldContent, "\n")
	newLines := strings.Split(newContent, "\n")
	fmt.Fprintf(&b, "@@ -1,%d +1,%d @@\n", len(oldLines), len(newLines))
	for _, line := range oldLines {
		b.WriteString("-" + line + "\n")
	}
	for _, line := range newLines {
		b.WriteString("+" + line + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func addPlusPrefix(content string) string {
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		lines[i] = "+" + line
	}
	return strings.Join(lines, "\n")
}

// ── Results ─────────────────────────────────────────────────────────────────

// ToolCallResult is what one buffered call turned into on disk.
//
// IsNew is not a flag this package sets from a guess. It is read back from the
// kernel's own evidence — the destination was observed absent immediately before
// the write committed — so "created" means the runtime looked and found nothing
// there, not that a buffer happened to hold an empty string.
type ToolCallResult struct {
	File     string
	Original string
	Modified string
	IsNew    bool
}

type ToolCallResults struct {
	Results []ToolCallResult

	// Execution is the kernel execution that produced these results.
	//
	// It is carried for the same reason the autonomy target resolution carries its
	// observations: a caller reporting what changed to a human has to be able to
	// say what PROVED it. "wrote 3 files" is not an answer a reader can check,
	// and the whole point of the migration is that the answer is checkable.
	//
	// It is the zero value when no write was attempted, in which case Results is
	// empty too — the two cannot disagree, because Results is only ever populated
	// from this execution's evidence.
	Execution kernelbridge.Applied
}

func (r ToolCallResults) Summary() string {
	if len(r.Results) == 0 {
		return "No files modified."
	}
	parts := make([]string, 0, len(r.Results))
	for _, res := range r.Results {
		if res.IsNew {
			parts = append(parts, fmt.Sprintf("Created %s (%d bytes)", res.File, len(res.Modified)))
		} else {
			parts = append(parts, fmt.Sprintf("Modified %s", res.File))
		}
	}
	return strings.Join(parts, "\n")
}

func (r ToolCallResults) HasContent() bool {
	return len(r.Results) > 0
}

func (r ToolCallResults) LastFile() string {
	if len(r.Results) == 0 {
		return ""
	}
	return r.Results[len(r.Results)-1].File
}

func (r ToolCallResults) FirstResult() *ToolCallResult {
	if len(r.Results) == 0 {
		return nil
	}
	return &r.Results[0]
}
