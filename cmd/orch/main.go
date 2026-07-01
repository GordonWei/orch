package main

import (
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/chzyer/readline"
	"github.com/gordonwei/orch/pkg/config"
	"github.com/gordonwei/orch/pkg/executor"
	"github.com/gordonwei/orch/pkg/memory"
	"github.com/gordonwei/orch/pkg/model"
	"github.com/gordonwei/orch/pkg/planner"
	"github.com/gordonwei/orch/pkg/registry"
)

func main() {
	args := parseArgs()

	// 載入設定
	cfg := config.Load()

	// 掃描工具
	reg := registry.Scan()

	// 開啟記憶層（SQLite）
	store, err := memory.Open(cfg.Memory.DBPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "⚠️  memory db open failed: %v (running without memory)\n", err)
	}
	if store != nil {
		defer store.Close()
	}

	// 自動啟動 MLX server（如果沒在跑）
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

	// 啟動時讀 briefing
	if store != nil && cfg.Memory.BriefingOnBoot {
		if brief, t, err := store.GetBriefing(); err == nil && brief != "" {
			fmt.Fprintf(os.Stderr, "📋 briefing (generated %s):\n   %s\n\n", t.Format("01/02 15:04"), brief)
		}
	}

	if args.showTools {
		fmt.Println(reg.ToJSON())
		return
	}

	if args.prompt != "" {
		runTask(reg, cfg, store, args.prompt)
		return
	}

	runREPL(reg, cfg, store)
}

// ===== 串流事件消費者 =====

// startEventPrinter 啟動一個 goroutine，從步驟生命週期 EventChan 讀取事件並格式化輸出。
// 回傳 WaitGroup，用來等待所有事件印完。
func startEventPrinter(events <-chan executor.StepEvent) *sync.WaitGroup {
	var wg sync.WaitGroup
	wg.Add(1)

	go func() {
		defer wg.Done()
		for ev := range events {
			printStepEvent(ev)
		}
	}()

	return &wg
}

// printStepEvent 格式化輸出步驟生命週期事件。
func printStepEvent(ev executor.StepEvent) {
	switch ev.Type {
	case executor.EventStepStart:
		fmt.Fprintf(os.Stderr, "⏳ [%s] Starting...\n", ev.StepID)
	case executor.EventStepDone:
		if ev.Result != nil {
			fmt.Fprintf(os.Stderr, "✅ [%s] Done (%s)\n", ev.StepID, ev.Result.Took.Round(100_000_000))
		} else {
			fmt.Fprintf(os.Stderr, "✅ [%s] Done\n", ev.StepID)
		}
	case executor.EventStepFailed:
		if ev.Result != nil && ev.Result.Err != nil {
			fmt.Fprintf(os.Stderr, "❌ [%s] Failed: %v\n", ev.StepID, ev.Result.Err)
		} else {
			fmt.Fprintf(os.Stderr, "❌ [%s] Failed\n", ev.StepID)
		}
	case executor.EventStepSkipped:
		fmt.Fprintf(os.Stderr, "⏭️  [%s] Skipped\n", ev.StepID)
	case executor.EventStepCancelled:
		fmt.Fprintf(os.Stderr, "🚫 [%s] Cancelled\n", ev.StepID)
	}
}

// startOutputPrinter 啟動一個 goroutine，從 OutputEvents 讀取逐行輸出並格式化顯示。
// 使用 ANSI dim 色彩與縮排，避免與主要訊息混淆。
func startOutputPrinter(events <-chan executor.OutputEvent) *sync.WaitGroup {
	var wg sync.WaitGroup
	wg.Add(1)

	go func() {
		defer wg.Done()
		for ev := range events {
			printOutputEvent(ev)
		}
	}()

	return &wg
}

// printOutputEvent 格式化輸出逐行串流事件。
// 使用 step ID 前綴，方便平行執行時辨識來源。
func printOutputEvent(ev executor.OutputEvent) {
	switch ev.Type {
	case executor.OutputLine:
		// 使用 ANSI dim + 縮排，視覺上與步驟生命週期訊息區隔
		fmt.Fprintf(os.Stderr, "\033[2m   │ [%s] %s\033[0m\n", ev.StepID, ev.Message)
	case executor.OutputProgress:
		fmt.Fprintf(os.Stderr, "   ⋯ [%s] %s\n", ev.StepID, ev.Message)
	}
}

