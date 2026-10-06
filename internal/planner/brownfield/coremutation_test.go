package brownfield

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/PizenLabs/izen/internal/ir"
	"github.com/PizenLabs/izen/internal/resource/file"
	appruntime "github.com/PizenLabs/izen/internal/runtime"
	"github.com/PizenLabs/izen/internal/runtime/substrate"
)

// recordingAuthority is a test double for the Core execution authority. It
// records every proposal it is handed and returns a scripted proof/error. It
// performs no filesystem effect, so a test that wants a real mutation wires
// *substrate.ConcreteSubstrate instead; this double is for proving that the
// graph path hands its intent to Core and derives its result from Core's
// answer rather than from a dispatch.
type recordingAuthority struct {
	mu        sync.Mutex
	proposals []substrate.Proposal
	proof     substrate.ExecutionProof
	err       error
	block     bool
}

func (r *recordingAuthority) Execute(ctx context.Context, prop substrate.Proposal) (substrate.ExecutionProof, error) {
	if r.block {
		<-ctx.Done()
		return substrate.ExecutionProof{}, ctx.Err()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.proposals = append(r.proposals, prop)
	return r.proof, r.err
}

func (r *recordingAuthority) recorded() []substrate.Proposal {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]substrate.Proposal(nil), r.proposals...)
}

// globFiles returns every regular file matching pattern, failing the test on a
// walk error that is not "not found".
func globFiles(t *testing.T, pattern string) []string {
	t.Helper()
	matches, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatalf("glob %s: %v", pattern, err)
	}
	return matches
}

// TestBrownfieldMutationSubmitsTruthfulProposal proves the graph write node
// hands Core the truthful operation intent — one FILE_WRITE for the exact
// workspace-relative target with the exact desired content — and derives its
// result from Core's answer. The authority is a recording double, so the only
// thing under test is the shape of the request.
func TestBrownfieldMutationSubmitsTruthfulProposal(t *testing.T) {
	root := t.TempDir()
	rec := &recordingAuthority{proof: substrate.ExecutionProof{Status: "committed"}}
	p, err := NewBrownfieldPlanner(root,
		WithVerifyCommand(func(string) string { return "true" }),
		WithMutationExecutor(rec))
	if err != nil {
		t.Fatalf("NewBrownfieldPlanner: %v", err)
	}

	content := []byte("<!DOCTYPE html><html><body>hello</body></html>")
	res, err := p.Plan(t.Context(), "edit", []ir.Artifact{ir.NewFile("src/page.html", content)})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if err := p.ExecuteAndRepair(t.Context(), appruntime.NewTaskEngine(nil), res.Graph, 0); err != nil {
		t.Fatalf("ExecuteAndRepair: %v", err)
	}

	props := rec.recorded()
	if len(props) != 1 {
		t.Fatalf("authority received %d proposal(s), want 1", len(props))
	}
	if len(props[0].Operations) != 1 {
		t.Fatalf("proposal carries %d operation(s), want 1", len(props[0].Operations))
	}
	op := props[0].Operations[0]
	if op.Type != substrate.OpFileWrite {
		t.Errorf("operation type = %s, want %s", op.Type, substrate.OpFileWrite)
	}
	if op.Target != "src/page.html" {
		t.Errorf("operation target = %q, want %q", op.Target, "src/page.html")
	}
	if string(op.Content) != string(content) {
		t.Errorf("operation content = %q, want %q", op.Content, content)
	}
}

// TestBrownfieldDeleteSubmitsDeleteIntent proves the delete direction is
// truthful too: the graph's delete intent reaches Core as FILE_DELETE, not as
// a write and not as a raw resource Delete.
func TestBrownfieldDeleteSubmitsDeleteIntent(t *testing.T) {
	root := t.TempDir()
	rec := &recordingAuthority{proof: substrate.ExecutionProof{Status: "committed"}}
	base, err := file.NewFileResource(root, "obsolete.html", 0)
	if err != nil {
		t.Fatalf("NewFileResource: %v", err)
	}
	target := &coreMutationTarget{base: base, exec: rec, seq: new(atomic.Uint64)}

	if err := target.DeleteContext(t.Context()); err != nil {
		t.Fatalf("DeleteContext: %v", err)
	}
	props := rec.recorded()
	if len(props) != 1 || len(props[0].Operations) != 1 {
		t.Fatalf("expected one single-operation proposal, got %+v", props)
	}
	if got := props[0].Operations[0].Type; got != substrate.OpFileDelete {
		t.Errorf("operation type = %s, want %s", got, substrate.OpFileDelete)
	}
}

