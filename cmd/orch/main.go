package main

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chzyer/readline"
	"github.com/gordonwei/orch/pkg/config"
	"github.com/gordonwei/orch/pkg/executor"
	"github.com/gordonwei/orch/pkg/memory"
	"github.com/gordonwei/orch/pkg/model"
	"github.com/gordonwei/orch/pkg/planner"
	"github.com/gordonwei/orch/pkg/registry"
	"github.com/gordonwei/orch/pkg/workflow"
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

	// 處理 subcommand（不需要 MLX server）
	if args.subcommand != "" {
		handleSubcommand(args, cfg, store)
		return
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

	// ===== stdin pipe integration =====
	stdinIsPipe := false
	var pipeData []byte
	stat, _ := os.Stdin.Stat()
	if (stat.Mode() & os.ModeCharDevice) == 0 {
		// stdin is a pipe
		stdinIsPipe = true
		pipeData, _ = io.ReadAll(os.Stdin)
	}

	prompt := args.prompt

	if stdinIsPipe && len(pipeData) > 0 {
		pipeStr := truncateStr(string(pipeData), 50000)
		if prompt == "" {
			// No prompt args given: treat pipe content AS the prompt
			prompt = strings.TrimSpace(string(pipeData))
			if len(prompt) > 50000 {
				prompt = prompt[:50000]
			}
		} else {
			// Prepend pipe content as context to the prompt
			prompt = fmt.Sprintf("Context (from stdin pipe, %d bytes):\n```\n%s\n```\n\nTask: %s", len(pipeData), pipeStr, prompt)
		}
	}

	if prompt != "" {
		runTask(reg, cfg, store, prompt, args.dryRun)
		return
	}

	// If stdin is a pipe, skip REPL mode (can't readline from pipe)
	if stdinIsPipe {
		fmt.Fprintf(os.Stderr, "⚠️  stdin is a pipe but no input received. Use: echo 'task' | orch\n")
		return
	}

	runREPL(reg, cfg, store)
}

// ===== Subcommand Handlers =====

