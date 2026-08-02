package handlers

import (
	"context"
	"errors"
	"testing"

	"github.com/PizenLabs/izen/internal/runtime"
)

func TestStubHandlersNotImplemented(t *testing.T) {
	stubs := []runtime.CommandHandler{
		&SubmitPromptHandler{},
		&SwitchModeHandler{},
		&ApprovePatchHandler{},
		&RejectPatchHandler{},
		&CancelHandler{},
	}
	for _, h := range stubs {
		err := h.Handle(context.Background(), runtime.CancelCmd{})
		if !errors.Is(err, ErrNotImplemented) {
			t.Errorf("Handle(%T) = %v, want ErrNotImplemented", h, err)
		}
	}
}

func TestStubHandlerRespectsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := (&SubmitPromptHandler{}).Handle(ctx, runtime.SubmitPromptCmd{Prompt: "p"})
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("Handle = %v, want context.Canceled", err)
	}
}

func TestRegisterDefaults(t *testing.T) {
	d := runtime.NewCommandDispatcher()
	if err := RegisterDefaults(d); err != nil {
		t.Fatalf("RegisterDefaults: %v", err)
	}
	for _, typ := range []runtime.CommandType{
		runtime.CommandSubmitPrompt,
		runtime.CommandSwitchMode,
		runtime.CommandApprovePatch,
		runtime.CommandRejectPatch,
		runtime.CommandCancel,
	} {
		if err := d.Dispatch(context.Background(), cmdFor(typ)); err == nil {
			t.Errorf("Dispatch(%q): want ErrNotImplemented, got nil", typ)
		}
	}
}

func TestRegisterDefaultsNilDispatcher(t *testing.T) {
	if err := RegisterDefaults(nil); err == nil {
		t.Fatal("RegisterDefaults(nil): want error, got nil")
	}
}

func cmdFor(typ runtime.CommandType) runtime.RuntimeCommand {
	switch typ {
	case runtime.CommandSubmitPrompt:
		return runtime.SubmitPromptCmd{Prompt: "p"}
	case runtime.CommandSwitchMode:
		return runtime.SwitchModeCmd{Mode: "plan"}
	case runtime.CommandApprovePatch:
		return runtime.ApprovePatchCmd{PatchID: "1"}
	case runtime.CommandRejectPatch:
		return runtime.RejectPatchCmd{PatchID: "1"}
	default:
		return runtime.CancelCmd{Reason: "r"}
	}
}
