// Package headless runs the runtime kernel with no user interface attached.
//
// It exists as executable proof of a claim the kernel's unit tests can only
// assert by inspection: that an execution can be driven, observed, and judged
// without a terminal, a renderer, or any presentation layer. If this package ever
// needed a TTY, the kernel's independence would be a fiction.
//
// It is also the smallest useful reference for how the pieces fit together:
// build a capability set, build an engine, admit a spec and a grant, run, and
// read the terminal result. Everything an interactive front end does, this does
// first — it simply has nowhere to draw.
package headless

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/PizenLabs/izen/runtime/capabilities/filesystem"
	"github.com/PizenLabs/izen/runtime/kernel"
)

// Runner drives one kernel execution and reports it truthfully.
//
// It holds no progress model, no display state, and no opinion about what the
// user should see next. Its entire responsibility is to run an execution and
// return what became true.
type Runner struct {
	root string
}

// New builds a Runner over the given workspace root.
func New(root string) (*Runner, error) {
	if strings.TrimSpace(root) == "" {
		return nil, kernel.Block{
			Class:  kernel.FailureInvalidSpec,
			Reason: "headless runner requires a workspace root",
		}
	}
	return &Runner{root: root}, nil
}

// CapabilitySet is the runner's real capability surface. It is computed once and
// is the complete answer to "what can this runner actually do".
func (r *Runner) CapabilitySurface() (string, error) {
	caps, err := filesystem.New(r.root)
	if err != nil {
		return "", err
	}
	registry, err := caps.Registry()
	if err != nil {
		return "", err
	}
	return registry.String(), nil
}

// Run admits the spec under the grant and executes it.
//
// The returned error is reserved for a caller mistake. A runtime refusal is NOT
// an error return: it is a Result carrying the refusal, because a caller needs to
// distinguish "nothing was proven" from "something was proven wrong", and a bare
// error collapses that distinction back into the single bit the kernel exists to
// replace.
func (r *Runner) Run(ctx context.Context, spec kernel.Spec, grant kernel.Grant) (kernel.Result, error) {
	caps, err := filesystem.New(r.root)
	if err != nil {
		return unavailable(spec.ExecutionID, err), nil
	}
	registry, err := caps.Registry()
	if err != nil {
		return unavailable(spec.ExecutionID, err), nil
	}

	engine, err := kernel.NewEngine(registry, kernel.WithVerifier(NewWorkspaceVerifier(r.root)))
	if err != nil {
		return unavailable(spec.ExecutionID, err), nil
	}
	if err := engine.Open(spec, grant); err != nil {
		// Admission refusals are attributed, not softened. Open has already
		// recorded the exact cause, so the result carries it.
		return resultFromOpenError(spec.ExecutionID, err), nil
	}
	return engine.Run(ctx), nil
}

// unavailable renders a wiring failure as a Result. The runner could not build its
// capability surface, which is a runtime refusal rather than a caller error.
func unavailable(executionID string, err error) kernel.Result {
	return kernel.Result{
		ExecutionID: executionID,
		Outcome:     kernel.OutcomeFailed,
		Class:       kernel.FailureCapabilityUnavailable,
		Reason:      err.Error(),
	}
}

// resultFromOpenError converts an admission refusal into a Result that names the
// refusal, rather than collapsing it into a generic failure.
func resultFromOpenError(executionID string, err error) kernel.Result {
	var block kernel.Block
	if errors.As(err, &block) {
		class := block.Class
		if !class.Valid() {
			class = kernel.FailureInvalidSpec
		}
		return kernel.Result{
			ExecutionID: executionID,
			Outcome:     outcomeFor(class),
			Class:       class,
			Reason:      block.Reason,
		}
	}
	return kernel.Result{
		ExecutionID: executionID,
		Outcome:     kernel.OutcomeFailed,
		Class:       kernel.FailureInvalidSpec,
		Reason:      err.Error(),
	}
}

func outcomeFor(class kernel.FailureClass) kernel.Outcome {
	switch class {
	case kernel.FailureAuthorization:
		return kernel.OutcomeRequiresAuthorization
	case kernel.FailureBudgetExhausted:
		return kernel.OutcomeBudgetExhausted
	case kernel.FailureCancelled:
		return kernel.OutcomeCancelled
	default:
		return kernel.OutcomeFailed
	}
}

