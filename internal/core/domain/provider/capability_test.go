package provider

import (
	"fmt"
	"sync"
	"testing"
)

func TestLimitSourcePriority(t *testing.T) {
	for want, got := range []LimitSource{LimitUnknown, LimitPolicy, LimitAdvertised, LimitObserved} {
		if int(got) != want {
			t.Fatalf("source = %d, want %d", got, want)
		}
	}
}

func TestResolvedOutputLimitPrecedence(t *testing.T) {
	orders := [][]LimitSource{
		{LimitPolicy, LimitAdvertised, LimitObserved},
		{LimitPolicy, LimitObserved, LimitAdvertised},
		{LimitAdvertised, LimitPolicy, LimitObserved},
		{LimitAdvertised, LimitObserved, LimitPolicy},
		{LimitObserved, LimitPolicy, LimitAdvertised},
		{LimitObserved, LimitAdvertised, LimitPolicy},
	}
	for mask := 0; mask < 8; mask++ {
		for orderIndex, order := range orders {
			t.Run(fmt.Sprintf("mask_%d/order_%d", mask, orderIndex), func(t *testing.T) {
				meta := DetectCapability("test", "model", 0, 0, false, false)
				want := OutputLimit{}
				for _, source := range order {
					if mask&(1<<(source-1)) == 0 {
						continue
					}
					limit := OutputLimit{Value: int(source) * 1000, Source: source}
					meta.RecordOutputLimit(limit)
					if source > want.Source {
						want = limit
					}
					if got := meta.ResolvedOutputLimit(); got != want {
						t.Fatalf("resolved = %+v, want %+v", got, want)
					}
				}
				if got := meta.ResolvedOutputLimit(); got != want {
					t.Fatalf("resolved = %+v, want %+v", got, want)
				}
			})
		}
	}
}

func TestRecordOutputLimitInvalid(t *testing.T) {
	for _, initial := range []OutputLimit{{}, {Value: 1024, Source: LimitPolicy}, {Value: 2048, Source: LimitAdvertised}, {Value: 4096, Source: LimitObserved}} {
		for _, source := range []LimitSource{-1, LimitUnknown, LimitPolicy, LimitAdvertised, LimitObserved, 4, 99} {
			for _, value := range []int{-100, -1, 0, 128} {
				if value > 0 && source >= LimitPolicy && source <= LimitObserved {
					continue
				}
				t.Run(fmt.Sprintf("initial_%d/source_%d/value_%d", initial.Source, source, value), func(t *testing.T) {
					meta := DetectCapability("", "", 0, 0, false, false)
					meta.RecordOutputLimit(initial)
					meta.RecordOutputLimit(OutputLimit{Value: value, Source: source})
					if got := meta.ResolvedOutputLimit(); got != initial {
						t.Fatalf("resolved = %+v, want %+v", got, initial)
					}
				})
			}
		}
	}
	var meta *ProviderMetadata
	meta.RecordOutputLimit(OutputLimit{Value: 128, Source: LimitObserved})
}

func TestOutputLimitLegacyMetadata(t *testing.T) {
	for _, value := range []int{-1, 0, 64, 4096} {
		t.Run(fmt.Sprint(value), func(t *testing.T) {
			meta := ProviderMetadata{MaxOutputTokens: value}
			want := OutputLimit{}
			if value > 0 {
				want = OutputLimit{Value: value, Source: LimitAdvertised}
			}
			if got := meta.ResolvedOutputLimit(); got != want {
				t.Fatalf("resolved = %+v, want %+v", got, want)
			}
			meta.RecordOutputLimit(OutputLimit{Value: 8000, Source: LimitPolicy})
			if value <= 0 {
				want = OutputLimit{Value: 8000, Source: LimitPolicy}
			}
			if got := meta.ResolvedOutputLimit(); got != want {
				t.Fatalf("resolved = %+v, want %+v", got, want)
			}
			want = OutputLimit{Value: 16, Source: LimitObserved}
			meta.RecordOutputLimit(want)
			if got := meta.ResolvedOutputLimit(); got != want {
				t.Fatalf("resolved = %+v, want %+v", got, want)
			}
			if meta.MaxOutputTokens != value {
				t.Fatal("recording mutated advertised compatibility field")
			}
		})
	}
}

