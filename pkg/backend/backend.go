// Package backend provides a unified interface for AI CLI backends (kiro, claude, gemini).
//
// Each backend adapter wraps the CLI invocation details. The system auto-detects
// available backends at startup and routes tasks to whichever is installed.
package backend

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/gordonwei/orch/pkg/session"
)

// Backend defines the interface for an AI CLI backend.
type Backend interface {
	// Name returns the backend identifier (e.g., "kiro", "claude", "gemini").
	Name() string

	// Available checks if the CLI binary is installed and reachable.
	Available() bool

	// Execute sends a prompt to the backend CLI and returns the output.
	// workDir sets the working directory for the command (empty = inherit).
	Execute(prompt string, workDir string) (string, error)

	// CLIArgs returns the command and arguments used to invoke this backend.
	// Useful for debugging and dry-run display.
	CLIArgs(prompt string) []string
}

// Registry holds all detected backends and manages routing/fallback.
type Registry struct {
	backends map[string]Backend
	primary  string   // user-configured primary backend
	order    []string // detection order for fallback
}

// NewRegistry creates a backend registry, auto-detecting available CLIs.
// primaryOverride: if non-empty, forces primary backend (from config/flag/env).
func NewRegistry(primaryOverride string) *Registry {
	r := &Registry{
		backends: make(map[string]Backend),
		order:    []string{},
	}

	// Register all known backends
	candidates := []Backend{
		&KiroBackend{},
		&ClaudeBackend{},
		&GeminiBackend{},
	}

	for _, b := range candidates {
		if b.Available() {
			r.backends[b.Name()] = b
			r.order = append(r.order, b.Name())
		}
	}

	// Determine primary
	if primaryOverride != "" && r.Has(primaryOverride) {
		r.primary = primaryOverride
	} else if primaryOverride != "" {
		// User specified a backend that's not installed
		fmt.Fprintf(os.Stderr, "⚠️  configured backend %q not found, auto-detecting...\n", primaryOverride)
		r.primary = r.autoSelectPrimary()
	} else {
		r.primary = r.autoSelectPrimary()
	}

	return r
}

// autoSelectPrimary picks the first available backend in preference order.
func (r *Registry) autoSelectPrimary() string {
	// Preference: kiro > claude > gemini (kiro is most versatile for local dev)
	preference := []string{"kiro", "claude", "gemini"}
	for _, name := range preference {
		if r.Has(name) {
			return name
		}
	}
	if len(r.order) > 0 {
		return r.order[0]
	}
	return ""
}

// Primary returns the primary backend. Returns nil if no backend is available.
func (r *Registry) Primary() Backend {
	if r.primary == "" {
		return nil
	}
	return r.backends[r.primary]
}

// PrimaryName returns the name of the primary backend.
func (r *Registry) PrimaryName() string {
	return r.primary
}

// Get returns a specific backend by name, or nil if not available.
func (r *Registry) Get(name string) Backend {
	return r.backends[name]
}

// Has checks if a backend is available.
func (r *Registry) Has(name string) bool {
	_, ok := r.backends[name]
	return ok
}

// Resolve returns the requested backend, falling back to primary if not available.
// This is the core fallback logic: if plan says "claude" but only "kiro" is installed,
// it returns kiro instead of failing.
func (r *Registry) Resolve(requested string) Backend {
	// Normalize name
	requested = normalizeName(requested)

	if b, ok := r.backends[requested]; ok {
		return b
	}
	// Fallback to primary
	return r.Primary()
}

// Available returns all detected backend names.
func (r *Registry) Available() []string {
	return r.order
}

// Summary returns a human-readable summary of available backends.
func (r *Registry) Summary() string {
	if len(r.order) == 0 {
		return "(no AI backends detected)"
	}
	var parts []string
	for _, name := range r.order {
		marker := "  "
		if name == r.primary {
			marker = "→ "
		}
		parts = append(parts, fmt.Sprintf("%s%s", marker, name))
	}
	return fmt.Sprintf("backends: [%s] (primary: %s)", strings.Join(r.order, ", "), r.primary)
}

// normalizeName maps common aliases to canonical backend names.
func normalizeName(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "kiro", "kiro-cli":
		return "kiro"
	case "claude", "claude-code":
		return "claude"
	case "gemini", "gemini-cli":
		return "gemini"
	default:
		return strings.ToLower(strings.TrimSpace(name))
	}
}

