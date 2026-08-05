package control

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PizenLabs/izen/pkg/engine/ir"
)

// TestWorkerPoolBoundedConcurrency verifies the pool never exceeds its limit,
// always returns one observation per item and closes the channel.
func TestWorkerPoolBoundedConcurrency(t *testing.T) {
	const limit = 3
	const total = 12
	var inflight atomic.Int32
	var peak atomic.Int32

	exec := ExecutorFunc(func(ctx context.Context, node *ir.ExecutionNode, vars ir.Variables) (ir.ObservationPayload, error) {
		cur := inflight.Add(1)
		defer inflight.Add(-1)
		for {
			old := peak.Load()
			if cur <= old || peak.CompareAndSwap(old, cur) {
				break
			}
		}
		time.Sleep(2 * time.Millisecond)
		return ir.ObservationPayload{OK: true}, nil
	})

	pool := NewWorkerPool(limit, exec)
	items := make([]WorkItem, total)
	for i := range items {
		items[i] = WorkItem{Node: &ir.ExecutionNode{ID: string(rune('a'+i%26)) + ":" + itoa(i)}}
	}

	got := 0
	for range pool.Submit(context.Background(), items) {
		got++
	}
	if got != total {
		t.Fatalf("observations = %d, want %d", got, total)
	}
	if peak.Load() > limit {
		t.Fatalf("peak concurrency = %d, limit %d", peak.Load(), limit)
	}
}

// TestWorkerPoolPanicRecovery converts a panicking executor into a failed
// observation instead of crashing the process.
func TestWorkerPoolPanicRecovery(t *testing.T) {
	exec := ExecutorFunc(func(ctx context.Context, node *ir.ExecutionNode, vars ir.Variables) (ir.ObservationPayload, error) {
		panic("boom")
	})
	pool := NewWorkerPool(1, exec)
	obs := <-pool.Submit(context.Background(), []WorkItem{{Node: &ir.ExecutionNode{ID: "x"}}})
	if obs.OK || obs.Err == "" {
		t.Fatalf("observation = %+v, want a failed observation with an error", obs)
	}
}

// TestWorkerPoolContextCancellation marks unfinished work as failed.
func TestWorkerPoolContextCancellation(t *testing.T) {
	exec := ExecutorFunc(func(ctx context.Context, node *ir.ExecutionNode, vars ir.Variables) (ir.ObservationPayload, error) {
		select {
		case <-ctx.Done():
			return ir.ObservationPayload{}, ctx.Err()
		case <-time.After(50 * time.Millisecond):
			return ir.ObservationPayload{OK: true}, nil
		}
	})
	pool := NewWorkerPool(1, exec)
	ctx, cancel := context.WithCancel(context.Background())
	ch := pool.Submit(ctx, []WorkItem{{Node: &ir.ExecutionNode{ID: "x"}}})
	cancel()
	obs := <-ch
	if obs.OK {
		t.Fatalf("observation = %+v, want failure after cancellation", obs)
	}
}

// TestWorkerPoolForwardsVariables verifies the dispatch-time variable snapshot
// reaches the executor.
func TestWorkerPoolForwardsVariables(t *testing.T) {
	var gotVars ir.Variables
	exec := ExecutorFunc(func(ctx context.Context, node *ir.ExecutionNode, vars ir.Variables) (ir.ObservationPayload, error) {
		gotVars = vars
		return ir.ObservationPayload{OK: true}, nil
	})
	pool := NewWorkerPool(1, exec)
	<-pool.Submit(context.Background(), []WorkItem{{
		Node: &ir.ExecutionNode{ID: "x"},
		Vars: ir.Variables{"config_path": "cfg/dev.yaml"},
	}})
	if gotVars["config_path"] != "cfg/dev.yaml" {
		t.Fatalf("vars = %v, want config_path forwarded", gotVars)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
