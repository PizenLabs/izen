package autonomy

import "testing"

func TestLoopHappyPath(t *testing.T) {
	l := NewAutonomousLoop(3)
	l.Start("user requested mutation")
	l.EvidenceReady("forensic evidence collected")
	l.AuthorizeBuild("capability granted")
	l.BuildDone("patch applied")
	l.VerifyPassed("tests passed")
	if l.State() != LoopStop {
		t.Errorf("state = %s, want stop", l.State())
	}
	if len(l.History()) != 5 {
		t.Errorf("history = %d steps, want 5", len(l.History()))
	}
}

func TestLoopFailureProducesDiagnosisNotTermination(t *testing.T) {
	l := NewAutonomousLoop(3)
	l.Start("user requested mutation")
	l.EvidenceReady("evidence sufficient")
	l.AuthorizeBuild("granted")
	l.BuildDone("patch applied")
	l.VerifyFailed("verification failed")

	if l.State() != LoopDiagnose {
		t.Fatalf("after verify failure state = %s, want diagnose (never terminate)", l.State())
	}

	l.DiagnosisReady("root cause identified")
	if l.State() != LoopInvestigate {
		t.Errorf("after diagnosis state = %s, want investigate (loop back)", l.State())
	}
	if l.Iterations() != 1 {
		t.Errorf("iterations = %d, want 1", l.Iterations())
	}
}

func TestLoopIterationBudgetExhaustedAsksUser(t *testing.T) {
	l := NewAutonomousLoop(2)
	runCycle := func() {
		l.EvidenceReady("evidence")
		l.AuthorizeBuild("granted")
		l.BuildDone("built")
		l.VerifyFailed("failed")
		l.DiagnosisReady("diagnosed")
	}
	l.Start("start")
	runCycle()
	runCycle()
	runCycle()
	if l.State() != LoopAskUser {
		t.Errorf("after exhausting budget state = %s, want ask_user", l.State())
	}
}

func TestLoopStopIsTerminal(t *testing.T) {
	l := NewAutonomousLoop(3)
	l.Start("start")
	l.EvidenceReady("evidence")
	if trans := l.AuthorizeBuild("granted"); len(trans) == 0 {
		t.Fatal("build authorization must be accepted")
	}
	l.BuildDone("built")
	l.VerifyPassed("ok")
	if trans := l.Start("restart"); len(trans) != 0 {
		t.Error("stopped loop must ignore further events")
	}
}

func TestLoopEdgeGuard(t *testing.T) {
	l := NewAutonomousLoop(3)
	// Build authorization is only legal from plan.
	l.Start("start")
	if trans := l.AuthorizeBuild("granted"); len(trans) != 0 {
		t.Error("AuthorizeBuild from investigate must be rejected")
	}
	l.EvidenceReady("evidence")
	if trans := l.AuthorizeBuild("granted"); len(trans) == 0 {
		t.Error("AuthorizeBuild from plan must be accepted")
	}
}

func TestLoopVerifyPassedFromWrongStateRejected(t *testing.T) {
	l := NewAutonomousLoop(3)
	l.Start("start")
	if trans := l.VerifyPassed("ok"); len(trans) != 0 {
		t.Error("VerifyPassed outside verify must be rejected")
	}
}
