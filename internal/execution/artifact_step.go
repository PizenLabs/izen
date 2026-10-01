package execution

// PHASE 12 — full-artifact bounded-step continuation.
//
// A full-artifact generation ("create index.html", "rewrite styles.css") is a
// LOGICAL task that may legitimately need several bounded MODEL INVOCATIONS.
// The distinction is mandatory: `finish_reason=length` is an INVOCATION
// outcome, never a task failure.
//
// Before this file the full-artifact branch made exactly one provider call and
// discarded the bytes at the output gate (`gateFor` → `res.Content = ""`),
// after which the Driver recovery matrix relabelled the contract
// FULL_REWRITE → BOUNDED_PATCH — a SEARCH/REPLACE request against a file that
// may not exist, which can never succeed. The read-only branch of the same
// executor already had the correct machinery (`llmstep.StepState` +
// `ResponseState` + `ContinuationUserTurn`); this file reuses exactly that
// machinery so the mutation path is not the one production LLM path left out of
// the bounded-step contract.
//
// SAFETY INVARIANT (spec §16): a partial generation is a CANDIDATE, never a
// mutation. Nothing here writes, authorizes, or admits anything. The accumulated
// bytes flow onward into the unchanged pipeline — artifact gate, diff
// compilation, admission, `AuthorizationEngine`, OCC, apply, verification — and
// a run whose budget is consumed with no completed artifact still returns the
// typed, recoverable `llmstep.OutputExhaustedError`.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/events"
	runtimegraph "github.com/PizenLabs/izen/internal/execution/graph"
	"github.com/PizenLabs/izen/internal/execution/ingestion"
	"github.com/PizenLabs/izen/internal/llmstep"
)

// ArtifactCandidateStatus is the truthful state of a full-artifact candidate.
// It is deliberately narrow: the runtime can only ever say "the provider cut
// this off" or "the provider finished".
type ArtifactCandidateStatus string

const (
	// CandidatePending: a step is in flight; no candidate exists yet.
	CandidatePending ArtifactCandidateStatus = "pending"
	// CandidatePartial: one or more invocations were exhausted and their
	// delivered output was folded into a bounded, non-authoritative prefix.
	CandidatePartial ArtifactCandidateStatus = "partial"
	// CandidateComplete: a step returned a COMPLETE provider outcome. The
	// accumulated bytes are a whole artifact candidate, still unadmitted.
	CandidateComplete ArtifactCandidateStatus = "complete"
)

// ArtifactCandidate is the bounded, NON-MUTATING record of one target's
// full-artifact generation.
//
// It carries a fingerprint and counters, never the content: the content itself
// lives in the local accumulator that feeds the next continuation prompt, and
// nothing in this struct can reach the workspace.
type ArtifactCandidate struct {
	// Target is the workspace-relative path the candidate belongs to.
	Target string
	// Status is the truthful lifecycle state of the candidate.
	Status ArtifactCandidateStatus
	// DeliveredBytes is the total number of output bytes the provider has
	// actually produced for this target across all invocations.
	DeliveredBytes int
	// ExhaustedSteps counts the invocations the provider cut at its ceiling.
	ExhaustedSteps int
	// Fingerprint is the SHA-256 of the accumulated prefix. It lets evidence
	// and projections correlate a candidate without transporting its bytes.
	Fingerprint string
	// Committed reports whether the accumulated bytes formed a COMPLETE
	// provider outcome. A false value means the artifact is a prefix and MUST
	// NOT be admitted.
	Committed bool
}

// String renders the compact, presentation-safe candidate summary. It is the
// only rendering of a candidate anywhere in the system: no path, no bytes.
func (c ArtifactCandidate) String() string {
	if c.Target == "" {
		return string(CandidatePending)
	}
	return fmt.Sprintf("%s %s status=%s delivered=%dB exhausted_steps=%d committed=%t",
		c.Target, shortFingerprint(c.Fingerprint), c.Status, c.DeliveredBytes, c.ExhaustedSteps, c.Committed)
}

