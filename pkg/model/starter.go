package model

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// ServerStarter handles auto-starting local LLM servers (MLX, Ollama).
type ServerStarter struct {
	backend    string
	pythonPath string
	model      string
	port       string
	endpoint   string
}

type StarterConfig struct {
	Backend    string // "mlx" or "ollama"
	PythonPath string // only for mlx: path to python in mlx-env
	Model      string // model to load
	Port       string // port to serve on
	Endpoint   string // full endpoint URL for health check
}

func NewStarter(cfg StarterConfig) *ServerStarter {
	port := cfg.Port
	if port == "" {
		port = "8080"
	}
	return &ServerStarter{
		backend:    cfg.Backend,
		pythonPath: cfg.PythonPath,
		model:      cfg.Model,
		port:       port,
		endpoint:   cfg.Endpoint,
	}
}

// EnsureRunning checks if the server is already running; if not, starts it.
// Returns nil if server is ready, error if failed to start.
func (s *ServerStarter) EnsureRunning(client LLM) error {
	if isAvailableWithRetry(client) {
		// Server answers /v1/models — but it might be a zombie (up for days,
		// memory collapsed, /v1/models still 200 but inference hangs forever).
		// Do a quick inference probe to confirm it's actually functional.
		if isInferenceHealthy(client) {
			return nil
		}
		// Server is a zombie. Log and try to restart. The port guard in
		// startMLX() will block the spawn, so we try to kill the old one first.
		fmt.Fprintf(os.Stderr, "   ⚠️  MLX server responds to ping but inference is stuck — restarting...\n")
		killMLXByPort(s.port)
		time.Sleep(2 * time.Second)
	}

	switch s.backend {
	case "mlx":
		return s.startMLX()
	case "ollama":
		return s.startOllama()
	default:
		return fmt.Errorf("auto-start not supported for backend %q", s.backend)
	}
}

// isInferenceHealthy does a minimal chat completion call with a very short
// max_tokens to verify the model can actually generate output, not just
// respond to /v1/models. Timeout is deliberately short (10s) — a healthy 7B
// model on Apple Silicon responds to a 1-token request in <1s.
func isInferenceHealthy(client LLM) bool {
	// We need a short-timeout version of the client. The main client's 60s
	// timeout would make a zombie detection take a full minute on every boot.
	// Use a dedicated probe client with 10s timeout.
	oc, ok := client.(*OpenAIClient)
	if !ok {
		// Non-OpenAI client — can't probe, assume healthy
		return true
	}
	probeClient := NewOpenAIClient(OpenAIClientConfig{
		Endpoint: oc.endpoint,
		Model:    oc.model,
		Backend:  oc.backend,
		Timeout:  10 * time.Second,
	})
	result, err := probeClient.Chat([]Message{
		{Role: "user", Content: "hi"},
	}, &ChatOptions{MaxTokens: 1, Temperature: 0})
	return err == nil && result != ""
}

// killMLXByPort attempts to kill any mlx_lm.server process listening on the
// given port. Best-effort — if it fails, the subsequent portInUse check in
// startMLX() will prevent a duplicate spawn anyway.
func killMLXByPort(port string) {
	// Use lsof to find PID listening on the port
	out, err := exec.Command("lsof", "-ti", "tcp:"+port).Output()
	if err != nil || len(out) == 0 {
		return
	}
	// out might have multiple PIDs (one per line); kill them all
	pids := string(out)
	for _, pid := range splitLines(pids) {
		if pid != "" {
			exec.Command("kill", pid).Run()
		}
	}
}

func splitLines(s string) []string {
	var lines []string
	for _, l := range []byte(s) {
		if l == '\n' {
			lines = append(lines, "")
		} else {
			if len(lines) == 0 {
				lines = append(lines, "")
			}
			lines[len(lines)-1] += string(l)
		}
	}
	return lines
}

