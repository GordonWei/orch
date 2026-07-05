package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/chzyer/readline"
	"github.com/gordonwei/orch/pkg/backend"
	"github.com/gordonwei/orch/pkg/config"
	"github.com/gordonwei/orch/pkg/eventbus"
	"github.com/gordonwei/orch/pkg/executor"
	"github.com/gordonwei/orch/pkg/memory"
	"github.com/gordonwei/orch/pkg/registry"
	"github.com/gordonwei/orch/pkg/workflow"
)

func runREPL(reg *registry.Registry, cfg *config.Config, store *memory.Store, br *backend.Registry, bus *eventbus.Bus) {
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

	// Session context: keeps recent conversation turns for backend context injection
	session := &sessionContext{maxTurns: 5}

	fmt.Fprintf(rl.Stdout(), "🟢 orch %s — AI Chief of Staff\n", version)
	fmt.Fprintf(rl.Stdout(), "   tools: %s\n", toolNames(reg))
	fmt.Fprintf(rl.Stdout(), "   type your request, /help for commands, ctrl+d to quit\n\n")

	for {
		line, err := rl.Readline()
		if err == readline.ErrInterrupt {
			continue
		}
		if err != nil {
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

		// Slash command
		if strings.HasPrefix(input, "/") {
			handleSlashCommand(rl, reg, cfg, store, br, input)
			continue
		}

		// Build context-enriched prompt for backend
		// Planning/classification uses raw input; session context is prepended only for backend execution
		sessionCtx := session.buildContext()
		enrichedInput := input
		if sessionCtx != "" {
			enrichedInput = fmt.Sprintf("[Prior conversation for context]\n%s\n[End prior conversation]\n\nCurrent request: %s", sessionCtx, input)
		}

		// runTask prints the output itself (immediately, before event-bus chains run).
		// The returned value is used ONLY to feed session context — do not print it here,
		// or every REPL reply appears twice.
		_, output := runTask(nil, reg, cfg, store, br, bus, enrichedInput, false)

		// Store only the raw input/output in session (not the enriched version)
		session.add(input, output)
		fmt.Fprintln(os.Stderr)
	}

	fmt.Fprintln(os.Stderr, "👋 bye")
}

// sessionContext maintains a sliding window of recent conversation turns.
type sessionContext struct {
	turns    []sessionTurn
	maxTurns int
}

type sessionTurn struct {
	input  string
	output string
}

func (s *sessionContext) add(input, output string) {
	s.turns = append(s.turns, sessionTurn{
		input:  truncateStr(input, 200),
		output: truncateStr(output, 200),
	})
	if len(s.turns) > s.maxTurns {
		s.turns = s.turns[len(s.turns)-s.maxTurns:]
	}
}

// buildContext returns a compact context string for backend injection.
// Only includes turns that have meaningful output.
func (s *sessionContext) buildContext() string {
	if len(s.turns) == 0 {
		return ""
	}
	var parts []string
	for _, t := range s.turns {
		if t.output == "" {
			parts = append(parts, fmt.Sprintf("User: %s", t.input))
		} else {
			parts = append(parts, fmt.Sprintf("User: %s\nAssistant: %s", t.input, t.output))
		}
	}
	return strings.Join(parts, "\n---\n")
}

// ===== REPL Slash Commands =====

func handleSlashCommand(rl *readline.Instance, reg *registry.Registry, cfg *config.Config, store *memory.Store, br *backend.Registry, input string) {
	parts := strings.Fields(input)
	cmd := strings.ToLower(parts[0])
	args := parts[1:]

	switch cmd {
	case "/help":
		printREPLHelp()

	case "/w", "/workflows":
		if len(args) > 0 {
			handleWorkflowExec(rl, reg, cfg, store, br, args[0])
		} else {
			handleWorkflowMenu(rl, reg, cfg, store, br)
		}

	case "/h", "/history":
		replHistory(store)

	case "/b", "/briefing":
		replBriefing(store)

	default:
		fmt.Fprintf(os.Stderr, "❓ unknown command: %s (type /help for available commands)\n", cmd)
	}
}

func printREPLHelp() {
	fmt.Fprintf(os.Stderr, `
📖 REPL Commands:
  /w, /workflows     — list all available workflows
  /w <number>        — execute workflow by number
  /h, /history       — last 10 history entries
  /b, /briefing      — show current briefing
  /help              — show this help
  tools              — list registered tools
  exit, quit, q      — exit

`)
}

func handleWorkflowMenu(rl *readline.Instance, reg *registry.Registry, cfg *config.Config, store *memory.Store, br *backend.Registry) {
	workflows, err := workflow.LoadAll(cfg.Workflows.Dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ failed to load workflows: %v\n", err)
		return
	}
	if len(workflows) == 0 {
		fmt.Fprintf(os.Stderr, "📋 no workflows available (dir: %s)\n", cfg.Workflows.Dir)
		return
	}

	fmt.Fprintf(os.Stderr, "📋 Available workflows:\n")
	for i, w := range workflows {
		fmt.Fprintf(os.Stderr, "  [%d] %s — %s\n", i+1, w.Name, w.Description)
	}
	fmt.Fprintf(os.Stderr, "\nEnter number to execute, or press Enter to cancel: ")

	oldPrompt := rl.Config.Prompt
	rl.SetPrompt("")
	choice, err := rl.Readline()
	rl.SetPrompt(oldPrompt)

	if err != nil {
		return
	}

	choice = strings.TrimSpace(choice)
	if choice == "" {
		fmt.Fprintf(os.Stderr, "(cancelled)\n")
		return
	}

	handleWorkflowExec(rl, reg, cfg, store, br, choice)
}

func handleWorkflowExec(rl *readline.Instance, reg *registry.Registry, cfg *config.Config, store *memory.Store, br *backend.Registry, numStr string) {
	idx, err := strconv.Atoi(numStr)
	if err != nil || idx < 1 {
		fmt.Fprintf(os.Stderr, "❌ invalid workflow number: %s\n", numStr)
		return
	}

	workflows, err := workflow.LoadAll(cfg.Workflows.Dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ failed to load workflows: %v\n", err)
		return
	}

	if idx > len(workflows) {
		fmt.Fprintf(os.Stderr, "❌ workflow #%d does not exist (total: %d)\n", idx, len(workflows))
		return
	}

	selected := &workflows[idx-1]
	fmt.Fprintf(os.Stderr, "🚀 executing workflow: %s\n", selected.Name)

	plan := workflow.ToPlanner(selected, nil, cfg)

	fmt.Fprintf(os.Stderr, "📝 %s\n", plan.TaskSummary)
	fmt.Fprintf(os.Stderr, "   difficulty: %s | category: %s | steps: %d\n",
		plan.Difficulty, plan.Category, len(plan.Steps))
	fmt.Fprintf(os.Stderr, "\n⚡ executing...\n")

	stepEvents := make(chan executor.StepEvent, 64)
	stepPrinterWg := startEventPrinter(stepEvents)

	outputEvents := make(chan executor.OutputEvent, 256)
	outputPrinterWg := startOutputPrinter(outputEvents)

	e := executor.New(cfg, br)
	e.EventChan = stepEvents
	e.OutputEvents = outputEvents

	result := e.Execute(plan)

	stepPrinterWg.Wait()
	close(outputEvents)
	outputPrinterWg.Wait()

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

func replHistory(store *memory.Store) {
	if store == nil {
		fmt.Fprintf(os.Stderr, "⚠️  memory store not available\n")
		return
	}

	entries, err := store.RecentHistory(10)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ failed to read history: %v\n", err)
		return
	}
	if len(entries) == 0 {
		fmt.Fprintf(os.Stderr, "📜 no history entries\n")
		return
	}

	fmt.Fprintf(os.Stderr, "📜 last %d history entries:\n", len(entries))
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

func replBriefing(store *memory.Store) {
	if store == nil {
		fmt.Fprintf(os.Stderr, "⚠️  memory store not available\n")
		return
	}

	brief, t, err := store.GetBriefing()
	if err != nil || brief == "" {
		fmt.Fprintf(os.Stderr, "📋 no briefing available (use `orch briefing gen` to generate)\n")
		return
	}

	fmt.Fprintf(os.Stderr, "📋 briefing (generated %s):\n   %s\n\n", t.Format("01/02 15:04"), brief)
}
