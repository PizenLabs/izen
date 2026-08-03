package ports

import (
	"context"
	"errors"
	"testing"
)

// staticAdapter implements every port over fixed function values so the
// interfaces can be exercised without any concrete outer-layer implementation.
type staticAdapter struct {
	execute func(ctx context.Context, command string) (ShellResult, error)
	write   func(ctx context.Context, path, content string) error
}

func (s *staticAdapter) Execute(ctx context.Context, command string) (ShellResult, error) {
	return s.execute(ctx, command)
}

func (s *staticAdapter) ExecuteIn(ctx context.Context, dir, command string) (ShellResult, error) {
	return s.execute(ctx, command)
}

func (s *staticAdapter) Write(ctx context.Context, path, content string) error {
	return s.write(ctx, path, content)
}

func (s *staticAdapter) Read(ctx context.Context, path string) (string, error) {
	return "", nil
}

func (s *staticAdapter) List(ctx context.Context, dir string) ([]string, error) {
	return nil, nil
}

func (s *staticAdapter) Exists(ctx context.Context, path string) bool {
	return false
}

func TestCapabilityBits(t *testing.T) {
	cases := []struct {
		bit  Capability
		name string
	}{
		{CapRead, "read"},
		{CapWrite, "write"},
		{CapShell, "shell"},
		{CapTest, "test"},
		{CapPatch, "patch"},
		{CapCheckpoint, "checkpoint"},
	}
	for _, tc := range cases {
		if !tc.bit.Has(tc.bit) {
			t.Errorf("%s.Has(self) = false", tc.bit)
		}
		if got := tc.bit.String(); got != tc.name {
			t.Errorf("Capability.String() = %q, want %q", got, tc.name)
		}
	}
	if !(CapRead | CapWrite).Has(CapRead) {
		t.Error("(Read|Write).Has(Read) = false")
	}
	if (CapRead | CapWrite).Has(CapShell) {
		t.Error("(Read|Write).Has(Shell) = true")
	}
}

func TestCapabilitySet(t *testing.T) {
	s := NewCapabilitySet(CapRead)
	if !s.Has(CapRead) {
		t.Fatal("set missing initial read bit")
	}
	s.Add(CapWrite | CapPatch)
	if !s.Has(CapPatch) || !s.Has(CapWrite) {
		t.Fatal("set missing added bits")
	}
	s.Remove(CapWrite)
	if s.Has(CapWrite) {
		t.Error("set still holds removed bit")
	}
	if got := s.Bits(); !got.Has(CapRead) || !got.Has(CapPatch) {
		t.Errorf("Bits() = %v, want read|patch", got)
	}
	if got := s.String(); got != "read,patch" {
		t.Errorf("String() = %q, want %q", got, "read,patch")
	}
}

func TestPortAdaptersContract(t *testing.T) {
	sh := &staticAdapter{
		execute: func(ctx context.Context, command string) (ShellResult, error) {
			return ShellResult{Stdout: "ok", ExitCode: 0}, nil
		},
	}
	var shellPort ShellPort = sh
	res, err := shellPort.Execute(context.Background(), "echo ok")
	if err != nil || res.ExitCode != 0 || res.Stdout != "ok" {
		t.Fatalf("ShellPort.Execute = (%+v, %v)", res, err)
	}

	wantErr := errors.New("no write")
	f := &staticAdapter{
		write: func(ctx context.Context, path, content string) error {
			if path != "a.go" || content != "code" {
				return errors.New("bad args")
			}
			return wantErr
		},
	}
	var filePort FilePort = f
	if err := filePort.Write(context.Background(), "a.go", "code"); !errors.Is(err, wantErr) {
		t.Fatalf("FilePort.Write err = %v, want %v", err, wantErr)
	}

	// Compile-time checks: the domain ports remain interface-only, so any
	// concrete adapter must satisfy them structurally.
	var _ PatchPort = patchAdapter{}
	var _ GitPort = gitAdapter{}
	var _ LLMPort = llmAdapter{}
}

type patchAdapter struct{}

func (patchAdapter) Parse(ctx context.Context, payload string) (PatchPayload, error) {
	return PatchPayload{}, nil
}

func (patchAdapter) Validate(ctx context.Context, patch PatchPayload, current string) error {
	return nil
}

func (patchAdapter) Apply(ctx context.Context, patch PatchPayload) (PatchResult, error) {
	return PatchResult{}, nil
}

type gitAdapter struct{}

func (gitAdapter) Status(ctx context.Context) ([]GitStatusEntry, error) { return nil, nil }

func (gitAdapter) Diff(ctx context.Context) (string, error) { return "", nil }

func (gitAdapter) DiffFile(ctx context.Context, path string) (string, error) { return "", nil }

func (gitAdapter) Commit(ctx context.Context, subject, body string) error { return nil }

func (gitAdapter) CurrentHash(ctx context.Context) (string, error) { return "", nil }

func (gitAdapter) Branch(ctx context.Context) (string, error) { return "", nil }

type llmAdapter struct{}

func (llmAdapter) Generate(ctx context.Context, req LLMRequest) (LLMResponse, error) {
	return LLMResponse{}, nil
}

func (llmAdapter) Stream(ctx context.Context, req LLMRequest, handler StreamHandler) (LLMResponse, error) {
	return LLMResponse{}, nil
}
