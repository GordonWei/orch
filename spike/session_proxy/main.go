// Spike: PTY-based interactive session proxy for AI CLI backends.
// Validates that we can spawn kiro/claude in a PTY, send stdin, and read stdout
// in a bidirectional streaming fashion from Go.
//
// Usage: go run main.go [claude|kiro]
package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

func main() {
	backend := "claude"
	if len(os.Args) > 1 {
		backend = os.Args[1]
	}

	var cmd *exec.Cmd
	switch backend {
	case "claude":
		cmd = exec.Command("claude", "--dangerously-skip-permissions")
	case "kiro":
		cmd = exec.Command("kiro-cli", "chat", "--trust-all-tools")
	default:
		fmt.Fprintf(os.Stderr, "unknown backend: %s (use claude or kiro)\n", backend)
		os.Exit(1)
	}

	// Open PTY
	ptmx, tty, err := openPTY()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to open pty: %v\n", err)
		os.Exit(1)
	}
	defer ptmx.Close()
	defer tty.Close()

	// Set terminal size (so the backend thinks it has a real terminal)
	setWinSize(ptmx, 132, 40)

	// Attach child process to the PTY slave
	cmd.Stdin = tty
	cmd.Stdout = tty
	cmd.Stderr = tty
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid:  true,
		Setctty: true,
	}

	fmt.Fprintf(os.Stderr, "🔧 spawning %s in PTY...\n", backend)

	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "failed to start: %v\n", err)
		os.Exit(1)
	}

	// Close tty in parent (child owns it now)
	tty.Close()

	// Handle SIGINT gracefully
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Goroutine: read PTY output and display (strip ANSI for readability)
	outputDone := make(chan struct{})
	go func() {
		defer close(outputDone)
		buf := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				// Write raw output to stderr for debugging, stripped to stdout
				cleaned := stripANSI(string(buf[:n]))
				if strings.TrimSpace(cleaned) != "" {
					fmt.Fprintf(os.Stdout, "%s", cleaned)
				}
			}
			if err != nil {
				if err != io.EOF {
					fmt.Fprintf(os.Stderr, "\n[pty read error: %v]\n", err)
				}
				return
			}
		}
	}()

	// Wait for backend to initialize
	fmt.Fprintf(os.Stderr, "⏳ waiting for backend to be ready...\n")
	time.Sleep(3 * time.Second)

	// Interactive loop: read user input from real stdin, forward to PTY
	fmt.Fprintf(os.Stderr, "\n✅ session ready. Type messages below (ctrl+d to quit):\n")
	fmt.Fprintf(os.Stderr, "─────────────────────────────────────────────────\n")

	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Fprintf(os.Stderr, "\n[you] > ")
		if !scanner.Scan() {
			break
		}
		line := scanner.Text()
		if line == "" {
			continue
		}

		// Send to PTY (simulates typing + Enter)
		_, err := ptmx.Write([]byte(line + "\r"))
		if err != nil {
			fmt.Fprintf(os.Stderr, "[write error: %v]\n", err)
			break
		}

		// Wait a bit for response
		time.Sleep(8 * time.Second)
	}

	// Cleanup: send exit command
	fmt.Fprintf(os.Stderr, "\n🔧 sending exit command...\n")
	switch backend {
	case "claude":
		ptmx.Write([]byte("/exit\r"))
	case "kiro":
		ptmx.Write([]byte("/quit\r"))
	}

	// Wait for process to exit
	select {
	case <-sigCh:
		cmd.Process.Kill()
	case <-outputDone:
	case <-time.After(10 * time.Second):
		cmd.Process.Kill()
	}

	cmd.Wait()
	fmt.Fprintf(os.Stderr, "👋 session ended\n")
}

// openPTY opens a pseudo-terminal pair (master/slave).
func openPTY() (master *os.File, slave *os.File, err error) {
	// Use posix_openpt via /dev/ptmx
	master, err = os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("open /dev/ptmx: %w", err)
	}

	// Grant and unlock
	if err := grantpt(master); err != nil {
		master.Close()
		return nil, nil, fmt.Errorf("grantpt: %w", err)
	}
	if err := unlockpt(master); err != nil {
		master.Close()
		return nil, nil, fmt.Errorf("unlockpt: %w", err)
	}

	// Get slave name
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
	// On macOS, use TIOCPTYGNAME
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

// setWinSize sets the terminal window size on the PTY master.
func setWinSize(f *os.File, cols, rows uint16) {
	ws := struct {
		Row    uint16
		Col    uint16
		Xpixel uint16
		Ypixel uint16
	}{rows, cols, 0, 0}
	syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), syscall.TIOCSWINSZ, uintptr(unsafe.Pointer(&ws)))
}

// stripANSI removes ANSI escape sequences from text.
func stripANSI(s string) string {
	var result strings.Builder
	i := 0
	for i < len(s) {
		if s[i] == 0x1B { // ESC
			i++
			if i < len(s) {
				switch s[i] {
				case '[': // CSI sequence
					i++
					for i < len(s) && !((s[i] >= 0x40 && s[i] <= 0x7E)) {
						i++
					}
					if i < len(s) {
						i++ // skip final byte
					}
				case ']': // OSC sequence
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
				case '(', ')', '7', '8', 'c', '>': // single char sequences
					i++
				default:
					i++
				}
			}
		} else if s[i] == '\r' {
			i++ // skip CR (keep LF only)
		} else if s[i] < 0x20 && s[i] != '\n' && s[i] != '\t' {
			i++ // skip other control chars
		} else {
			result.WriteByte(s[i])
			i++
		}
	}
	return result.String()
}
