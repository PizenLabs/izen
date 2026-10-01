package execution

// ── PHASE 15: STREAM TOKEN SYNCHRONIZATION ───────────────────────────────────
//
// Provider billing is a fact about bytes that crossed the wire, and it is settled
// the moment the stream terminates. Whether those bytes then parsed into a valid
// artifact is a fact about the artifact boundary, settled later and elsewhere.
// Coupling the two is how an aggregate drifts from the invoice.
//
// The tests below pin the three properties that make the coupling impossible:
//
//	ONCE      Finalize is idempotent, so no terminator can double-count and no
//	          terminator has to prove it is the only one.
//	MONOTONIC Readings merge by maximum, so a repeated cumulative reading cannot
//	          double the bill and a late smaller estimate cannot shrink it.
//	UNKNOWN   A stream that reported nothing stays unknown. "Unknown" and "zero"
//	          are different facts and only one of them is honest.

import (
	"sync"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
)

func TestPhase15_StreamAggregatorMergesByMaximum(t *testing.T) {
	a := NewStreamTokenAggregator("mock", time.Now())
	// A provider seeds an estimated prompt baseline...
	a.OnReading(ai.ProviderUsage{Known: true, Estimated: true, PromptTokens: 2181})
	// ...then reports the authoritative total.
	a.OnReading(ai.ProviderUsage{Known: true, PromptTokens: 5883, CompletionTokens: 902, FinishReason: "stop"})

	live := a.Live()
	if live.PromptTokens != 5883 {
		t.Errorf("prompt = %d, want the authoritative 5883 (an estimate must not shadow it)", live.PromptTokens)
	}
	if live.CompletionTokens != 902 {
		t.Errorf("completion = %d, want 902", live.CompletionTokens)
	}
	// Authority is latched: once a stream reported a real count, a later estimated
	// frame must not demote the account, because demotion changes what the number
	// MEANS to every consumer downstream.
	if live.Estimated {
		t.Error("the account was demoted to estimated after an authoritative reading")
	}
	if !live.Known {
		t.Error("the account is not known")
	}
}

func TestPhase15_StreamAggregatorDoesNotDoubleCount(t *testing.T) {
	a := NewStreamTokenAggregator("mock", time.Now())
	for i := 0; i < 5; i++ {
		// The same cumulative reading, delivered five times.
		a.OnReading(ai.ProviderUsage{Known: true, PromptTokens: 100, CompletionTokens: 50})
	}
	final := a.Finalize(ai.ProviderUsage{})
	if final.PromptTokens != 100 || final.CompletionTokens != 50 {
		t.Fatalf("repeated cumulative readings were summed: %d/%d, want 100/50",
			final.PromptTokens, final.CompletionTokens)
	}
	if final.TotalTokens != 150 {
		t.Errorf("total = %d, want the derived 150", final.TotalTokens)
	}
}

func TestPhase15_StreamAggregatorFinalizeIsIdempotent(t *testing.T) {
	var flushes int
	var flushedPrompt int
	a := NewStreamTokenAggregator("mock", time.Now())
	a.SetFlushRequest(func(prompt, completion, reasoning int) {
		flushes++
		flushedPrompt = prompt
	})
	a.OnReading(ai.ProviderUsage{Known: true, PromptTokens: 700, CompletionTokens: 300})

	first := a.Finalize(ai.ProviderUsage{})
	second := a.Finalize(ai.ProviderUsage{})
	// A third call with a DIFFERENT fallback must not resurrect the earlier
	// argument either: the first verdict wins.
	third := a.Finalize(ai.ProviderUsage{Known: true, PromptTokens: 999999})

	if first != second || first != third {
		t.Fatalf("Finalize is not idempotent: %+v / %+v / %+v", first, second, third)
	}
	if flushes != 1 {
		t.Fatalf("flush count = %d, want exactly 1 — a double flush double-counts the bill", flushes)
	}
	if flushedPrompt != 700 {
		t.Errorf("flushed prompt = %d, want 700", flushedPrompt)
	}
	done, settled := a.Finalized()
	if !done {
		t.Error("the aggregator reports itself unfinalized")
	}
	if settled.PromptTokens != 700 {
		t.Errorf("settled prompt = %d, want 700", settled.PromptTokens)
	}
}

func TestPhase15_StreamAggregatorIgnoresLateReadings(t *testing.T) {
	a := NewStreamTokenAggregator("mock", time.Now())
	a.OnReading(ai.ProviderUsage{Known: true, PromptTokens: 100})
	a.Finalize(ai.ProviderUsage{})
	// A late frame from the same stream arrives after the terminator settled the
	// account. Accepting it would make the reported total depend on goroutine
	// scheduling, which is the opposite of a billing figure.
	a.OnReading(ai.ProviderUsage{Known: true, PromptTokens: 5000})
	final := a.Finalize(ai.ProviderUsage{})
	if final.PromptTokens != 100 {
		t.Fatalf("a late reading changed a settled account: %d, want 100", final.PromptTokens)
	}
}

