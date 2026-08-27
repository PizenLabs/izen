package execution

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution/ingestion"
)

// TestIngestionTraceAttachedOnTransportNormalizedSuccess proves the transport
// normalization pipeline is wired into the L1 Execution Gate: a fenced provider
// response is normalized, the raw output is preserved verbatim on the
// IngestionTrace, and the trace is attached to the active execution log context
// (ExecutionResult) for forensic traceability.
func TestIngestionTraceAttachedOnTransportNormalizedSuccess(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "index.html", validHTML)
	fenced := "```html\n" + validHTML + "\n```"
	mock := &mockProvider{responses: []*ai.Response{{Content: fenced, Usage: reproUsage}}}
	x := testExecutor(t, root, mock, events.NewBus(events.DefaultBufferSize))

	res, err := x.Execute(context.Background(), ExecuteRequest{
		RequestID: "ing-success",
		Mode:      "build",
		Prompt:    "rewrite index.html",
		Target:    "index.html",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IngestionTrace == nil {
		t.Fatal("ExecutionResult.IngestionTrace is nil — transport pipeline not wired into L1 gate")
	}
	// Raw preservation: the exact, unmutated fenced response is retained.
	if res.IngestionTrace.RawOutput != fenced {
		t.Fatalf("RawOutput not preserved exactly:\n got %q\nwant %q", res.IngestionTrace.RawOutput, fenced)
	}
	if res.IngestionTrace.Classification != ingestion.ClassTransportNormalized {
		t.Fatalf("classification = %s, want %s", res.IngestionTrace.Classification, ingestion.ClassTransportNormalized)
	}
}

// TestIngestionTraceAttachedOnSyntaxInvalid proves that a payload failing basic
// envelope integrity (unterminated <script>) is classified ClassSyntaxInvalid,
// rejected to the contract retry loop (ErrArtifactRetryableRejected) WITHOUT
// silent tag completion, and the forensic IngestionTrace is attached to the
// sealed terminal evidence.
func TestIngestionTraceAttachedOnSyntaxInvalid(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "index.html", validHTML)
	mock := &mockProvider{responses: []*ai.Response{{Content: malformedHTML, Usage: reproUsage}}}
	x := testExecutor(t, root, mock, events.NewBus(events.DefaultBufferSize))

	res, err := x.Execute(context.Background(), ExecuteRequest{
		RequestID: "ing-syntax",
		Mode:      "build",
		Prompt:    "rewrite index.html",
		Target:    "index.html",
	})
	if err == nil {
		t.Fatal("Execute must fail on the syntactically invalid payload")
	}
	if !errors.Is(err, ErrArtifactRetryableRejected) {
		t.Fatalf("err = %v, want ErrArtifactRetryableRejected (contract retry feedback)", err)
	}
	if res.IngestionTrace == nil {
		t.Fatal("ExecutionResult.IngestionTrace is nil on syntax-invalid rejection")
	}
	if res.IngestionTrace.Classification != ingestion.ClassSyntaxInvalid {
		t.Fatalf("classification = %s, want %s", res.IngestionTrace.Classification, ingestion.ClassSyntaxInvalid)
	}
	// No silent semantic repair: the unclosed <script> is preserved verbatim.
	if strings.Contains(res.IngestionTrace.NormalizedPayload, "</script>") {
		t.Fatalf("ingestion silently closed the <script> tag: %q", res.IngestionTrace.NormalizedPayload)
	}
	if !strings.Contains(res.IngestionTrace.RawOutput, "<script>") {
		t.Fatalf("raw output lost the <script> tag: %q", res.IngestionTrace.RawOutput)
	}
	// Forensic trace attached to the terminal (rejected) evidence.
	if res.Evidence == nil {
		t.Fatal("rejected execution must seal terminal evidence")
	}
	if res.Evidence.IngestionTrace() == nil {
		t.Fatal("IngestionTrace not attached to sealed ExecutionEvidence")
	}
}
