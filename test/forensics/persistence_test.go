package forensics_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/events/audit"
	"github.com/PizenLabs/izen/internal/forensics"
)

// ── POST-MORTEM RECONSTRUCTION ───────────────────────────────────────────────
//
// The point of persisting a trace is that it OUTLIVES the process. A reader that
// only works against a live subscription answers questions about runs that are
// still in flight, which is precisely when nobody is asking.
//
// These tests drive a real run against a REAL audit logger rooted at a real
// directory — the same wiring `cmd/izen/main.go` uses — flush it, drop every
// in-process reference, and then rebuild the trace from the file alone.

// TestForensics_TraceSurvivesTheProcess proves a run is reconstructable from
// the durable audit log alone.
func TestForensics_TraceSurvivesTheProcess(t *testing.T) {
	root := t.TempDir()
	auditDir := filepath.Join(root, ".izen", "audit")

	live := runWithAudit(t, root, auditDir, []*ai.Response{
		answered(1800, 260, searchReplacePong),
	})
	if live.State != "awaiting_human" {
		t.Fatalf("live state = %s, want awaiting_human at the approval gate", live.State)
	}

	// Every in-process reference is now gone; only the file remains.
	path := filepath.Join(auditDir, "events.ndjson")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("audit log was not written: %v", err)
	}
	postMortem, err := forensics.ReadNDJSON(path, "")
	if err != nil {
		t.Fatalf("read persisted trace: %v", err)
	}
	t.Logf("\n%s", postMortem)

	// The post-mortem trace must answer the same questions the live one did.
	if postMortem.Summary.Status == "" {
		t.Fatal("post-mortem trace carries no run summary")
	}
	if postMortem.Authorization.Verdict == "" {
		t.Fatal("post-mortem trace carries no authorization verdict")
	}
	if postMortem.Spec.Intent == "" {
		t.Fatal("post-mortem trace carries no frozen execution spec")
	}
	if len(postMortem.Decisions) == 0 {
		t.Fatal("post-mortem trace carries no continuation decisions")
	}
	// Structural counters must survive the redaction boundary that removes raw
	// prompt bytes. Asserting the token count proves the reader restored the
	// concrete payload type rather than falling back to raw JSON.
	if postMortem.Summary.InputTokens != live.TokensIn {
		t.Fatalf("post-mortem input tokens = %d, want %d", postMortem.Summary.InputTokens, live.TokensIn)
	}
}

// TestForensics_ReadNDJSONSkipsATruncatedTail proves a log whose writer was
// killed mid-line is still readable.
//
// A truncated final line is the NORMAL state of a log belonging to a crashed
// process. Aborting the read on it would make the forensic log useless for
// precisely the incidents it exists to explain.
func TestForensics_ReadNDJSONSkipsATruncatedTail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.ndjson")

	good := `{"id":"a","timestamp":"2026-01-01T00:00:00Z","source":"execution.authorized","kind":"system","payload":{"run_id":"run-1","granted":true,"blocked":false,"verdict":"allow","authority":"preflight_admission_gate"}}` + "\n"
	truncated := `{"id":"b","timestamp":"2026-01-01T00:00:01Z","source":"execution.summary","kind":"system","payload":{"run_id":"run-1","st`

	if err := os.WriteFile(path, []byte(good+truncated), 0o644); err != nil {
		t.Fatal(err)
	}
	tr, err := forensics.ReadNDJSON(path, "run-1")
	if err != nil {
		t.Fatalf("a log with a truncated tail must still be readable: %v", err)
	}
	if tr.Authorization.Verdict != "allow" {
		t.Fatalf("authorization verdict = %q, want allow", tr.Authorization.Verdict)
	}
	if tr.SkippedLines != 1 {
		t.Fatalf("skipped lines = %d, want 1", tr.SkippedLines)
	}
}

