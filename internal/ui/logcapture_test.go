package ui

import (
	"log"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// TestStdLogCaptureRingBufferEvictsOldest proves the capture sink is a
// bounded ring: beyond the cap the OLDEST entries are evicted, so memory
// stays bounded no matter how chatty redirected dependencies become.
func TestStdLogCaptureRingBufferEvictsOldest(t *testing.T) {
	c := &stdLogCapture{}
	for i := 0; i < stdLogCaptureMaxEntries+10; i++ {
		if _, err := c.Write([]byte("entry-" + strconv.Itoa(i) + "\n")); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	dump := c.Dump()
	if len(dump) != stdLogCaptureMaxEntries {
		t.Fatalf("ring holds %d entries, want %d", len(dump), stdLogCaptureMaxEntries)
	}
	// Entries 0..9 were evicted; entry 10 is the oldest survivor.
	if !strings.Contains(dump[0], "entry-10") {
		t.Errorf("oldest retained entry = %q, want entry-10", dump[0])
	}
	newest := "entry-" + strconv.Itoa(stdLogCaptureMaxEntries+9)
	if got := dump[len(dump)-1]; !strings.Contains(got, newest) {
		t.Errorf("newest entry = %q, want %q", got, newest)
	}
}

// TestStdLogCaptureTruncatesLongLines bounds one captured line.
func TestStdLogCaptureTruncatesLongLines(t *testing.T) {
	c := &stdLogCapture{}
	huge := strings.Repeat("y", stdLogCaptureMaxLineLen*4) + "\n"
	if _, err := c.Write([]byte(huge)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	dump := c.Dump()
	if len(dump) != 1 || len(dump[0]) > stdLogCaptureMaxLineLen {
		t.Fatalf("captured line count/len = %d/%d, want 1 and <= %d",
			len(dump), len(dump[0]), stdLogCaptureMaxLineLen)
	}
}

// TestStdLogCaptureIsConcurrencySafe exercises parallel writers under -race.
func TestStdLogCaptureIsConcurrencySafe(t *testing.T) {
	c := &stdLogCapture{}
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				_, _ = c.Write([]byte("worker log line\n"))
			}
		}()
	}
	wg.Wait()
	// A bounded ring retains exactly its cap under sustained writes.
	if got := len(c.Dump()); got != stdLogCaptureMaxEntries {
		t.Fatalf("captured %d entries, want the ring cap %d (writes beyond it evict, never block)",
			got, stdLogCaptureMaxEntries)
	}
}

// TestInstallStdLogCaptureRedirectsStandardLogger is the altscreen guard:
// while installed, standard-logger output lands in the in-memory ring and
// never reaches os.Stdout/os.Stderr — even when a background engine logs
// mid-frame through the global logger.
func TestInstallStdLogCaptureRedirectsStandardLogger(t *testing.T) {
	restore := installStdLogCapture()
	defer restore()

	log.Printf("[boundary2] decomposition unavailable: %v — falling back to explicit re-scope", "synthetic ceiling error")

	found := false
	for _, line := range stdLogCaptureDump() {
		if strings.Contains(line, "[boundary2] decomposition unavailable") &&
			strings.Contains(line, "falling back to explicit re-scope") {
			found = true
		}
	}
	if !found {
		t.Fatalf("standard-logger output not captured in ring buffer:\n%s",
			strings.Join(stdLogCaptureDump(), "\n"))
	}
}

// TestRestoreReattachesPreviousWriter verifies the restore closure puts the
// original writer back in place so post-run diagnostics reach stderr again.
func TestRestoreReattachesPreviousWriter(t *testing.T) {
	sentinel := &stdLogCapture{}
	prev := log.Writer()
	log.SetOutput(sentinel)
	restore := installStdLogCapture()
	log.Printf("captured while active")
	restore()
	defer func() { log.SetOutput(prev) }()
	log.Printf("back on sentinel")

	// Post-restore output must reach the sentinel again — and ONLY the
	// post-restore line (the captured one stayed in the ring).
	lines := sentinel.Dump()
	if len(lines) != 1 || !strings.Contains(lines[0], "back on sentinel") {
		t.Fatalf("sentinel entries after restore = %+v, want exactly the post-restore line", lines)
	}
}