// Report renders a result as a truthful, human-readable execution report.
//
// It prints the four axes separately and names the unmet clauses. It never prints
// the word "success" — the outcome line is the verdict, and the axes explain it.
// A report that said "done" while the mutation axis read NONE would be the exact
// failure this runtime exists to prevent, and printing it would reintroduce the
// failure at the presentation layer.
func Report(w io.Writer, result kernel.Result) error {
	if w == nil {
		return errors.New("headless: nil writer")
	}
	state := result.State

	var sb strings.Builder
	fmt.Fprintf(&sb, "execution   %s\n", result.ExecutionID)
	fmt.Fprintf(&sb, "outcome     %s\n", result.Outcome)
	if result.Class != "" {
		fmt.Fprintf(&sb, "class       %s\n", result.Class)
	}
	fmt.Fprintf(&sb, "revision    %d\n", result.Revision)
	if result.Reason != "" {
		fmt.Fprintf(&sb, "reason      %s\n", result.Reason)
	}

	fmt.Fprintf(&sb, "\naxes\n")
	fmt.Fprintf(&sb, "  provider  %s   (transport: the backend stopped responding)\n", state.Provider)
	fmt.Fprintf(&sb, "  artifact  %s   (parser: something usable was extracted)\n", state.Artifact)
	fmt.Fprintf(&sb, "  mutation  %s   (filesystem: the workspace actually changed)\n", state.Mutation)
	fmt.Fprintf(&sb, "  verify    %s   (independent check)\n", state.Verify)

	if mutated := result.MutatedTargets(); len(mutated) > 0 {
		fmt.Fprintf(&sb, "\nmutated targets\n")
		for _, t := range mutated {
			fmt.Fprintf(&sb, "  %s\n", t)
		}
	}
	if observed := result.ObservedTargets(); len(observed) > 0 {
		fmt.Fprintf(&sb, "\nobserved targets\n")
		for _, t := range observed {
			fmt.Fprintf(&sb, "  %s\n", t)
		}
	}
	if len(result.Evidence) > 0 {
		fmt.Fprintf(&sb, "\nevidence\n")
		counts := map[string]int{}
		order := []string{}
		for _, e := range result.Evidence {
			key := string(e.Kind)
			if _, seen := counts[key]; !seen {
				order = append(order, key)
			}
			counts[key]++
		}
		sort.Strings(order)
		for _, k := range order {
			fmt.Fprintf(&sb, "  %-24s %d\n", k, counts[k])
		}
	}
	if len(result.Unmet) > 0 {
		fmt.Fprintf(&sb, "\nunmet contract clauses\n")
		for _, c := range result.Unmet {
			fmt.Fprintf(&sb, "  %s\n", c)
		}
	}

	fmt.Fprintf(&sb, "\nbudget\n  %s\n", result.Budget.String())

	_, err := io.WriteString(w, sb.String())
	return err
}

// NewWorkspaceVerifier builds the verification seam a headless run uses.
//
// It verifies the single thing that can be verified about a workspace fact
// without a domain: that every target the contract named still exists (or is
// still absent, for a DELETE) at verification time. That is a real check against
// the real filesystem, and it is honest about its own limits — it is NOT a
// content validator, and it does not pretend to be one.
func NewWorkspaceVerifier(root string) kernel.Verifier {
	return kernel.VerifierFunc(func(ctx context.Context, req kernel.VerificationRequest) (kernel.Verdict, error) {
		if len(req.Targets) == 0 {
			return kernel.VerdictNotApplicable, nil
		}
		caps, err := filesystem.New(root)
		if err != nil {
			return kernel.VerdictFail, err
		}
		grant, err := kernel.NewGrant("verifier", []kernel.CapabilityID{kernel.FileExists}, nil)
		if err != nil {
			return kernel.VerdictFail, err
		}
		exists := &existenceCheck{caps: caps, grant: grant}
		for _, target := range req.Targets {
			present, err := exists.present(ctx, target)
			if err != nil {
				return kernel.VerdictFail, err
			}
			if req.Contract.Kind == kernel.ContractDelete {
				if present {
					return kernel.VerdictFail, nil
				}
				continue
			}
			if !present {
				return kernel.VerdictFail, nil
			}
		}
		return kernel.VerdictPass, nil
	})
}

// existenceCheck reuses the filesystem capability rather than opening its own
// path-resolution code. One confinement implementation, not two.
type existenceCheck struct {
	caps  *filesystem.Capability
	grant kernel.Grant
}

func (e *existenceCheck) present(ctx context.Context, target string) (bool, error) {
	caps, err := filesystem.New(e.caps.Root())
	if err != nil {
		return false, err
	}
	for _, c := range caps.Capabilities() {
		if c.ID() != kernel.FileExists {
			continue
		}
		if err := ctx.Err(); err != nil {
			return false, err
		}
		obs, err := c.Invoke(ctx, kernel.Request{
			Capability: kernel.FileExists,
			Step:       "verify",
			Target:     target,
			Args:       map[string]string{},
			Grant:      e.grant,
		})
		if err != nil {
			return false, err
		}
		for _, f := range obs.Facts {
			if f.Kind == kernel.EvidenceFilePresent {
				return true, nil
			}
		}
	}
	return false, nil
}
