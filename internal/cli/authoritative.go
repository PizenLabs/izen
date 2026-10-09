package cli

// ── AUTHORITATIVE CLI EXECUTION PATH ────────────────────────────────────────
//
// This file replaces the CLI's private proposal lifecycle (orchestrator.RunCycle
// + ProposalProviderCLI + ProposalValidator + ApprovalGate + FileExecutor) with
// the SAME authoritative runtime the TUI drives: the composition-root autonomy
// Driver over the RuntimeExecutor execution plane.
//
// It does not create a new lifecycle. The Driver owns the bounded loop,
// continuation, recovery, cancellation and terminal truth. This file owns
// exactly one thing the runtime must never own: playing the human at a parked
// boundary — rendering the runtime's own candidate review, collecting a
// decision, and issuing the mutation authorization on approve. The loop is
// driven ONLY through the structural port below, so this package never imports
// internal/runtime/autonomy.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/PizenLabs/izen/internal/autonomy"
)

// maxBoundaryResolutions bounds the number of human boundary decisions one CLI
// run may resolve. The Driver's own loop bounds already cap execution; this cap
// exists only so a boundary surface that never converges (e.g. an operator
// answering a clarify with the same ambiguous token forever) cannot spin the
// CLI process. It authorizes nothing.
const maxBoundaryResolutions = 16

// ApprovalPolicy is the command surface's declared stance at a parked
// approval boundary. It selects behaviour ONLY at the human surface; it never
// changes admission, authorization, mutation or verification, all of which
// remain the runtime's. The default (ApprovalAsk) is the historical
// interactive contract.
type ApprovalPolicy string

const (
	// ApprovalAsk prompts the operator for every approval decision. It is the
	// default and the only policy that can answer a clarify boundary.
	ApprovalAsk ApprovalPolicy = "ask"
	// ApprovalAlways auto-approves every held candidate. The authorization is
	// still issued for the reviewed candidate identity and digest before the
	// driver is resumed, so approval binds to the exact canonical mutation.
	// It is the non-interactive automation policy.
	ApprovalAlways ApprovalPolicy = "always"
	// ApprovalNever auto-rejects every held candidate; nothing is applied.
	ApprovalNever ApprovalPolicy = "never"
)

// ParseApprovalPolicy parses the -approve flag vocabulary.
func ParseApprovalPolicy(s string) (ApprovalPolicy, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "ask", "interactive":
		return ApprovalAsk, nil
	case "always", "auto", "yes", "approve":
		return ApprovalAlways, nil
	case "never", "deny", "no", "reject":
		return ApprovalNever, nil
	default:
		return "", fmt.Errorf("cli: unknown approval policy %q (want ask|always|never)", s)
	}
}

// HumanAction is the operator's answer to a parked boundary.
type HumanAction string

const (
	// HumanApprove authorizes the held candidate. The mutation is applied by the
	// runtime only after the authorization is issued.
	HumanApprove HumanAction = "approve"
	// HumanReject declines the held candidate; nothing is applied.
	HumanReject HumanAction = "reject"
	// HumanInspect declines to apply but records an explicit inspection. It is
	// realized as a non-applying rejection because the authoritative runtime has
	// no "apply nothing but keep the run open" transition; the terminal truth is
	// still "not committed".
	HumanInspect HumanAction = "inspect"
)

// HumanDecision is one answer to a parked boundary.
type HumanDecision struct {
	// Action is the decision for an approval boundary.
	Action HumanAction
	// Reason is the operator-visible reason for a rejection.
	Reason string
	// Clarify is the chosen target for a clarify boundary.
	Clarify string
}

// AuthoritativeDriver is the narrow lifecycle surface the CLI may drive. It is
// implemented by the composition-root autonomy Driver. Every method is an
// authoritative runtime transition; none is an execution or a provider call.
type AuthoritativeDriver interface {
	Run(ctx context.Context, objective string) (*autonomy.LoopTermination, error)
	ResumeApprove(ctx context.Context) (*autonomy.LoopTermination, error)
	ResumeReject(ctx context.Context, reason string) (*autonomy.LoopTermination, error)
	ResumeClarify(ctx context.Context, target string) (*autonomy.LoopTermination, error)
	Abort(reason string) (*autonomy.LoopTermination, error)
	State() autonomy.RuntimeState
	Boundary() *autonomy.HumanBoundary
	SetScope(scope string)
	RunID() string
}

