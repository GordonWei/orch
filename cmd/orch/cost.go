package main

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/gordonwei/orch/pkg/memory"
)

// handleCostCmd displays API usage cost statistics.
// Subcommands:
//
//	orch cost          — show all-time dashboard
//	orch cost recent   — show last 20 API calls
//	orch cost today    — show today's usage
//	orch cost week     — show last 7 days
//	orch cost month    — show last 30 days
func handleCostCmd(store *memory.Store, args []string) {
	if store == nil {
		fmt.Fprintln(os.Stderr, "❌ memory store not available")
		return
	}

	subcmd := ""
	if len(args) > 0 {
		subcmd = args[0]
	}

	switch subcmd {
	case "recent":
		entries, err := store.RecentAPIUsage(20)
		if err != nil {
			fmt.Fprintf(os.Stderr, "❌ %v\n", err)
			return
		}
		if len(entries) == 0 {
			fmt.Println("No API usage recorded yet.")
			return
		}
		printRecentUsage(entries)

	case "today":
		since := time.Now().Truncate(24 * time.Hour)
		printDashboard(store, since, "Today")
	case "week":
		since := time.Now().AddDate(0, 0, -7)
		printDashboard(store, since, "Last 7 days")
	case "month":
		since := time.Now().AddDate(0, -1, 0)
		printDashboard(store, since, "Last 30 days")
	default:
		// all-time dashboard
		printDashboard(store, time.Time{}, "All time")
	}
}

func printDashboard(store *memory.Store, since time.Time, label string) {
	// Header
	fmt.Printf("╔══════════════════════════════════════════════╗\n")
	fmt.Printf("║  📊 orch cost dashboard — %s\n", label)
	if !since.IsZero() {
		fmt.Printf("║  📅 since %s\n", since.Format("2006-01-02 15:04"))
	}
	fmt.Printf("╚══════════════════════════════════════════════╝\n\n")

	// --- Routing Stats ---
	var routingStats []memory.RoutingStat
	var err error
	if since.IsZero() {
		routingStats, err = store.GetRoutingStats()
	} else {
		routingStats, err = store.GetRoutingStatsSince(since)
	}
	if err == nil && len(routingStats) > 0 {
		printRoutingStats(routingStats)
		fmt.Println()
	}

	// --- API Usage Summary ---
	var summaries []memory.UsageSummary
	if since.IsZero() {
		summaries, err = store.GetUsageSummary()
	} else {
		summaries, err = store.GetUsageSince(since)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ %v\n", err)
		return
	}
	if len(summaries) == 0 {
		fmt.Println("No API usage recorded yet.")
		fmt.Println("\nEnable Bedrock or Vertex AI in config.yaml to start tracking costs.")
		return
	}
	fmt.Println("💰 API Cost Breakdown:")
	printUsageSummary(summaries)

	// --- Daily Trend (only for week/month) ---
	if !since.IsZero() {
		days := int(time.Since(since).Hours() / 24)
		if days >= 2 {
			dailyCosts, err := store.GetDailyCosts(days)
			if err == nil && len(dailyCosts) > 0 {
				fmt.Println()
				printDailyTrend(dailyCosts)
			}
		}
	}
}

func printRoutingStats(stats []memory.RoutingStat) {
	total := 0
	localCount := 0
	cloudCount := 0
	for _, s := range stats {
		total += s.Count
		switch s.Agent {
		case "local", "mlx", "chat":
			localCount += s.Count
		default:
			cloudCount += s.Count
		}
	}

	localPct := 0.0
	if total > 0 {
		localPct = float64(localCount) / float64(total) * 100
	}

	fmt.Println("🔀 Routing Distribution:")
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, s := range stats {
		pct := float64(s.Count) / float64(total) * 100
		bar := renderBar(pct, 20)
		fmt.Fprintf(w, "  %s\t%s %s\t%d calls (%.0f%%)\n", agentIcon(s.Agent), s.Agent, bar, s.Count, pct)
	}
	w.Flush()
	fmt.Printf("\n  📌 Local routing: %.0f%% (%d/%d) — cloud calls saved!\n", localPct, localCount, total)
}

