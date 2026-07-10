package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gordonwei/orch/pkg/backend"
	"github.com/gordonwei/orch/pkg/config"
	"github.com/gordonwei/orch/pkg/eventbus"
	"github.com/gordonwei/orch/pkg/executor"
	"github.com/gordonwei/orch/pkg/memory"
	"github.com/gordonwei/orch/pkg/model"
	"github.com/gordonwei/orch/pkg/planner"
	"github.com/gordonwei/orch/pkg/registry"
)

// version is set at build time via -ldflags "-X main.version=v0.4"
var version = "dev"
var verbose bool

// shutdownFunc is a hook for the signal handler to call Shutdown() on the session manager.
var (
	shutdownMu   sync.Mutex
	shutdownFunc func()
)

// RegisterShutdown sets the function to call on SIGINT/SIGTERM.
func RegisterShutdown(fn func()) {
	shutdownMu.Lock()
	defer shutdownMu.Unlock()
	shutdownFunc = fn
}

func main() {
	args := parseArgs()
	verbose = args.verbose

	// --version flag
	if args.showVersion {
		fmt.Printf("orch %s\n", version)
		return
	}

	// Load config
	cfg := config.Load()

	// Scan tools
	reg := registry.Scan()

	// Open memory layer (SQLite)
	store, err := memory.Open(cfg.Memory.DBPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "⚠️  memory db open failed: %v (running without memory)\n", err)
	}
	if store != nil {
		defer store.Close()
	}

	// Signal handler (graceful shutdown)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		fmt.Fprintf(os.Stderr, "\n⚡ received %v, shutting down...\n", sig)
		cancel()
		// Run registered shutdown hooks (e.g. session manager)
		shutdownMu.Lock()
		fn := shutdownFunc
		shutdownMu.Unlock()
		if fn != nil {
			fn()
		}
		os.Exit(130)
	}()

	// Handle subcommands (no MLX server needed)
	if args.subcommand != "" {
		handleSubcommand(args, cfg, store)
		return
	}

	// Determine backend override: --backend flag > ORCH_BACKEND env > config ai_backend.primary
	backendOverride := args.backend
	if backendOverride == "" {
		backendOverride = os.Getenv("ORCH_BACKEND")
	}
	if backendOverride == "" {
		backendOverride = cfg.AIBackend.Primary
	}

	// Initialize backend registry (auto-detect available CLIs)
	br := backend.NewRegistry(backendOverride)
	if len(br.Available()) > 0 {
		fmt.Fprintf(os.Stderr, "🤖 %s\n", br.Summary())
	} else {
		fmt.Fprintf(os.Stderr, "⚠️  no AI backends detected (install kiro-cli, claude, or gemini for cloud planning)\n")
	}

	// Auto-start MLX server (if not running)
	activeModel := cfg.ActiveModel()
	if activeModel.AutoStart {
		starter := model.NewStarter(model.StarterConfig{
			Backend:    activeModel.Backend,
			PythonPath: activeModel.PythonPath,
			Model:      activeModel.Model,
			Port:       activeModel.Port,
			Endpoint:   activeModel.Endpoint,
		})
		llmClient := model.NewOpenAIClient(model.OpenAIClientConfig{
			Endpoint: activeModel.Endpoint,
			Model:    activeModel.Model,
			Backend:  activeModel.Backend,
		})
		if err := starter.EnsureRunning(llmClient); err != nil {
			fmt.Fprintf(os.Stderr, "   ⚠️  %v\n", err)
		}
	}

	// Show briefing on boot
	if store != nil && cfg.Memory.BriefingOnBoot {
		if brief, t, err := store.GetBriefing(); err == nil && brief != "" {
			fmt.Fprintf(os.Stderr, "📋 briefing (generated %s):\n   %s\n\n", t.Format("01/02 15:04"), brief)
		}
	}

	if args.showTools {
		fmt.Println(reg.ToJSON())
		return
	}

	// ===== stdin pipe integration =====
	stdinIsPipe := false
	var pipeData []byte
	stat, _ := os.Stdin.Stat()
	if (stat.Mode() & os.ModeCharDevice) == 0 {
		stdinIsPipe = true
		pipeData, _ = io.ReadAll(os.Stdin)
	}

	prompt := args.prompt

	if stdinIsPipe && len(pipeData) > 0 {
		pipeStr := truncateStr(string(pipeData), 50000)
		if prompt == "" {
			prompt = strings.TrimSpace(string(pipeData))
			if len(prompt) > 50000 {
				prompt = prompt[:50000]
			}
		} else {
			prompt = fmt.Sprintf("Context (from stdin pipe, %d bytes):\n```\n%s\n```\n\nTask: %s", len(pipeData), pipeStr, prompt)
		}
	}

	if prompt != "" {
		// Load reactive trigger rules
		rules, err := eventbus.LoadRules(cfg.Workflows.Dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "⚠️  reactive rules load failed: %v\n", err)
		}
		var bus *eventbus.Bus
		if len(rules) > 0 {
			bus = eventbus.New(rules)
			fmt.Fprintf(os.Stderr, "🔗 reactive rules: %d loaded\n", len(rules))
		}

		// runTask prints the output itself; the returned string is only for REPL session context.
		ok, _ := runTask(ctx, reg, cfg, store, br, bus, prompt, args.dryRun)
		if !ok {
			os.Exit(1)
		}
		return
	}

	// If stdin is a pipe, skip REPL mode
	if stdinIsPipe {
		fmt.Fprintf(os.Stderr, "⚠️  stdin is a pipe but no input received. Use: echo 'task' | orch\n")
		return
	}

	// Load reactive trigger rules for REPL
	replRules, _ := eventbus.LoadRules(cfg.Workflows.Dir)
	var replBus *eventbus.Bus
	if len(replRules) > 0 {
		replBus = eventbus.New(replRules)
	}

	runREPL(reg, cfg, store, br, replBus)
}

