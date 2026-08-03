package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/events"
)

func TestDispatcherDispatchRoutesToRegisteredHandler(t *testing.T) {
	d := NewCommandDispatcher()
	var got RuntimeCommand
	rec := HandlerFunc(func(_ context.Context, cmd RuntimeCommand) error {
		got = cmd
		return nil
	})
	if err := d.Register(CommandSwitchMode, rec); err != nil {
		t.Fatalf("Register: %v", err)
	}

	cmd := SwitchModeCmd{Mode: "plan"}
	if err := d.Dispatch(context.Background(), cmd); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if got == nil || got.Type() != CommandSwitchMode {
		t.Fatalf("Dispatch routed to wrong handler: got %v", got)
	}
}

func TestDispatcherDispatchUnhandledCommand(t *testing.T) {
	d := NewCommandDispatcher()
	err := d.Dispatch(context.Background(), CancelCmd{Reason: "x"})
	if !errors.Is(err, ErrUnhandledCommand) {
		t.Fatalf("Dispatch error = %v, want ErrUnhandledCommand", err)
	}
}

func TestDispatcherDispatchNilCommand(t *testing.T) {
	d := NewCommandDispatcher()
	err := d.Dispatch(context.Background(), nil)
	if !errors.Is(err, ErrNilCommand) {
		t.Fatalf("Dispatch error = %v, want ErrNilCommand", err)
	}
}

func TestDispatcherDispatchNilDispatcher(t *testing.T) {
	var d *CommandDispatcher
	err := d.Dispatch(context.Background(), CancelCmd{})
	if err == nil {
		t.Fatal("Dispatch on nil dispatcher: want error, got nil")
	}
}

func TestDispatcherRegisterRejectsInvalid(t *testing.T) {
	d := NewCommandDispatcher()
	tests := []struct {
		name string
		typ  CommandType
		hand CommandHandler
	}{
		{"empty type", "", HandlerFunc(func(_ context.Context, _ RuntimeCommand) error { return nil })},
		{"nil handler", CommandCancel, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := d.Register(tt.typ, tt.hand); err == nil {
				t.Fatal("Register: want error, got nil")
			}
		})
	}
}

func TestDispatcherRegisterDuplicate(t *testing.T) {
	d := NewCommandDispatcher()
	h := HandlerFunc(func(_ context.Context, _ RuntimeCommand) error { return nil })
	if err := d.Register(CommandCancel, h); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	err := d.Register(CommandCancel, h)
	if err == nil {
		t.Fatal("second Register: want duplicate error, got nil")
	}
}

func TestDispatcherConcurrentDispatch(t *testing.T) {
	d := NewCommandDispatcher()
	if err := d.Register(CommandCancel, HandlerFunc(func(ctx context.Context, cmd RuntimeCommand) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		_ = cmd
		return nil
	})); err != nil {
		t.Fatalf("Register: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := d.Dispatch(context.Background(), CancelCmd{Reason: "r"}); err != nil {
				t.Errorf("Dispatch: %v", err)
			}
		}()
	}
	wg.Wait()
}

func TestRuntimeExecuteNilCommand(t *testing.T) {
	r := NewRuntime(NewCommandDispatcher())
	if err := r.Execute(context.Background(), nil); !errors.Is(err, ErrNilCommand) {
		t.Fatalf("Execute error = %v, want ErrNilCommand", err)
	}
}

func TestRuntimeExecuteUninitialized(t *testing.T) {
	var r *Runtime
	if err := r.Execute(context.Background(), CancelCmd{}); err == nil {
		t.Fatal("Execute on nil runtime: want error, got nil")
	}
}

func TestRuntimeExecuteDispatches(t *testing.T) {
	d := NewCommandDispatcher()
	if err := d.Register(CommandCancel, HandlerFunc(func(_ context.Context, cmd RuntimeCommand) error {
		if cmd.Type() != CommandCancel {
			return fmt.Errorf("unexpected command type %q", cmd.Type())
		}
		return nil
	})); err != nil {
		t.Fatalf("Register: %v", err)
	}
	r := NewRuntime(d)
	if err := r.Execute(context.Background(), CancelCmd{Reason: "r"}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
}

func TestRuntimeExecutePublishesCommandReceived(t *testing.T) {
	d := NewCommandDispatcher()
	if err := d.Register(CommandSubmitPrompt, HandlerFunc(func(_ context.Context, _ RuntimeCommand) error {
		return nil
	})); err != nil {
		t.Fatalf("Register: %v", err)
	}

	bus := events.NewBus(16)
	received := make(chan events.DomainEvent, 4)
	sub := bus.Subscribe(events.EventCommandReceived, func(ev events.DomainEvent) {
		received <- ev
	})
	defer sub.Cancel()

	r := NewRuntime(d, WithEventBus(bus))
	cmd := SubmitPromptCmd{Prompt: "refactor dispatcher", Mode: "plan"}
	if err := r.Execute(context.Background(), cmd); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	select {
	case ev := <-received:
		if ev.Type() != events.EventCommandReceived {
			t.Fatalf("event type = %q, want %q", ev.Type(), events.EventCommandReceived)
		}
		p, ok := ev.Payload().(events.CommandReceivedPayload)
		if !ok {
			t.Fatalf("payload type %T, want CommandReceivedPayload", ev.Payload())
		}
		if p.Command != string(CommandSubmitPrompt) {
			t.Errorf("payload command = %q, want %q", p.Command, CommandSubmitPrompt)
		}
		if p.Mode != "plan" {
			t.Errorf("payload mode = %q, want plan", p.Mode)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for CommandReceived event")
	}
}
