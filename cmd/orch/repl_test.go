package main

import (
	"errors"
	"testing"
	"time"
)

// TestCallReadlineWithTimeout_Recovers verifies the normal path: a readFn
// that returns promptly is passed through unchanged.
func TestCallReadlineWithTimeout_Recovers(t *testing.T) {
	line, err := callReadlineWithTimeout(func() (string, error) {
		return "hello", nil
	}, time.Second)
	if err != nil {
		t.Fatalf("callReadlineWithTimeout returned error: %v", err)
	}
	if line != "hello" {
		t.Errorf("line = %q, want %q", line, "hello")
	}
}

// TestCallReadlineWithTimeout_BoundsAHang is a regression test for a real
// incident (2026-07-31): chzyer/readline's internal Operation.ioloop()
// goroutine can exit permanently on an EOF read and is never restarted. The
// EOF-retry in runREPL calls rl.Readline() again assuming a transient App
// Nap suspension, but if the underlying goroutine is actually dead, that
// call blocks forever on a channel nothing will ever send to again —
// confirmed via a live goroutine dump on a hung session. Simulates that dead
// goroutine with a readFn that blocks on a channel that's never closed, and
// asserts callReadlineWithTimeout returns errReadlineTimeout well within the
// requested bound instead of hanging.
func TestCallReadlineWithTimeout_BoundsAHang(t *testing.T) {
	block := make(chan struct{}) // deliberately never closed

	start := time.Now()
	_, err := callReadlineWithTimeout(func() (string, error) {
		<-block
		return "unreachable", nil
	}, 200*time.Millisecond)
	elapsed := time.Since(start)

	if !errors.Is(err, errReadlineTimeout) {
		t.Fatalf("err = %v, want errReadlineTimeout", err)
	}
	if elapsed >= 2*time.Second {
		t.Errorf("callReadlineWithTimeout took %v — did not bound the hang", elapsed)
	}
}
