// Package executor provides a parallel DAG execution engine.
//
// Steps follow the DependsOn field to form a directed acyclic graph (DAG).
// Steps with no dependencies start immediately as goroutines in parallel.
// Steps with dependencies wait for all upstream steps to succeed before starting.
package executor

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gordonwei/orch/pkg/apibackend"
	"github.com/gordonwei/orch/pkg/backend"
	"github.com/gordonwei/orch/pkg/config"
	"github.com/gordonwei/orch/pkg/memory"
	"github.com/gordonwei/orch/pkg/planner"
)

// StepResult records a single step's execution result.
type StepResult struct {
	StepID      string
	Description string
	Agent       string
	Output      string
	Err         error
	Took        time.Duration
	Verified    bool
	Files       map[string]string // path → description (files produced by this step)
	KV          map[string]string // structured key-value data for downstream steps
}

// Result records the execution result of an entire Plan.
type Result struct {
	Steps       []StepResult
	Success     bool
	Took        time.Duration
	Err         error
	RePlanCount int
}

// ===== Step Lifecycle Events (EventChan) =====

// EventType defines the type of step lifecycle events.
type EventType int

const (
	// EventStepStart — step starts execution.
	EventStepStart EventType = iota
	// EventStepDone — step execution complete (success).
	EventStepDone
	// EventStepFailed — step execution failed.
	EventStepFailed
	// EventStepSkipped — upstream failed with skip policy; step skipped but downstream can continue.
	EventStepSkipped
	// EventStepCancelled — upstream failed with abort policy; step cancelled.
	EventStepCancelled
)

// StepEvent is a step lifecycle event sent via EventChan.
type StepEvent struct {
	Type   EventType
	StepID string
	Result *StepResult // populated for EventStepDone / EventStepFailed
	Err    error       // cancellation reason for EventStepCancelled
}

// ===== Executor =====

// Executor is the parallel DAG execution engine.
type Executor struct {
	timeout    time.Duration
	maxRetries int
	maxRePlans int
	cfg        *config.Config
	backendReg *backend.Registry
	rePlanFunc func(failedContext string) error // callback to trigger re-plan

	// ApprovalFunc, if set, is called before executing high-risk shell commands.
	// It receives the command string and returns true if the user approves execution.
	// If nil, all commands execute without approval.
	ApprovalFunc func(command string) bool

	// EventChan, if set (non-nil), emits step lifecycle events (start/done/failed) during execution.
	// The caller must create the channel before calling Execute(); Execute() closes it when done.
	EventChan chan StepEvent

	// OutputEvents, if set (non-nil), streams stdout line-by-line for shell commands
	// and emits periodic progress events for AI agent calls.
	// This channel is NOT closed by Executor — the caller manages its lifecycle.
	OutputEvents chan<- OutputEvent

	// APIBackends holds stateless API backends (Bedrock, Vertex AI) keyed by name.
	// If nil or empty, API backend steps will fail with a clear error.
	APIBackends map[string]apibackend.APIBackend

	// MemoryStore, if set, is used to record API usage (token counts + cost).
	MemoryStore *memory.Store
}

// New creates a new Executor instance.
func New(cfg *config.Config, br *backend.Registry) *Executor {
	return &Executor{
		timeout:    10 * time.Minute,
		maxRetries: 3,
		maxRePlans: 2,
		cfg:        cfg,
		backendReg: br,
	}
}

// SetRePlanFunc sets the callback for re-planning when a step fails.
// When a step's on_failure=re-plan, this function is called with the failure context.
func (e *Executor) SetRePlanFunc(fn func(failedContext string) error) {
	e.rePlanFunc = fn
}

// emit safely sends a step lifecycle event to EventChan (if set).
func (e *Executor) emit(ev StepEvent) {
	if e.EventChan != nil {
		e.EventChan <- ev
	}
}

// ===== DAG Construction and Validation =====

// dagNode represents a node in the DAG, containing the step and its dependency relationships.
type dagNode struct {
	step        planner.Step
	upstreams   []string // upstream step IDs this step depends on
	downstreams []string // downstream step IDs that depend on this step
}

