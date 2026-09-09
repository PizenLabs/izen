package audit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/PizenLabs/izen/internal/state"
)

func TestConcurrentEventLoggingNoInterleave(t *testing.T) {
	root := t.TempDir()
	logger := NewLogger(root)

	const writers = 8
	const perWriter = 25
	var wg sync.WaitGroup
	errCh := make(chan error, writers*perWriter)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				payload := map[string]any{"writer": w, "seq": i}
				if err := logger.LogEvent(fmt.Sprintf("sess-%d", w), EventPatchStaged, payload); err != nil {
					errCh <- err
				}
			}
		}(w)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("LogEvent: %v", err)
	}

	path := state.LocalPath(root, state.AuditDir, EventsFile)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	lines := splitLines(string(data))
	nonEmpty := 0
	for _, line := range lines {
		if line == "" {
			continue
		}
		nonEmpty++
		var evt Event
		if err := json.Unmarshal([]byte(line), &evt); err != nil {
			t.Fatalf("line not parseable (interleave?): %v\nline: %q", err, line)
		}
	}
	if nonEmpty != writers*perWriter {
		t.Fatalf("events = %d, want %d", nonEmpty, writers*perWriter)
	}

	events, err := logger.ReadEvents()
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	if len(events) != writers*perWriter {
		t.Fatalf("ReadEvents = %d, want %d", len(events), writers*perWriter)
	}
}

func TestMutationLogLinesAreParseable(t *testing.T) {
	root := t.TempDir()
	logger := NewLogger(root)
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = logger.LogMutation(MutationEntry{
				File:   fmt.Sprintf("file-%d.go", i),
				Action: "write",
			})
		}(i)
	}
	wg.Wait()

	data, err := os.ReadFile(filepath.Join(root, ".izen", "audit", "mutations.log"))
	if err != nil {
		t.Fatalf("read mutations: %v", err)
	}
	for _, line := range splitLines(string(data)) {
		if line == "" {
			continue
		}
		var e MutationEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("mutation line not parseable: %v\nline: %q", err, line)
		}
	}
}