func runTask(reg *registry.Registry, cfg *config.Config, store *memory.Store, prompt string) {
	// 1. Plan
	fmt.Fprintf(os.Stderr, "🧠 planning...\n")
	p := planner.New(reg, cfg)
	plan, err := p.GeneratePlan(prompt)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ planning failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "📝 %s\n", plan.TaskSummary)
	fmt.Fprintf(os.Stderr, "   difficulty: %s | category: %s | steps: %d\n",
		plan.Difficulty, plan.Category, len(plan.Steps))

	// 如果是一般對話（category=chat 或 agent=local），直接用 MLX 回答
	if plan.Category == "chat" || (len(plan.Steps) == 1 && plan.Steps[0].Agent == "local") {
		answer, err := p.DirectChat(prompt)
		if err != nil {
			fmt.Fprintf(os.Stderr, "   ⚠️  local chat failed: %v, falling back to executor\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "\n")
			fmt.Println(answer)
			// 寫 history
			if store != nil {
				store.AddHistory(memory.HistoryEntry{
					Input:         prompt,
					Category:      "chat",
					Agent:         "local",
					OutputSummary: truncateStr(answer, 500),
					Success:       true,
					Tags:          []string{"chat"},
				})
			}
			return
		}
	}

	// 2. Execute（含串流事件支援）
	fmt.Fprintf(os.Stderr, "\n⚡ executing...\n")

	// 建立步驟生命週期事件 channel（由 Executor 在 Execute 完成後關閉）
	stepEvents := make(chan executor.StepEvent, 64)
	stepPrinterWg := startEventPrinter(stepEvents)

	// 建立逐行輸出串流事件 channel（由我們在 Execute 完成後關閉）
	outputEvents := make(chan executor.OutputEvent, 256)
	outputPrinterWg := startOutputPrinter(outputEvents)

	e := executor.New(cfg)
	e.EventChan = stepEvents       // 步驟生命週期事件
	e.OutputEvents = outputEvents  // 逐行輸出串流

	// 設置 re-plan callback
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

		// re-plan 需要新的 EventChan（舊的已被 Execute 關閉）
		newStepEvents := make(chan executor.StepEvent, 64)
		newStepWg := startEventPrinter(newStepEvents)
		e.EventChan = newStepEvents

		result = e.Execute(newPlan)

		// 等待新 channel 的事件印完
		newStepWg.Wait()
		return nil
	})

	result = e.Execute(plan)

	// Execute 完成後 EventChan 已被關閉，等待 printer 印完
	stepPrinterWg.Wait()

	// 關閉 OutputEvents channel，等待 output printer 印完
	close(outputEvents)
	outputPrinterWg.Wait()

	// 3. Report
	fmt.Fprintf(os.Stderr, "\n")
	if result.Success {
		fmt.Fprintf(os.Stderr, "🏁 complete (%s)\n", result.Took.Round(100_000_000))
		// 印最後一步的 output 作為成品
		if len(result.Steps) > 0 {
			last := result.Steps[len(result.Steps)-1]
			if last.Output != "" {
				fmt.Print(last.Output)
			}
		}
	} else {
		fmt.Fprintf(os.Stderr, "💀 task failed after %s\n", result.Took.Round(100_000_000))
		if result.Err != nil {
			fmt.Fprintf(os.Stderr, "   error: %v\n", result.Err)
		}
		for _, s := range result.Steps {
			if s.Err != nil {
				fmt.Fprintf(os.Stderr, "   failed at [%s]: %v\n", s.StepID, s.Err)
			}
		}
	}

	// 4. 寫 history
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
	}

	if !result.Success {
		os.Exit(1)
	}
}

func runREPL(reg *registry.Registry, cfg *config.Config, store *memory.Store) {
	rl, err := readline.NewEx(&readline.Config{
		Prompt:          "› ",
		HistoryFile:     os.Getenv("HOME") + "/.orch_history",
		InterruptPrompt: "^C",
		EOFPrompt:       "exit",
		Stdin:           os.Stdin,
		Stdout:          os.Stderr,
		Stderr:          os.Stderr,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "readline init failed: %v\n", err)
		return
	}
	defer rl.Close()

	fmt.Fprintf(rl.Stdout(), "🟢 orch v0.1 — AI 幕僚長\n")
	fmt.Fprintf(rl.Stdout(), "   tools: %s\n", toolNames(reg))
	fmt.Fprintf(rl.Stdout(), "   type your request, ctrl+d to quit\n\n")

	for {
		line, err := rl.Readline()
		if err == readline.ErrInterrupt {
			// ctrl+c：清除當前輸入，不退出
			continue
		}
		if err != nil {
			// ctrl+d 或真正的 EOF
			break
		}
		input := strings.TrimSpace(line)
		if input == "" {
			continue
		}
		if input == "exit" || input == "quit" || input == "q" {
			break
		}
		if input == "tools" {
			fmt.Println(reg.ToJSON())
			continue
		}

		runTask(reg, cfg, store, input)
		fmt.Fprintln(os.Stderr)
	}

	fmt.Fprintln(os.Stderr, "👋 bye")
}

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

type cliArgs struct {
	prompt    string
	showTools bool
}

func parseArgs() cliArgs {
	args := cliArgs{}
	var prompts []string

	for i := 1; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--tools":
			args.showTools = true
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
	fmt.Fprintf(os.Stderr, `orch - AI 幕僚長 CLI

Usage:
  orch <prompt>        Oneshot: plan → execute → deliver
  orch                 REPL mode: continuous interaction
  orch --tools         Show available tools

Examples:
  orch "查 S3 bucket 用量"
  orch "把 litellm helm values 加上 rate limiting"
  orch "整理今天的會議記錄並同步 Notion"
`)
}
