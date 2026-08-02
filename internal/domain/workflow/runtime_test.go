package workflow

import (
	"errors"
	"sync"
	"testing"
)

func TestPhaseString(t *testing.T) {
	cases := map[Phase]string{
		PhaseAsk:         "ask",
		PhaseInvestigate: "investigate",
		PhasePlan:        "plan",
		PhaseBuild:       "build",
		PhaseReview:      "review",
	}
	for phase, want := range cases {
		if got := phase.String(); got != want {
			t.Errorf("Phase(%d).String() = %q, want %q", int(phase), got, want)
		}
	}
}

func TestPhaseValidity(t *testing.T) {
	for _, phase := range []Phase{PhaseAsk, PhaseInvestigate, PhasePlan, PhaseBuild, PhaseReview} {
		if !phase.Valid() {
			t.Errorf("Phase(%d).Valid() = false, want true", int(phase))
		}
	}
	for _, phase := range []Phase{Phase(-1), Phase(99)} {
		if phase.Valid() {
			t.Errorf("Phase(%d).Valid() = true, want false", int(phase))
		}
	}
}

func TestPhaseIsTerminal(t *testing.T) {
	if !PhaseReview.IsTerminal() {
		t.Error("PhaseReview.IsTerminal() = false, want true")
	}
	for _, phase := range []Phase{PhaseAsk, PhaseInvestigate, PhasePlan, PhaseBuild} {
		if phase.IsTerminal() {
			t.Errorf("Phase(%d).IsTerminal() = true, want false", int(phase))
		}
	}
}

func TestPhaseOrdering(t *testing.T) {
	order := []Phase{PhaseAsk, PhaseInvestigate, PhasePlan, PhaseBuild, PhaseReview}
	for i := 0; i < len(order); i++ {
		for j := 0; j < len(order); j++ {
			precedes := i < j
			if got := order[i].Precedes(order[j]); got != precedes {
				t.Errorf("%s.Precedes(%s) = %v, want %v", order[i], order[j], got, precedes)
			}
			follows := i > j
			if got := order[i].Follows(order[j]); got != follows {
				t.Errorf("%s.Follows(%s) = %v, want %v", order[i], order[j], got, follows)
			}
		}
	}
}

// atPhase builds a runtime positioned at the given phase via legal forward
// transitions.
func atPhase(t *testing.T, p Phase) WorkflowRuntime {
	t.Helper()
	r := NewWorkflowRuntime()
	if p == PhaseAsk {
		return r
	}
	if err := r.Transition(p); err != nil {
		t.Fatalf("setup transition to %s: %v", p, err)
	}
	return r
}

func TestNewWorkflowRuntimeStartsAtAsk(t *testing.T) {
	if got := NewWorkflowRuntime().Phase(); got != PhaseAsk {
		t.Errorf("initial phase = %s, want %s", got, PhaseAsk)
	}
}

func TestTransitionRules(t *testing.T) {
	cases := []struct {
		name string
		from Phase
		to   Phase
		ok   bool
	}{
		{name: "ask to ask noop", from: PhaseAsk, to: PhaseAsk, ok: true},
		{name: "ask forward investigate", from: PhaseAsk, to: PhaseInvestigate, ok: true},
		{name: "ask forward plan", from: PhaseAsk, to: PhasePlan, ok: true},
		{name: "ask forward build", from: PhaseAsk, to: PhaseBuild, ok: true},
		{name: "ask forward review", from: PhaseAsk, to: PhaseReview, ok: true},
		{name: "investigate forward plan", from: PhaseInvestigate, to: PhasePlan, ok: true},
		{name: "plan forward build", from: PhasePlan, to: PhaseBuild, ok: true},
		{name: "build forward review", from: PhaseBuild, to: PhaseReview, ok: true},
		{name: "review to review noop", from: PhaseReview, to: PhaseReview, ok: true},
		{name: "build replan", from: PhaseBuild, to: PhasePlan, ok: true},
		{name: "review replan", from: PhaseReview, to: PhasePlan, ok: true},
		{name: "review backward build", from: PhaseReview, to: PhaseBuild, ok: false},
		{name: "review backward investigate", from: PhaseReview, to: PhaseInvestigate, ok: false},
		{name: "build backward ask", from: PhaseBuild, to: PhaseAsk, ok: false},
		{name: "plan backward ask", from: PhasePlan, to: PhaseAsk, ok: false},
		{name: "plan backward investigate", from: PhasePlan, to: PhaseInvestigate, ok: false},
		{name: "investigate backward ask", from: PhaseInvestigate, to: PhaseAsk, ok: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := atPhase(t, tc.from)
			err := r.Transition(tc.to)
			if tc.ok && err != nil {
				t.Fatalf("Transition(%s) = %v, want nil", tc.to, err)
			}
			if !tc.ok {
				if err == nil {
					t.Fatalf("Transition(%s) succeeded, want error", tc.to)
				}
				if !errors.Is(err, ErrInvalidTransition) {
					t.Fatalf("Transition(%s) error = %v, want ErrInvalidTransition", tc.to, err)
				}
				if got := r.Phase(); got != tc.from {
					t.Errorf("phase after rejected transition = %s, want %s", got, tc.from)
				}
			}
		})
	}
}

