package orchestrator

import (
	"errors"
	"testing"

	"github.com/PizenLabs/izen/internal/runtime/executor"
	"github.com/PizenLabs/izen/internal/runtime/gate"
	"github.com/PizenLabs/izen/internal/runtime/harness"
	"github.com/PizenLabs/izen/internal/runtime/substrate"
)

// TestLoopConstruction_NilSubstrate asserts explicit construction: NewLoop
// fails fast with ErrNilSubstrate when passed a nil Substrate. Hidden global
// DI fallbacks are forbidden — production bootstrap must pass a concrete
// Substrate.
func TestLoopConstruction_NilSubstrate(t *testing.T) {
	t.Parallel()

	extractor := NewMemoryBackedExtractor("target.go")
	gp := gate.NewPipeline()
	exec := executor.NewExecutor()

	if _, err := NewLoop(extractor, gp, exec, FSSnapshotReader{}, nil); !errors.Is(err, ErrNilSubstrate) {
		t.Fatalf("NewLoop(nil substrate) err = %v, want ErrNilSubstrate", err)
	}
	if _, err := NewLoopWithSubstrate(extractor, gp, exec, FSSnapshotReader{}, nil); !errors.Is(err, ErrNilSubstrate) {
		t.Fatalf("NewLoopWithSubstrate(nil substrate) err = %v, want ErrNilSubstrate", err)
	}
}

// TestLoopConstruction_ExplicitSubstrate proves the production path: a
// concrete Substrate wires successfully.
func TestLoopConstruction_ExplicitSubstrate(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	sub := substrate.NewConcreteSubstrate(dir)
	l, err := NewLoop(
		&MemoryBackedExtractor{Pipeline: harness.NewExtractorPipeline()},
		gate.NewPipeline(),
		executor.NewExecutor(),
		FSSnapshotReader{},
		sub,
	)
	if err != nil {
		t.Fatalf("NewLoop(explicit substrate): %v", err)
	}
	if l == nil || l.State() != StateIdle {
		t.Fatal("explicit construction must yield an idle Loop")
	}
}