// HumanBridge renders a parked boundary from the runtime's own facts and
// returns the operator's decision. It never reads the workspace and never
// produces a proposal payload: the review it renders is the boundary the
// runtime populated.
type HumanBridge interface {
	// Decide renders the boundary and returns one decision. A bridge error is a
	// human-surface failure and aborts the run; it is never treated as a reject.
	Decide(ctx context.Context, b *autonomy.HumanBoundary) (HumanDecision, error)
	// RenderParked projects a non-resumable boundary (inform / decomposition /
	// decision surface) so the operator can see why the run stopped.
	RenderParked(b *autonomy.HumanBoundary) error
}

// AuthorizeFn issues the mutation authorization for a reviewed candidate. It is
// invoked ONLY after the operator approves and BEFORE the driver is resumed. A
// non-nil error refuses the apply: the candidate is rejected, never applied
// without an authorization bound to the reviewed candidate identity and digest.
type AuthorizeFn func(targets []string, candidateID, candidateDigest string) error

// AuthoritativeResult is the terminal projection of one authoritative CLI run.
type AuthoritativeResult struct {
	// Termination is the runtime's terminal state. It is nil only when the run
	// parked at a non-resumable boundary.
	Termination *autonomy.LoopTermination
	// Parked is the non-resumable boundary the run stopped at, if any.
	Parked *autonomy.HumanBoundary
	// Approved reports whether the operator approved at least one candidate.
	Approved bool
	// Inspected reports whether the operator inspected a candidate.
	Inspected bool
	// Rejected reports whether the run declined at least one candidate or was
	// denied by an explicit never-approve policy. It is distinct from an
	// aborted run that never reached a boundary.
	Rejected bool
	// ClarifyRequired reports that a clarify boundary was reached and left
	// unanswered (the non-interactive contract cannot pick a target). It is a
	// human-required outcome, never a silent success.
	ClarifyRequired bool
	// RunID is the runtime's stable identity for the run.
	RunID string
}

// RunAuthoritative drives one objective through the authoritative runtime
// lifecycle and plays the human at every parked boundary. The scope is the
// command surface the run is admitted under (e.g. "$orchestrate") and is copied
// verbatim to the runtime.
//
// Cancellation is preserved: a canceled context surfaces as the runtime's own
// abort/termination, never as a fabricated success. A boundary that is not
// resumable (inform / decomposition / decision surface) is returned parked and
// is never silently converted into a resume.
func RunAuthoritative(
	ctx context.Context,
	driver AuthoritativeDriver,
	bridge HumanBridge,
	authorize AuthorizeFn,
	scope string,
	objective string,
) (AuthoritativeResult, error) {
	if driver == nil {
		return AuthoritativeResult{}, errors.New("cli: no authoritative runtime driver wired")
	}
	if bridge == nil {
		return AuthoritativeResult{}, errors.New("cli: no human bridge wired")
	}
	if strings.TrimSpace(objective) == "" {
		return AuthoritativeResult{}, errors.New("cli: empty objective")
	}
	if strings.TrimSpace(scope) == "" {
		scope = "$orchestrate"
	}
	driver.SetScope(scope)

	res := AuthoritativeResult{RunID: driver.RunID()}

	term, err := driver.Run(ctx, objective)
	if err != nil {
		res.Termination = term
		return res, fmt.Errorf("cli: authoritative run: %w", err)
	}

	for i := 0; i < maxBoundaryResolutions && driver.State() == autonomy.RuntimeAwaitingHuman; i++ {
		b := driver.Boundary()
		if b == nil {
			break
		}
		switch b.Action {
		case autonomy.HumanBoundaryApproval:
			decision, derr := bridge.Decide(ctx, b)
			if derr != nil {
				res.Termination = term
				return res, derr
			}
			switch decision.Action {
			case HumanApprove:
				if authorize != nil {
					if aerr := authorize(b.CandidateTargets, b.CandidateID, b.CandidateDigest); aerr != nil {
						// Authorization refused: the candidate is rejected. A
						// refusal is never allowed to fall through to an apply.
						term, err = driver.ResumeReject(ctx, "authorization refused: "+aerr.Error())
						break
					}
				}
				res.Approved = true
				term, err = driver.ResumeApprove(ctx)
			case HumanInspect:
				res.Inspected = true
				res.Rejected = true
				term, err = driver.ResumeReject(ctx, "inspect: no mutation applied")
			default:
				reason := decision.Reason
				if reason == "" {
					reason = "rejected by operator"
				}
				res.Rejected = true
				term, err = driver.ResumeReject(ctx, reason)
			}
		case autonomy.HumanBoundaryClarify:
			decision, derr := bridge.Decide(ctx, b)
			if derr != nil {
				res.Termination = term
				return res, derr
			}
			if strings.TrimSpace(decision.Clarify) == "" {
				res.ClarifyRequired = true
				term, err = driver.Abort("clarify boundary left unanswered")
			} else {
				term, err = driver.ResumeClarify(ctx, decision.Clarify)
			}
		default:
			// inform / decomposition_proposal / proposal: this single human
			// bridge has no resume surface for these. Project the boundary and
			// return it parked — never invent a resume.
			if perr := bridge.RenderParked(b); perr != nil {
				res.Termination = term
				return res, perr
			}
			res.Termination = term
			res.Parked = b
			return res, nil
		}
		if err != nil {
			res.Termination = term
			return res, fmt.Errorf("cli: authoritative resume: %w", err)
		}
	}

	if driver.State() == autonomy.RuntimeAwaitingHuman {
		// The human surface did not converge inside the bounded number of
		// resolutions. Abort the run rather than loop forever.
		term, err = driver.Abort("human boundary did not converge")
		if err != nil {
			res.Termination = term
			return res, fmt.Errorf("cli: authoritative abort: %w", err)
		}
	}
	res.Termination = term
	res.RunID = driver.RunID()
	return res, nil
}

