package executor

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/gordonwei/orch/pkg/config"
	"github.com/gordonwei/orch/pkg/planner"
)

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

type Result struct {
	Steps    []StepResult
	Success  bool
	Took     time.Duration
	Err      error
	RePlanCount int
}

type Executor struct {
	timeout      time.Duration
	maxRetries   int
	maxRePlans   int
	cfg          *config.Config
	rePlanFunc   func(failedContext string) error // callback to trigger re-plan
}

func New(cfg *config.Config) *Executor {
	return &Executor{
		timeout:    10 * time.Minute,
		maxRetries: 3,
		maxRePlans: 2,
		cfg:        cfg,
	}
}

// SetRePlanFunc sets the callback that is invoked when a step fails and has on_failure=re-plan.
// The callback receives the failure context and should re-invoke the planner.
func (e *Executor) SetRePlanFunc(fn func(failedContext string) error) {
	e.rePlanFunc = fn
}

func (e *Executor) Execute(plan *planner.Plan) Result {
	start := time.Now()

	// 驗證 DependsOn ordering：確保被依賴的 step 排在前面
	if err := validateStepOrder(plan.Steps); err != nil {
		return Result{
			Steps:   nil,
			Success: false,
			Took:    time.Since(start),
			Err:     err,
		}
	}

	var results []StepResult
	contextChain := "" // 前步 output 串接

	allSuccess := true
	for _, step := range plan.Steps {
		fmt.Fprintf(os.Stderr, "\n📋 [%s] %s\n", step.ID, step.Description)
		fmt.Fprintf(os.Stderr, "   agent: %s\n", step.Agent)

		sr := e.executeStep(step, contextChain)
		results = append(results, sr)

		if sr.Err != nil {
			fmt.Fprintf(os.Stderr, "   ❌ failed: %v\n", sr.Err)

			// 根據 on_failure 策略決定下一步
			switch step.OnFailure {
			case "skip":
				fmt.Fprintf(os.Stderr, "   ⏭️  skipping (on_failure=skip)\n")
				continue
			case "re-plan":
				fmt.Fprintf(os.Stderr, "   🔁 requesting re-plan...\n")
				if e.rePlanFunc != nil {
					failCtx := fmt.Sprintf("Step [%s] failed: %v\nPrior context:\n%s", step.ID, sr.Err, contextChain)
					if err := e.rePlanFunc(failCtx); err != nil {
						fmt.Fprintf(os.Stderr, "   ⚠️  re-plan failed: %v\n", err)
					}
				}
				allSuccess = false
				return Result{
					Steps:       results,
					Success:     false,
					Took:        time.Since(start),
					Err:         fmt.Errorf("re-plan triggered at step %s", step.ID),
					RePlanCount: 1,
				}
			case "abort":
				allSuccess = false
				break
			default: // "retry" is already handled in executeStep
				allSuccess = false
				break
			}
			break
		}

		fmt.Fprintf(os.Stderr, "   ✅ done (%s)\n", sr.Took.Round(100*time.Millisecond))

		// 把 output 加入 context chain 給下一步用
		if sr.Output != "" {
			contextChain += fmt.Sprintf("\n--- Output from %s ---\n%s\n", step.ID, truncate(sr.Output, 4000))
		}
		if len(sr.KV) > 0 {
			contextChain += fmt.Sprintf("--- KV from %s ---\n", step.ID)
			for k, v := range sr.KV {
				contextChain += fmt.Sprintf("  %s=%s\n", k, v)
			}
		}
		if len(sr.Files) > 0 {
			contextChain += fmt.Sprintf("--- Files from %s ---\n", step.ID)
			for path, desc := range sr.Files {
				contextChain += fmt.Sprintf("  %s: %s\n", path, desc)
			}
		}
	}

	return Result{
		Steps:   results,
		Success: allSuccess,
		Took:    time.Since(start),
	}
}

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

		// 驗證
		if step.VerifyCmd != "" {
			if verifyErr := e.verify(step.VerifyCmd); verifyErr != nil {
				err = fmt.Errorf("verification failed: %w", verifyErr)
				continue
			}
		}

		// 成功
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

func (e *Executor) runStep(step planner.Step, priorContext string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), e.timeout)
	defer cancel()

	var cmd *exec.Cmd

	switch step.Agent {
	case "kiro":
		prompt := step.Prompt
		if prompt == "" {
			prompt = step.Description
		}
		if priorContext != "" {
			prompt = fmt.Sprintf("Context from prior steps:\n%s\n\nTask: %s", priorContext, prompt)
		}
		cmd = exec.CommandContext(ctx, "kiro-cli", "chat", "--trust-all-tools", prompt)

	case "claude":
		prompt := step.Prompt
		if prompt == "" {
			prompt = step.Description
		}
		if priorContext != "" {
			prompt = fmt.Sprintf("Context from prior steps:\n%s\n\nTask: %s", priorContext, prompt)
		}
		cmd = exec.CommandContext(ctx, "claude", "-p", prompt)

	case "gemini":
		prompt := step.Prompt
		if prompt == "" {
			prompt = step.Description
		}
		cmd = exec.CommandContext(ctx, "gemini", "-p", prompt)

	case "shell":
		if step.Command == "" {
			return "", fmt.Errorf("shell step has no command")
		}
		cmd = exec.CommandContext(ctx, "bash", "-c", step.Command)

	default:
		// 直接當 shell command 跑（terraform, kubectl, helm, aws, gcloud）
		if step.Command != "" {
			cmd = exec.CommandContext(ctx, "bash", "-c", step.Command)
		} else {
			return "", fmt.Errorf("agent %q has no command or prompt", step.Agent)
		}
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Dir = e.findWorkDir(step)

	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("%s failed: %w\nstderr: %s", step.Agent, err, stderr.String())
	}

	return stdout.String(), nil
}

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

// validateStepOrder 確保 DependsOn 指向的 step 排在當前 step 之前
func validateStepOrder(steps []planner.Step) error {
	seen := make(map[string]bool)
	for _, step := range steps {
		if step.DependsOn != "" && !seen[step.DependsOn] {
			return fmt.Errorf("step %q depends on %q which has not been executed yet (ordering error)", step.ID, step.DependsOn)
		}
		seen[step.ID] = true
	}
	return nil
}

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

func truncate(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max] + "\n... (truncated)"
}

// parseFiles extracts file declarations from output.
// Format: lines starting with "FILE:" followed by "path|description"
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

// parseKV extracts key-value pairs from output.
// Format: lines starting with "KV:" followed by "key=value"
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