// TestBrownfieldPlanFailsClosedWithoutAuthority proves the invariant directly:
// a brownfield plan that would write cannot do so without a Core execution
// authority. It fails closed rather than falling back to a raw resource.
func TestBrownfieldPlanFailsClosedWithoutAuthority(t *testing.T) {
	root := t.TempDir()
	p, err := NewBrownfieldPlanner(root)
	if err != nil {
		t.Fatalf("NewBrownfieldPlanner: %v", err)
	}
	_, err = p.Plan(t.Context(), "edit", []ir.Artifact{ir.NewFile("page.html", []byte("<h1>x</h1>"))})
	if !errors.Is(err, ErrNoMutationAuthority) {
		t.Fatalf("Plan error = %v, want ErrNoMutationAuthority", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "page.html")); statErr == nil {
		t.Fatal("a plan without a Core authority mutated the workspace")
	}
}

// TestBrownfieldAuthorityRefusalIsReportedAndInert proves the graph derives
// its result from Core's answer: an authority refusal fails the write node and
// the target is untouched, and no Core proof artifact is produced for a
// mutation that never landed.
func TestBrownfieldAuthorityRefusalIsReportedAndInert(t *testing.T) {
	root := t.TempDir()
	rec := &recordingAuthority{err: errors.New("authorization denied")}
	p, err := NewBrownfieldPlanner(root,
		WithVerifyCommand(func(string) string { return "true" }),
		WithMutationExecutor(rec))
	if err != nil {
		t.Fatalf("NewBrownfieldPlanner: %v", err)
	}

	res, err := p.Plan(t.Context(), "edit", []ir.Artifact{ir.NewFile("page.html", []byte("<h1>x</h1>"))})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if err := p.ExecuteAndRepair(t.Context(), appruntime.NewTaskEngine(nil), res.Graph, 0); err == nil {
		t.Fatal("a refused mutation was reported as success")
	}
	if _, statErr := os.Stat(filepath.Join(root, "page.html")); statErr == nil {
		t.Fatal("a refused mutation changed the workspace")
	}
	if proofs := globFiles(t, filepath.Join(root, ".izen", "substrate", "*.proof")); len(proofs) != 0 {
		t.Fatalf("a refused mutation produced proof evidence: %v", proofs)
	}
}

// TestBrownfieldContextCancellationPropagates proves the Core-routed target
// threads the operation context into the authority, so a canceled or
// deadline-bound run cannot silently commit.
func TestBrownfieldContextCancellationPropagates(t *testing.T) {
	root := t.TempDir()
	rec := &recordingAuthority{block: true}
	base, err := file.NewFileResource(root, "page.html", 0)
	if err != nil {
		t.Fatalf("NewFileResource: %v", err)
	}
	target := &coreMutationTarget{base: base, exec: rec, seq: new(atomic.Uint64)}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := target.WriteContext(ctx, []byte("<h1>x</h1>")); !errors.Is(err, context.Canceled) {
		t.Fatalf("WriteContext error = %v, want context.Canceled", err)
	}
}