// TestForensics_ReadNDJSONReportsAMissingRun proves the reader fails loudly when
// asked for a run that is not in the log, rather than returning an empty trace
// that reads like a clean run.
func TestForensics_ReadNDJSONReportsAMissingRun(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.ndjson")
	line := `{"id":"a","timestamp":"2026-01-01T00:00:00Z","source":"execution.authorized","kind":"system","payload":{"run_id":"run-1","granted":true,"verdict":"allow"}}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := forensics.ReadNDJSON(path, "run-99"); err == nil {
		t.Fatal("reader returned a trace for a run that is not in the log")
	}
}

// TestForensics_ReadNDJSONDrainsAcrossALiveProcess proves the durable read and
// the live read produce the SAME control-plane facts.
//
// Two readers over the same run that disagree about what happened would make
// every conclusion drawn from either of them unfalsifiable. The projection is
// shared; only the transport differs, and this is the assertion that it stays
// that way.
func TestForensics_ReadNDJSONDrainsAcrossALiveProcess(t *testing.T) {
	root := t.TempDir()
	auditDir := filepath.Join(root, ".izen", "audit")
	live := runWithAudit(t, root, auditDir, []*ai.Response{
		answered(1800, 260, searchReplacePong),
	})
	path := filepath.Join(auditDir, "events.ndjson")

	fromDisk, err := forensics.ReadNDJSON(path, "")
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	if fromDisk.Authorization.Verdict != live.AuthorizationVerdict {
		t.Fatalf("authorization verdict diverged: live=%q disk=%q",
			live.AuthorizationVerdict, fromDisk.Authorization.Verdict)
	}
	if len(fromDisk.Decisions) != live.Decisions {
		t.Fatalf("continuation decisions diverged: live=%d disk=%d",
			live.Decisions, len(fromDisk.Decisions))
	}
	if fromDisk.Summary.ModelCalls != live.SummaryModelCalls {
		t.Fatalf("model call count diverged: live=%d disk=%d",
			live.SummaryModelCalls, fromDisk.Summary.ModelCalls)
	}
	t.Logf("live and post-mortem agree: %d decisions, %d model calls, verdict=%q",
		len(fromDisk.Decisions), fromDisk.Summary.ModelCalls, fromDisk.Authorization.Verdict)
}

// ── BENCHMARK C — CONTINUATION ──────────────────────────────────────────────
//
// A task whose completion legitimately requires more than one model
// interaction: the first answer is refused, so the second must therefore differ
// from the first.
//
// What it tests: whether IZEN has a real continuation — one that changes what it
// asks — or merely a bounded retry. The discriminator is the request
// fingerprint: a continuation that re-issues the identical prompt has learned
// nothing, and the reader flags it NON_PROGRESSING_CONTINUATION.

func TestBenchmarkC_Continuation(t *testing.T) {
	p := &scriptedProvider{name: "scripted", responses: []*ai.Response{
		// Call #1 returns prose where the artifact contract was demanded.
		answered(900, 120, "Sure! Here is what I would do to change that file, in plain prose."),
		// Call #2 answers the contract.
		answered(1200, 240, searchReplacePong),
	}}
	r := harness(t, p)
	r.write("note.txt", "foo\nbar\nbaz\n")

	term, err := r.Driver.Run(context.Background(), "change bar to qux @note.txt")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	tr := r.report()

	reqs := p.recorded()
	t.Logf("termination: %v", fmtTerm(term))
	t.Logf("model calls: %d", len(reqs))
	for i, rq := range reqs {
		t.Logf("  call #%d budget=%d fingerprint=%s user_chars=%d",
			i+1, rq.MaxTokens, shortFP(ai.RequestFingerprint(rq)),
			len(rq.Messages[len(rq.Messages)-1].Content))
	}

	// ── THE CONTINUATION QUESTION ─────────────────────────────────────
	// A continuation exists only if the runtime changed what it asked. Report
	// the fingerprints so the answer is visible either way; assert only the
	// property the architecture claims structurally — that a refused contract
	// cannot produce an unbounded number of calls.
	if len(reqs) > 1 {
		first, second := ai.RequestFingerprint(reqs[0]), ai.RequestFingerprint(reqs[1])
		if first == second {
			t.Logf("IDENTICAL REQUEST re-issued on continuation (fp=%s)", shortFP(first))
			for _, pat := range tr.Patterns {
				if pat == forensics.PatternNonProgressingContinue {
					t.Fatalf("continuation re-issued an identical request (fp=%s)\n%s", shortFP(first), tr)
				}
			}
		} else {
			t.Logf("CONTINUATION CHANGED THE REQUEST: %s -> %s", shortFP(first), shortFP(second))
		}
	}
	if len(reqs) > 3 {
		t.Fatalf("a refused contract produced %d provider calls\n%s", len(reqs), tr)
	}
}

func shortFP(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

// auditOutcome is what the audit harness reports back to a test once the
// in-process objects it would otherwise need have been dropped.
type auditOutcome struct {
	State                string
	TokensIn             int
	AuthorizationVerdict string
	Decisions            int
	SummaryModelCalls    int
}

// runWithAudit drives one bounded run against a REAL audit logger rooted at a
// real directory — the same wiring `cmd/izen/main.go` uses — flushes it, closes
// it, and returns only the facts a post-mortem comparison needs.
func runWithAudit(t *testing.T, root, auditDir string, responses []*ai.Response) auditOutcome {
	t.Helper()
	bus := events.NewBus(events.DefaultBufferSize)

	logger, err := audit.NewLogger(auditDir, bus)
	if err != nil {
		t.Fatalf("audit logger: %v", err)
	}
	if err := logger.Start(); err != nil {
		t.Fatalf("audit start: %v", err)
	}

	p := &scriptedProvider{name: "scripted", responses: responses}
	r := harnessOn(t, root, bus, p)

	if _, err := r.Driver.Run(context.Background(), "change bar to qux @note.txt"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// The production durability seam: a blocking flush, so the log on disk is
	// complete before the test reads it. Reading before this point would test
	// the logger's buffering schedule rather than the forensic record.
	if err := logger.Flush(); err != nil {
		t.Fatalf("audit flush: %v", err)
	}
	if err := logger.Close(); err != nil {
		t.Fatalf("audit close: %v", err)
	}

	tr := r.trace()
	return auditOutcome{
		State:                string(r.Driver.State()),
		TokensIn:             tr.Summary.InputTokens,
		AuthorizationVerdict: tr.Authorization.Verdict,
		Decisions:            len(tr.Decisions),
		SummaryModelCalls:    tr.Summary.ModelCalls,
	}
}

var _ = time.Second