func handleSubcommand(args cliArgs, cfg *config.Config, store *memory.Store) {
	switch args.subcommand {
	case "history":
		handleHistory(args.subArgs, store)
	case "briefing":
		handleBriefing(args.subArgs, cfg, store)
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

	// orch history (no sub-args) → list recent 20
	if len(subArgs) == 0 {
		entries, err := store.RecentHistory(20)
		if err != nil {
			fmt.Fprintf(os.Stderr, "❌ %v\n", err)
			os.Exit(1)
		}
		if len(entries) == 0 {
			fmt.Println("（無歷史紀錄）")
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
			fmt.Printf("（找不到包含 \"%s\" 的紀錄）\n", keyword)
			return
		}
		printHistoryEntries(entries)

	case "clear":
		// PruneHistory with 0 seconds → clear all
		count, err := store.PruneHistory(0)
		if err != nil {
			fmt.Fprintf(os.Stderr, "❌ %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("🗑️  已清除 %d 筆歷史紀錄\n", count)

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

	// orch briefing (no sub-args) → show current briefing
	if len(subArgs) == 0 {
		brief, t, err := store.GetBriefing()
		if err != nil {
			fmt.Fprintf(os.Stderr, "❌ %v\n", err)
			os.Exit(1)
		}
		if brief == "" {
			fmt.Println("（尚無 briefing）")
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
		fmt.Println("✅ briefing 已更新")

	case "gen":
		handleBriefingGen(cfg, store)

	default:
		fmt.Fprintf(os.Stderr, "Usage: orch briefing [set <text> | gen]\n")
		os.Exit(1)
	}
}

func handleBriefingGen(cfg *config.Config, store *memory.Store) {
	// 取最近 10 筆 history
	entries, err := store.RecentHistory(10)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ failed to read history: %v\n", err)
		os.Exit(1)
	}
	if len(entries) == 0 {
		fmt.Fprintf(os.Stderr, "⚠️  沒有歷史紀錄可供生成 briefing\n")
		os.Exit(1)
	}

	// 組合 history context
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

	// 使用 MLX 生成 briefing
	activeModel := cfg.ActiveModel()
	llmClient := model.NewOpenAIClient(model.OpenAIClientConfig{
		Endpoint: activeModel.Endpoint,
		Model:    activeModel.Model,
		Backend:  activeModel.Backend,
	})

	if !llmClient.Available() {
		fmt.Fprintf(os.Stderr, "❌ MLX server 不可用，無法生成 briefing\n")
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "🧠 generating briefing from recent %d tasks...\n", len(entries))

	prompt := fmt.Sprintf(`以下是使用者最近的任務歷史紀錄：

%s

請用繁體中文寫一段精簡的 briefing（一段話，不超過 200 字），摘要：
1. 最近主要在做什麼
2. 哪些成功、哪些失敗
3. 接下來可能需要關注的事項

只輸出 briefing 本文，不要加標題或格式。`, sb.String())

	messages := []model.Message{
		{Role: "system", Content: "你是一個精簡的任務摘要助手。用繁體中文回覆。"},
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

	fmt.Printf("✅ briefing 已生成並儲存：\n   %s\n", answer)
}

// printHistoryEntries 格式化輸出歷史紀錄
func printHistoryEntries(entries []memory.HistoryEntry) {
	for _, e := range entries {
		status := "✅"
		if !e.Success {
			status = "❌"
		}

		// 解析 timestamp 顯示
		ts := e.Timestamp
		if t, err := time.Parse(time.RFC3339, e.Timestamp); err == nil {
			ts = t.Format("2006-01-02 15:04")
		}

		// 格式化耗時
		took := fmt.Sprintf("%.1fs", float64(e.TookMs)/1000.0)

		// 截斷 input
		input := truncateStr(e.Input, 40)

		fmt.Printf("#%-3d  %s  %s  [%s] %s  (%s, %s)\n",
			e.ID, ts, status, e.Category, input, e.Agent, took)
	}
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

// ===== Dry Run =====

// printDryRun 輸出格式化的計畫預覽，包含 ASCII DAG 視覺化。
func printDryRun(plan *planner.Plan) {
	fmt.Println("📋 Dry Run — Plan Preview")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println()
	fmt.Printf("Task: %s\n", plan.TaskSummary)
	fmt.Printf("Difficulty: %s | Category: %s | Steps: %d\n", plan.Difficulty, plan.Category, len(plan.Steps))
	fmt.Println()

	// Render DAG
	dag := renderDAG(plan.Steps)
	fmt.Println("┌─────────────────────────────────────────┐")
	fmt.Println("│ DAG Execution Graph                      │")
	fmt.Println("├─────────────────────────────────────────┤")
	fmt.Println("│                                          │")
	for _, line := range strings.Split(dag, "\n") {
		if line == "" {
			continue
		}
		// Pad line to fit in the box (40 chars wide content area)
		padded := line
		runeLen := len([]rune(padded))
		if runeLen < 40 {
			padded = padded + strings.Repeat(" ", 40-runeLen)
		}
		fmt.Printf("│  %s│\n", padded)
	}
	fmt.Println("│                                          │")
	fmt.Println("└─────────────────────────────────────────┘")
	fmt.Println()

	// Step details
	fmt.Println("Steps:")
	for i, step := range plan.Steps {
		fmt.Printf("  %d. [%s] %s\n", i+1, step.ID, step.Description)

		// agent + command/prompt
		if step.Command != "" {
			fmt.Printf("     agent: %s | command: %s\n", step.Agent, step.Command)
		} else if step.Prompt != "" {
			promptPreview := truncateStr(step.Prompt, 50)
			fmt.Printf("     agent: %s | prompt: %s\n", step.Agent, promptPreview)
		} else {
			fmt.Printf("     agent: %s\n", step.Agent)
		}

		// depends_on + on_failure
		deps := "(none)"
		if len(step.DependsOn) > 0 {
			deps = strings.Join(step.DependsOn, ", ")
		}
		onFailure := step.OnFailure
		if onFailure == "" {
			onFailure = "fail"
		}
		fmt.Printf("     depends_on: %s | on_failure: %s\n", deps, onFailure)

		if i < len(plan.Steps)-1 {
			fmt.Println()
		}
	}
}

// renderDAG builds a text-based DAG visualization using topological levels.
// Steps at the same level (no dependencies between them) are shown on the same row.
func renderDAG(steps []planner.Step) string {
	if len(steps) == 0 {
		return "(empty plan)\n"
	}

	// Build index: stepID → position in steps slice
	stepIndex := make(map[string]int)
	for i, s := range steps {
		stepIndex[s.ID] = i
	}

	// Compute topological levels (longest path from root)
	levels := make([]int, len(steps))
	for i, s := range steps {
		maxDepLevel := -1
		for _, dep := range s.DependsOn {
			if idx, ok := stepIndex[dep]; ok {
				if levels[idx] > maxDepLevel {
					maxDepLevel = levels[idx]
				}
			}
		}
		levels[i] = maxDepLevel + 1
	}

	// Group steps by level
	maxLevel := 0
	for _, l := range levels {
		if l > maxLevel {
			maxLevel = l
		}
	}

	levelGroups := make([][]int, maxLevel+1)
	for i, l := range levels {
		levelGroups[l] = append(levelGroups[l], i)
	}

	var sb strings.Builder

	// Simple case: linear chain (each level has exactly 1 step)
	allSingle := true
	for _, group := range levelGroups {
		if len(group) != 1 {
			allSingle = false
			break
		}
	}
	if allSingle {
		for i, s := range steps {
			if i < len(steps)-1 {
				sb.WriteString(fmt.Sprintf("[%s] ──▶ ", s.ID))
			} else {
				sb.WriteString(fmt.Sprintf("[%s]", s.ID))
			}
		}
		sb.WriteString("\n")
		return sb.String()
	}

	// General case: show fan-in / fan-out patterns
	for lvl := 0; lvl <= maxLevel; lvl++ {
		group := levelGroups[lvl]

		if lvl < maxLevel {
			nextGroup := levelGroups[lvl+1]

			if len(group) == 1 && len(nextGroup) == 1 {
				// One-to-one
				sb.WriteString(fmt.Sprintf("[%s] ──▶ ", steps[group[0]].ID))
			} else if len(group) > 1 && len(nextGroup) >= 1 {
				// Fan-in: multiple steps converge to next level
				for i, idx := range group {
					if i == 0 {
						sb.WriteString(fmt.Sprintf("[%s] ──┐\n", steps[idx].ID))
					} else if i == len(group)-1 {
						sb.WriteString(fmt.Sprintf("[%s] ──┘\n", steps[idx].ID))
					} else {
						sb.WriteString(fmt.Sprintf("[%s] ──┤\n", steps[idx].ID))
					}
				}
				// Draw the merge point → next level targets
				padding := strings.Repeat(" ", 12)
				for _, nIdx := range nextGroup {
					sb.WriteString(fmt.Sprintf("%s├──▶ [%s]\n", padding, steps[nIdx].ID))
				}
				lvl++ // skip the next level since we already drew it
			} else if len(group) == 1 && len(nextGroup) > 1 {
				// Fan-out: one step fans out to multiple
				sb.WriteString(fmt.Sprintf("[%s] ──┬──▶ [%s]\n", steps[group[0]].ID, steps[nextGroup[0]].ID))
				padding := strings.Repeat(" ", len(steps[group[0]].ID)+3)
				for i := 1; i < len(nextGroup); i++ {
					connector := "├"
					if i == len(nextGroup)-1 {
						connector = "└"
					}
					sb.WriteString(fmt.Sprintf("%s%s──▶ [%s]\n", padding, connector, steps[nextGroup[i]].ID))
				}
				lvl++ // skip next level
			} else {
				// Fallback: just list
				for _, idx := range group {
					sb.WriteString(fmt.Sprintf("[%s]\n", steps[idx].ID))
				}
				sb.WriteString("  │\n  ▼\n")
			}
		} else {
			// Last level
			for _, idx := range group {
				sb.WriteString(fmt.Sprintf("[%s]\n", steps[idx].ID))
			}
		}
	}

	return sb.String()
}

func runTask(reg *registry.Registry, cfg *config.Config, store *memory.Store, prompt string, dryRun bool) {
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

	// ===== --dry-run: print plan preview and exit =====
	if dryRun {
		fmt.Fprintf(os.Stderr, "\n")
		printDryRun(plan)
		return
	}

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
	fmt.Fprintf(rl.Stdout(), "   type your request, /help for commands, ctrl+d to quit\n\n")

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

		// Slash command 處理
		if strings.HasPrefix(input, "/") {
			handleSlashCommand(rl, reg, cfg, store, input)
			continue
		}

		runTask(reg, cfg, store, input, false)
		fmt.Fprintln(os.Stderr)
	}

	fmt.Fprintln(os.Stderr, "👋 bye")
}

// ===== REPL Slash Commands =====

// handleSlashCommand 處理所有 / 開頭的 REPL 特殊命令
func handleSlashCommand(rl *readline.Instance, reg *registry.Registry, cfg *config.Config, store *memory.Store, input string) {
	parts := strings.Fields(input)
	cmd := strings.ToLower(parts[0])
	args := parts[1:]

	switch cmd {
	case "/help":
		printREPLHelp()

	case "/w", "/workflows":
		if len(args) > 0 {
			// /w <number> — 直接執行指定編號
			handleWorkflowExec(rl, reg, cfg, store, args[0])
		} else {
			// /w — 列出工作流選單，等使用者選擇
			handleWorkflowMenu(rl, reg, cfg, store)
		}

	case "/h", "/history":
		replHistory(store)

	case "/b", "/briefing":
		replBriefing(store)

	default:
		fmt.Fprintf(os.Stderr, "❓ 未知命令: %s（輸入 /help 查看可用命令）\n", cmd)
	}
}

// printREPLHelp 列出所有 REPL 命令
func printREPLHelp() {
	fmt.Fprintf(os.Stderr, `
📖 REPL 命令：
  /w, /workflows     — 列出所有可用工作流
  /w <number>        — 執行指定編號的工作流
  /h, /history       — 列出最近 10 筆歷史
  /b, /briefing      — 顯示當前 briefing
  /help              — 列出所有 REPL 命令
  tools              — 列出已註冊工具
  exit, quit, q      — 離開

`)
}

// handleWorkflowMenu 列出工作流選單並等待使用者選擇
func handleWorkflowMenu(rl *readline.Instance, reg *registry.Registry, cfg *config.Config, store *memory.Store) {
	workflows, err := workflow.LoadAll(cfg.Workflows.Dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ 載入工作流失敗: %v\n", err)
		return
	}
	if len(workflows) == 0 {
		fmt.Fprintf(os.Stderr, "📋 目前沒有可用工作流（目錄: %s）\n", cfg.Workflows.Dir)
		return
	}

	// 列出工作流
	fmt.Fprintf(os.Stderr, "📋 可用工作流：\n")
	for i, w := range workflows {
		fmt.Fprintf(os.Stderr, "  [%d] %s — %s\n", i+1, w.Name, w.Description)
	}
	fmt.Fprintf(os.Stderr, "\n輸入編號執行，或按 Enter 取消：")

	// 讀取使用者選擇
	oldPrompt := rl.Config.Prompt
	rl.SetPrompt("")
	choice, err := rl.Readline()
	rl.SetPrompt(oldPrompt)

	if err != nil {
		return
	}

	choice = strings.TrimSpace(choice)
	if choice == "" {
		fmt.Fprintf(os.Stderr, "（已取消）\n")
		return
	}

	handleWorkflowExec(rl, reg, cfg, store, choice)
}

// handleWorkflowExec 執行指定編號的工作流
func handleWorkflowExec(rl *readline.Instance, reg *registry.Registry, cfg *config.Config, store *memory.Store, numStr string) {
	idx, err := strconv.Atoi(numStr)
	if err != nil || idx < 1 {
		fmt.Fprintf(os.Stderr, "❌ 無效的工作流編號: %s\n", numStr)
		return
	}

	workflows, err := workflow.LoadAll(cfg.Workflows.Dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ 載入工作流失敗: %v\n", err)
		return
	}

	if idx > len(workflows) {
		fmt.Fprintf(os.Stderr, "❌ 工作流編號 %d 不存在（共 %d 個）\n", idx, len(workflows))
		return
	}

	selected := &workflows[idx-1]
	fmt.Fprintf(os.Stderr, "🚀 執行工作流: %s\n", selected.Name)

	// 轉換為 Plan
	plan := workflow.ToPlanner(selected, nil, cfg)

	fmt.Fprintf(os.Stderr, "📝 %s\n", plan.TaskSummary)
	fmt.Fprintf(os.Stderr, "   difficulty: %s | category: %s | steps: %d\n",
		plan.Difficulty, plan.Category, len(plan.Steps))
	fmt.Fprintf(os.Stderr, "\n⚡ executing...\n")

	// 建立步驟生命週期事件 channel
	stepEvents := make(chan executor.StepEvent, 64)
	stepPrinterWg := startEventPrinter(stepEvents)

	// 建立逐行輸出串流事件 channel
	outputEvents := make(chan executor.OutputEvent, 256)
	outputPrinterWg := startOutputPrinter(outputEvents)

	e := executor.New(cfg)
	e.EventChan = stepEvents
	e.OutputEvents = outputEvents

	result := e.Execute(plan)

	// 等待事件印完
	stepPrinterWg.Wait()
	close(outputEvents)
	outputPrinterWg.Wait()

	// Report
	fmt.Fprintf(os.Stderr, "\n")
	if result.Success {
		fmt.Fprintf(os.Stderr, "🏁 workflow complete (%s)\n", result.Took.Round(100_000_000))
		if len(result.Steps) > 0 {
			last := result.Steps[len(result.Steps)-1]
			if last.Output != "" {
				fmt.Print(last.Output)
			}
		}
	} else {
		fmt.Fprintf(os.Stderr, "💀 workflow failed after %s\n", result.Took.Round(100_000_000))
		if result.Err != nil {
			fmt.Fprintf(os.Stderr, "   error: %v\n", result.Err)
		}
		for _, s := range result.Steps {
			if s.Err != nil {
				fmt.Fprintf(os.Stderr, "   failed at [%s]: %v\n", s.StepID, s.Err)
			}
		}
	}

	// 寫 history
	if store != nil {
		var outputSummary string
		if len(result.Steps) > 0 {
			last := result.Steps[len(result.Steps)-1]
			outputSummary = truncateStr(last.Output, 500)
		}
		store.AddHistory(memory.HistoryEntry{
			Input:         fmt.Sprintf("[workflow] %s", selected.Name),
			Category:      "workflow",
			Agent:         "workflow",
			OutputSummary: outputSummary,
			Success:       result.Success,
			Tags:          []string{"workflow", selected.Name},
			TookMs:        result.Took.Milliseconds(),
		})
	}

	fmt.Fprintln(os.Stderr)
}

// replHistory 顯示最近 10 筆歷史
func replHistory(store *memory.Store) {
	if store == nil {
		fmt.Fprintf(os.Stderr, "⚠️  memory store 未啟用\n")
		return
	}

	entries, err := store.RecentHistory(10)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ 讀取歷史失敗: %v\n", err)
		return
	}
	if len(entries) == 0 {
		fmt.Fprintf(os.Stderr, "📜 尚無歷史紀錄\n")
		return
	}

	fmt.Fprintf(os.Stderr, "📜 最近 %d 筆歷史：\n", len(entries))
	for _, e := range entries {
		status := "✅"
		if !e.Success {
			status = "❌"
		}
		summary := truncateStr(e.Input, 60)
		fmt.Fprintf(os.Stderr, "  %s [%s] %s — %s\n", status, e.Timestamp, e.Category, summary)
	}
	fmt.Fprintln(os.Stderr)
}

// replBriefing 顯示當前 briefing
func replBriefing(store *memory.Store) {
	if store == nil {
		fmt.Fprintf(os.Stderr, "⚠️  memory store 未啟用\n")
		return
	}

	brief, t, err := store.GetBriefing()
	if err != nil || brief == "" {
		fmt.Fprintf(os.Stderr, "📋 目前沒有 briefing（使用 orch briefing 產生）\n")
		return
	}

	fmt.Fprintf(os.Stderr, "📋 briefing (generated %s):\n   %s\n\n", t.Format("01/02 15:04"), brief)
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
	prompt     string
	showTools  bool
	dryRun     bool
	subcommand string   // "history", "briefing", or ""
	subArgs    []string // remaining args after subcommand
}

func parseArgs() cliArgs {
	args := cliArgs{}

	if len(os.Args) < 2 {
		return args
	}

	// Check for subcommands first
	switch os.Args[1] {
	case "history", "briefing":
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
  orch <prompt>              Oneshot: plan → execute → deliver
  orch                       REPL mode: continuous interaction
  orch --tools               Show available tools
  orch --dry-run <prompt>    Plan only, don't execute

Subcommands:
  orch history               列出最近 20 筆任務歷史
  orch history search <kw>   搜尋歷史
  orch history clear         清除所有歷史

  orch briefing              顯示當前 briefing
  orch briefing set <text>   手動設定 briefing
  orch briefing gen          用 MLX 從近期 history 自動生成 briefing

Examples:
  orch "查 S3 bucket 用量"
  orch --dry-run "整合 AWS 和 GCP 用量報告"
  orch "把 litellm helm values 加上 rate limiting"
  kubectl get pods -o json | orch "哪些 pod 不健康？"
  cat error.log | orch "分析這個錯誤"
  orch history search "kubectl"
  orch briefing gen
`)
}