// shortFingerprint renders the leading edge of a hex digest for compact evidence.
func shortFingerprint(digest string) string {
	if len(digest) > 12 {
		return digest[:12]
	}
	return digest
}

// fingerprintBytes returns the content address of an accumulated candidate.
func fingerprintBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// maxArtifactContinuationHintBytes bounds the literal delivered prefix that is
// quoted back into a continuation prompt. The full accumulated prefix is always
// available to the model as the working context; this cap exists only so a
// pathological multi-step generation cannot grow the prompt without limit.
const maxArtifactContinuationHintBytes = 24 * 1024

// artifactContinuationTurn builds the continuation user turn for a full-artifact
// step. It is state-based, exactly like `llmstep.ContinuationUserTurn`: it
// rebuilds from the ORIGINAL base prompt plus the compact delivered-prefix
// state, and never replays a transcript. Because the artifact IS the output,
// the delivered prefix is quoted literally (bounded) instead of summarized —
// the model must be able to see the exact bytes it already emitted to continue
// the same document instead of restarting it.
func artifactContinuationTurn(base, delivered, target string, stepMaxTokens int) string {
	var b strings.Builder
	b.WriteString(base)
	b.WriteString("\n\n[SYSTEM: OUTPUT BUDGET EXHAUSTED — BOUNDED CONTINUATION]\n")
	b.WriteString("The previous response for ")
	b.WriteString(target)
	b.WriteString(" was cut off at the provider's output ceiling (finish_reason=length). It was NOT complete.\n")
	b.WriteString("You already emitted the following bytes for this file. Continue EXACTLY from where it stops:\n\n")
	b.WriteString("<<< DELIVERED SO FAR (do not repeat, do not restart) >>>\n")
	b.WriteString(boundDelivered(delivered))
	b.WriteString("\n<<< END DELIVERED SO FAR >>>\n\n")
	b.WriteString("Emit ONLY the remaining part of the same file, starting at the exact character the delivered prefix ends on.\n")
	b.WriteString("Do not repeat any delivered byte, do not add explanations, do not re-open the document.\n")
	if stepMaxTokens > 0 {
		fmt.Fprintf(&b, "Keep this whole response under %d output tokens.\n", stepMaxTokens)
	}
	return b.String()
}

// boundDelivered caps the literal delivered prefix quoted into the continuation
// prompt while keeping its TAIL — the boundary the model must resume from.
func boundDelivered(delivered string) string {
	if len(delivered) <= maxArtifactContinuationHintBytes {
		return delivered
	}
	return "…[earlier bytes omitted]\n" + delivered[len(delivered)-maxArtifactContinuationHintBytes:]
}

// artifactStepOutcome is the result of the bounded-step full-artifact lifecycle
// for ONE target.
type artifactStepOutcome struct {
	// Raw is the accumulated artifact candidate. On success it is the whole
	// artifact; on budget exhaustion it is the partial prefix and the error is
	// the typed llmstep condition.
	Raw string
	// Candidate is the truthful, non-authoritative record of what happened.
	Candidate ArtifactCandidate
}

