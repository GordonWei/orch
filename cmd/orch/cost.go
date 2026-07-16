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
//	orch cost          — show all-time summary
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
		// show last 20 entries
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
		printUsageSince(store, time.Now().Truncate(24*time.Hour))
	case "week":
		printUsageSince(store, time.Now().AddDate(0, 0, -7))
	case "month":
		printUsageSince(store, time.Now().AddDate(0, -1, 0))
	default:
		// all-time summary
		summaries, err := store.GetUsageSummary()
		if err != nil {
			fmt.Fprintf(os.Stderr, "❌ %v\n", err)
			return
		}
		if len(summaries) == 0 {
			fmt.Println("No API usage recorded yet.")
			fmt.Println("\nEnable Bedrock or Vertex AI in config.yaml to start tracking costs.")
			return
		}
		printUsageSummary(summaries)
	}
}

func printUsageSummary(summaries []memory.UsageSummary) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "BACKEND\tMODEL\tCALLS\tINPUT TOKENS\tOUTPUT TOKENS\tCOST (USD)")
	fmt.Fprintln(w, "-------\t-----\t-----\t------------\t-------------\t----------")

	var totalCost float64
	var totalCalls int
	for _, s := range summaries {
		fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\t$%.4f\n",
			s.Backend, truncateModel(s.Model), s.TotalCalls,
			formatTokens(s.TotalInput), formatTokens(s.TotalOutput), s.TotalCostUSD)
		totalCost += s.TotalCostUSD
		totalCalls += s.TotalCalls
	}
	fmt.Fprintf(w, "\nTOTAL\t\t%d\t\t\t$%.4f\n", totalCalls, totalCost)
	w.Flush()
}

func printUsageSince(store *memory.Store, since time.Time) {
	summaries, err := store.GetUsageSince(since)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ %v\n", err)
		return
	}
	if len(summaries) == 0 {
		fmt.Printf("No API usage since %s.\n", since.Format("2006-01-02"))
		return
	}
	fmt.Printf("— Usage since %s —\n\n", since.Format("2006-01-02 15:04"))
	printUsageSummary(summaries)
}

func printRecentUsage(entries []memory.APIUsageEntry) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "TIME\tBACKEND\tMODEL\tIN/OUT\tCOST\tLATENCY\tPROMPT")
	fmt.Fprintln(w, "----\t-------\t-----\t------\t----\t-------\t------")
	for _, e := range entries {
		ts := e.Timestamp
		if len(ts) > 16 {
			ts = ts[:16] // trim seconds
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%d/%d\t$%.4f\t%dms\t%s\n",
			ts, e.Backend, truncateModel(e.Model),
			e.InputTokens, e.OutputTokens, e.CostUSD, e.LatencyMs,
			truncatePrompt(e.PromptPreview, 40))
	}
	w.Flush()
}

func truncateModel(model string) string {
	// Show last part after the last dot or slash for readability
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
