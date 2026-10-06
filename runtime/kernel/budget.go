package kernel

import (
	"fmt"
	"strings"
)

// Budget bounds an execution in the units the work is actually measured in.
// Two axes exist because conflating them produces the classic lie where a
// truncated answer is reported as a finished one:
//
//	Steps bounds how much work may be dispatched at all.
//	Output bounds how many bytes of provider output may be consumed.
//
// A provider that stops because it hit its output ceiling consumed its budget
// and returned an incomplete prefix. The kernel records that as an exhausted
// budget, never as completion, regardless of how confident the text looked.
type Budget struct {
	// MaxSteps bounds the number of steps the kernel may invoke. Zero declares
	// no step bound.
	MaxSteps int
	// MaxOutputBytes bounds the total provider or command output the kernel may
	// consume. Zero declares no output bound.
	MaxOutputBytes int
	// MaxStepsPerCapability bounds how many times any single capability may be
	// invoked within one execution. Zero declares no per-capability bound. It
	// exists because a program that retries one capability forever otherwise has
	// no bound the engine can enforce.
	MaxStepsPerCapability int
}

// Unbounded reports whether the budget declares no bound at all.
func (b Budget) Unbounded() bool {
	return b.MaxSteps == 0 && b.MaxOutputBytes == 0 && b.MaxStepsPerCapability == 0
}

// Validate reports whether the budget is internally consistent. Negative bounds
// are refused rather than clamped, because a silently adjusted limit is a limit
// nobody chose.
func (b Budget) Validate() error {
	if b.MaxSteps < 0 {
		return blockf(FailureInvalidSpec, "", "", "budget MaxSteps is negative (%d)", b.MaxSteps)
	}
	if b.MaxOutputBytes < 0 {
		return blockf(FailureInvalidSpec, "", "", "budget MaxOutputBytes is negative (%d)", b.MaxOutputBytes)
	}
	if b.MaxStepsPerCapability < 0 {
		return blockf(FailureInvalidSpec, "", "", "budget MaxStepsPerCapability is negative (%d)", b.MaxStepsPerCapability)
	}
	return nil
}

// Accounting is the runtime's own record of budget consumption. It is derived
// from what the kernel actually did, never from what a caller claimed it did.
//
// It is a value with no methods that mutate it; the reducer constructs a new
// Accounting on every transition. That is what makes "how much was spent" a
// question with a single answer in the whole system.
type Accounting struct {
	// Declared is the budget the execution was admitted under.
	Declared Budget
	// StepsInvoked counts capability invocations the kernel actually made.
	StepsInvoked int
	// PerCapability counts invocations keyed by capability identifier.
	PerCapability map[CapabilityID]int
	// OutputBytes counts provider or command output bytes the kernel actually
	// consumed.
	OutputBytes int
}

// Remaining reports the unconsumed step budget. A zero MaxSteps means no bound
// was declared, and the reported remainder is then the unbound sentinel rather
// than a number, so callers cannot mistake "unbounded" for "plenty left".
func (a Accounting) Remaining() int {
	if a.Declared.MaxSteps == 0 {
		return unboundedBudget
	}
	remaining := a.Declared.MaxSteps - a.StepsInvoked
	if remaining < 0 {
		return 0
	}
	return remaining
}

// RemainingOutput reports the unconsumed output budget in bytes.
func (a Accounting) RemainingOutput() int {
	if a.Declared.MaxOutputBytes == 0 {
		return unboundedBudget
	}
	remaining := a.Declared.MaxOutputBytes - a.OutputBytes
	if remaining < 0 {
		return 0
	}
	return remaining
}

// unboundedBudget is the sentinel returned by Remaining when no bound was
// declared. It is deliberately absurd as a count, so arithmetic against it
// fails loudly in a test rather than quietly in production.
const unboundedBudget = -1

// UnboundedSentinel reports whether a remainder is the no-bound-declared
// sentinel.
func UnboundedSentinel(remainder int) bool { return remainder == unboundedBudget }

// charge records the consumption of one invocation and returns the updated
// accounting. It is a pure function so the reducer stays deterministic.
func (a Accounting) charge(cap CapabilityID, outputBytes int) Accounting {
	next := Accounting{
		Declared:      a.Declared,
		StepsInvoked:  a.StepsInvoked + 1,
		PerCapability: make(map[CapabilityID]int, len(a.PerCapability)+1),
		OutputBytes:   a.OutputBytes + outputBytes,
	}
	for k, v := range a.PerCapability {
		next.PerCapability[k] = v
	}
	next.PerCapability[cap]++
	return next
}

// allows reports whether charging one more invocation of cap stays within every
// declared bound. It is checked BEFORE an invocation, never after, so an
// exhausted budget stops work rather than merely reporting it afterwards.
func (a Accounting) allows(cap CapabilityID) error {
	if a.Declared.MaxSteps > 0 && a.StepsInvoked >= a.Declared.MaxSteps {
		return blockf(FailureBudgetExhausted, "", cap,
			"step budget exhausted (%d/%d steps invoked)", a.StepsInvoked, a.Declared.MaxSteps)
	}
	if a.Declared.MaxStepsPerCapability > 0 {
		if used := a.PerCapability[cap]; used >= a.Declared.MaxStepsPerCapability {
			return blockf(FailureBudgetExhausted, "", cap,
				"per-capability budget exhausted for %q (%d/%d invocations)", cap, used, a.Declared.MaxStepsPerCapability)
		}
	}
	return nil
}

// allowsOutput reports whether consuming outputBytes more stays within the
// declared output bound.
//
// It is checked after an invocation returns, because the size of an invocation's
// output is not knowable before it runs. That ordering has a consequence worth
// stating: the kernel cannot prevent the overshoot, only refuse to treat the
// result as complete. So the caller must settle the execution as budget-exhausted
// rather than proceeding — which is exactly what a provider that was cut off at
// its token ceiling requires.
func (a Accounting) allowsOutput(outputBytes int) error {
	if a.Declared.MaxOutputBytes <= 0 {
		return nil
	}
	if a.OutputBytes+outputBytes > a.Declared.MaxOutputBytes {
		return blockf(FailureBudgetExhausted, "", "",
			"output budget exhausted (%d + %d > %d bytes); the delivered data is an incomplete prefix",
			a.OutputBytes, outputBytes, a.Declared.MaxOutputBytes)
	}
	return nil
}

// String renders the accounting for a log record.
func (a Accounting) String() string {
	counts := make([]string, 0, len(a.PerCapability))
	for id, n := range a.PerCapability {
		counts = append(counts, fmt.Sprintf("%s=%d", id, n))
	}
	return fmt.Sprintf("steps=%d output_bytes=%d per_capability={%s} declared_max_steps=%d declared_max_output_bytes=%d",
		a.StepsInvoked, a.OutputBytes, strings.Join(counts, ","), a.Declared.MaxSteps, a.Declared.MaxOutputBytes)
}
