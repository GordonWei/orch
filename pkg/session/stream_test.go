package session

import (
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestReadStream_ClosedOnKill(t *testing.T) {
	sess := spawnShell(t, 500*time.Millisecond)
	defer sess.Kill()

	// Send a command so a stream channel is created
	if err := sess.Send("echo hello"); err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	stream := sess.ReadStream()
	if stream == nil {
		t.Fatal("ReadStream returned nil")
	}

	// Kill the session — stream should close
	if err := sess.Kill(); err != nil {
		t.Fatalf("Kill failed: %v", err)
	}

	// The channel must close within a reasonable time
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case _, ok := <-stream:
			if !ok {
				return // Channel closed — expected
			}
			// Got a chunk, keep draining
		case <-timer.C:
			t.Fatal("ReadStream channel was not closed after Kill")
		}
	}
}

func TestReadStream_ClosedOnIdle(t *testing.T) {
	sess := spawnShell(t, 300*time.Millisecond)
	defer sess.Kill()

	// Send a quick command; the shell will respond then go silent
	if err := sess.Send("echo streaming_test"); err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	stream := sess.ReadStream()

	// Drain all chunks — channel should close when idle fires
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case _, ok := <-stream:
			if !ok {
				return // Channel closed — success
			}
		case <-timer.C:
			t.Fatal("ReadStream channel was not closed after idle timeout")
		}
	}
}

func TestReadStream_ResetOnSend(t *testing.T) {
	sess := spawnShell(t, 300*time.Millisecond)
	defer sess.Kill()

	// First send
	if err := sess.Send("echo first"); err != nil {
		t.Fatalf("Send failed: %v", err)
	}
	stream1 := sess.ReadStream()

	// Wait for idle to close stream1
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for {
		select {
		case _, ok := <-stream1:
			if !ok {
				goto secondSend
			}
		case <-timer.C:
			t.Fatal("first stream did not close")
		}
	}

secondSend:
	// Second send should create a new channel
	if err := sess.Send("echo second"); err != nil {
		t.Fatalf("second Send failed: %v", err)
	}
	stream2 := sess.ReadStream()

	// stream2 should eventually close on idle
	timer2 := time.NewTimer(3 * time.Second)
	defer timer2.Stop()
	for {
		select {
		case _, ok := <-stream2:
			if !ok {
				return // success
			}
		case <-timer2.C:
			t.Fatal("second stream did not close")
		}
	}
}

func TestReadStream_NilBeforeSend(t *testing.T) {
	sess := spawnShell(t, 500*time.Millisecond)
	defer sess.Kill()

	// Before any Send, ReadStream should return a closed channel
	stream := sess.ReadStream()
	select {
	case _, ok := <-stream:
		if ok {
			t.Fatal("expected closed channel before any Send, got a value")
		}
		// Channel was closed — expected
	case <-time.After(100 * time.Millisecond):
		t.Fatal("ReadStream channel should be immediately closed before Send")
	}
}

// spawnShell creates a Session backed by /bin/sh for testing purposes.
// This bypasses the backend selection logic and directly spawns a shell,
// reusing the same PTY and readLoop infrastructure.
func spawnShell(t *testing.T, idleTime time.Duration) *Session {
	t.Helper()

	cmd := exec.Command("/bin/sh")

	ptmx, tty, err := openPTY()
	if err != nil {
		t.Fatalf("openPTY: %v", err)
	}

	setWinSize(ptmx, 80, 24)

	cmd.Stdin = tty
	cmd.Stdout = tty
	cmd.Stderr = tty
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid:  true,
		Setctty: true,
	}
	cmd.Env = append(os.Environ(), "PS1=$ ")

	if err := cmd.Start(); err != nil {
		ptmx.Close()
		tty.Close()
		t.Fatalf("start shell: %v", err)
	}
	tty.Close()

	s := &Session{
		cfg: Config{
			Backend:  BackendClaude,
			Cols:     80,
			Rows:     24,
			IdleTime: idleTime,
		},
		cmd:  cmd,
		ptmx: ptmx,
		idle: NewIdleDetector(idleTime),
		done: make(chan struct{}),
		mu:   sync.Mutex{},
	}

	// Register idle callback (same as Spawn does)
	s.idle.onIdleFunc = func() {
		s.mu.Lock()
		if s.streamCh != nil {
			close(s.streamCh)
			s.streamCh = nil
		}
		s.mu.Unlock()
	}

	go s.readLoop()
	s.started = true

	// Wait for initial shell prompt
	time.Sleep(200 * time.Millisecond)

	return s
}

// TestRead_BackwardCompat verifies that the old Read() still works correctly.
func TestRead_BackwardCompat(t *testing.T) {
	sess := spawnShell(t, 300*time.Millisecond)
	defer sess.Kill()

	if err := sess.Send("echo backward_compat_test"); err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	output, err := sess.Read()
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}

	if !strings.Contains(output, "backward_compat_test") {
		t.Fatalf("expected output to contain 'backward_compat_test', got: %q", output)
	}
}