// buildDAG builds and validates a DAG from the step list.
func buildDAG(steps []planner.Step) (map[string]*dagNode, error) {
	nodes := make(map[string]*dagNode, len(steps))

	// Create all nodes
	for _, step := range steps {
		if _, exists := nodes[step.ID]; exists {
			return nil, fmt.Errorf("duplicate step ID: %q", step.ID)
		}
		nodes[step.ID] = &dagNode{
			step:      step,
			upstreams: step.DependsOn,
		}
	}

	// Verify dependencies exist and build downstream pointers
	for id, node := range nodes {
		for _, dep := range node.upstreams {
			upstream, exists := nodes[dep]
			if !exists {
				return nil, fmt.Errorf("step %q depends on non-existent step %q", id, dep)
			}
			upstream.downstreams = append(upstream.downstreams, id)
		}
	}

	// Cycle detection (DFS)
	if err := detectCycle(nodes); err != nil {
		return nil, err
	}

	return nodes, nil
}

// detectCycle detects cycles in the DAG using DFS three-color marking.
func detectCycle(nodes map[string]*dagNode) error {
	const (
		white = 0 // unvisited
		gray  = 1 // visiting (on current DFS path)
		black = 2 // completed
	)

	color := make(map[string]int, len(nodes))
	for id := range nodes {
		color[id] = white
	}

	var dfs func(id string) error
	dfs = func(id string) error {
		color[id] = gray
		for _, downstream := range nodes[id].downstreams {
			switch color[downstream] {
			case gray:
				return fmt.Errorf("cycle detected: %s → %s", id, downstream)
			case white:
				if err := dfs(downstream); err != nil {
					return err
				}
			}
		}
		color[id] = black
		return nil
	}

	for id := range nodes {
		if color[id] == white {
			if err := dfs(id); err != nil {
				return err
			}
		}
	}
	return nil
}

// ===== Parallel DAG Execution Engine =====

