package execution

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
)

func TestSchedulerJoinBarrierAndOrder(t *testing.T) {
	var mu sync.Mutex
	starts, ends := map[string]time.Time{}, map[string]time.Time{}
	run := func(ctx context.Context, call ai.ToolCall, emit func([]byte)) (ToolResult, error) {
		mu.Lock()
		starts[call.ID] = time.Now()
		mu.Unlock()
		d := 35 * time.Millisecond
		if call.ID == "D" {
			d = 5 * time.Millisecond
		}
		select {
		case <-time.After(d):
		case <-ctx.Done():
			return ToolResult{ID: call.ID}, ctx.Err()
		}
		mu.Lock()
		ends[call.ID] = time.Now()
		mu.Unlock()
		return ToolResult{ID: call.ID, Output: []byte(call.ID)}, nil
	}
	calls := make([]ai.ToolCall, 0, 5)
	for _, id := range []string{"A", "B", "C", "D", "E"} {
		calls = append(calls, ai.ToolCall{ID: id, Function: ai.ToolCallFunction{Name: map[string]string{"D": "write_file"}[id]}})
	}
	for i := range calls {
		if calls[i].Function.Name == "" {
			calls[i].Function.Name = "read_file"
		}
	}
	got, err := NewScheduler(nil).Execute(context.Background(), calls, run)
	if err != nil {
		t.Fatal(err)
	}
	for i, id := range []string{"A", "B", "C", "D", "E"} {
		if got[i].ID != id {
			t.Fatalf("result[%d] = %q, want %q", i, got[i].ID, id)
		}
	}
	for _, id := range []string{"A", "B", "C"} {
		if !starts[id].Before(ends[id]) {
			t.Fatalf("%s did not run", id)
		}
		if !ends[id].Before(starts["D"]) {
			t.Fatalf("D started before %s ended", id)
		}
	}
	if !ends["D"].Before(starts["E"]) {
		t.Fatal("E started before D ended")
	}
	overlapAB := starts["A"].Before(ends["B"]) && starts["B"].Before(ends["A"])
	overlapAC := starts["A"].Before(ends["C"]) && starts["C"].Before(ends["A"])
	if !overlapAB && !overlapAC {
		t.Fatal("read calls did not overlap")
	}
}
