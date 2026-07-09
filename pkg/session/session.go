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
	cfg     Config
	cmd     *exec.Cmd
	ptmx    *os.File
	idle    *IdleDetector
	output  strings.Builder
	mu      sync.Mutex
	done    chan struct{}
	started bool
	exited  bool
}

// Spawn creates and starts a new interactive session.
func Spawn(cfg Config) (*Session, error) {
	var cmd *exec.Cmd
	switch cfg.Backend {
	case BackendClaude:
		cmd = exec.Command("claude", "--dangerously-skip-permissions")
	case BackendKiro:
		cmd = exec.Command("kiro-cli", "chat", "--trust-all-tools")
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

	// Reset idle detector and clear output buffer before sending
	s.idle.Reset()
	s.output.Reset()

	_, err := s.ptmx.Write([]byte(input + "\r"))
	return err
}

// Read returns all buffered output since the last Send, blocking until idle.
// It waits for the backend to become idle (stdout silent for IdleTime).
func (s *Session) Read() (string, error) {
	// Wait for idle or session exit
	select {
	case <-s.idle.Wait():
	case <-s.done:
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	result := s.output.String()
	return result, nil
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
	defer s.mu.Unlock()

	if s.exited {
		return nil
	}

	// Try graceful exit first
	exitCmd := "/exit\r"
	if s.cfg.Backend == BackendKiro {
		exitCmd = "/quit\r"
	}
	s.ptmx.Write([]byte(exitCmd))

	// Give it 3 seconds to exit gracefully
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
		s.mu.Unlock()
		close(s.done)
	}()

	buf := make([]byte, 4096)
	for {
		n, err := s.ptmx.Read(buf)
		if n > 0 {
			cleaned := stripANSI(string(buf[:n]))
			if strings.TrimSpace(cleaned) != "" {
				s.mu.Lock()
				s.output.WriteString(cleaned)
				s.mu.Unlock()
				s.idle.Ping()
			}
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

// stripANSI removes ANSI escape sequences and control characters.
func stripANSI(s string) string {
	var result strings.Builder
	i := 0
	for i < len(s) {
		if s[i] == 0x1B {
			i++
			if i < len(s) {
				switch s[i] {
				case '[':
					i++
					for i < len(s) && !(s[i] >= 0x40 && s[i] <= 0x7E) {
						i++
					}
					if i < len(s) {
						i++
					}
				case ']':
					i++
					for i < len(s) && s[i] != 0x07 && !(i+1 < len(s) && s[i] == 0x1B && s[i+1] == '\\') {
						i++
					}
					if i < len(s) {
						if s[i] == 0x07 {
							i++
						} else if i+1 < len(s) {
							i += 2
						}
					}
				case '(', ')', '7', '8', 'c', '>':
					i++
				default:
					i++
				}
			}
		} else if s[i] == '\r' {
			i++
		} else if s[i] < 0x20 && s[i] != '\n' && s[i] != '\t' {
			i++
		} else {
			result.WriteByte(s[i])
			i++
		}
	}
	return result.String()
}
