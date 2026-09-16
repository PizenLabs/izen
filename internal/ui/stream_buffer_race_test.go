package ui

import (
	"sync"
	"testing"
)

// TestStreamBuffer_ConcurrentAppendAndRead hammers Append from producers while
// the UI thread reads Blocks/Content/Thinking/Len. Run under -race: any data
// race on StreamBuffer reads fails the test.
func TestStreamBuffer_ConcurrentAppendAndRead(t *testing.T) {
	t.Parallel()

	b := NewStreamBuffer()
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				if (i+w)%2 == 0 {
					b.Append(KindContent, "c")
				} else {
					b.Append(KindThinking, "t")
				}
			}
		}(w)
	}
	// Concurrent UI-thread reads.
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				_ = b.Blocks()
				_ = b.Content()
				_ = b.Thinking()
				_ = b.Len()
				_ = b.HasContent()
				_ = b.HasThinking()
			}
		}()
	}
	wg.Wait()
	if b.Len() == 0 {
		t.Fatal("expected buffered bytes after concurrent appends")
	}
}
