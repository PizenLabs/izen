package ui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/events"
)

func TestHandleDomainEventProjection(t *testing.T) {
	tests := []struct {
		name string
		ev   events.DomainEvent
		want string
	}{
		{"command received", events.NewCommandReceived("refactor LICENSE", "plan"),
			"[plan] received command: refactor LICENSE"},
		{"intent parsed", events.NewIntentParsed("direct_mutation", "refactor LICENSE", 1.0),
			"[intent] parsed: direct_mutation (100% confidence)"},
		{"plan staged", events.NewPlanStaged(3, []string{"a", "b", "c"}, "plan"),
			"[plan] staged 3 tasks"},
		{"patch attempted", events.NewPatchAttempted("x.go", "ATOMIC_REPLACE", 2),
			"[build] patch attempt 2: x.go (ATOMIC_REPLACE)"},
		{"patch applied", events.NewPatchApplied("x.go", 12, 4, 350*time.Millisecond),
			"[build] applied patch to x.go (+12/-4 lines)"},
		{"execution failed", events.NewExecutionFailed(events.FailureRecoverable, errors.New("boom"), "build.compilation"),
			"[error][recoverable] boom (stage: build.compilation)"},
		{"self-healing attempt", events.NewSelfHealingAttempt(2, "worker.go", "TYPE_MISMATCH"),
			"[self-heal] retry 2: worker.go (TYPE_MISMATCH)"},
		{"self-healing exhausted", events.NewSelfHealingExhausted(4, "./x.go:5: undefined: foo"),
			"[self-heal] exhausted after 4 attempt(s); workspace rolled back clean"},
		{"stage completed", events.NewStageCompleted("review", 5*time.Millisecond, "ok"),
			"[stage] review completed (ok)"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := &model{}
			m.handleDomainEvent(tc.ev)

			if len(m.records) != 1 {
				t.Fatalf("got %d records, want 1", len(m.records))
			}
			if got := m.records[0].text; got != tc.want {
				t.Errorf("record = %q, want %q", got, tc.want)
			}
			if m.records[0].role != roleActivity {
				t.Errorf("record role = %v, want roleActivity", m.records[0].role)
			}
		})
	}
}

func TestHandleDomainEventPatchAppliedAppendsActivityTree(t *testing.T) {
	m := &model{activityTree: NewActivityTree()}
	m.handleDomainEvent(events.NewPatchApplied("x.go", 12, 4, 350*time.Millisecond))

	entries := m.activityTree.Entries()
	if len(entries) != 1 {
		t.Fatalf("got %d tree entries, want 1", len(entries))
	}
	ev := entries[0]
	if ev.Kind != EventFileMutate {
		t.Errorf("entry kind = %v, want EventFileMutate", ev.Kind)
	}
	if ev.FileMutate == nil {
		t.Fatal("FileMutate is nil")
	}
	if ev.FileMutate.File != "x.go" || ev.FileMutate.LinesAdd != 12 || ev.FileMutate.LinesDel != 4 {
		t.Errorf("FileMutate = %+v", ev.FileMutate)
	}
}

func TestHandleDomainEventNilIsNoop(t *testing.T) {
	m := &model{activityTree: NewActivityTree()}
	m.handleDomainEvent(nil)
	if len(m.records) != 0 {
		t.Errorf("got %d records for nil event, want 0", len(m.records))
	}
	if m.activityTree.Len() != 0 {
		t.Errorf("got %d tree entries for nil event, want 0", m.activityTree.Len())
	}
}

func TestTruncateForActivity(t *testing.T) {
	short := "hello world"
	if got := truncateForActivity(short); got != short {
		t.Errorf("short input = %q, want unchanged", got)
	}
	long := strings.Repeat("x", 200)
	got := truncateForActivity(long)
	if len(got) != 90 {
		t.Errorf("long input truncated to %d chars, want 90", len(got))
	}
	if !strings.HasSuffix(got, "...") {
		t.Errorf("truncated output missing ellipsis: %q", got)
	}
	whitespace := "   padded   "
	if got := truncateForActivity(whitespace); got != "padded" {
		t.Errorf("whitespace input = %q, want trimmed", got)
	}
}
