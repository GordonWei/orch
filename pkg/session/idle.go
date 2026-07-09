package session

import (
	"sync"
	"time"
)

// IdleDetector monitors output activity and determines when a backend
// has finished producing output (stdout silent for a configured duration).
type IdleDetector struct {
	timeout  time.Duration
	mu       sync.Mutex
	lastPing time.Time
	timer    *time.Timer
	idleCh   chan struct{}
	fired    bool
}

// NewIdleDetector creates an idle detector with the given timeout.
// The detector starts in "active" state — call Ping() when output arrives,
// and the idle channel fires after `timeout` of silence.
func NewIdleDetector(timeout time.Duration) *IdleDetector {
	d := &IdleDetector{
		timeout:  timeout,
		lastPing: time.Now(),
		idleCh:   make(chan struct{}),
	}
	d.timer = time.AfterFunc(timeout, d.onIdle)
	// Stop immediately — we don't start counting until Reset() is called
	d.timer.Stop()
	return d
}

// Ping signals that new output was received. Resets the idle timer.
func (d *IdleDetector) Ping() {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.lastPing = time.Now()
	if d.fired {
		return // already signaled, don't restart
	}
	d.timer.Reset(d.timeout)
}

// Reset prepares the detector for a new send/read cycle.
// It resets the idle state and starts the timer.
func (d *IdleDetector) Reset() {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.fired = false
	d.idleCh = make(chan struct{})
	d.lastPing = time.Now()
	d.timer.Reset(d.timeout)
}

// Wait returns a channel that is closed when idle is detected.
func (d *IdleDetector) Wait() <-chan struct{} {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.idleCh
}

// IsIdle returns true if the detector has fired (no output for timeout duration).
func (d *IdleDetector) IsIdle() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.fired
}

// SilentFor returns how long since the last output was received.
func (d *IdleDetector) SilentFor() time.Duration {
	d.mu.Lock()
	defer d.mu.Unlock()
	return time.Since(d.lastPing)
}

func (d *IdleDetector) onIdle() {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.fired {
		return
	}
	d.fired = true
	close(d.idleCh)
}