func TestDetectCapabilitySessionCopies(t *testing.T) {
	meta := DetectCapability("test", "model", 4096, 32768, true, true)
	copyBefore := meta
	independent := DetectCapability("test", "model", 4096, 32768, true, true)
	for _, value := range []int{64, 8192, 256} {
		want := OutputLimit{Value: value, Source: LimitObserved}
		copyBefore.RecordOutputLimit(want)
		copyAfter := meta
		for _, view := range []ProviderMetadata{meta, copyBefore, copyAfter} {
			if got := view.ResolvedOutputLimit(); got != want {
				t.Fatalf("resolved = %+v, want %+v", got, want)
			}
			if view.IsConstrained() != (value <= ConstrainedOutputThreshold) {
				t.Fatal("constraint classification did not use resolved limit")
			}
		}
		meta.RecordOutputLimit(OutputLimit{Value: 128, Source: LimitAdvertised})
		meta.RecordOutputLimit(OutputLimit{Value: 1, Source: LimitPolicy})
		if got := copyBefore.ResolvedOutputLimit(); got != want {
			t.Fatalf("lower priority overrode observation: %+v", got)
		}
	}
	if got := independent.ResolvedOutputLimit(); got != (OutputLimit{Value: 4096, Source: LimitAdvertised}) {
		t.Fatalf("observation leaked across sessions: %+v", got)
	}
	if meta.Provider != "test" || meta.ModelID != "model" || meta.ContextWindow != 32768 || !meta.SupportsReasoningUsage || !meta.SupportsReasoningBudget {
		t.Fatalf("metadata changed: %+v", meta)
	}
	normalized := DetectCapability("", "", -1, -1, false, false)
	if normalized.MaxOutputTokens != 0 || normalized.ContextWindow != 0 || normalized.ResolvedOutputLimit() != (OutputLimit{}) || normalized.IsConstrained() {
		t.Fatalf("negative values not normalized: %+v", normalized)
	}
}

func TestOutputLimitConcurrentCopies(t *testing.T) {
	meta := DetectCapability("test", "model", 4096, 0, false, false)
	var wg sync.WaitGroup
	for worker := 0; worker < 16; worker++ {
		wg.Add(1)
		go func(view ProviderMetadata, source LimitSource) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				view.RecordOutputLimit(OutputLimit{Value: int(source) * 64, Source: source})
				got := view.ResolvedOutputLimit()
				if got.Source < source || got.Source > LimitObserved || got.Value <= 0 {
					t.Errorf("invalid concurrent resolution: %+v", got)
					return
				}
				if budget := StepBudgetForTask(1024, 2048, view, 0); budget <= 0 || budget > 1024 {
					t.Errorf("invalid concurrent budget: %d", budget)
					return
				}
			}
		}(meta, LimitSource(worker%3+1))
	}
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 500 {
				view := meta
				if got := view.ResolvedOutputLimit(); got.Value <= 0 {
					t.Errorf("invalid concurrent read: %+v", got)
				}
			}
		}()
	}
	wg.Wait()
	if got := meta.ResolvedOutputLimit(); got != (OutputLimit{Value: 192, Source: LimitObserved}) {
		t.Fatalf("resolved = %+v, want observed 192", got)
	}
}

func TestEffectiveStepBudgetHardCaps(t *testing.T) {
	for _, capValue := range []int{1, 16, 64, 127, 128, 129, 1024} {
		for bound := 0; bound < 3; bound++ {
			for _, margin := range []int{-1, 0, 1, 128, 4096, int(^uint(0) >> 1)} {
				t.Run(fmt.Sprintf("cap_%d/bound_%d/margin_%d", capValue, bound, margin), func(t *testing.T) {
					bounds := [3]int{4096, 4096, 4096}
					bounds[bound] = capValue
					got := EffectiveStepBudget(bounds[0], bounds[1], bounds[2], margin)
					want := capValue
					if margin > 0 {
						want = max(capValue-margin, min(MinStepBudget, capValue))
					}
					if got != want || got <= 0 || got > capValue {
						t.Fatalf("budget = %d, want %d, cap %d", got, want, capValue)
					}
				})
			}
		}
	}
	for _, bounds := range [][3]int{{0, 0, 0}, {-1, -1, -1}} {
		if got := EffectiveStepBudget(bounds[0], bounds[1], bounds[2], 4096); got != DefaultRequestedStepBudget {
			t.Fatalf("unbounded budget = %d", got)
		}
	}
}

func TestStepBudgetForTaskResolvedLimit(t *testing.T) {
	meta := DetectCapability("", "", 0, 0, false, false)
	view := meta
	for _, limit := range []OutputLimit{
		{Value: 64, Source: LimitPolicy},
		{Value: 256, Source: LimitAdvertised},
		{Value: 16, Source: LimitObserved},
		{Value: 512, Source: LimitObserved},
	} {
		meta.RecordOutputLimit(limit)
		if got := StepBudgetForTask(4096, 8192, view, 0); got != limit.Value {
			t.Fatalf("budget = %d, want %d from %+v", got, limit.Value, limit)
		}
	}
	if got := StepBudgetForTask(4096, 7, view, 4096); got != 7 {
		t.Fatalf("task cap exceeded: %d", got)
	}
	if got := StepBudgetForTask(4096, 8192, ProviderMetadata{MaxOutputTokens: 32}, 4096); got != 32 {
		t.Fatalf("legacy cap exceeded: %d", got)
	}
}