// isAvailableWithRetry re-checks client.Available() a few times before
// concluding the server is actually down. A single failed check isn't
// reliable enough to decide to spawn a competing process: mlx_lm.server
// handles requests one at a time, so a server that's busy (e.g. mid-generation
// for another orch session) can make a single GET /v1/models fail or time
// out even though it's perfectly healthy.
//
// Observed live (2026-08-01): this false negative repeatedly triggered
// startMLX(), which spawns a redundant mlx_lm.server via the deprecated
// `python -m mlx_lm.server` invocation — it crashes immediately on the port
// already held by the real, healthy server, leaving a startup-crash zombie
// process behind for no benefit every time it happened.
func isAvailableWithRetry(client LLM) bool {
	const attempts = 3
	const delay = 300 * time.Millisecond
	for i := 0; i < attempts; i++ {
		if client.Available() {
			return true
		}
		if i < attempts-1 {
			time.Sleep(delay)
		}
	}
	return false
}

func (s *ServerStarter) startMLX() error {
	if _, err := os.Stat(s.pythonPath); err != nil {
		return fmt.Errorf("mlx python not found at %s: %w", s.pythonPath, err)
	}

	// Check if something is already listening on the port — even if
	// Available() returned false (server might be busy, not dead).
	// Spawning a second server on the same port just creates a zombie.
	if portInUse(s.port) {
		return fmt.Errorf("port %s already in use (MLX server may be busy or starting up) — not spawning a duplicate", s.port)
	}

	fmt.Fprintf(os.Stderr, "🍎 starting MLX server...\n")

	// Use the direct mlx_lm.server entry point (installed by pip as a
	// console_scripts wrapper), NOT `python -m mlx_lm.server` which is
	// deprecated and produces duplicate processes when the real server is
	// already running via the correct entry point.
	mlxServerBin := filepath.Join(filepath.Dir(s.pythonPath), "mlx_lm.server")
	var cmd *exec.Cmd
	if _, err := os.Stat(mlxServerBin); err == nil {
		cmd = exec.Command(mlxServerBin, "--model", s.model, "--port", s.port)
	} else {
		// Fallback: if the entry point script doesn't exist (older install),
		// use python -m. This shouldn't happen with modern mlx-lm installs.
		cmd = exec.Command(s.pythonPath, "-m", "mlx_lm.server", "--model", s.model, "--port", s.port)
	}
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("MLX server start failed: %w", err)
	}

	return s.waitForReady(cmd.Process.Pid, 30*time.Second)
}

func (s *ServerStarter) startOllama() error {
	// Ollama: just need `ollama serve` running, then `ollama run <model>` to preload
	fmt.Fprintf(os.Stderr, "🦙 starting Ollama server...\n")

	cmd := exec.Command("ollama", "serve")
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		// might already be running, that's fine
		return nil
	}

	return s.waitForReady(cmd.Process.Pid, 15*time.Second)
}

func (s *ServerStarter) waitForReady(pid int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)

	// Build a temporary client just for health checking
	checker := NewOpenAIClient(OpenAIClientConfig{
		Endpoint: s.endpoint,
		Model:    s.model,
		Timeout:  5 * time.Second,
	})

	elapsed := 0
	for time.Now().Before(deadline) {
		time.Sleep(1 * time.Second)
		elapsed++
		if checker.Available() {
			fmt.Fprintf(os.Stderr, "\r   ✅ %s server ready (pid %d, %ds)          \n", s.backend, pid, elapsed)
			return nil
		}
		// Progress indicator every second
		fmt.Fprintf(os.Stderr, "\r   ⏳ waiting for %s server... %ds", s.backend, elapsed)
	}

	fmt.Fprintf(os.Stderr, "\n")
	return fmt.Errorf("%s server timeout after %s", s.backend, timeout)
}

// portInUse returns true if something is already listening on localhost:port.
// Used as a guard before spawning a new server to avoid duplicate processes
// fighting for the same port (one will crash immediately but linger as a
// zombie adopted by PID 1).
func portInUse(port string) bool {
	conn, err := net.DialTimeout("tcp", "localhost:"+port, 1*time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}
