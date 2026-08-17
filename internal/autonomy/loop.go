package autonomy

import "fmt"

// LoopState is a position in the autonomous execution loop.
type LoopState string

// Loop positions. The loop is a decision loop, not a pipeline: failure feeds
// back into diagnosis, never into termination.
const (
	LoopIdle        LoopState = "idle"
	LoopInvestigate LoopState = "investigate"
	LoopPlan        LoopState = "plan"
	LoopBuild       LoopState = "build"
	LoopVerify      LoopState = "verify"
	LoopDiagnose    LoopState = "diagnose"
	LoopAskUser     LoopState = "ask_user"
	LoopStop        LoopState = "stop"
)

// String returns the canonical loop position label.
func (s LoopState) String() string {
	return string(s)
}

// VerificationResult is the outcome of a build verification step.
type VerificationResult struct {
	Passed    bool
	Stage     string
	Diagnosis string
}

// LoopEvent drives the loop forward.
type LoopEvent string

const (
	LoopStart                LoopEvent = "start"
	LoopEvidenceSufficient   LoopEvent = "evidence_sufficient"
	LoopEvidenceInsufficient LoopEvent = "evidence_insufficient"
	LoopCapabilityGranted    LoopEvent = "capability_granted"
	LoopCapabilityDenied     LoopEvent = "capability_denied"
	LoopBuildCompleted       LoopEvent = "build_completed"
	LoopVerifyPassed         LoopEvent = "verify_passed"
	LoopVerifyFailed         LoopEvent = "verify_failed"
	LoopDiagnosisReady       LoopEvent = "diagnosis_ready"
	LoopStopRequested        LoopEvent = "stop_requested"
)

// LoopTransition records one observable step of the loop: the from/to states
// and the reason the runtime moved. Transitions are emitted as canonical events
// so every autonomy decision is traceable.
type LoopTransition struct {
	From   LoopState
	To     LoopState
	Event  LoopEvent
	Reason string
}

// String renders the transition compactly (e.g. "plan -> build").
func (t LoopTransition) String() string {
	return fmt.Sprintf("%s -> %s (%s)", t.From, t.To, t.Event)
}

// AutonomousLoop is the controlled execution loop state machine:
//
//	INVESTIGATE -> evidence sufficient? -> PLAN -> capability available?
//	-> BUILD -> verification -> failed? -> DIAGNOSE -> INVESTIGATE again
//
// Failure never terminates the loop: it produces a diagnosis and loops back to
// investigation. The loop only stops on success, on an explicit stop request,
// or when the user must be asked (ASK_USER).
type AutonomousLoop struct {
	state         LoopState
	history       []LoopTransition
	maxIterations int
	iterations    int
}

// NewAutonomousLoop returns a loop starting at idle. maxIterations bounds the
// investigate->build iterations to guarantee termination (default 3).
func NewAutonomousLoop(maxIterations int) *AutonomousLoop {
	if maxIterations <= 0 {
		maxIterations = 3
	}
	return &AutonomousLoop{
		state:         LoopIdle,
		maxIterations: maxIterations,
	}
}

// State returns the current loop position.
func (l *AutonomousLoop) State() LoopState {
	if l == nil {
		return LoopIdle
	}
	return l.state
}

// History returns the observed transitions, oldest first.
func (l *AutonomousLoop) History() []LoopTransition {
	if l == nil {
		return nil
	}
	out := make([]LoopTransition, len(l.history))
	copy(out, l.history)
	return out
}

// Iterations returns the completed investigate->build cycle count.
func (l *AutonomousLoop) Iterations() int {
	if l == nil {
		return 0
	}
	return l.iterations
}

// Start launches the loop into the investigation phase.
func (l *AutonomousLoop) Start(reason string) []LoopTransition {
	if l == nil {
		return nil
	}
	return l.send(LoopStart, LoopIdle, LoopInvestigate, reason)
}

