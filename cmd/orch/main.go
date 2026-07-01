package main

import (
	"fmt"
	"os"
	"strings"

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

	// 2. Execute
	fmt.Fprintf(os.Stderr, "\n⚡ executing...\n")
	e := executor.New(cfg)

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
		result = e.Execute(newPlan)
		return nil
	})

	result = e.Execute(plan)

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
