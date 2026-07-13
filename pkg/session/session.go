// Package session manages PTY-based interactive sessions with AI CLI backends.
// It provides spawn, send, read, kill, and idle-detection capabilities.
package session

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// Backend represents a supported AI CLI backend.
type Backend string

const (
	BackendClaude Backend = "claude"
	BackendKiro   Backend = "kiro"
	BackendGemini Backend = "gemini"
)

// Config holds session configuration.
type Config struct {
	Backend  Backend
	WorkDir  string
	Cols     uint16
	Rows     uint16
	IdleTime time.Duration // how long stdout must be silent to consider "idle"
}

// DefaultConfig returns sensible defaults.
func DefaultConfig(backend Backend) Config {
	return Config{
		Backend:  backend,
		Cols:     132,
		Rows:     40,
		IdleTime: 5 * time.Second,
	}
}

// Session is a live PTY-backed interactive session with an AI CLI.
type Session struct {
	cfg      Config
	cmd      *exec.Cmd
	ptmx     *os.File
	idle     *IdleDetector
	output   strings.Builder
	mu       sync.Mutex
	done     chan struct{}
	started  bool
	exited   bool
	streamCh chan string // current streaming channel; nil when not active
}

// Spawn creates and starts a new interactive session.
func Spawn(cfg Config) (*Session, error) {
	var cmd *exec.Cmd
	switch cfg.Backend {
	case BackendClaude:
		cmd = exec.Command("claude", "--dangerously-skip-permissions")
	case BackendKiro:
		cmd = exec.Command("kiro-cli", "chat", "--trust-all-tools")
	case BackendGemini:
		cmd = exec.Command("gemini", "--skip-trust", "--yolo")
	default:
		return nil, fmt.Errorf("unsupported backend: %s", cfg.Backend)
	}

	if cfg.WorkDir != "" {
		cmd.Dir = cfg.WorkDir
	}
	if cfg.Cols == 0 {
		cfg.Cols = 132
	}
	if cfg.Rows == 0 {
		cfg.Rows = 40
	}
	if cfg.IdleTime == 0 {
		cfg.IdleTime = 5 * time.Second
	}

	ptmx, tty, err := openPTY()
	if err != nil {
		return nil, fmt.Errorf("open pty: %w", err)
	}

	setWinSize(ptmx, cfg.Cols, cfg.Rows)

	cmd.Stdin = tty
	cmd.Stdout = tty
	cmd.Stderr = tty
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid:  true,
		Setctty: true,
	}

	if err := cmd.Start(); err != nil {
		ptmx.Close()
		tty.Close()
		return nil, fmt.Errorf("start %s: %w", cfg.Backend, err)
	}

	// Child owns the tty slave now
	tty.Close()

	s := &Session{
		cfg:  cfg,
		cmd:  cmd,
		ptmx: ptmx,
		idle: NewIdleDetector(cfg.IdleTime),
		done: make(chan struct{}),
	}

	// Register idle callback to close the stream channel when backend goes idle
	s.idle.onIdleFunc = func() {
		s.mu.Lock()
		if s.streamCh != nil {
			close(s.streamCh)
			s.streamCh = nil
		}
		s.mu.Unlock()
	}

	// Start reading output in background
	go s.readLoop()
	s.started = true

	return s, nil
}

// Send writes input to the session's stdin (appends \r to simulate Enter).
func (s *Session) Send(input string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.exited {
		return fmt.Errorf("session already exited")
	}

	// Close old stream channel if still open
	if s.streamCh != nil {
		close(s.streamCh)
		s.streamCh = nil
	}

	// Reset idle detector and clear output buffer before sending
	s.idle.Reset()
	s.output.Reset()

	// Create a new stream channel for this send/read cycle
	s.streamCh = make(chan string, 64)

	_, err := s.ptmx.Write([]byte(input + "\r"))
	return err
}

// ReadStream returns a channel that emits output chunks as they arrive.
// The channel is closed when the backend becomes idle or the session exits.
// Each chunk is already ANSI-stripped.
func (s *Session) ReadStream() <-chan string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.streamCh == nil {
		// No active stream; return a closed channel so callers don't block.
		ch := make(chan string)
		close(ch)
		return ch
	}
	return s.streamCh
}

// Read returns all buffered output since the last Send, blocking until idle.
// It waits for the backend to become idle (stdout silent for IdleTime).
func (s *Session) Read() (string, error) {
	// Drain the stream channel to collect all output
	ch := s.ReadStream()
	var buf strings.Builder
	for chunk := range ch {
		buf.WriteString(chunk)
	}

	// Also include anything remaining in the output buffer (from before
	// streamCh existed or edge-case timing)
	s.mu.Lock()
	remaining := s.output.String()
	s.mu.Unlock()

	if remaining != "" && buf.Len() == 0 {
		return remaining, nil
	}
	return buf.String(), nil
}

