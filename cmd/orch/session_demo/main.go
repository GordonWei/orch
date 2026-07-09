// Demo: validates pkg/session end-to-end with a real backend.
// Usage: go run ./cmd/orch/session_demo [claude|kiro]
package main

import (
	"bufio"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gordonwei/orch/pkg/session"
)

func main() {
	backend := session.BackendClaude
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "kiro":
			backend = session.BackendKiro
		case "claude":
			backend = session.BackendClaude
		default:
			fmt.Fprintf(os.Stderr, "usage: session_demo [claude|kiro]\n")
			os.Exit(1)
		}
	}

	cfg := session.DefaultConfig(backend)
	fmt.Fprintf(os.Stderr, "🔧 spawning %s session (idle timeout: %s)...\n", cfg.Backend, cfg.IdleTime)

	sess, err := session.Spawn(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ spawn failed: %v\n", err)
		os.Exit(1)
	}

	// Handle ctrl+c
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Fprintf(os.Stderr, "\n🛑 caught signal, killing session...\n")
		sess.Kill()
	}()

	// Wait for backend to boot
	fmt.Fprintf(os.Stderr, "⏳ waiting for backend to initialize...\n")
	time.Sleep(3 * time.Second)
	fmt.Fprintf(os.Stderr, "✅ session ready. Type messages (ctrl+d to quit):\n")
	fmt.Fprintf(os.Stderr, "─────────────────────────────────────────────────\n")

	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Fprintf(os.Stderr, "\n[you] > ")
		if !scanner.Scan() {
			break
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		if err := sess.Send(line); err != nil {
			fmt.Fprintf(os.Stderr, "❌ send error: %v\n", err)
			break
		}

		fmt.Fprintf(os.Stderr, "⏳ waiting for response...\n")
		start := time.Now()
		output, err := sess.Read()
		elapsed := time.Since(start)

		if err != nil {
			fmt.Fprintf(os.Stderr, "❌ read error: %v\n", err)
			break
		}

		fmt.Fprintf(os.Stdout, "\n%s\n", output)
		fmt.Fprintf(os.Stderr, "── response took %s (idle detected) ──\n", elapsed.Round(time.Millisecond))

		if !sess.Alive() {
			fmt.Fprintf(os.Stderr, "⚠️  session exited unexpectedly\n")
			break
		}
	}

	fmt.Fprintf(os.Stderr, "\n🔧 killing session...\n")
	sess.Kill()
	fmt.Fprintf(os.Stderr, "👋 done\n")
}