// DriverTerminalBridge is the terminal HumanBridge: it renders the runtime's
// own candidate review and reads the operator's decision from a reader. It
// preserves the CLI's historical answer vocabulary — "y"/"yes" applies,
// "i"/"inspect" inspects without applying, anything else (including Enter)
// rejects.
type DriverTerminalBridge struct {
	in     io.Reader
	out    io.Writer
	policy ApprovalPolicy
}

// NewDriverTerminalBridge returns a terminal bridge reading decisions from in
// and rendering to out. The default approval policy is ApprovalAsk.
func NewDriverTerminalBridge(in io.Reader, out io.Writer) *DriverTerminalBridge {
	return &DriverTerminalBridge{in: in, out: out, policy: ApprovalAsk}
}

// WithPolicy sets the bridge's non-interactive approval policy and returns the
// bridge. An unknown/empty policy is normalized to ApprovalAsk.
func (b *DriverTerminalBridge) WithPolicy(p ApprovalPolicy) *DriverTerminalBridge {
	if b == nil {
		return b
	}
	switch p {
	case ApprovalAlways, ApprovalNever:
		b.policy = p
	default:
		b.policy = ApprovalAsk
	}
	return b
}

// Policy returns the bridge's effective approval policy.
func (b *DriverTerminalBridge) Policy() ApprovalPolicy {
	if b == nil {
		return ApprovalAsk
	}
	return b.policy
}

// Decide renders the boundary and blocks for one answer.
func (b *DriverTerminalBridge) Decide(ctx context.Context, bnd *autonomy.HumanBoundary) (HumanDecision, error) {
	if bnd == nil {
		return HumanDecision{}, errors.New("cli: nil human boundary")
	}
	if b == nil || b.in == nil || b.out == nil {
		return HumanDecision{}, errors.New("cli: terminal bridge is not fully wired")
	}
	switch bnd.Action {
	case autonomy.HumanBoundaryApproval:
		if err := b.renderApproval(bnd); err != nil {
			return HumanDecision{}, fmt.Errorf("cli: render approval: %w", err)
		}
		switch b.policy {
		case ApprovalAlways:
			// Non-interactive auto-approve: the review is still rendered (the
			// evidence is not skipped), but no input is read. The caller still
			// issues the candidate-bound authorization before resuming.
			if _, err := fmt.Fprintln(b.out, "auto-approve policy: applying reviewed candidate"); err != nil {
				return HumanDecision{}, fmt.Errorf("cli: render auto-approve notice: %w", err)
			}
			return HumanDecision{Action: HumanApprove}, nil
		case ApprovalNever:
			if _, err := fmt.Fprintln(b.out, "never-approve policy: rejecting reviewed candidate"); err != nil {
				return HumanDecision{}, fmt.Errorf("cli: render never-approve notice: %w", err)
			}
			return HumanDecision{Action: HumanReject, Reason: "non-interactive never-approve policy"}, nil
		}
		if _, err := fmt.Fprint(b.out, "Apply this proposal? [y/N] "); err != nil {
			return HumanDecision{}, fmt.Errorf("cli: render approval prompt: %w", err)
		}
		line, err := readDecisionLine(ctx, b.in)
		if err != nil {
			return HumanDecision{}, err
		}
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "y", "yes":
			return HumanDecision{Action: HumanApprove}, nil
		case "i", "inspect":
			return HumanDecision{Action: HumanInspect}, nil
		default:
			return HumanDecision{Action: HumanReject, Reason: "operator declined"}, nil
		}
	case autonomy.HumanBoundaryClarify:
		if _, err := fmt.Fprintf(b.out, "izen: clarification required — %s\n", bnd.Reason); err != nil {
			return HumanDecision{}, fmt.Errorf("cli: render clarify reason: %w", err)
		}
		for i, opt := range bnd.Options {
			if _, err := fmt.Fprintf(b.out, "  %d) %s\n", i+1, opt); err != nil {
				return HumanDecision{}, fmt.Errorf("cli: render clarify option: %w", err)
			}
		}
		if b.policy != ApprovalAsk {
			// Non-interactive surfaces cannot pick a target. Returning an empty
			// decision makes RunAuthoritative abort with a clear human-required
			// outcome; it never guesses a target.
			if _, err := fmt.Fprintln(b.out, "non-interactive policy: clarification cannot be answered"); err != nil {
				return HumanDecision{}, fmt.Errorf("cli: render clarify notice: %w", err)
			}
			return HumanDecision{}, nil
		}
		if _, err := fmt.Fprint(b.out, "Select a target: "); err != nil {
			return HumanDecision{}, fmt.Errorf("cli: render clarify prompt: %w", err)
		}
		line, err := readDecisionLine(ctx, b.in)
		if err != nil {
			return HumanDecision{}, err
		}
		return HumanDecision{Clarify: strings.TrimSpace(line)}, nil
	default:
		return HumanDecision{}, fmt.Errorf("cli: boundary %s is not decidable by the terminal bridge", bnd.Action)
	}
}

