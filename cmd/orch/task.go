package main

import (
	"context"
	"fmt"
	"os"
	"strings"
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
			e.HookRunner = hookRunner
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
		// Ground DirectChat in whatever briefing is currently cached (kept
		// fresh on boot when cfg.Memory.BriefingSourceFile is set — see
		// generateBriefingFromFile) instead of the model answering "what's
		// pending" from nothing.
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
	e.HookRunner = hookRunner

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

				// MLX Gate
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

				// Output summarization
				chainPrompt := action.Prompt
				if rule != nil && rule.Then.Summarize && mlxAvailable && len(output) > 1000 {
					fmt.Fprintf(os.Stderr, "\n📝 summarizing output (%d chars → MLX)...\n", len(output))
					summary, err := llmClient.Chat([]model.Message{
						{Role: "system", Content: "Summarize the following text concisely. Keep key facts, remove verbosity."},
						{Role: "user", Content: output},
					}, &model.ChatOptions{MaxTokens: 512, Temperature: 0.1})
					if err == nil && summary != "" {
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

				b := br.Resolve(action.Agent)
				if b == nil {
					fmt.Fprintf(os.Stderr, "   ⚠️  no backend for %q, skipping\n", action.Agent)
					continue
				}

				chainOutput, err := b.Execute(chainPrompt, "")
				if err != nil {
					fmt.Fprintf(os.Stderr, "   ❌ chain failed: %v\n", err)
					fmt.Fprintf(os.Stderr, "   💡 to retry: orch \"%s\"\n", truncateStr(action.Prompt, 80))
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