func TestTransitionInvalidTarget(t *testing.T) {
	r := atPhase(t, PhasePlan)
	err := r.Transition(Phase(99))
	if err == nil {
		t.Fatal("Transition(Phase(99)) succeeded, want error")
	}
	if !errors.Is(err, ErrInvalidPhase) {
		t.Fatalf("error = %v, want ErrInvalidPhase", err)
	}
	var te *TransitionError
	if !errors.As(err, &te) {
		t.Fatalf("error = %v, want *TransitionError", err)
	}
	if te.From != PhasePlan || te.To != Phase(99) {
		t.Errorf("TransitionError fields = (%s -> %s), want (%s -> %s)", te.From, te.To, PhasePlan, Phase(99))
	}
}

func TestCanTransitionMatchesTransition(t *testing.T) {
	for _, from := range []Phase{PhaseAsk, PhaseInvestigate, PhasePlan, PhaseBuild, PhaseReview} {
		r := atPhase(t, from)
		for _, to := range []Phase{PhaseAsk, PhaseInvestigate, PhasePlan, PhaseBuild, PhaseReview, Phase(99)} {
			can := r.CanTransition(to)
			err := r.Transition(to)
			if can != (err == nil) {
				t.Errorf("from %s to %s: CanTransition = %v but Transition err = %v", from, to, can, err)
			}
		}
	}
}

func TestReset(t *testing.T) {
	r := atPhase(t, PhaseReview)
	r.Reset()
	if got := r.Phase(); got != PhaseAsk {
		t.Errorf("phase after Reset = %s, want %s", got, PhaseAsk)
	}
	if err := r.Transition(PhasePlan); err != nil {
		t.Errorf("forward transition after Reset failed: %v", err)
	}
}

func TestIsTerminal(t *testing.T) {
	if NewWorkflowRuntime().IsTerminal() {
		t.Error("fresh runtime IsTerminal() = true, want false")
	}
	if !atPhase(t, PhaseReview).IsTerminal() {
		t.Error("runtime at PhaseReview IsTerminal() = false, want true")
	}
}

func TestCustomRuleOverride(t *testing.T) {
	r := NewWorkflowRuntime(WithTransitionRule(func(from, to Phase) error {
		return nil
	}))
	if err := r.Transition(PhaseBuild); err != nil {
		t.Fatalf("forward transition failed: %v", err)
	}
	if err := r.Transition(PhaseAsk); err != nil {
		t.Errorf("custom rule should allow backward transition, got %v", err)
	}
}

func TestNilRuleFallsBackToDefault(t *testing.T) {
	r := NewWorkflowRuntime(WithTransitionRule(nil))
	if err := r.Transition(PhasePlan); err != nil {
		t.Errorf("default rule not applied for nil option: %v", err)
	}
	if err := r.Transition(PhaseAsk); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("backward transition = %v, want ErrInvalidTransition", err)
	}
}

func TestConcurrentAccess(t *testing.T) {
	r := NewWorkflowRuntime()
	var wg sync.WaitGroup
	errCh := make(chan error, 64)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_ = r.Phase()
				_ = r.CanTransition(PhaseReview)
				_ = r.IsTerminal()
				// PhaseBuild is forward-or-noop from every reachable phase.
				if err := r.Transition(PhaseBuild); err != nil {
					errCh <- err
				}
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Errorf("concurrent transition error: %v", err)
	}
}