// Execute executes all steps in the plan, scheduled with DAG parallelism.
//
// Steps with no dependencies start immediately in parallel; steps with dependencies
// wait until all upstream steps succeed before starting.
// If EventChan is set, events are emitted during execution and the channel is closed when done.
func (e *Executor) Execute(plan *planner.Plan) Result {
	start := time.Now()

	// Close EventChan when done (if set)
	if e.EventChan != nil {
		defer close(e.EventChan)
	}

	// Build DAG
	nodes, err := buildDAG(plan.Steps)
	if err != nil {
		return Result{
			Steps:   nil,
			Success: false,
			Took:    time.Since(start),
			Err:     err,
		}
	}

	// Execution context
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var (
		mu         sync.Mutex
		results    = make(map[string]*StepResult)
		completed  = make(map[string]chan struct{}) // closed when each step completes
		wg         sync.WaitGroup
		globalErr  error
		rePlanFlag bool
	)

	// Create completion signal channel for each step
	for id := range nodes {
		completed[id] = make(chan struct{})
	}

	// buildContext collects output from all upstream steps to compose prior context for this step.
	buildContext := func(deps []string) string {
		mu.Lock()
		defer mu.Unlock()

		var buf strings.Builder
		for _, dep := range deps {
			sr := results[dep]
			if sr == nil || sr.Err != nil {
				continue
			}
			if sr.Output != "" {
				buf.WriteString(fmt.Sprintf("\n--- Output from %s ---\n%s\n", dep, truncate(sr.Output, 4000)))
			}
			if len(sr.KV) > 0 {
				buf.WriteString(fmt.Sprintf("--- KV from %s ---\n", dep))
				for k, v := range sr.KV {
					buf.WriteString(fmt.Sprintf("  %s=%s\n", k, v))
				}
			}
			if len(sr.Files) > 0 {
				buf.WriteString(fmt.Sprintf("--- Files from %s ---\n", dep))
				for path, desc := range sr.Files {
					buf.WriteString(fmt.Sprintf("  %s: %s\n", path, desc))
				}
			}
		}
		return buf.String()
	}

	// Launch goroutine for each step
	for _, node := range nodes {
		wg.Add(1)
		go func(n *dagNode) {
			defer wg.Done()
			defer close(completed[n.step.ID])

			// Wait for all upstream steps to complete
			for _, dep := range n.upstreams {
				select {
				case <-completed[dep]:
					// upstream completed
				case <-ctx.Done():
					// global cancellation
					e.emit(StepEvent{Type: EventStepCancelled, StepID: n.step.ID, Err: ctx.Err()})
					mu.Lock()
					results[n.step.ID] = &StepResult{
						StepID:      n.step.ID,
						Description: n.step.Description,
						Agent:       n.step.Agent,
						Err:         fmt.Errorf("cancelled: %w", ctx.Err()),
					}
					mu.Unlock()
					return
				}
			}

			// Check if global context is cancelled
			select {
			case <-ctx.Done():
				e.emit(StepEvent{Type: EventStepCancelled, StepID: n.step.ID, Err: ctx.Err()})
				mu.Lock()
				results[n.step.ID] = &StepResult{
					StepID:      n.step.ID,
					Description: n.step.Description,
					Agent:       n.step.Agent,
					Err:         fmt.Errorf("cancelled: %w", ctx.Err()),
				}
				mu.Unlock()
				return
			default:
			}

			// Check if upstream failed (with non-skip policy)
			mu.Lock()
			upstreamFailed := false
			for _, dep := range n.upstreams {
				sr := results[dep]
				if sr != nil && sr.Err != nil {
					// Check upstream step's on_failure policy
					upNode := nodes[dep]
					if upNode != nil && upNode.step.OnFailure != "skip" {
						upstreamFailed = true
						break
					}
				}
			}
			mu.Unlock()

			if upstreamFailed {
				e.emit(StepEvent{Type: EventStepCancelled, StepID: n.step.ID, Err: fmt.Errorf("upstream failed")})
				mu.Lock()
				results[n.step.ID] = &StepResult{
					StepID:      n.step.ID,
					Description: n.step.Description,
					Agent:       n.step.Agent,
					Err:         fmt.Errorf("cancelled due to upstream failure"),
				}
				mu.Unlock()
				return
			}

			// Build context for this step (from all upstream step outputs)
			priorContext := buildContext(n.upstreams)

			// Emit start event
			fmt.Fprintf(os.Stderr, "\n📋 [%s] %s\n", n.step.ID, n.step.Description)
			fmt.Fprintf(os.Stderr, "   agent: %s\n", n.step.Agent)
			e.emit(StepEvent{Type: EventStepStart, StepID: n.step.ID})

			// Execute step (with retry logic)
			sr := e.executeStep(n.step, priorContext)

			// Store result
			mu.Lock()
			results[n.step.ID] = &sr
			mu.Unlock()

			if sr.Err != nil {
				fmt.Fprintf(os.Stderr, "   ❌ failed: %v\n", sr.Err)
				e.emit(StepEvent{Type: EventStepFailed, StepID: n.step.ID, Result: &sr})

				// Handle according to on_failure policy
				switch n.step.OnFailure {
				case "skip":
					fmt.Fprintf(os.Stderr, "   ⏭️  skipping (on_failure=skip)\n")
					e.emit(StepEvent{Type: EventStepSkipped, StepID: n.step.ID, Result: &sr})
					return

				case "re-plan":
					fmt.Fprintf(os.Stderr, "   🔁 requesting re-plan...\n")
					mu.Lock()
					rePlanFlag = true
					if e.rePlanFunc != nil {
						failCtx := fmt.Sprintf("Step [%s] failed: %v\nPrior context:\n%s", n.step.ID, sr.Err, priorContext)
						_ = e.rePlanFunc(failCtx)
					}
					mu.Unlock()
					cancel()
					return

				case "abort":
					fmt.Fprintf(os.Stderr, "   🛑 aborting all (on_failure=abort)\n")
					mu.Lock()
					globalErr = fmt.Errorf("aborted at step %s: %w", n.step.ID, sr.Err)
					mu.Unlock()
					cancel()
					return

				default: // "retry" — already handled in executeStep; if still failed, abort
					fmt.Fprintf(os.Stderr, "   🛑 all retries exhausted, aborting\n")
					mu.Lock()
					globalErr = fmt.Errorf("step %s failed after retries: %w", n.step.ID, sr.Err)
					mu.Unlock()
					cancel()
					return
				}
			}

			fmt.Fprintf(os.Stderr, "   ✅ done (%s)\n", sr.Took.Round(100*time.Millisecond))
			e.emit(StepEvent{Type: EventStepDone, StepID: n.step.ID, Result: &sr})
		}(node)
	}

	// Wait for all goroutines to complete
	wg.Wait()

	// Collect results (in original step order)
	mu.Lock()
	var orderedResults []StepResult
	allSuccess := true
	for _, step := range plan.Steps {
		if sr, ok := results[step.ID]; ok {
			orderedResults = append(orderedResults, *sr)
			if sr.Err != nil {
				allSuccess = false
			}
		}
	}
	mu.Unlock()

	if rePlanFlag {
		return Result{
			Steps:       orderedResults,
			Success:     false,
			Took:        time.Since(start),
			Err:         fmt.Errorf("re-plan triggered"),
			RePlanCount: 1,
		}
	}

	if globalErr != nil {
		return Result{
			Steps:   orderedResults,
			Success: false,
			Took:    time.Since(start),
			Err:     globalErr,
		}
	}

	return Result{
		Steps:   orderedResults,
		Success: allSuccess,
		Took:    time.Since(start),
	}
}