// renderApproval renders the runtime's own review facts: the exact candidate
// diff, its metrics, the authoritative target set, the operation and the
// runtime's evidence checklist. It never synthesizes a payload.
func (b *DriverTerminalBridge) renderApproval(bnd *autonomy.HumanBoundary) error {
	if _, err := fmt.Fprintf(b.out, "izen: proposal run=%s targets=%v operation=%s (+%d -%d)\n",
		bnd.RequestID, bnd.CandidateTargets, bnd.CandidateOperation,
		bnd.CandidateAddedLines, bnd.CandidateRemovedLines); err != nil {
		return fmt.Errorf("cli: render proposal header: %w", err)
	}
	if bnd.CandidateOperationEvidence != "" {
		if _, err := fmt.Fprintf(b.out, "  why: %s\n", bnd.CandidateOperationEvidence); err != nil {
			return fmt.Errorf("cli: render proposal evidence: %w", err)
		}
	}
	for _, row := range bnd.CandidateEvidence {
		mark := " "
		if row.Satisfied {
			mark = "x"
		}
		if _, err := fmt.Fprintf(b.out, "  [%s] %s %s\n", mark, row.Label, row.Detail); err != nil {
			return fmt.Errorf("cli: render proposal checklist: %w", err)
		}
	}
	if strings.TrimSpace(bnd.CandidateDiff) != "" {
		if _, err := fmt.Fprintln(b.out, bnd.CandidateDiff); err != nil {
			return fmt.Errorf("cli: render proposal diff: %w", err)
		}
	}
	return nil
}

// RenderParked renders a non-resumable boundary.
func (b *DriverTerminalBridge) RenderParked(bnd *autonomy.HumanBoundary) error {
	if bnd == nil {
		return nil
	}
	if b == nil || b.out == nil {
		return nil
	}
	if bnd.Reason != "" {
		if _, err := fmt.Fprintf(b.out, "izen: run parked at %s boundary: %s\n", bnd.Action, bnd.Reason); err != nil {
			return fmt.Errorf("cli: render parked boundary: %w", err)
		}
	} else {
		if _, err := fmt.Fprintf(b.out, "izen: run parked at %s boundary\n", bnd.Action); err != nil {
			return fmt.Errorf("cli: render parked boundary: %w", err)
		}
	}
	if len(bnd.Options) > 0 {
		if _, err := fmt.Fprintf(b.out, "  options: %v\n", bnd.Options); err != nil {
			return fmt.Errorf("cli: render parked options: %w", err)
		}
	}
	return nil
}

// readDecisionLine reads one line from r, honoring context cancellation so a
// withdrawn run never blocks on stdin forever.
func readDecisionLine(ctx context.Context, r io.Reader) (string, error) {
	type result struct {
		line string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		read := readLine(r)
		ch <- result(read)
	}()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case res := <-ch:
		if res.err != nil {
			return "", fmt.Errorf("cli: read decision: %w", res.err)
		}
		return res.line, nil
	}
}