func TestPhase15_StreamAggregatorStaysUnknownWhenNothingWasReported(t *testing.T) {
	var flushes int
	a := NewStreamTokenAggregator("mock", time.Now())
	a.SetFlushRequest(func(prompt, completion, reasoning int) { flushes++ })

	final := a.Finalize(ai.ProviderUsage{})
	if final.Known {
		t.Fatal("a stream that reported nothing was recorded as known")
	}
	if flushes != 0 {
		t.Fatalf("flush count = %d, want 0 — an unknown account must not be committed as a count", flushes)
	}
	// Live() must be equally honest: no reading, no account.
	if live := a.Live(); live.Known {
		t.Fatal("Live reported a known account with no reading")
	}
}

func TestPhase15_StreamAggregatorUsesTheFallbackOnlyWhenAdmitted(t *testing.T) {
	// The caller decides whether an estimate is admissible; the aggregator records
	// which accounting it produced so the difference stays visible downstream.
	estimated := ai.ProviderUsage{Known: true, Estimated: true, PromptTokens: 40, CompletionTokens: 10}
	a := NewStreamTokenAggregator("mock", time.Now())
	final := a.Finalize(estimated)
	if !final.Known || !final.Estimated {
		t.Fatalf("an admitted fallback was not recorded as an estimate: %+v", final)
	}
	if final.PromptTokens != 40 || final.CompletionTokens != 10 {
		t.Errorf("fallback counts lost: %+v", final)
	}

	// A real reading always outranks an admitted fallback, even a smaller one:
	// the fallback exists only for streams that reported NOTHING.
	b := NewStreamTokenAggregator("mock", time.Now())
	b.OnReading(ai.ProviderUsage{Known: true, PromptTokens: 12, CompletionTokens: 3})
	real := b.Finalize(estimated)
	if real.PromptTokens != 12 || real.CompletionTokens != 3 {
		t.Fatalf("the fallback overrode a real reading: %+v", real)
	}
}

func TestPhase15_StreamAggregatorIgnoresUnknownReadings(t *testing.T) {
	a := NewStreamTokenAggregator("mock", time.Now())
	a.OnReading(ai.ProviderUsage{Known: true, PromptTokens: 500})
	// An unknown frame carries no information and must not erase a real count.
	a.OnReading(ai.ProviderUsage{Known: false, PromptTokens: 0})
	final := a.Finalize(ai.ProviderUsage{})
	if final.PromptTokens != 500 {
		t.Fatalf("an unknown reading erased a real count: %+v", final)
	}
}

func TestPhase15_StreamAggregatorDerivesTheTotalButNotTheHalves(t *testing.T) {
	a := NewStreamTokenAggregator("mock", time.Now())
	a.OnReading(ai.ProviderUsage{Known: true, PromptTokens: 300, CompletionTokens: 200})
	final := a.Finalize(ai.ProviderUsage{})
	if final.TotalTokens != 500 {
		t.Errorf("total = %d, want the derived 500", final.TotalTokens)
	}
	// A provider that reports a total but no halves is a transport quirk, not a
	// licence to invent counts: the total is preserved, the halves stay zero.
	b := NewStreamTokenAggregator("mock", time.Now())
	b.OnReading(ai.ProviderUsage{Known: true, TotalTokens: 777})
	quirk := b.Finalize(ai.ProviderUsage{})
	if quirk.TotalTokens != 777 {
		t.Errorf("total = %d, want the reported 777", quirk.TotalTokens)
	}
	if quirk.PromptTokens != 0 || quirk.CompletionTokens != 0 {
		t.Errorf("halves were synthesised from a total: %d/%d", quirk.PromptTokens, quirk.CompletionTokens)
	}
}

// TestPhase15_StreamAggregatorIsConcurrencySafe is the property that makes the
// type usable at all: a provider may report usage from its own reader goroutine
// while the executor's teardown finalizes. Run under -race, a missing lock here
// is a torn token count on a customer's bill.
func TestPhase15_StreamAggregatorIsConcurrencySafe(t *testing.T) {
	a := NewStreamTokenAggregator("mock", time.Now())
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				a.OnReading(ai.ProviderUsage{Known: true, PromptTokens: n*10 + j, CompletionTokens: j})
				_ = a.Live()
			}
		}(i)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		a.SetFlushRequest(func(prompt, completion, reasoning int) {})
		_ = a.Finalize(ai.ProviderUsage{})
	}()
	wg.Wait()

	done, _ := a.Finalized()
	if !done {
		t.Fatal("the concurrent finalize did not settle the account")
	}
}

func TestPhase15_StreamAggregatorNilReceiverIsInert(t *testing.T) {
	var a *StreamTokenAggregator
	// Every method must be safe on a nil receiver: the executor constructs the
	// aggregator unconditionally, and a test harness may reach the flush path
	// without one.
	a.OnReading(ai.ProviderUsage{Known: true, PromptTokens: 1})
	a.SetFlushRequest(func(prompt, completion, reasoning int) { t.Fatal("nil aggregator flushed") })
	if got := a.Finalize(ai.ProviderUsage{Known: true}); got.Known {
		t.Fatal("a nil aggregator invented an account")
	}
	if a.Live().Known {
		t.Fatal("a nil aggregator reported a live account")
	}
	if done, _ := a.Finalized(); done {
		t.Fatal("a nil aggregator reported itself finalized")
	}
}
