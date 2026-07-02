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

		if step.Command != "" {
			fmt.Printf("     agent: %s | command: %s\n", step.Agent, step.Command)
		} else if step.Prompt != "" {
			promptPreview := truncateStr(step.Prompt, 50)
			fmt.Printf("     agent: %s | prompt: %s\n", step.Agent, promptPreview)
		} else {
			fmt.Printf("     agent: %s\n", step.Agent)
		}

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