// ===== Single Step Execution (with retry and verification) =====

// executeStep executes a single step, includes retry logic.
func (e *Executor) executeStep(step planner.Step, priorContext string) StepResult {
	start := time.Now()
	var output string
	var err error

	for attempt := 0; attempt <= e.maxRetries; attempt++ {
		if attempt > 0 {
			fmt.Fprintf(os.Stderr, "   🔄 retry %d/%d\n", attempt, e.maxRetries)
		}

		output, err = e.runStep(step, priorContext)
		if err != nil {
			continue
		}

		// Verify command
		if step.VerifyCmd != "" {
			if verifyErr := e.verify(step.VerifyCmd); verifyErr != nil {
				err = fmt.Errorf("verification failed: %w", verifyErr)
				continue
			}
		}

		// Success
		return StepResult{
			StepID:      step.ID,
			Description: step.Description,
			Agent:       step.Agent,
			Output:      output,
			Took:        time.Since(start),
			Verified:    step.VerifyCmd != "",
			Files:       parseFiles(output),
			KV:          parseKV(output),
		}
	}

	return StepResult{
		StepID:      step.ID,
		Description: step.Description,
		Agent:       step.Agent,
		Output:      output,
		Err:         err,
		Took:        time.Since(start),
	}
}

// runStep executes the concrete command for a step.
// If OutputEvents is set:
//   - shell commands: use StdoutPipe for line-by-line streaming
//   - AI agents (kiro/claude/gemini): emit periodic progress events
//
// If OutputEvents is nil: fully backward-compatible behavior (buffered).
func (e *Executor) runStep(step planner.Step, priorContext string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), e.timeout)
	defer cancel()

	var cmd *exec.Cmd

	switch step.Agent {
	case "kiro", "claude", "gemini":
		prompt := step.Prompt
		if prompt == "" {
			prompt = step.Description
		}
		if priorContext != "" {
			prompt = fmt.Sprintf("Context from prior steps:\n%s\n\nTask: %s", priorContext, prompt)
		}
		// Resolve backend with fallback: if requested agent is unavailable, use primary
		if e.backendReg == nil {
			return "", fmt.Errorf("no AI backend registry configured for agent %q", step.Agent)
		}
		b := e.backendReg.Resolve(step.Agent)
		if b == nil {
			return "", fmt.Errorf("no AI backend available for agent %q", step.Agent)
		}
		workDir := e.findWorkDir(step)
		output, err := b.Execute(prompt, workDir)
		return output, err

	case "bedrock", "vertexai":
		// Stateless API backend path — direct HTTP API call, no PTY session
		ab, ok := e.APIBackends[step.Agent]
		if !ok || ab == nil {
			return "", fmt.Errorf("API backend %q not configured (check api_backends in config.yaml)", step.Agent)
		}
		if !ab.Available() {
			return "", fmt.Errorf("API backend %q credentials not available", step.Agent)
		}

		prompt := step.Prompt
		if prompt == "" {
			prompt = step.Description
		}
		if priorContext != "" {
			prompt = fmt.Sprintf("Context from prior steps:\n%s\n\nTask: %s", priorContext, prompt)
		}

		// Build request — check session_logs for /pass context (multi-turn)
		var messages []apibackend.Message
		if e.MemoryStore != nil {
			// Read recent session_logs for this backend (context passed via /pass)
			logs, err := e.MemoryStore.RecentSessionLogs(step.Agent, 10)
			if err == nil && len(logs) > 0 {
				for _, log := range logs {
					messages = append(messages, apibackend.Message{
						Role:    log.Role,
						Content: log.Content,
					})
				}
			}
		}
		// Append current prompt as the final user message
		messages = append(messages, apibackend.Message{
			Role:    "user",
			Content: prompt,
		})

		req := apibackend.Request{
			Messages:    messages,
			MaxTokens:   4096,
			Temperature: 0.7,
		}

		resp, err := ab.Invoke(ctx, req)
		if err != nil {
			return "", fmt.Errorf("API backend %q invocation failed: %w", step.Agent, err)
		}

		// Persist the response to session_logs (so /pass can read it back later)
		if e.MemoryStore != nil && resp != nil {
			if err := e.MemoryStore.AddSessionLog(step.Agent, "assistant", resp.Content); err != nil {
				fmt.Fprintf(os.Stderr, "⚠️  failed to persist API response to session log: %v\n", err)
			}
		}

		// Record API usage (token count + cost estimation)
		if e.MemoryStore != nil && resp != nil {
			costUSD := estimateCost(step.Agent, resp.Model, resp.InputTokens, resp.OutputTokens)
			preview := TruncateWithSuffix(prompt, 100, "…")
			if err := e.MemoryStore.AddAPIUsage(memory.APIUsageEntry{
				Backend:       step.Agent,
				Model:         resp.Model,
				InputTokens:   resp.InputTokens,
				OutputTokens:  resp.OutputTokens,
				CostUSD:       costUSD,
				LatencyMs:     resp.Latency.Milliseconds(),
				PromptPreview: preview,
			}); err != nil {
				fmt.Fprintf(os.Stderr, "⚠️  failed to record API usage: %v\n", err)
			}
		}

		return resp.Content, nil

	case "shell":
		if step.Command == "" {
			return "", fmt.Errorf("shell step has no command")
		}
		// Approval gate: check if command is high-risk
		if e.ApprovalFunc != nil && e.isHighRiskCommand(step.Command) {
			if !e.ApprovalFunc(step.Command) {
				return "", fmt.Errorf("user denied execution of high-risk command")
			}
		}
		cmd = exec.CommandContext(ctx, "bash", "-c", step.Command)

	default:
		// Run directly as shell command (terraform, kubectl, helm, aws, gcloud)
		if step.Command != "" {
			// Approval gate: check if command is high-risk
			if e.ApprovalFunc != nil && e.isHighRiskCommand(step.Command) {
				if !e.ApprovalFunc(step.Command) {
					return "", fmt.Errorf("user denied execution of high-risk command")
				}
			}
			cmd = exec.CommandContext(ctx, "bash", "-c", step.Command)
		} else if step.Prompt != "" {
			// No command but has prompt — delegate to primary backend
			prompt := step.Prompt
			if priorContext != "" {
				prompt = fmt.Sprintf("Context from prior steps:\n%s\n\nTask: %s", priorContext, prompt)
			}
			if e.backendReg == nil {
				return "", fmt.Errorf("no AI backend registry configured for agent %q", step.Agent)
			}
			b := e.backendReg.Primary()
			if b == nil {
				return "", fmt.Errorf("no AI backend available for agent %q", step.Agent)
			}
			workDir := e.findWorkDir(step)
			return b.Execute(prompt, workDir)
		} else {
			return "", fmt.Errorf("agent %q has no command or prompt", step.Agent)
		}
	}

	cmd.Dir = e.findWorkDir(step)

	// Choose execution strategy based on agent type and whether OutputEvents is enabled
	isShellLike := step.Agent == "shell" || step.Command != ""

	if e.OutputEvents != nil && isShellLike {
		// Shell commands: use StdoutPipe for line-by-line streaming
		return e.runShellStreaming(cmd, step)
	}

	if e.OutputEvents != nil && !isShellLike {
		// AI agent calls: emit periodic progress events
		return e.runWithProgress(cmd, step)
	}

	// No streaming (backward-compatible): buffer all output
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("%s failed: %w\nstderr: %s", step.Agent, err, stderr.String())
	}

	return stdout.String(), nil
}