// invokeArtifactBoundedStep runs the bounded-step lifecycle for ONE full-artifact
// target under the canonical RuntimeExecutor.
//
// Contract:
//   - The artifact contract, the target, the authority, the authorization and
//     the OCC lineage are STABLE across every step. A step advances the
//     generation; it never changes what the run is allowed to do.
//   - `baseReq` is the already-compiled step-1 request; continuations are
//     re-compiled through `recompile` so the continuation state is prompt
//     material that escapes under the same model-aware budget as the first turn.
//   - Exhaustion is bounded by the shared `llmstep.DefaultMaxContinuationSteps`
//     request budget. When it is consumed with delivered bytes, the executor
//     returns the accumulated candidate AND the typed recoverable
//     `llmstep.OutputExhaustedError` so the Driver matrix still sees the truth.
func (x *RuntimeExecutor) invokeArtifactBoundedStep(
	ctx context.Context,
	requestID, model, target string,
	g *runtimegraph.Graph,
	baseReq ai.Request,
	recompile func(userTurn string, stepMaxTokens int) (ai.Request, error),
	streamCb StreamCallback,
	maxTokens int,
	constrained bool,
	disableReasoning bool,
	invs *[]ModelInvocation,
	trace **ingestion.IngestionTrace,
) (artifactStepOutcome, error) {
	var out artifactStepOutcome
	out.Candidate = ArtifactCandidate{Target: target, Status: CandidatePending}

	step := llmstep.NewStepState(model, constrained, maxTokens, llmstep.DefaultMaxContinuationSteps)
	// state is the compact continuation state: what has been delivered, never a
	// transcript. It is local to this target and dies with the call.
	state := llmstep.NewResponseState(target, "the complete new file content")

	var accumulated strings.Builder

	for {
		aiReq := baseReq
		if step.Ordinal() > 1 {
			userTurn := artifactContinuationTurn(
				baseUserTurn(baseReq), accumulated.String(), target, step.MaxTokens())
			var err error
			aiReq, err = recompile(userTurn, step.MaxTokens())
			if err != nil {
				return out, fmt.Errorf("executor: artifact continuation compilation: %w", err)
			}
		}
		aiReq.Model = model
		aiReq.MaxTokens = step.MaxTokens()
		if disableReasoning {
			aiReq.Reasoning = &ai.ReasoningConfig{Disabled: true}
		}

		// The step lifecycle is published as the SAME reasoning.step /
		// reasoning.continuation / reasoning.state events the read-only path
		// already emits, so every projection can tell an exhausted INVOCATION
		// from a failed task without parsing a log string.
		x.emit(events.NewStepStarted(model, step.Ordinal(), step.MaxTokens()))
		if step.Ordinal() > 1 {
			x.emit(events.NewContinuationStarted(step.Ordinal(), step.MaxTokens()))
		}
		// model.invoked is emitted when the invocation BEGINS — before the
		// provider call — so the event stream truthfully records the start.
		g.BeginModel(model)

		var providerMetadata ai.ResponseMetadata
		raw, usage, itrace, callErr := x.invokeStream(ctx, aiReq, requestID, model, g, streamCb, &providerMetadata)
		if itrace != nil {
			*trace = itrace
		}
		// Billed invocation evidence for EVERY attempt: an exhausted-at-the-gate
		// call is a completed provider response whose payload was cut by the
		// provider — its authoritative usage is recorded, never dropped.
		inv := ModelInvocation{
			Model:               model,
			InteractionContract: aiReq.InteractionContract,
			ContractDescriptor:  cloneExecutionDescriptor(aiReq.Contract),
		}
		populateInvocationTelemetry(&inv, aiReq, usage, providerMetadata)
		if callErr == nil || isOutputExhausted(callErr) {
			g.CompleteModelWithMetadata(providerResponseEvent(x.providerName(), aiReq, usage, providerMetadata, len(raw)))
		}
		*invs = append(*invs, inv)

		if callErr != nil && !isOutputExhausted(callErr) {
			return out, fmt.Errorf("executor: artifact invocation: %w", callErr)
		}

		// BOUNDARY 3 — OUTPUT GATE. Only a COMPLETE provider outcome may proceed
		// to the artifact boundary; an exhausted one is an INVOCATION outcome
		// and is classified here, not at the task level.
		gate := gateFor(target, inv.FinishReason)
		if gate == nil {
			if accumulated.Len() > 0 {
				accumulated.WriteString("\n")
			}
			accumulated.WriteString(raw)
			step.RecordDelivered(target)
			state.AddAnswered(target)
			x.emit(events.NewStateCommitted(step.Ordinal(), len(step.Committed()), "artifact.full_file"))
			x.emit(events.NewStepCompleted(step.Ordinal(), len(step.Committed()), usage.FinishReason))
			state.Complete()
			out.Raw = accumulated.String()
			out.Candidate = ArtifactCandidate{
				Target:         target,
				Status:         CandidateComplete,
				DeliveredBytes: accumulated.Len(),
				ExhaustedSteps: step.Ordinal() - 1,
				Fingerprint:    fingerprintBytes([]byte(out.Raw)),
				Committed:      true,
			}
			return out, nil
		}

		if gate.Outcome != CanonicalOutputExhausted {
			// A refusal or an unknown terminal reason is NOT resumable: it is
			// handed back unchanged so the recovery matrix classifies it exactly
			// as it does today.
			return out, gate
		}

		// OUTPUT_EXHAUSTED: preserve the delivered prefix as a bounded candidate
		// and decide continuation eligibility. Nothing is admitted, authorized
		// or written here.
		deliveredThisStep := strings.TrimSpace(raw) != ""
		if deliveredThisStep {
			if accumulated.Len() > 0 {
				accumulated.WriteString("\n")
			}
			accumulated.WriteString(raw)
		}
		out.Candidate = ArtifactCandidate{
			Target:         target,
			Status:         CandidatePartial,
			DeliveredBytes: accumulated.Len(),
			ExhaustedSteps: step.Ordinal(),
			Fingerprint:    fingerprintBytes([]byte(accumulated.String())),
			Committed:      false,
		}
		x.emit(events.NewStepExhausted(step.Ordinal(), usage.CompletionTokens, out.Candidate.DeliveredBytes))

		// NO-PROGRESS GUARD: a step that was cut off but delivered NOTHING new
		// cannot advance the artifact. Continuing would burn the remaining
		// request budget on a loop that produces the same empty result, so the
		// exhaustion is reported immediately — a truthful, cheaper halt rather
		// than an unbounded retry. This mirrors the pure library's no-progress
		// protection (internal/continuation detectNoProgress).
		if !deliveredThisStep {
			out.Raw = accumulated.String()
			return out, &llmstep.OutputExhaustedError{
				Step: step.Ordinal(),
				Hint: fmt.Sprintf("provider output ceiling exhausted for %s with no new delivered bytes; continuation would not advance the artifact",
					target),
			}
		}

		if !step.CanContinue() {
			// Request budget consumed. A delivered prefix is preserved as
			// evidence and returned with the TYPED, RECOVERABLE condition — never
			// a silent success and never an artifact admission.
			out.Raw = accumulated.String()
			x.emit(events.NewStateCommitted(step.Ordinal(), len(step.Committed()), "artifact.partial"))
			return out, &llmstep.OutputExhaustedError{
				Step: step.Ordinal(),
				Hint: fmt.Sprintf("provider output ceiling exhausted for %s after %d invocation(s); %d delivered byte(s) preserved as a partial candidate",
					target, step.Ordinal(), accumulated.Len()),
			}
		}

		// Schedule the bounded continuation. Authority, target, artifact
		// contract and workspace version stay STABLE; only the step advances.
		step.Advance()
		state.AdvanceCursor()
		x.emit(events.NewStateRejected(step.Ordinal()-1, "exhausted artifact step delivered a prefix, not a whole document"))
		x.emit(events.NewContinuationScheduled(step.Ordinal(), out.Candidate.DeliveredBytes, step.ContinuationsLeft()))
	}
}

// baseUserTurn extracts the step-1 user turn from the already-compiled request
// so every continuation is rebuilt from the SAME base instead of accumulating
// duplicate instruction blocks.
func baseUserTurn(req ai.Request) string {
	if turn, ok := lastUserMessage(req); ok {
		return turn
	}
	return ""
}
