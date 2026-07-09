package session

import (
	"testing"
	"time"
)

func TestIdleDetector_FiresAfterTimeout(t *testing.T) {
	d := NewIdleDetector(100 * time.Millisecond)
	d.Reset()

	select {
	case <-d.Wait():
		// expected
	case <-time.After(500 * time.Millisecond):
		t.Fatal("idle detector did not fire within expected time")
	}

	if !d.IsIdle() {
		t.Fatal("expected IsIdle() to be true after firing")
	}
}

func TestIdleDetector_PingResetsTimer(t *testing.T) {
	d := NewIdleDetector(100 * time.Millisecond)
	d.Reset()

	// Ping every 50ms for 200ms — should not fire during pings
	for i := 0; i < 4; i++ {
		time.Sleep(50 * time.Millisecond)
		d.Ping()
	}

	if d.IsIdle() {
		t.Fatal("should not be idle while pinging")
	}

	// Now stop pinging — should fire after 100ms
	select {
	case <-d.Wait():
		// expected
	case <-time.After(500 * time.Millisecond):
		t.Fatal("idle detector did not fire after pings stopped")
	}
}

func TestIdleDetector_ResetAfterFired(t *testing.T) {
	d := NewIdleDetector(50 * time.Millisecond)
	d.Reset()

	// Wait for first fire
	<-d.Wait()
	if !d.IsIdle() {
		t.Fatal("expected idle after first fire")
	}

	// Reset for new cycle
	d.Reset()
	if d.IsIdle() {
		t.Fatal("should not be idle immediately after Reset")
	}

	// Should fire again
	select {
	case <-d.Wait():
		// expected
	case <-time.After(500 * time.Millisecond):
		t.Fatal("idle detector did not fire on second cycle")
	}
}

func TestIdleDetector_SilentFor(t *testing.T) {
	d := NewIdleDetector(1 * time.Second)
	d.Reset()
	d.Ping()

	time.Sleep(50 * time.Millisecond)
	sf := d.SilentFor()
	if sf < 40*time.Millisecond || sf > 100*time.Millisecond {
		t.Fatalf("SilentFor() = %s, expected ~50ms", sf)
	}
}
