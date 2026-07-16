package context

import "testing"

func TestTaskLedgerDefaultsPending(t *testing.T) {
	l := NewTaskLedger()
	if l.Status(7) != TaskPending {
		t.Fatalf("expected default TaskPending, got %v", l.Status(7))
	}
	if l.IsCompleted(7) {
		t.Fatal("expected not completed by default")
	}
}

func TestTaskLedgerMarkCompleted(t *testing.T) {
	l := NewTaskLedger()
	l.MarkCompleted(1)
	if l.Status(1) != TaskCompleted {
		t.Fatalf("expected TaskCompleted, got %v", l.Status(1))
	}
	if !l.IsCompleted(1) {
		t.Fatal("expected IsCompleted true")
	}
}

func TestTaskLedgerStateTransitions(t *testing.T) {
	l := NewTaskLedger()
	l.MarkPending(2)
	if l.Status(2) != TaskPending {
		t.Fatalf("expected TaskPending, got %v", l.Status(2))
	}
	l.MarkExecuting(2)
	if l.Status(2) != TaskExecuting {
		t.Fatalf("expected TaskExecuting, got %v", l.Status(2))
	}
	l.MarkFailed(2)
	if l.Status(2) != TaskFailed {
		t.Fatalf("expected TaskFailed, got %v", l.Status(2))
	}
	l.MarkCompleted(2)
	if l.Status(2) != TaskCompleted {
		t.Fatalf("expected TaskCompleted after completion, got %v", l.Status(2))
	}
}

func TestTaskLedgerAll(t *testing.T) {
	l := NewTaskLedger()
	l.MarkCompleted(1)
	l.MarkFailed(2)
	l.MarkPending(3)

	all := l.All()
	if len(all) != 3 {
		t.Fatalf("expected 3 tracked tasks, got %d", len(all))
	}
	if all[1] != TaskCompleted || all[2] != TaskFailed || all[3] != TaskPending {
		t.Fatalf("unexpected ledger snapshot: %v", all)
	}
}

func TestTaskStatusString(t *testing.T) {
	cases := map[TaskStatus]string{
		TaskPending:   "PENDING",
		TaskExecuting: "EXECUTING",
		TaskCompleted: "COMPLETED",
		TaskFailed:    "FAILED",
	}
	for s, want := range cases {
		if s.String() != want {
			t.Fatalf("expected %q, got %q", want, s.String())
		}
	}
}
