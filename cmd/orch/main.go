package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gordonwei/orch/pkg/apibackend"
	"github.com/gordonwei/orch/pkg/backend"
	"github.com/gordonwei/orch/pkg/config"
	"github.com/gordonwei/orch/pkg/eventbus"
	"github.com/gordonwei/orch/pkg/executor"
	"github.com/gordonwei/orch/pkg/memory"
	"github.com/gordonwei/orch/pkg/model"
	"github.com/gordonwei/orch/pkg/planner"
	"github.com/gordonwei/orch/pkg/registry"
	"github.com/gordonwei/orch/pkg/router"
	"github.com/gordonwei/orch/pkg/workflow"
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

		// Run registered shutdown hooks (e.g. session manager) in the
		// background so a second Ctrl+C can still force an immediate exit
		// even if the hook (session Shutdown() can take up to ~8s) hangs.
		shutdownMu.Lock()
		fn := shutdownFunc
		shutdownMu.Unlock()

		done := make(chan struct{})
		go func() {
			if fn != nil {
				fn()
			} else {
				// No shutdown hook registered (one-shot/subcommand mode
				// never enters the REPL, so RegisterShutdown was never
				// called) — give in-flight work a brief grace period,
				// matching the previous unconditional behavior.
				time.Sleep(500 * time.Millisecond)
			}
			close(done)
		}()

		select {
		case <-done:
		case <-sigCh:
			fmt.Fprintf(os.Stderr, "\n⚡ received second signal, forcing exit...\n")
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

	// Initialize stateless API backends (Bedrock, Vertex AI)
	apiBackends := make(map[string]apibackend.APIBackend)
	if cfg.APIBackends.Bedrock.Enabled {
		ab := apibackend.NewBedrock(apibackend.BedrockConfig{
			Region:  cfg.APIBackends.Bedrock.Region,
			ModelID: cfg.APIBackends.Bedrock.ModelID,
		})
		apiBackends["bedrock"] = ab
		if ab.Available() {
			fmt.Fprintf(os.Stderr, "   ☁️  bedrock: %s (%s) ✓\n", cfg.APIBackends.Bedrock.ModelID, cfg.APIBackends.Bedrock.Region)
		} else {
			fmt.Fprintf(os.Stderr, "   ⚠️  bedrock: enabled but credentials not found\n")
		}
	}
	if cfg.APIBackends.VertexAI.Enabled {
		vab := apibackend.NewVertexAI(apibackend.VertexAIConfig{
			ProjectID: cfg.APIBackends.VertexAI.ProjectID,
			Region:    cfg.APIBackends.VertexAI.Region,
			ModelID:   cfg.APIBackends.VertexAI.ModelID,
		})
		apiBackends["vertexai"] = vab
		if vab.Available() {
			fmt.Fprintf(os.Stderr, "   ☁️  vertexai: %s (%s/%s) ✓\n", cfg.APIBackends.VertexAI.ModelID, cfg.APIBackends.VertexAI.ProjectID, cfg.APIBackends.VertexAI.Region)
		} else {
			fmt.Fprintf(os.Stderr, "   ⚠️  vertexai: enabled but ADC not found\n")
		}
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
		brief, t, err := store.GetBriefing()
		if cfg.Memory.BriefingSourceFile != "" {
			// A source file is configured — always re-summarize it fresh rather
			// than trust whatever's cached, so this can never silently go stale
			// the way a manually-triggered `orch briefing gen` does. Falls back
			// to the last cached briefing (if any) on any failure (file missing,
			// MLX down) instead of blocking startup.
			if fresh, genErr := generateBriefingFromFile(cfg, store); genErr == nil {
				brief, t, err = fresh, time.Now(), nil
			} else if verbose {
				fmt.Fprintf(os.Stderr, "   ⚠️  briefing_source_file: %v (showing last cached briefing)\n", genErr)
			}
		}
		if err == nil && brief != "" {
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
		ok, _ := runTask(ctx, reg, cfg, store, br, apiBackends, bus, prompt, args.dryRun)
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

	runREPL(reg, cfg, store, br, apiBackends, replBus)
}

// ===== Subcommand Handlers =====

func handleSubcommand(args cliArgs, cfg *config.Config, store *memory.Store) {
	switch args.subcommand {
	case "history":
		handleHistory(args.subArgs, store)
	case "session-history":
		handleSessionHistoryClear(args.subArgs, store)
	case "briefing":
		handleBriefing(args.subArgs, cfg, store)
	case "cost":
		handleCostCmd(store, args.subArgs)
	case "init":
		handleInit()
	default:
		fmt.Fprintf(os.Stderr, "❌ unknown subcommand: %s\n", args.subcommand)
		os.Exit(1)
	}
}

// handleSessionHistoryClear exposes memory.Store.PruneSessionLogs via the CLI
// (orch session-history clear [days]). Without this, session_logs (written
// on every REPL turn and every /pass) had no reachable cleanup path and grew
// unbounded, unlike the sibling `history` table which has `orch history clear`.
func handleSessionHistoryClear(subArgs []string, store *memory.Store) {
	if store == nil {
		fmt.Fprintf(os.Stderr, "❌ memory store not available\n")
		os.Exit(1)
	}

	if len(subArgs) == 0 || subArgs[0] != "clear" {
		fmt.Fprintf(os.Stderr, "Usage: orch session-history clear [older-than-days]\n")
		os.Exit(1)
	}

	olderThanDays := 0
	if len(subArgs) > 1 {
		days, err := strconv.Atoi(subArgs[1])
		if err != nil || days < 0 {
			fmt.Fprintf(os.Stderr, "❌ invalid days value: %s\n", subArgs[1])
			os.Exit(1)
		}
		olderThanDays = days
	}

	count, err := store.PruneSessionLogs(olderThanDays)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("🗑️  cleared %d session-history entries\n", count)
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

// generateBriefingFromFile reads cfg.Memory.BriefingSourceFile fresh, summarizes it via
// the local model, and saves the result via store.SetBriefing(). Returns an error (never
// os.Exit) so the boot path can fall back to the last cached briefing on any failure —
// a missing file or a down MLX server on startup shouldn't block using orch.
func generateBriefingFromFile(cfg *config.Config, store *memory.Store) (string, error) {
	path := cfg.Memory.BriefingSourceFile
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading briefing_source_file %s: %w", path, err)
	}

	activeModel := cfg.ActiveModel()
	llmClient := model.NewOpenAIClient(model.OpenAIClientConfig{
		Endpoint: activeModel.Endpoint,
		Model:    activeModel.Model,
		Backend:  activeModel.Backend,
	})
	if !llmClient.Available() {
		return "", fmt.Errorf("local model unavailable, cannot summarize %s", path)
	}

	// Measured on this machine's 7B model with a cold MLX server (fresh
	// model load, no prefix cache): a 24000-char prompt did not finish
	// within 150s. Keeping this close to the original, already-tested
	// 12000-char/200-word budget trades completeness (a long handoff doc
	// gets truncated before covering every section) for staying reliably
	// fast on every boot — DirectChat now actually receives whatever this
	// produces (see SetBriefing wiring in runTask), so even a partial
	// summary is a real improvement over the previous behavior of not
	// reaching the model at all.
	prompt := fmt.Sprintf(`Here is the current content of a project status/handoff document (%s):

%s

Write a concise briefing (max 220 words) summarizing:
1. What is currently in progress / the latest status
2. Any items flagged as blocking or needing attention (e.g. marked 🔴)
3. Distinct pending/to-do items, listed individually where space allows
4. What to focus on today

Output only the briefing text, no titles or formatting. Reply in the same language as the document.`, filepath.Base(path), truncateStr(string(data), 12000))

	messages := []model.Message{
		{Role: "system", Content: "You are a concise project status summarization assistant."},
		{Role: "user", Content: prompt},
	}

	// Measured on this machine's 7B model: local decode runs at roughly
	// 16 tokens/sec, so this budget already costs up to ~32s in the worst
	// case on every orch boot (briefing_source_file is re-summarized fresh
	// every time) — higher risks the boot itself feeling hung.
	answer, err := llmClient.Chat(messages, &model.ChatOptions{
		MaxTokens:   512,
		Temperature: 0.3,
	})
	if err != nil {
		return "", fmt.Errorf("summarizing %s: %w", path, err)
	}

	answer = strings.TrimSpace(answer)
	if err := store.SetBriefing(answer); err != nil {
		return "", fmt.Errorf("saving briefing: %w", err)
	}
	return answer, nil
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

func runTask(ctx context.Context, reg *registry.Registry, cfg *config.Config, store *memory.Store, br *backend.Registry, apiBackends map[string]apibackend.APIBackend, bus *eventbus.Bus, prompt string, dryRun bool) (bool, string) {
	// 0. Workflow trigger match — check before AI planning
	if workflows, err := workflow.LoadAll(cfg.Workflows.Dir); err == nil && len(workflows) > 0 {
		if matched := workflow.Match(prompt, workflows); matched != nil {
			fmt.Fprintf(os.Stderr, "📋 workflow matched: %s\n", matched.Name)
			plan := workflow.ToPlanner(matched, nil, cfg)

			if dryRun {
				fmt.Fprintf(os.Stderr, "📝 %s\n", plan.TaskSummary)
				fmt.Fprintf(os.Stderr, "   difficulty: %s | category: %s | steps: %d\n",
					plan.Difficulty, plan.Category, len(plan.Steps))
				fmt.Fprintf(os.Stderr, "\n")
				printDryRun(plan)
				return true, ""
			}

			fmt.Fprintf(os.Stderr, "🚀 executing workflow: %s (%d steps)\n", matched.Name, len(plan.Steps))

			stepEvents := make(chan executor.StepEvent, 64)
			stepPrinterWg := startEventPrinter(stepEvents)
			outputEvents := make(chan executor.OutputEvent, 256)
			outputPrinterWg := startOutputPrinter(outputEvents)

			e := executor.New(cfg, br)
			e.EventChan = stepEvents
			e.OutputEvents = outputEvents
			e.ApprovalFunc = promptApproval
			e.APIBackends = apiBackends
			e.MemoryStore = store
			result := e.Execute(plan)

			stepPrinterWg.Wait()
			close(outputEvents)
			outputPrinterWg.Wait()

			fmt.Fprintf(os.Stderr, "\n")
			if result.Success {
				fmt.Fprintf(os.Stderr, "🏁 workflow complete (%s)\n", result.Took.Round(100*time.Millisecond))
				if len(result.Steps) > 0 {
					last := result.Steps[len(result.Steps)-1]
					if last.Output != "" {
						fmt.Print(last.Output)
					}
				}
			} else {
				fmt.Fprintf(os.Stderr, "💀 workflow failed after %s\n", result.Took.Round(100*time.Millisecond))
				if result.Err != nil {
					fmt.Fprintf(os.Stderr, "   error: %v\n", result.Err)
				}
			}

			if store != nil {
				var outputSummary string
				if len(result.Steps) > 0 {
					outputSummary = truncateStr(result.Steps[len(result.Steps)-1].Output, 500)
				}
				store.AddHistory(memory.HistoryEntry{
					Input:         prompt,
					Category:      "workflow",
					Agent:         "workflow",
					OutputSummary: outputSummary,
					Success:       result.Success,
					Tags:          []string{"workflow", matched.Name},
					TookMs:        result.Took.Milliseconds(),
				})
			}

			if !result.Success {
				return false, ""
			}
			var finalOutput string
			if len(result.Steps) > 0 {
				finalOutput = result.Steps[len(result.Steps)-1].Output
			}
			return true, finalOutput
		}
	}

	// 1. Plan
	fmt.Fprintf(os.Stderr, "🧠 planning...\n")
	r := router.New(cfg.RouteRules)
	p := planner.New(reg, cfg, br, r)
	p.Verbose = verbose
	if store != nil {
		// Ground DirectChat in whatever briefing is currently cached (kept
		// fresh on boot when cfg.Memory.BriefingSourceFile is set — see
		// generateBriefingFromFile) instead of the model answering "what's
		// pending" from nothing.
		if brief, _, err := store.GetBriefing(); err == nil {
			p.SetBriefing(brief)
		}
	}
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
	e.ApprovalFunc = promptApproval
	e.APIBackends = apiBackends
	e.MemoryStore = store

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
	case "history", "session-history", "briefing", "cost", "init":
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

  orch session-history clear [days]   Clear session mode conversation logs
                                       (all, or older than N days)

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

// promptApprovalMu serializes concurrent approval prompts (DAG steps run in
// their own goroutines; without this, two high-risk steps triggered at the
// same time would interleave their prompts and race on stdin).
var promptApprovalMu sync.Mutex

// promptApproval asks the user to confirm execution of a high-risk command.
// Returns true if user approves, false if denied.
//
// Non-interactive sessions (stdin is a pipe/redirect, e.g. CI/cron) cannot be
// prompted — that case is detected explicitly and denied with a clear message
// rather than silently blocking on a Scanln that will hit EOF. Set
// ORCH_AUTO_APPROVE=1 to bypass the gate entirely in those environments.
func promptApproval(command string) bool {
	promptApprovalMu.Lock()
	defer promptApprovalMu.Unlock()

	if os.Getenv("ORCH_AUTO_APPROVE") == "1" {
		fmt.Fprintf(os.Stderr, "⚠️  ORCH_AUTO_APPROVE=1: auto-approving high-risk command: %s\n", command)
		return true
	}

	stat, _ := os.Stdin.Stat()
	if stat != nil && (stat.Mode()&os.ModeCharDevice) == 0 {
		fmt.Fprintf(os.Stderr, "\n⚠️  HIGH-RISK COMMAND DETECTED (non-interactive session, cannot prompt):\n")
		fmt.Fprintf(os.Stderr, "   %s\n", command)
		fmt.Fprintf(os.Stderr, "   Denied by default. Set ORCH_AUTO_APPROVE=1 to allow high-risk commands in CI/scripts.\n")
		return false
	}

	fmt.Fprintf(os.Stderr, "\n⚠️  HIGH-RISK COMMAND DETECTED:\n")
	fmt.Fprintf(os.Stderr, "   %s\n", command)
	fmt.Fprintf(os.Stderr, "   Execute? [y/N]: ")

	// Read the response off the main goroutine so Ctrl+C can interrupt a
	// pending prompt instead of blocking forever on Scanln.
	responseCh := make(chan string, 1)
	go func() {
		var response string
		fmt.Scanln(&response)
		responseCh <- response
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT)
	defer signal.Stop(sigCh)

	select {
	case response := <-responseCh:
		response = strings.TrimSpace(strings.ToLower(response))
		return response == "y" || response == "yes"
	case <-sigCh:
		fmt.Fprintf(os.Stderr, "\n⚡ cancelled, denying command\n")
		return false
	}
}

// truncateStr truncates s to at most max runes (not bytes) so CJK text isn't
// cut mid-character into invalid UTF-8 — this codebase is CJK-heavy (handoff
// docs, task summaries, prompts) and a byte-slice truncation here previously
// risked feeding a broken multi-byte sequence straight into MLX request JSON.
func truncateStr(s string, max int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "..."
}