// runShellStreaming uses StdoutPipe for line-by-line streaming of shell command output,
// while retaining the full output for downstream context chaining.
func (e *Executor) runShellStreaming(cmd *exec.Cmd, step planner.Step) (string, error) {
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	// Get stdout pipe
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		// fallback: revert to buffered mode
		var stdout bytes.Buffer
		cmd.Stdout = &stdout
		if runErr := cmd.Run(); runErr != nil {
			return stdout.String(), fmt.Errorf("%s failed: %w\nstderr: %s", step.Agent, runErr, stderr.String())
		}
		return stdout.String(), nil
	}

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("%s failed to start: %w", step.Agent, err)
	}

	// Read stdout line-by-line, emit OutputEvent, and collect full output
	var stdout bytes.Buffer
	streamErr := StreamReader(pipe, &stdout, e.OutputEvents, step.ID)

	// Wait for command to finish
	cmdErr := cmd.Wait()

	if streamErr != nil {
		return stdout.String(), fmt.Errorf("stream read error: %w", streamErr)
	}
	if cmdErr != nil {
		return stdout.String(), fmt.Errorf("%s failed: %w\nstderr: %s", step.Agent, cmdErr, stderr.String())
	}

	return stdout.String(), nil
}

// runWithProgress is used for AI agent calls (kiro/claude/gemini).
// Since their output cannot be streamed in real-time, periodic progress events are emitted instead.
func (e *Executor) runWithProgress(cmd *exec.Cmd, step planner.Step) (string, error) {
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("%s failed to start: %w", step.Agent, err)
	}

	// Background goroutine emits periodic progress events
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		elapsed := 0
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				elapsed += 5
				EmitProgress(e.OutputEvents, step.ID,
					fmt.Sprintf("%s executing... (%ds)", step.Agent, elapsed))
			}
		}
	}()

	err := cmd.Wait()
	close(done)

	if err != nil {
		return stdout.String(), fmt.Errorf("%s failed: %w\nstderr: %s", step.Agent, err, stderr.String())
	}

	return stdout.String(), nil
}