// EvidenceReady advances investigation to planning once evidence is sufficient.
func (l *AutonomousLoop) EvidenceReady(reason string) []LoopTransition {
	if l == nil {
		return nil
	}
	return l.send(LoopEvidenceSufficient, LoopInvestigate, LoopPlan, reason)
}

// EvidenceInsufficient loops investigation back to itself (re-scope the search).
func (l *AutonomousLoop) EvidenceInsufficient(reason string) []LoopTransition {
	if l == nil {
		return nil
	}
	return l.send(LoopEvidenceInsufficient, LoopInvestigate, LoopInvestigate, reason)
}

// AuthorizeBuild advances planning to build once the capability is available.
func (l *AutonomousLoop) AuthorizeBuild(reason string) []LoopTransition {
	if l == nil {
		return nil
	}
	return l.send(LoopCapabilityGranted, LoopPlan, LoopBuild, reason)
}

// DenyBuild returns the loop to ask_user: the capability authority refused.
func (l *AutonomousLoop) DenyBuild(reason string) []LoopTransition {
	if l == nil {
		return nil
	}
	return l.send(LoopCapabilityDenied, LoopPlan, LoopAskUser, reason)
}

// BuildDone advances build to verification.
func (l *AutonomousLoop) BuildDone(reason string) []LoopTransition {
	if l == nil {
		return nil
	}
	return l.send(LoopBuildCompleted, LoopBuild, LoopVerify, reason)
}

// VerifyPassed terminates the loop on success.
func (l *AutonomousLoop) VerifyPassed(reason string) []LoopTransition {
	if l == nil {
		return nil
	}
	return l.send(LoopVerifyPassed, LoopVerify, LoopStop, reason)
}

// VerifyFailed advances to diagnosis — never to termination.
func (l *AutonomousLoop) VerifyFailed(reason string) []LoopTransition {
	if l == nil {
		return nil
	}
	return l.send(LoopVerifyFailed, LoopVerify, LoopDiagnose, reason)
}

// DiagnosisReady loops diagnosis back to investigation, bounded by maxIterations.
// When the iteration budget is exhausted the loop asks the user instead of
// burning more autonomous cycles.
func (l *AutonomousLoop) DiagnosisReady(reason string) []LoopTransition {
	if l == nil {
		return nil
	}
	if l.iterations >= l.maxIterations {
		return l.send(LoopDiagnosisReady, LoopDiagnose, LoopAskUser, reason+fmt.Sprintf(" (iteration budget %d exhausted)", l.maxIterations))
	}
	trans := l.send(LoopDiagnosisReady, LoopDiagnose, LoopInvestigate, reason)
	if len(trans) > 0 {
		l.iterations++
	}
	return trans
}

// AskUser parks the loop until the user responds.
func (l *AutonomousLoop) AskUser(reason string) []LoopTransition {
	if l == nil {
		return nil
	}
	return l.send(LoopStopRequested, LoopAskUser, LoopAskUser, reason)
}

// Stop halts the loop.
func (l *AutonomousLoop) Stop(reason string) []LoopTransition {
	if l == nil {
		return nil
	}
	return l.send(LoopStopRequested, l.state, LoopStop, reason)
}

// send applies one transition and records it. Returns the newly recorded
// transitions so callers can publish them.
func (l *AutonomousLoop) send(ev LoopEvent, from, to LoopState, reason string) []LoopTransition {
	if l == nil || l.state == LoopStop {
		return nil
	}
	// A parked loop only accepts a stop; anything else must be re-started.
	if l.state == LoopAskUser && to != LoopAskUser && to != LoopStop {
		return nil
	}
	// Edge guard: the transition is legal when no source state is required,
	// when the loop is already in the expected source state, or when this is a
	// Start that may re-enter the loop from any parked position.
	canProceed := from == "" || l.state == from || (ev == LoopStart && from == LoopIdle)
	if !canProceed {
		return nil
	}
	old := l.state
	t := LoopTransition{From: old, To: to, Event: ev, Reason: reason}
	l.history = append(l.history, t)
	l.state = to
	return []LoopTransition{t}
}