// ===== Subcommand Handlers =====

func handleSubcommand(args cliArgs, cfg *config.Config, store *memory.Store) {
	switch args.subcommand {
	case "history":
		handleHistory(args.subArgs, store)
	case "briefing":
		handleBriefing(args.subArgs, cfg, store)
	case "init":
		handleInit()
	default:
		fmt.Fprintf(os.Stderr, "❌ unknown subcommand: %s\n", args.subcommand)
		os.Exit(1)
	}
}

func handleHistory(subArgs []string, store *memory.Store) {
	if store == nil {
		fmt.Fprintf(os.Stderr, "❌ memory store not available\n")
		os.Exit(1)
	}

	if len(subArgs) == 0 {
		entries, err := store.RecentHistory(20)
		if err != nil {
			fmt.Fprintf(os.Stderr, "❌ %v\n", err)
			os.Exit(1)
		}
		if len(entries) == 0 {
			fmt.Println("(no history entries)")
			return
		}
		printHistoryEntries(entries)
		return
	}

	switch subArgs[0] {
	case "search":
		if len(subArgs) < 2 {
			fmt.Fprintf(os.Stderr, "Usage: orch history search <keyword>\n")
			os.Exit(1)
		}
		keyword := strings.Join(subArgs[1:], " ")
		entries, err := store.SearchHistory(keyword, 20)
		if err != nil {
			fmt.Fprintf(os.Stderr, "❌ %v\n", err)
			os.Exit(1)
		}
		if len(entries) == 0 {
			fmt.Printf("(no entries matching \"%s\")\n", keyword)
			return
		}
		printHistoryEntries(entries)

	case "clear":
		count, err := store.PruneHistory(0)
		if err != nil {
			fmt.Fprintf(os.Stderr, "❌ %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("🗑️  cleared %d history entries\n", count)

	default:
		fmt.Fprintf(os.Stderr, "Usage: orch history [search <kw> | clear]\n")
		os.Exit(1)
	}
}

func handleBriefing(subArgs []string, cfg *config.Config, store *memory.Store) {
	if store == nil {
		fmt.Fprintf(os.Stderr, "❌ memory store not available\n")
		os.Exit(1)
	}

	if len(subArgs) == 0 {
		brief, t, err := store.GetBriefing()
		if err != nil {
			fmt.Fprintf(os.Stderr, "❌ %v\n", err)
			os.Exit(1)
		}
		if brief == "" {
			fmt.Println("(no briefing)")
			return
		}
		fmt.Printf("📋 briefing (generated %s):\n   %s\n", t.Format("2006-01-02 15:04"), brief)
		return
	}

	switch subArgs[0] {
	case "set":
		if len(subArgs) < 2 {
			fmt.Fprintf(os.Stderr, "Usage: orch briefing set <text>\n")
			os.Exit(1)
		}
		text := strings.Join(subArgs[1:], " ")
		if err := store.SetBriefing(text); err != nil {
			fmt.Fprintf(os.Stderr, "❌ %v\n", err)
			os.Exit(1)
		}
		fmt.Println("✅ briefing updated")

	case "gen":
		handleBriefingGen(cfg, store)

	default:
		fmt.Fprintf(os.Stderr, "Usage: orch briefing [set <text> | gen]\n")
		os.Exit(1)
	}
}

func handleBriefingGen(cfg *config.Config, store *memory.Store) {
	entries, err := store.RecentHistory(10)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ failed to read history: %v\n", err)
		os.Exit(1)
	}
	if len(entries) == 0 {
		fmt.Fprintf(os.Stderr, "⚠️  no history entries to generate briefing from\n")
		os.Exit(1)
	}

	var sb strings.Builder
	for _, e := range entries {
		status := "✅"
		if !e.Success {
			status = "❌"
		}
		sb.WriteString(fmt.Sprintf("%s [%s] %s (agent: %s, %dms)\n", status, e.Category, e.Input, e.Agent, e.TookMs))
		if e.OutputSummary != "" {
			sb.WriteString(fmt.Sprintf("   → %s\n", truncateStr(e.OutputSummary, 200)))
		}
	}

	activeModel := cfg.ActiveModel()
	llmClient := model.NewOpenAIClient(model.OpenAIClientConfig{
		Endpoint: activeModel.Endpoint,
		Model:    activeModel.Model,
		Backend:  activeModel.Backend,
	})

	if !llmClient.Available() {
		fmt.Fprintf(os.Stderr, "❌ MLX server unavailable, cannot generate briefing\n")
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "🧠 generating briefing from recent %d tasks...\n", len(entries))

	prompt := fmt.Sprintf(`Here are the user's recent task history entries:

%s

Write a concise briefing (one paragraph, max 200 words) summarizing:
1. What was mainly worked on recently
2. What succeeded and what failed
3. What to focus on next

Output only the briefing text, no titles or formatting.`, sb.String())

	messages := []model.Message{
		{Role: "system", Content: "You are a concise task summarization assistant."},
		{Role: "user", Content: prompt},
	}

	answer, err := llmClient.Chat(messages, &model.ChatOptions{
		MaxTokens:   512,
		Temperature: 0.3,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ LLM generation failed: %v\n", err)
		os.Exit(1)
	}

	answer = strings.TrimSpace(answer)
	if err := store.SetBriefing(answer); err != nil {
		fmt.Fprintf(os.Stderr, "❌ failed to save briefing: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("✅ briefing generated and saved:\n   %s\n", answer)
}

// ===== Task Execution =====

func runTask(ctx context.Context, reg *registry.Registry, cfg *config.Config, store *memory.Store, br *backend.Registry, bus *eventbus.Bus, prompt string, dryRun bool) (bool, string) {
	// 1. Plan
	fmt.Fprintf(os.Stderr, "🧠 planning...\n")
	p := planner.New(reg, cfg, br)
	p.Verbose = verbose
	plan, err := p.GeneratePlan(prompt)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ planning failed: %v\n", err)
		return false, ""
	}

	fmt.Fprintf(os.Stderr, "📝 %s\n", plan.TaskSummary)
	fmt.Fprintf(os.Stderr, "   difficulty: %s | category: %s | steps: %d\n",
		plan.Difficulty, plan.Category, len(plan.Steps))

	if dryRun {
		fmt.Fprintf(os.Stderr, "\n")
		printDryRun(plan)
		return true, ""
	}

	// Direct chat for simple conversations
	if plan.Category == "chat" || (len(plan.Steps) == 1 && plan.Steps[0].Agent == "local") {
		answer, err := p.DirectChat(prompt) // prompt already contains session context if from REPL
		if err != nil {
			fmt.Fprintf(os.Stderr, "   ⚠️  local chat failed: %v, falling back to executor\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "\n")
			fmt.Println(answer)
			if store != nil {
				store.AddHistory(memory.HistoryEntry{
					Input:         prompt,
					Category:      "chat",
					Agent:         "local",
					OutputSummary: truncateStr(answer, 500),
					Success:       true,
					Tags:          []string{"chat"},
				})
				if cfg.Memory.HistoryLimit > 0 {
					store.AutoPrune(cfg.Memory.HistoryLimit)
				}
			}
			return true, answer
		}
	}

	// 2. Execute
	fmt.Fprintf(os.Stderr, "\n⚡ executing...\n")

	stepEvents := make(chan executor.StepEvent, 64)
	stepPrinterWg := startEventPrinter(stepEvents)

	outputEvents := make(chan executor.OutputEvent, 256)
	outputPrinterWg := startOutputPrinter(outputEvents)

	e := executor.New(cfg, br)
	e.EventChan = stepEvents
	e.OutputEvents = outputEvents

	// re-plan callback
	rePlanCount := 0
	maxRePlans := 2
	var result executor.Result

	e.SetRePlanFunc(func(failedContext string) error {
		rePlanCount++
		if rePlanCount > maxRePlans {
			return fmt.Errorf("max re-plans (%d) exceeded", maxRePlans)
		}
		fmt.Fprintf(os.Stderr, "\n🔁 re-planning (attempt %d/%d)...\n", rePlanCount, maxRePlans)
		rePlanPrompt := fmt.Sprintf("The previous plan failed. Here's the context:\n%s\n\nOriginal request: %s\n\nPlease create a new plan that avoids the previous failure.", failedContext, prompt)
		newPlan, err := p.GeneratePlan(rePlanPrompt)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "📝 new plan: %s (%d steps)\n", newPlan.TaskSummary, len(newPlan.Steps))

		newStepEvents := make(chan executor.StepEvent, 64)
		newStepWg := startEventPrinter(newStepEvents)
		e.EventChan = newStepEvents

		result = e.Execute(newPlan)
		newStepWg.Wait()
		return nil
	})

	result = e.Execute(plan)

	stepPrinterWg.Wait()
	close(outputEvents)
	outputPrinterWg.Wait()

	// 3. Report
	// Print the step output here, BEFORE the event bus runs: chains may block on cloud
	// backends for minutes, and the user should see the main result immediately.
	// Callers must NOT print the returned output again — it exists only so the REPL
	// can feed it into session context.
	fmt.Fprintf(os.Stderr, "\n")
	if result.Success {
		fmt.Fprintf(os.Stderr, "🏁 complete (%s)\n", result.Took.Round(100*time.Millisecond))
		if len(result.Steps) > 0 {
			last := result.Steps[len(result.Steps)-1]
			if last.Output != "" {
				fmt.Print(last.Output)
			}
		}
	} else {
		fmt.Fprintf(os.Stderr, "💀 task failed after %s\n", result.Took.Round(100*time.Millisecond))
		if result.Err != nil {
			fmt.Fprintf(os.Stderr, "   error: %v\n", result.Err)
		}
		for _, s := range result.Steps {
			if s.Err != nil {
				fmt.Fprintf(os.Stderr, "   failed at [%s]: %v\n", s.StepID, s.Err)
			}
		}
	}

	// 4. Write history
	if store != nil {
		var outputSummary string
		if len(result.Steps) > 0 {
			last := result.Steps[len(result.Steps)-1]
			outputSummary = truncateStr(last.Output, 500)
		}
		agent := "shell"
		if len(plan.Steps) > 0 {
			agent = plan.Steps[0].Agent
		}
		store.AddHistory(memory.HistoryEntry{
			Input:         prompt,
			Category:      plan.Category,
			Agent:         agent,
			OutputSummary: outputSummary,
			Success:       result.Success,
			Tags:          []string{plan.Category, plan.Difficulty},
			TookMs:        result.Took.Milliseconds(),
		})

		// Auto-prune if history_limit is configured
		if cfg.Memory.HistoryLimit > 0 {
			if pruned, err := store.AutoPrune(cfg.Memory.HistoryLimit); err == nil && pruned > 0 {
				fmt.Fprintf(os.Stderr, "🗑️  auto-pruned %d old history entries\n", pruned)
			}
		}
	}

	if !result.Success {
		return false, ""
	}

	// 5. Event Bus — check trigger rules for reactive chaining
	if bus != nil && bus.HasRules() && result.Success {
		// Get MLX client for gate/summarize (if available)
		activeModel := cfg.ActiveModel()
		llmClient := model.NewOpenAIClient(model.OpenAIClientConfig{
			Endpoint: activeModel.Endpoint,
			Model:    activeModel.Model,
			Backend:  activeModel.Backend,
		})
		mlxAvailable := llmClient.Available()

		for _, stepResult := range result.Steps {
			if stepResult.Err != nil {
				continue
			}

			// Prepare output (apply truncation/summarization)
			output := stepResult.Output

			event := eventbus.Event{
				Type:     "step.done",
				Agent:    stepResult.Agent,
				Category: plan.Category,
				StepID:   stepResult.StepID,
				Output:   output,
				Tags:     []string{plan.Category, plan.Difficulty},
			}

			actions, err := bus.Process(event)
			if err != nil {
				fmt.Fprintf(os.Stderr, "⚠️  eventbus error: %v\n", err)
				continue
			}

			for _, action := range actions {
				rule := findRule(bus, action.RuleName)

				// MLX Gate: ask local model if this trigger is worth dispatching
				if rule != nil && rule.Then.GateWithMLX && mlxAvailable {
					gatePrompt := fmt.Sprintf("Should this task be dispatched to a cloud AI? Answer YES or NO only.\nTask: %s\nContext: %s",
						action.Prompt, truncateStr(output, 500))
					gateResp, err := llmClient.Chat([]model.Message{
						{Role: "system", Content: "You are a gate keeper. Answer only YES or NO."},
						{Role: "user", Content: gatePrompt},
					}, &model.ChatOptions{MaxTokens: 10, Temperature: 0.0})
					if err == nil && !strings.Contains(strings.ToUpper(gateResp), "YES") {
						fmt.Fprintf(os.Stderr, "\n🚫 gate blocked: %s (MLX said no)\n", action.RuleName)
						continue
					}
				}

				// Output summarization: compress large output before passing downstream
				chainPrompt := action.Prompt
				if rule != nil && rule.Then.Summarize && mlxAvailable && len(output) > 1000 {
					fmt.Fprintf(os.Stderr, "\n📝 summarizing output (%d chars → MLX)...\n", len(output))
					summary, err := llmClient.Chat([]model.Message{
						{Role: "system", Content: "Summarize the following text concisely. Keep key facts, remove verbosity."},
						{Role: "user", Content: output},
					}, &model.ChatOptions{MaxTokens: 512, Temperature: 0.1})
					if err == nil && summary != "" {
						// Re-render prompt with summarized output
						summarizedEvent := event
						summarizedEvent.Output = summary
						if newActions, err := bus.Process(summarizedEvent); err == nil {
							for _, na := range newActions {
								if na.RuleName == action.RuleName {
									chainPrompt = na.Prompt
									break
								}
							}
						}
					}
				}

				// MaxContext truncation
				if rule != nil && rule.Then.MaxContext > 0 {
					chainPrompt = eventbus.TruncateOutput(chainPrompt, rule.Then.MaxContext)
				}

				fmt.Fprintf(os.Stderr, "\n🔗 trigger: %s → %s\n", action.RuleName, action.Agent)

				// Resolve backend for the triggered action
				b := br.Resolve(action.Agent)
				if b == nil {
					fmt.Fprintf(os.Stderr, "   ⚠️  no backend for %q, skipping\n", action.Agent)
					continue
				}

				// Execute the triggered action
				chainOutput, err := b.Execute(chainPrompt, "")
				if err != nil {
					fmt.Fprintf(os.Stderr, "   ❌ chain failed: %v\n", err)
					fmt.Fprintf(os.Stderr, "   💡 to retry: orch \"%s\"\n", truncateStr(action.Prompt, 80))
					// Record failed chain in history for visibility
					if store != nil {
						store.AddHistory(memory.HistoryEntry{
							Input:         fmt.Sprintf("[chain:%s] %s", action.RuleName, truncateStr(action.Prompt, 200)),
							Category:      "chain",
							Agent:         action.Agent,
							OutputSummary: fmt.Sprintf("FAILED: %v", err),
							Success:       false,
							Tags:          []string{"chain", action.RuleName, "failed"},
						})
					}
				} else {
					fmt.Fprintf(os.Stderr, "   ✅ chain complete\n")
					if chainOutput != "" {
						fmt.Print(chainOutput)
					}
					// Record successful chain in history
					if store != nil {
						store.AddHistory(memory.HistoryEntry{
							Input:         fmt.Sprintf("[chain:%s] %s", action.RuleName, truncateStr(action.Prompt, 200)),
							Category:      "chain",
							Agent:         action.Agent,
							OutputSummary: truncateStr(chainOutput, 500),
							Success:       true,
							Tags:          []string{"chain", action.RuleName},
						})
					}
				}
			}
		}
	}

	// Return the last step's output for REPL session context.
	// It was already printed in the Report section above — callers must not print it again.
	var finalOutput string
	if len(result.Steps) > 0 {
		last := result.Steps[len(result.Steps)-1]
		finalOutput = last.Output
	}
	return true, finalOutput
}

// findRule looks up a rule by name in the bus.
func findRule(bus *eventbus.Bus, name string) *eventbus.TriggerRule {
	for _, r := range bus.Rules() {
		if r.Name == name {
			return &r
		}
	}
	return nil
}

// ===== Arg Parsing =====

type cliArgs struct {
	prompt      string
	showTools   bool
	showVersion bool
	dryRun      bool
	verbose     bool
	backend     string // --backend override
	subcommand  string
	subArgs     []string
}

func parseArgs() cliArgs {
	args := cliArgs{}

	if len(os.Args) < 2 {
		return args
	}

	// Check for subcommands first
	switch os.Args[1] {
	case "history", "briefing", "init":
		args.subcommand = os.Args[1]
		if len(os.Args) > 2 {
			args.subArgs = os.Args[2:]
		}
		return args
	}

	// Normal arg parsing (flags + prompt)
	var prompts []string
	for i := 1; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--tools":
			args.showTools = true
		case "--dry-run":
			args.dryRun = true
		case "--verbose":
			args.verbose = true
		case "--version", "-v":
			args.showVersion = true
		case "--backend":
			if i+1 < len(os.Args) {
				i++
				args.backend = os.Args[i]
			}
		case "--help", "-h":
			printUsage()
			os.Exit(0)
		default:
			prompts = append(prompts, os.Args[i])
		}
	}

	args.prompt = strings.Join(prompts, " ")
	return args
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `orch %s - AI Task Orchestration CLI

Usage:
  orch <prompt>              Oneshot: plan → execute → deliver
  orch                       REPL mode: continuous interaction
  orch --tools               Show available tools
  orch --dry-run <prompt>    Plan only, don't execute
  orch --verbose <prompt>    Show detailed MLX debug output
  orch --backend <name>      Override AI backend (kiro/claude/gemini)
  orch --version             Show version

Subcommands:
  orch init                  Interactive setup wizard
  orch history               List last 20 task history entries
  orch history search <kw>   Search history
  orch history clear         Clear all history

  orch briefing              Show current briefing
  orch briefing set <text>   Manually set briefing
  orch briefing gen          Auto-generate briefing from recent history via MLX

Environment:
  ORCH_BACKEND               Override AI backend (same as --backend)
  ORCH_CONFIG                Override config file path

Examples:
  orch init
  orch "check S3 bucket usage"
  orch --backend gemini "summarize this doc"
  orch --dry-run "consolidate AWS and GCP usage report"
  kubectl get pods -o json | orch "which pods are unhealthy?"
  cat error.log | orch "analyze this error"
  orch history search "kubectl"
  orch briefing gen
`, version)
}

// ===== Helpers =====

func toolNames(reg *registry.Registry) string {
	var names []string
	for _, t := range reg.Available() {
		names = append(names, t.Name)
	}
	return strings.Join(names, ", ")
}

func truncateStr(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
