package model

import (
	"fmt"
	"os"
	"os/exec"
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
		return nil
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

	fmt.Fprintf(os.Stderr, "🍎 starting MLX server...\n")
	cmd := exec.Command(s.pythonPath, "-m", "mlx_lm.server", "--model", s.model, "--port", s.port)
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