// ReadRaw returns whatever is currently in the output buffer without waiting.
func (s *Session) ReadRaw() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.output.String()
}

// IsIdle returns true if the backend hasn't produced output for IdleTime.
func (s *Session) IsIdle() bool {
	return s.idle.IsIdle()
}

// Kill terminates the session immediately.
func (s *Session) Kill() error {
	s.mu.Lock()
	exited := s.exited
	s.mu.Unlock()

	if exited {
		return nil
	}

	// Try graceful exit first
	exitCmd := "/exit\r"
	if s.cfg.Backend == BackendKiro {
		exitCmd = "/quit\r"
	} else if s.cfg.Backend == BackendGemini {
		exitCmd = "/quit\r"
	}
	s.ptmx.Write([]byte(exitCmd))

	// Give it 3 seconds to exit gracefully. Note: s.mu must NOT be held
	// while waiting on s.done — readLoop's own cleanup (which closes
	// s.done) needs to acquire s.mu first, so holding it here would
	// deadlock against the very signal we're waiting for.
	select {
	case <-s.done:
		return nil
	case <-time.After(3 * time.Second):
	}

	// Force kill
	if s.cmd.Process != nil {
		s.cmd.Process.Kill()
	}

	<-s.done
	return nil
}

// Done returns a channel that is closed when the session exits.
func (s *Session) Done() <-chan struct{} {
	return s.done
}

// Alive returns true if the session process is still running.
func (s *Session) Alive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.exited
}

// readLoop continuously reads PTY output and feeds idle detector.
func (s *Session) readLoop() {
	defer func() {
		s.mu.Lock()
		s.exited = true
		// Close the stream channel on exit so readers unblock
		if s.streamCh != nil {
			close(s.streamCh)
			s.streamCh = nil
		}
		s.mu.Unlock()
		close(s.done)
	}()

	stripper := &StripState{}
	buf := make([]byte, 4096)
	for {
		n, err := s.ptmx.Read(buf)
		if n > 0 {
			cleaned := stripper.Strip(string(buf[:n]))
			if strings.TrimSpace(cleaned) != "" {
				s.mu.Lock()
				s.output.WriteString(cleaned)
				// Non-blocking send to stream channel
				if s.streamCh != nil {
					select {
					case s.streamCh <- cleaned:
					default:
						// Channel full, drop chunk to avoid blocking readLoop
					}
				}
				s.mu.Unlock()
			}
			// Any PTY activity resets the idle timer, even when the cleaned
			// output is empty (e.g. while the backend is drawing an
			// alt-screen TUI/spinner). If Ping only fired on non-blank
			// output, the idle timer could expire mid-render, Read() would
			// return prematurely, and the real answer written once the TUI
			// exits alt-screen would never be observed (the next Send()
			// resets the output buffer before anyone reads it).
			s.idle.Ping()
		}
		if err != nil {
			if err != io.EOF {
				// PTY closed — process likely exited
			}
			return
		}
	}
}

// --- PTY helpers (macOS native, no third-party deps) ---

func openPTY() (master *os.File, slave *os.File, err error) {
	master, err = os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("open /dev/ptmx: %w", err)
	}

	if err := grantpt(master); err != nil {
		master.Close()
		return nil, nil, fmt.Errorf("grantpt: %w", err)
	}
	if err := unlockpt(master); err != nil {
		master.Close()
		return nil, nil, fmt.Errorf("unlockpt: %w", err)
	}

	slaveName, err := ptsname(master)
	if err != nil {
		master.Close()
		return nil, nil, fmt.Errorf("ptsname: %w", err)
	}

	slave, err = os.OpenFile(slaveName, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		master.Close()
		return nil, nil, fmt.Errorf("open slave %s: %w", slaveName, err)
	}

	return master, slave, nil
}

func grantpt(f *os.File) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), syscall.TIOCPTYGRANT, 0)
	if errno != 0 {
		return errno
	}
	return nil
}

func unlockpt(f *os.File) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), syscall.TIOCPTYUNLK, 0)
	if errno != 0 {
		return errno
	}
	return nil
}

func ptsname(f *os.File) (string, error) {
	var buf [128]byte
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), syscall.TIOCPTYGNAME, uintptr(unsafe.Pointer(&buf[0])))
	if errno != 0 {
		return "", errno
	}
	name := string(buf[:])
	if idx := strings.IndexByte(name, 0); idx >= 0 {
		name = name[:idx]
	}
	return name, nil
}

func setWinSize(f *os.File, cols, rows uint16) {
	ws := struct {
		Row    uint16
		Col    uint16
		Xpixel uint16
		Ypixel uint16
	}{rows, cols, 0, 0}
	syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), syscall.TIOCSWINSZ, uintptr(unsafe.Pointer(&ws)))
}


