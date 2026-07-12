package main

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gordonwei/orch/pkg/executor"
	"github.com/gordonwei/orch/pkg/memory"
	"github.com/gordonwei/orch/pkg/planner"
)

// ===== Step Event Printer =====

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

func printStepEvent(ev executor.StepEvent) {
	switch ev.Type {
	case executor.EventStepStart:
		fmt.Fprintf(os.Stderr, "⏳ [%s] Starting...\n", ev.StepID)
	case executor.EventStepDone:
		if ev.Result != nil {
			fmt.Fprintf(os.Stderr, "✅ [%s] Done (%s)\n", ev.StepID, ev.Result.Took.Round(100*time.Millisecond))
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

// ===== Output Event Printer =====

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

func printOutputEvent(ev executor.OutputEvent) {
	switch ev.Type {
	case executor.OutputLine:
		fmt.Fprintf(os.Stderr, "\033[2m   │ [%s] %s\033[0m\n", ev.StepID, ev.Message)
	case executor.OutputProgress:
		fmt.Fprintf(os.Stderr, "   ⋯ [%s] %s\n", ev.StepID, ev.Message)
	}
}

// ===== History Printer =====

func printHistoryEntries(entries []memory.HistoryEntry) {
	for _, e := range entries {
		status := "✅"
		if !e.Success {
			status = "❌"
		}

		ts := e.Timestamp
		if t, err := time.Parse(time.RFC3339, e.Timestamp); err == nil {
			ts = t.Format("2006-01-02 15:04")
		}

		took := fmt.Sprintf("%.1fs", float64(e.TookMs)/1000.0)
		input := truncateStr(e.Input, 40)

		fmt.Printf("#%-3d  %s  %s  [%s] %s  (%s, %s)\n",
			e.ID, ts, status, e.Category, input, e.Agent, took)
	}
}

// ===== Dry Run Printer =====

func printDryRun(plan *planner.Plan) {
	fmt.Println("📋 Execution Plan (dry-run):")
	fmt.Printf("   Task: %s\n", plan.TaskSummary)
	fmt.Printf("   Difficulty: %s | Category: %s | Steps: %d\n", plan.Difficulty, plan.Category, len(plan.Steps))
	fmt.Println()

	if len(plan.Steps) == 0 {
		fmt.Println("   (no steps)")
		return
	}

	// Single step: simple format, no tree lines
	if len(plan.Steps) == 1 {
		step := plan.Steps[0]
		fmt.Printf("   ── [%s] %s (agent: %s)\n", step.ID, step.Description, step.Agent)
		detail := stepDetail(step)
		if detail != "" {
			fmt.Printf("         └─ %s\n", detail)
		}
		return
	}

	// Multiple steps: DAG tree visualization
	for i, step := range plan.Steps {
		// Choose tree connector
		var connector string
		var childPrefix string
		if i == 0 {
			connector = "┌─"
			childPrefix = "│"
		} else if i == len(plan.Steps)-1 {
			connector = "└─"
			childPrefix = " "
		} else {
			connector = "├─"
			childPrefix = "│"
		}

		// Build the step header line
		header := fmt.Sprintf("[%s] %s (agent: %s)", step.ID, step.Description, step.Agent)
		if len(step.DependsOn) > 0 {
			header += " ← depends on: " + strings.Join(step.DependsOn, ", ")
		}

		fmt.Printf("   %s %s\n", connector, header)

		// Show command or prompt detail
		detail := stepDetail(step)
		if detail != "" {
			fmt.Printf("   %s     └─ %s\n", childPrefix, detail)
		}
	}
}

// stepDetail returns a truncated "command: ..." or "prompt: ..." string for a step.
func stepDetail(step planner.Step) string {
	if step.Command != "" {
		return "command: " + truncateStr(step.Command, 80)
	}
	if step.Prompt != "" {
		return "prompt: " + truncateStr(step.Prompt, 80)
	}
	return ""
}
