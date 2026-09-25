package execution

import (
	"errors"
	"testing"

	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/protocol"
)

func TestLegacyEngineBindsContractBeforeCommandDispatch(t *testing.T) {
	engine := NewEngine(t.TempDir(), config.Default(), nil)
	readOnly := protocol.Describe(protocol.DirectCompletion)
	if err := engine.SetInteractionContract(protocol.DirectCompletion, &readOnly); err != nil {
		t.Fatal(err)
	}
	if err := engine.AdmitOperation(string(protocol.OperationFileMutate)); !errors.Is(err, ErrAuthorityExceeded) {
		t.Fatalf("engine mutation admission error = %v, want ErrAuthorityExceeded", err)
	}
	if _, err := engine.Run("echo should-not-run"); !errors.Is(err, ErrAuthorityExceeded) {
		t.Fatalf("engine command admission error = %v, want ErrAuthorityExceeded", err)
	}
	if _, err := engine.Runner.Run("echo direct-field-bypass"); !errors.Is(err, ErrAuthorityExceeded) {
		t.Fatalf("direct runner admission error = %v, want ErrAuthorityExceeded", err)
	}
	if err := engine.Patches.Apply(&Patch{File: "a.txt", Modified: "changed"}); !errors.Is(err, ErrAuthorityExceeded) {
		t.Fatalf("direct patch admission error = %v, want ErrAuthorityExceeded", err)
	}
}