func printUsageSummary(summaries []memory.UsageSummary) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "  BACKEND\tMODEL\tCALLS\tINPUT\tOUTPUT\tCOST")
	fmt.Fprintln(w, "  -------\t-----\t-----\t-----\t------\t----")

	var totalCost float64
	var totalCalls int
	var totalInput, totalOutput int
	for _, s := range summaries {
		fmt.Fprintf(w, "  %s\t%s\t%d\t%s\t%s\t$%.4f\n",
			s.Backend, truncateModel(s.Model), s.TotalCalls,
			formatTokens(s.TotalInput), formatTokens(s.TotalOutput), s.TotalCostUSD)
		totalCost += s.TotalCostUSD
		totalCalls += s.TotalCalls
		totalInput += s.TotalInput
		totalOutput += s.TotalOutput
	}
	fmt.Fprintln(w, "  -------\t-----\t-----\t-----\t------\t----")
	fmt.Fprintf(w, "  TOTAL\t\t%d\t%s\t%s\t$%.4f\n",
		totalCalls, formatTokens(totalInput), formatTokens(totalOutput), totalCost)
	w.Flush()
}

func printDailyTrend(costs []memory.DailyCost) {
	fmt.Println("📈 Daily Trend:")
	maxCost := 0.0
	for _, dc := range costs {
		if dc.CostUSD > maxCost {
			maxCost = dc.CostUSD
		}
	}

	for _, dc := range costs {
		bar := ""
		if maxCost > 0 {
			bar = renderBar(dc.CostUSD/maxCost*100, 15)
		}
		fmt.Printf("  %s  %s $%.4f (%d calls)\n", dc.Date, bar, dc.CostUSD, dc.Calls)
	}
}

func printRecentUsage(entries []memory.APIUsageEntry) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "TIME\tBACKEND\tMODEL\tIN/OUT\tCOST\tLATENCY\tPROMPT")
	fmt.Fprintln(w, "----\t-------\t-----\t------\t----\t-------\t------")
	for _, e := range entries {
		ts := e.Timestamp
		if len(ts) > 16 {
			ts = ts[:16]
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%d/%d\t$%.4f\t%dms\t%s\n",
			ts, e.Backend, truncateModel(e.Model),
			e.InputTokens, e.OutputTokens, e.CostUSD, e.LatencyMs,
			truncatePrompt(e.PromptPreview, 40))
	}
	w.Flush()
}

func renderBar(pct float64, width int) string {
	filled := int(pct / 100.0 * float64(width))
	if filled > width {
		filled = width
	}
	if filled < 0 {
		filled = 0
	}
	return "[" + strings.Repeat("█", filled) + strings.Repeat("░", width-filled) + "]"
}

func agentIcon(agent string) string {
	switch agent {
	case "local", "mlx", "chat":
		return "🍎"
	case "kiro":
		return "🤖"
	case "claude":
		return "🟣"
	case "gemini":
		return "💎"
	case "shell":
		return "🐚"
	case "bedrock":
		return "☁️"
	case "vertexai":
		return "☁️"
	default:
		return "•"
	}
}

func truncateModel(model string) string {
	if idx := strings.LastIndex(model, "/"); idx >= 0 {
		return model[idx+1:]
	}
	if len(model) > 30 {
		return model[:27] + "..."
	}
	return model
}

func formatTokens(n int) string {
	if n >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
	if n >= 1_000 {
		return fmt.Sprintf("%.1fK", float64(n)/1_000)
	}
	return fmt.Sprintf("%d", n)
}

func truncatePrompt(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > max {
		return s[:max-3] + "..."
	}
	return s
}