// DetectBackends returns a list of available AI CLI backend names.
// Useful for display/init without constructing a full registry.
func DetectBackends() []string {
	candidates := []struct {
		name   string
		binary string
	}{
		{"kiro", "kiro-cli"},
		{"claude", "claude"},
		{"gemini", "gemini"},
	}

	var available []string
	for _, c := range candidates {
		if _, err := exec.LookPath(c.binary); err == nil {
			available = append(available, c.name)
		}
	}
	return available
}

// ===== Kiro Backend =====

// KiroBackend wraps kiro-cli chat invocation.
type KiroBackend struct{}

func (k *KiroBackend) Name() string { return "kiro" }

func (k *KiroBackend) Available() bool {
	_, err := exec.LookPath("kiro-cli")
	return err == nil
}

func (k *KiroBackend) Execute(prompt string, workDir string) (string, error) {
	cmd := exec.Command("kiro-cli", "chat", "--trust-all-tools", prompt)
	out, err := runCmd(cmd, workDir)
	return cleanKiroChatOutput(out), err
}

func (k *KiroBackend) CLIArgs(prompt string) []string {
	return []string{"kiro-cli", "chat", "--trust-all-tools", prompt}
}

// ===== Claude Backend =====

// ClaudeBackend wraps claude -p invocation.
type ClaudeBackend struct{}

func (c *ClaudeBackend) Name() string { return "claude" }

func (c *ClaudeBackend) Available() bool {
	_, err := exec.LookPath("claude")
	return err == nil
}

func (c *ClaudeBackend) Execute(prompt string, workDir string) (string, error) {
	cmd := exec.Command("claude", "-p", "--dangerously-skip-permissions", prompt)
	cmd.Env = append(os.Environ(), "CLAUDE_CODE_ENTRYPOINT=cli")
	return runCmd(cmd, workDir)
}

func (c *ClaudeBackend) CLIArgs(prompt string) []string {
	return []string{"claude", "-p", "--dangerously-skip-permissions", prompt}
}

// ===== Gemini Backend =====

// GeminiBackend wraps gemini CLI invocation.
type GeminiBackend struct{}

func (g *GeminiBackend) Name() string { return "gemini" }

func (g *GeminiBackend) Available() bool {
	_, err := exec.LookPath("gemini")
	return err == nil
}

func (g *GeminiBackend) Execute(prompt string, workDir string) (string, error) {
	cmd := exec.Command("gemini", "--yolo", "-p", prompt)
	return runCmd(cmd, workDir)
}

func (g *GeminiBackend) CLIArgs(prompt string) []string {
	return []string{"gemini", "--yolo", "-p", prompt}
}

// ===== Helpers =====

// defaultTimeout is the maximum duration for a single backend CLI execution.
const defaultTimeout = 5 * time.Minute

func runCmd(cmd *exec.Cmd, workDir string) (string, error) {
	if workDir != "" {
		cmd.Dir = workDir
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("%s failed to start: %w", cmd.Path, err)
	}

	// Wait with timeout
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	timer := time.NewTimer(defaultTimeout)
	defer timer.Stop()

	select {
	case err := <-done:
		out := stripANSI(stdout.String())
		if err != nil {
			return out, fmt.Errorf("%s failed: %w\nstderr: %s",
				cmd.Path, err, stderr.String())
		}
		return out, nil
	case <-timer.C:
		// Timeout: kill the process
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
		return stripANSI(stdout.String()), fmt.Errorf("%s timed out after %s", cmd.Path, defaultTimeout)
	}
}

// stripANSI removes ANSI escape sequences and control characters from CLI
// output captured via exec.Command. Backend CLIs (kiro-cli, claude, gemini)
// are built for a live TTY and print colored prompts/chrome even when their
// stdout is redirected to a pipe; without this, those bytes end up printed
// verbatim as part of the "answer" orch hands back to the user.
func stripANSI(s string) string {
	return (&session.StripState{}).Strip(s)
}

// kiroPromptChromeRe matches kiro-cli's leading colored "> " prompt marker,
// which is plain text ("> ") once stripANSI removes the color codes around
// it. It's chrome meant for a live TTY chat UI, not part of the answer.
var kiroPromptChromeRe = regexp.MustCompile(`^\s*>\s*`)

// cleanKiroChatOutput strips kiro-cli TTY chrome that stripANSI alone can't
// remove: the leading "> " prompt marker described above.
func cleanKiroChatOutput(s string) string {
	s = kiroPromptChromeRe.ReplaceAllString(s, "")
	return strings.TrimSpace(s)
}