// verify executes the step verification command.
func (e *Executor) verify(verifyCmd string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bash", "-c", verifyCmd)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("verify cmd failed: %w\n%s", err, stderr.String())
	}
	return nil
}

// ===== Helper Functions =====

// findWorkDir determines the working directory based on step description, command, etc.
func (e *Executor) findWorkDir(step planner.Step) string {
	text := strings.ToLower(step.Description + " " + step.Prompt + " " + step.Command)

	base := e.cfg.Workspace.Root
	if base == "" {
		base = "."
	}

	for _, sd := range e.cfg.Workspace.Subdirs {
		for _, kw := range sd.Keywords {
			if strings.Contains(text, strings.ToLower(kw)) {
				fullPath := filepath.Join(base, sd.Name)
				if info, err := os.Stat(fullPath); err == nil && info.IsDir() {
					return fullPath
				}
			}
		}
	}

	return ""
}

// truncate truncates an overly long string.
// TruncateWithSuffix trims s to at most max bytes, cutting on a UTF-8 rune
// boundary so multi-byte characters (e.g. Traditional Chinese) are never
// split into invalid UTF-8, then appends suffix. Exported so callers outside
// this package (e.g. cmd/orch) can reuse the same safe truncation instead of
// each slicing strings by byte index independently.
func TruncateWithSuffix(s string, max int, suffix string) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + suffix
}

// Truncate is TruncateWithSuffix with this package's conventional
// "\n... (truncated)" marker.
func Truncate(s string, max int) string {
	return TruncateWithSuffix(s, max, "\n... (truncated)")
}

// truncate is the package-internal alias for Truncate, kept so existing
// call sites within this package don't need the package-qualified name.
func truncate(s string, max int) string {
	return Truncate(s, max)
}

// parseFiles extracts file declarations from step output.
// Format: lines starting with "FILE:" containing "path|description".
func parseFiles(output string) map[string]string {
	files := make(map[string]string)
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "FILE:") {
			parts := strings.SplitN(strings.TrimPrefix(line, "FILE:"), "|", 2)
			if len(parts) == 2 {
				files[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
			} else if len(parts) == 1 {
				files[strings.TrimSpace(parts[0])] = ""
			}
		}
	}
	if len(files) == 0 {
		return nil
	}
	return files
}