// TestBrownfieldAuthorizedMutationCrossesKernel is the full-path proof against
// a REAL existing workspace: the mutation is authorized by Core, transacted,
// committed through kernelbridge and the runtime kernel, changes the requested
// target, and produces both primitive (kernel) evidence and Core (substrate)
// evidence that verify.
func TestBrownfieldAuthorizedMutationCrossesKernel(t *testing.T) {
	root := t.TempDir()
	// A brownfield workspace: the target already exists.
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("<h1>old</h1>"), 0o644); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}

	exec := substrate.NewConcreteSubstrate(root)
	p, err := NewBrownfieldPlanner(root,
		WithVerifyCommand(func(string) string { return "true" }),
		WithMutationExecutor(exec))
	if err != nil {
		t.Fatalf("NewBrownfieldPlanner: %v", err)
	}

	newContent := []byte("<!DOCTYPE html><html><body>new</body></html>")
	res, err := p.Plan(t.Context(), "edit", []ir.Artifact{ir.NewFile("index.html", newContent)})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if err := p.ExecuteAndRepair(t.Context(), appruntime.NewTaskEngine(nil), res.Graph, 0); err != nil {
		t.Fatalf("ExecuteAndRepair: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(root, "index.html"))
	if err != nil {
		t.Fatalf("read mutated target: %v", err)
	}
	if string(got) != string(newContent) {
		t.Fatalf("target content = %q, want %q", got, newContent)
	}

	// Primitive + Core evidence: the durable proof artifact records the
	// kernel's adjudicated outcome for the mutation, not the graph's intent.
	proofs := globFiles(t, filepath.Join(root, ".izen", "substrate", "*.proof"))
	if len(proofs) == 0 {
		t.Fatal("no Core proof artifact was written for an authorized mutation")
	}
	var sawProven bool
	for _, path := range proofs {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("read proof %s: %v", path, readErr)
		}
		if strings.Contains(string(data), "outcome=PROVEN") && strings.Contains(string(data), "landed=true") {
			sawProven = true
		}
	}
	if !sawProven {
		t.Fatalf("no proof recorded a PROVEN, landed mutation: %v", proofs)
	}

	// Core evidence store: a status is recorded per transaction.
	records := globFiles(t, filepath.Join(root, ".izen", "substrate", "evidence", "*.json"))
	if len(records) == 0 {
		t.Fatal("no Core evidence record was written")
	}
	var sawCommitted bool
	for _, path := range records {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("read evidence %s: %v", path, readErr)
		}
		if strings.Contains(string(data), `"status": "committed"`) {
			sawCommitted = true
		}
	}
	if !sawCommitted {
		t.Fatalf("no Core evidence record reported a committed transaction: %v", records)
	}
}

// TestBrownfieldSymlinkEscapeRefused proves Core authorization is enforced at
// use time: a target that is lexically inside the workspace but a symlink
// resolves outside it mutates nothing beyond the boundary and produces no
// mutation evidence.
func TestBrownfieldSymlinkEscapeRefused(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	exec := substrate.NewConcreteSubstrate(root)
	p, err := NewBrownfieldPlanner(root,
		WithVerifyCommand(func(string) string { return "true" }),
		WithMutationExecutor(exec))
	if err != nil {
		t.Fatalf("NewBrownfieldPlanner: %v", err)
	}

	res, err := p.Plan(t.Context(), "edit", []ir.Artifact{ir.NewFile("link/evil.html", []byte("<h1>evil</h1>"))})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if err := p.ExecuteAndRepair(t.Context(), appruntime.NewTaskEngine(nil), res.Graph, 0); err == nil {
		t.Fatal("a symlink escape was not refused")
	}
	if _, statErr := os.Stat(filepath.Join(outside, "evil.html")); statErr == nil {
		t.Fatal("a symlink escape mutated a file outside the workspace")
	}
	// No mutation evidence: the durable proof artifact (which carries the
	// per-mutation kernel outcome) is only written for a committed mutation.
	if proofs := globFiles(t, filepath.Join(root, ".izen", "substrate", "*.proof")); len(proofs) != 0 {
		t.Fatalf("a refused escape produced proof evidence: %v", proofs)
	}
	// The refusal is still recorded as a failed transaction, so the attempt is
	// auditable without being a mutation.
	records := globFiles(t, filepath.Join(root, ".izen", "substrate", "evidence", "*.json"))
	if len(records) == 0 {
		t.Fatal("a refused escape left no audit record")
	}
	var sawFailed bool
	for _, path := range records {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("read evidence %s: %v", path, readErr)
		}
		if strings.Contains(string(data), `"status": "failed"`) {
			sawFailed = true
		}
	}
	if !sawFailed {
		t.Fatalf("no failed transaction was recorded for the refused escape: %v", records)
	}
}

