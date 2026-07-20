// Package hooks provides a plugin system for executing user-defined shell scripts
// at specific points in the orch lifecycle (pre/post execution, session events, routing).
//
// Hooks receive JSON context via STDIN and communicate results via exit codes:
//   - Exit 0: success, STDOUT captured (injected as context where applicable)
//   - Exit 2: block execution (only when BlockOnFailure=true on pre_route/pre_execute)
//   - Other: warning, STDERR shown to user, execution continues
//
// This is roadmap item #10: "Plugin system (hook 腳本)"
package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/gordonwei/orch/pkg/config"
)

// Trigger defines when a hook fires.
type Trigger string

const (
	PreRoute       Trigger = "pre_route"
	PreExecute     Trigger = "pre_execute"
	PostExecute    Trigger = "post_execute"
	OnSessionStart Trigger = "on_session_start"
	OnSessionEnd   Trigger = "on_session_end"
)

// AllTriggers returns all valid trigger types.
func AllTriggers() []Trigger {
	return []Trigger{PreRoute, PreExecute, PostExecute, OnSessionStart, OnSessionEnd}
}

// HookDef defines a single hook entry in config.yaml.
type HookDef struct {
	Name           string `yaml:"name"`
	Command        string `yaml:"command"`
	Timeout        int    `yaml:"timeout"`           // seconds, default 10
	BlockOnFailure bool   `yaml:"block_on_failure"`  // exit 2 = block (pre_route/pre_execute only)
}

// HooksConfig is the top-level hooks section in config.yaml.
// Maps trigger name → list of hook definitions.
type HooksConfig map[string][]HookDef

// HookEvent is the JSON payload sent to hook scripts via STDIN.
type HookEvent struct {
	Trigger  Trigger           `json:"trigger"`
	Input    string            `json:"input,omitempty"`    // user input (pre_route) or step prompt
	Agent    string            `json:"agent,omitempty"`    // target agent
	StepID   string            `json:"step_id,omitempty"`  // DAG step ID
	Command  string            `json:"command,omitempty"`  // shell command (for shell steps)
	Output   string            `json:"output,omitempty"`   // step output (post_execute only)
	ExitCode int               `json:"exit_code,omitempty"` // step exit code (post_execute only)
	Cwd      string            `json:"cwd,omitempty"`      // current working directory
	Meta     map[string]string `json:"meta,omitempty"`     // arbitrary metadata
}

// HookResult records a single hook execution outcome.
type HookResult struct {
	Name     string
	Stdout   string
	Stderr   string
	ExitCode int
	Blocked  bool          // true if exit==2 && BlockOnFailure
	Err      error         // non-nil if hook failed to start or timed out
	Took     time.Duration
}

// Runner manages and executes hooks.
type Runner struct {
	hooks map[Trigger][]HookDef
}

// NewRunner creates a Runner from the hooks config map.
// Returns nil if no hooks are configured (safe to call methods on nil Runner).
func NewRunner(cfg HooksConfig) *Runner {
	if len(cfg) == 0 {
		return nil
	}

	hooks := make(map[Trigger][]HookDef)
	for triggerStr, defs := range cfg {
		trigger := Trigger(triggerStr)
		for i := range defs {
			if defs[i].Timeout <= 0 {
				defs[i].Timeout = 10
			}
			defs[i].Command = config.ExpandHome(defs[i].Command)
		}
		hooks[trigger] = defs
	}

	return &Runner{hooks: hooks}
}

// HasHooks returns true if hooks are registered for the given trigger.
// Safe to call on nil Runner.
func (r *Runner) HasHooks(trigger Trigger) bool {
	if r == nil {
		return false
	}
	return len(r.hooks[trigger]) > 0
}

// Run executes all hooks for the given trigger sequentially.
// Returns results for each hook. If a hook blocks (exit 2 + BlockOnFailure),
// subsequent hooks for the same trigger are skipped.
// Safe to call on nil Runner (returns nil, nil).
func (r *Runner) Run(ctx context.Context, event HookEvent) ([]HookResult, error) {
	if r == nil {
		return nil, nil
	}

	defs := r.hooks[event.Trigger]
	if len(defs) == 0 {
		return nil, nil
	}

	// Serialize event to JSON for STDIN
	eventJSON, err := json.Marshal(event)
	if err != nil {
		return nil, fmt.Errorf("hooks: failed to marshal event: %w", err)
	}

	var results []HookResult
	for _, def := range defs {
		result := r.runOne(ctx, def, eventJSON)
		results = append(results, result)

		// If blocked, stop processing remaining hooks for this trigger
		if result.Blocked {
			break
		}
	}

	return results, nil
}

// Blocked returns true if any result in the slice has Blocked=true.
func Blocked(results []HookResult) bool {
	for _, r := range results {
		if r.Blocked {
			return true
		}
	}
	return false
}

// BlockReason returns the stderr of the first blocked hook, for user feedback.
func BlockReason(results []HookResult) string {
	for _, r := range results {
		if r.Blocked {
			if r.Stderr != "" {
				return r.Stderr
			}
			return fmt.Sprintf("hook %q blocked execution (exit 2)", r.Name)
		}
	}
	return ""
}

// CombinedStdout joins all successful hook stdout (non-empty) with newlines.
// Useful for injecting hook output as context.
func CombinedStdout(results []HookResult) string {
	var parts []string
	for _, r := range results {
		if r.ExitCode == 0 && r.Stdout != "" {
			parts = append(parts, r.Stdout)
		}
	}
	return strings.Join(parts, "\n")
}

// runOne executes a single hook definition.
func (r *Runner) runOne(ctx context.Context, def HookDef, stdinJSON []byte) HookResult {
	start := time.Now()

	// Create timeout context
	timeout := time.Duration(def.Timeout) * time.Second
	hookCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(hookCtx, "bash", "-c", def.Command)
	cmd.Stdin = bytes.NewReader(stdinJSON)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	took := time.Since(start)

	result := HookResult{
		Name:   def.Name,
		Stdout: strings.TrimSpace(stdout.String()),
		Stderr: strings.TrimSpace(stderr.String()),
		Took:   took,
	}

	if err != nil {
		if hookCtx.Err() == context.DeadlineExceeded {
			result.Err = fmt.Errorf("hook %q timed out after %v", def.Name, timeout)
			result.ExitCode = -1
			return result
		}

		// Extract exit code
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
		} else {
			result.Err = fmt.Errorf("hook %q failed to execute: %w", def.Name, err)
			result.ExitCode = -1
			return result
		}
	}

	// Check if this hook blocks execution
	if result.ExitCode == 2 && def.BlockOnFailure {
		result.Blocked = true
	}

	return result
}
