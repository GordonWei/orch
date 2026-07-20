package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gordonwei/orch/pkg/config"
	"github.com/gordonwei/orch/pkg/memory"
	"github.com/gordonwei/orch/pkg/model"
)

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