// TestBrownfieldKernelCreatesParentDirectories proves the eager os.MkdirAll
// that used to run before the brownfield graph is gone: a nested target is
// created by the kernel's file.write capability as part of the authorized
// mutation.
func TestBrownfieldKernelCreatesParentDirectories(t *testing.T) {
	root := t.TempDir()
	exec := substrate.NewConcreteSubstrate(root)
	p, err := NewBrownfieldPlanner(root,
		WithVerifyCommand(func(string) string { return "true" }),
		WithMutationExecutor(exec))
	if err != nil {
		t.Fatalf("NewBrownfieldPlanner: %v", err)
	}

	content := []byte("<h1>nested</h1>")
	res, err := p.Plan(t.Context(), "edit", []ir.Artifact{ir.NewFile("a/b/c/page.html", content)})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if err := p.ExecuteAndRepair(t.Context(), appruntime.NewTaskEngine(nil), res.Graph, 0); err != nil {
		t.Fatalf("ExecuteAndRepair: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(root, "a", "b", "c", "page.html"))
	if err != nil {
		t.Fatalf("read nested target: %v", err)
	}
	if string(got) != string(content) {
		t.Fatalf("nested content = %q, want %q", got, content)
	}
}

// TestBrownfieldVerificationFailureWritesNothing proves the Core pre-commit
// verification gate applies to brownfield writes: a Go artifact whose content
// does not parse is refused before it reaches disk, and no proof is produced
// for a mutation that never landed.
func TestBrownfieldVerificationFailureWritesNothing(t *testing.T) {
	root := t.TempDir()
	exec := substrate.NewConcreteSubstrate(root)
	p, err := NewBrownfieldPlanner(root,
		WithVerifyCommand(func(string) string { return "true" }),
		WithMutationExecutor(exec))
	if err != nil {
		t.Fatalf("NewBrownfieldPlanner: %v", err)
	}

	res, err := p.Plan(t.Context(), "edit", []ir.Artifact{ir.NewFile("broken.go", []byte("this is not go"))})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if err := p.ExecuteAndRepair(t.Context(), appruntime.NewTaskEngine(nil), res.Graph, 0); err == nil {
		t.Fatal("an invalid artifact passed Core verification")
	}
	if _, statErr := os.Stat(filepath.Join(root, "broken.go")); statErr == nil {
		t.Fatal("an artifact rejected by Core verification reached disk")
	}
	if proofs := globFiles(t, filepath.Join(root, ".izen", "substrate", "*.proof")); len(proofs) != 0 {
		t.Fatalf("a refused mutation produced proof evidence: %v", proofs)
	}
}

// TestBrownfieldDirectoryTargetRefused proves a failed write is reported as a
// failure rather than an optimistic success: a target that is a directory is
// refused and the directory is unchanged.
func TestBrownfieldDirectoryTargetRefused(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "adir"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	exec := substrate.NewConcreteSubstrate(root)
	p, err := NewBrownfieldPlanner(root,
		WithVerifyCommand(func(string) string { return "true" }),
		WithMutationExecutor(exec))
	if err != nil {
		t.Fatalf("NewBrownfieldPlanner: %v", err)
	}

	res, err := p.Plan(t.Context(), "edit", []ir.Artifact{ir.NewFile("adir", []byte("<h1>x</h1>"))})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if err := p.ExecuteAndRepair(t.Context(), appruntime.NewTaskEngine(nil), res.Graph, 0); err == nil {
		t.Fatal("writing over a directory was reported as success")
	}
	info, statErr := os.Stat(filepath.Join(root, "adir"))
	if statErr != nil || !info.IsDir() {
		t.Fatalf("the directory target was disturbed: info=%v err=%v", info, statErr)
	}
}

// TestBrownfieldMissingDeleteIsToleratedNoOp proves delete semantics are
// truthful: removing a file that is already absent is not a failure (the end
// state holds) but it is also not reported as a durable removal.
func TestBrownfieldMissingDeleteIsToleratedNoOp(t *testing.T) {
	root := t.TempDir()
	exec := substrate.NewConcreteSubstrate(root)
	base, err := file.NewFileResource(root, "already-gone.html", 0)
	if err != nil {
		t.Fatalf("NewFileResource: %v", err)
	}
	target := &coreMutationTarget{base: base, exec: exec, seq: new(atomic.Uint64)}

	if err := target.DeleteContext(t.Context()); err != nil {
		t.Fatalf("deleting an absent target must be a tolerated no-op, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "already-gone.html")); !os.IsNotExist(statErr) {
		t.Fatalf("deleting an absent target created one: %v", statErr)
	}
}