// parseKV extracts key-value pairs from step output.
// Format: lines starting with "KV:" containing "key=value".
func parseKV(output string) map[string]string {
	kv := make(map[string]string)
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "KV:") {
			parts := strings.SplitN(strings.TrimPrefix(line, "KV:"), "=", 2)
			if len(parts) == 2 {
				kv[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
			}
		}
	}
	if len(kv) == 0 {
		return nil
	}
	return kv
}

// ===== Approval Gate =====

// isHighRiskCommand checks whether a command matches any high-risk pattern.
// The pattern list comes from e.cfg.HighRiskPatterns (config-driven, see
// pkg/config.Config.HighRiskPatterns) instead of a hardcoded package-level
// list, so users can add/replace patterns via config.yaml without a rebuild
// — the same config-driven approach pkg/router already uses for route rules.
// Falls back to config.DefaultHighRiskPatterns() if cfg or the list is unset
// (e.g. an Executor built without going through config.Load()).
func (e *Executor) isHighRiskCommand(command string) bool {
	patterns := config.DefaultHighRiskPatterns()
	if e.cfg != nil && len(e.cfg.HighRiskPatterns) > 0 {
		patterns = e.cfg.HighRiskPatterns
	}

	lower := strings.ToLower(command)
	for _, pattern := range patterns {
		if strings.Contains(lower, pattern) {
			return true
		}
	}
	return false
}

// ===== Cost Estimation =====

// estimateCost calculates estimated USD cost based on backend/model pricing.
// Prices are per 1M tokens as of 2024 public pricing.
func estimateCost(backend, model string, inputTokens, outputTokens int) float64 {
	type pricing struct {
		inputPer1M  float64
		outputPer1M float64
	}

	// Known pricing (per 1M tokens, USD)
	prices := map[string]pricing{
		// Bedrock — Anthropic Claude models (both direct model ID and inference profile ID)
		"us.anthropic.claude-sonnet-4-20250514-v1:0":    {3.0, 15.0},
		"anthropic.claude-sonnet-4-20250514-v1:0":       {3.0, 15.0},
		"us.anthropic.claude-3-5-sonnet-20241022-v2:0":  {3.0, 15.0},
		"anthropic.claude-3-5-sonnet-20241022-v2:0":     {3.0, 15.0},
		"us.anthropic.claude-3-5-haiku-20241022-v1:0":   {0.8, 4.0},
		"anthropic.claude-3-5-haiku-20241022-v1:0":      {0.8, 4.0},
		"anthropic.claude-3-haiku-20240307-v1:0":        {0.25, 1.25},
		"anthropic.claude-3-sonnet-20240229-v1:0":       {3.0, 15.0},
		"anthropic.claude-3-opus-20240229-v1:0":         {15.0, 75.0},
		// Bedrock — Amazon Nova models
		"us.amazon.nova-pro-v1:0":   {0.8, 3.2},
		"amazon.nova-pro-v1:0":      {0.8, 3.2},
		"us.amazon.nova-lite-v1:0":  {0.06, 0.24},
		"amazon.nova-lite-v1:0":     {0.06, 0.24},
		"us.amazon.nova-micro-v1:0": {0.035, 0.14},
		"amazon.nova-micro-v1:0":    {0.035, 0.14},
		// Vertex AI — Gemini models
		"gemini-2.0-flash":     {0.075, 0.30},
		"gemini-2.0-flash-001": {0.075, 0.30},
		"gemini-1.5-pro":       {1.25, 5.0},
		"gemini-1.5-flash":     {0.075, 0.30},
		"gemini-1.5-flash-002": {0.075, 0.30},
	}

	p, ok := prices[model]
	if !ok {
		// Fallback: use conservative estimate based on backend
		switch backend {
		case "bedrock":
			p = pricing{3.0, 15.0} // assume Claude 3.5 Sonnet tier
		case "vertexai":
			p = pricing{0.075, 0.30} // assume Gemini Flash tier
		default:
			p = pricing{1.0, 5.0} // generic fallback
		}
	}

	cost := (float64(inputTokens) * p.inputPer1M / 1_000_000) +
		(float64(outputTokens) * p.outputPer1M / 1_000_000)
	return cost
}
