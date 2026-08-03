package presentation

import (
	"context"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/runtime"
	"github.com/PizenLabs/izen/internal/runtime/compose"
)

func TestCommandFromInput(t *testing.T) {
	tests := []struct {
		line string
		mode string
		want runtime.CommandType
	}{
		{"/plan", "ask", runtime.CommandSwitchMode},
		{"/mode build", "plan", runtime.CommandSwitchMode},
		{"  /review  ", "plan", runtime.CommandSwitchMode},
		{"explain the cache", "ask", runtime.CommandSubmitPrompt},
		{"  ", "ask", ""},
	}
	for _, tt := range tests {
		cmd, ok := CommandFromInput(tt.line, tt.mode)
		if tt.want == "" {
			if ok {
				t.Fatalf("CommandFromInput(%q) = %v, want !ok", tt.line, cmd)
			}
			continue
		}
		if !ok {
			t.Fatalf("CommandFromInput(%q) = !ok, want %v", tt.line, tt.want)
		}
		if cmd.Type() != tt.want {
			t.Errorf("CommandFromInput(%q).Type() = %v, want %v", tt.line, cmd.Type(), tt.want)
		}
		if sm, ok := cmd.(runtime.SwitchModeCmd); ok && sm.Mode != "plan" && sm.Mode != "build" && sm.Mode != "review" {
			t.Errorf("SwitchModeCmd.Mode = %q, want plan/build/review", sm.Mode)
		}
	}
}

func TestCommandFromKey(t *testing.T) {
	enter := tea.KeyMsg{Type: tea.KeyEnter}
	esc := tea.KeyMsg{Type: tea.KeyEscape}
	ctrlC := tea.KeyMsg{Type: tea.KeyCtrlC}
	altA := tea.KeyMsg{Type: tea.KeyRunes, Alt: true, Runes: []rune{'a'}}
	altR := tea.KeyMsg{Type: tea.KeyRunes, Alt: true, Runes: []rune{'r'}}
	typeChar := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}}

	tests := []struct {
		name   string
		msg    tea.KeyMsg
		ctx    KeyContext
		want   runtime.CommandType
		wantOK bool
	}{
		{"approve enter", enter, KeyContext{PatchID: "p1"}, runtime.CommandApprovePatch, true},
		{"approve alt+a", altA, KeyContext{PatchID: "p1"}, runtime.CommandApprovePatch, true},
		{"reject esc", esc, KeyContext{PatchID: "p1"}, runtime.CommandRejectPatch, true},
		{"reject alt+r", altR, KeyContext{PatchID: "p1"}, runtime.CommandRejectPatch, true},
		{"busy cancel esc", esc, KeyContext{Busy: true}, runtime.CommandCancel, true},
		{"busy cancel ctrl+c", ctrlC, KeyContext{Busy: true}, runtime.CommandCancel, true},
		{"idle esc not mapped", esc, KeyContext{}, "", false},
		{"idle char not mapped", typeChar, KeyContext{}, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd, ok := CommandFromKey(tt.msg, tt.ctx)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if ok && cmd.Type() != tt.want {
				t.Errorf("type = %v, want %v", cmd.Type(), tt.want)
			}
		})
	}
}

// recorder is a fake Sender that captures forwarded messages.
type recorder struct {
	mu   sync.Mutex
	msgs []tea.Msg
}

func (r *recorder) Send(m tea.Msg) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.msgs = append(r.msgs, m)
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.msgs)
}

func TestBridge_ExecuteRoutesCommands(t *testing.T) {
	app, err := compose.Wire()
	if err != nil {
		t.Fatalf("Wire: %v", err)
	}
	defer app.Close()

	b := New(app.Runtime)

	if err := b.Submit(context.Background(), "plan the migration", "plan"); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if app.Workflow.Phase().String() != "plan" {
		t.Fatalf("phase = %s, want plan", app.Workflow.Phase())
	}
	if err := b.ApprovePatch(context.Background(), "p1"); err != nil {
		t.Fatalf("ApprovePatch: %v", err)
	}
	if err := b.RejectPatch(context.Background(), "p2", "too risky"); err != nil {
		t.Fatalf("RejectPatch: %v", err)
	}
	if err := b.Cancel(context.Background(), "stop"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
}

func TestBridge_NilRuntimeNoop(t *testing.T) {
	b := New(nil)
	if err := b.Submit(context.Background(), "p", ""); err != nil {
		t.Fatalf("nil runtime Submit = %v, want nil", err)
	}
}

func TestEventSink_ForwardsPresentationEvents(t *testing.T) {
	app, err := compose.Wire()
	if err != nil {
		t.Fatalf("Wire: %v", err)
	}
	defer app.Close()

	rec := &recorder{}
	sink := NewEventSink(rec, app.Runtime, func(ev runtime.PresentationEvent) tea.Msg {
		return presentationProbeMsg{summary: ev.Summary}
	})
	defer sink.Close()

	if err := app.Runtime.Execute(context.Background(), runtime.SubmitPromptCmd{Prompt: "plan the API", Mode: "plan"}); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for rec.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if rec.count() == 0 {
		t.Fatal("expected at least one forwarded presentation event, got none")
	}
}

func TestEventSink_CloseStopsDelivery(t *testing.T) {
	app, err := compose.Wire()
	if err != nil {
		t.Fatalf("Wire: %v", err)
	}
	defer app.Close()

	rec := &recorder{}
	sink := NewEventSink(rec, app.Runtime, func(ev runtime.PresentationEvent) tea.Msg {
		return presentationProbeMsg{summary: ev.Summary}
	})
	sink.Close()

	if err := app.Runtime.Execute(context.Background(), runtime.CancelCmd{Reason: "x"}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if rec.count() != 0 {
		t.Fatalf("expected no events after Close, got %d", rec.count())
	}
}

// presentationProbeMsg is a local message carrier used by the sink tests.
type presentationProbeMsg struct {
	summary string
}
