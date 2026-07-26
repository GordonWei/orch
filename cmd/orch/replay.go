package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/gordonwei/orch/pkg/config"
	"github.com/gordonwei/orch/pkg/memory"
)

// handleReplay recalls a previous task's input from history so you can run it
// again. It does not re-execute automatically — orch has no registry/backend
// wiring available in this subcommand context, so it prints the original
// input and lets you pipe or copy-paste it back into orch yourself.
//
//	orch replay         — recall the most recent task
//	orch replay list    — show last 10 tasks, pick one
//	orch replay <N>     — recall history entry number N (from `orch replay list`)
func handleReplay(args []string, cfg *config.Config, store *memory.Store) {
	if store == nil {
		fmt.Fprintln(os.Stderr, "❌ memory store not available")
		os.Exit(1)
	}

	subcmd := ""
	if len(args) > 0 {
		subcmd = args[0]
	}

	switch {
	case subcmd == "list" || subcmd == "ls":
		entries, err := store.RecentHistory(10)
		if err != nil {
			fmt.Fprintf(os.Stderr, "❌ %v\n", err)
			os.Exit(1)
		}
		if len(entries) == 0 {
			fmt.Println("No history entries yet.")
			return
		}
		printReplayList(entries)
		fmt.Fprintf(os.Stderr, "\n💡 Use: orch replay <number> to recall it\n")

	case subcmd == "" || subcmd == "last":
		// Replay last successful non-chat task
		entries, err := store.RecentHistory(20)
		if err != nil {
			fmt.Fprintf(os.Stderr, "❌ %v\n", err)
			os.Exit(1)
		}
		entry := findReplayable(entries)
		if entry == nil {
			fmt.Fprintln(os.Stderr, "❌ no replayable task found in recent history")
			fmt.Fprintln(os.Stderr, "   (chat-only entries are excluded)")
			os.Exit(1)
		}
		confirmAndReplay(entry, cfg)

	default:
		// Try to parse as a number (index from replay list)
		idx, err := strconv.Atoi(subcmd)
		if err != nil {
			fmt.Fprintf(os.Stderr, "❌ invalid argument: %s (use 'list', 'last', or a number)\n", subcmd)
			os.Exit(1)
		}
		entries, err := store.RecentHistory(20)
		if err != nil {
			fmt.Fprintf(os.Stderr, "❌ %v\n", err)
			os.Exit(1)
		}
		if idx < 1 || idx > len(entries) {
			fmt.Fprintf(os.Stderr, "❌ index %d out of range (1-%d)\n", idx, len(entries))
			os.Exit(1)
		}
		entry := &entries[idx-1]
		confirmAndReplay(entry, cfg)
	}
}

func printReplayList(entries []memory.HistoryEntry) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "#\tTIME\tAGENT\tCATEGORY\tSUCCESS\tINPUT")
	fmt.Fprintln(w, "-\t----\t-----\t--------\t-------\t-----")
	for i, e := range entries {
		ts := e.Timestamp
		if len(ts) > 16 {
			ts = ts[:16]
		}
		status := "✓"
		if !e.Success {
			status = "✗"
		}
		input := truncatePrompt(e.Input, 50)
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\n", i+1, ts, e.Agent, e.Category, status, input)
	}
	w.Flush()
}

// findReplayable finds the most recent non-chat, successful entry.
func findReplayable(entries []memory.HistoryEntry) *memory.HistoryEntry {
	for i := range entries {
		e := &entries[i]
		// Skip chat-only entries — they're not meaningful to replay
		if e.Category == "chat" || e.Category == "" {
			continue
		}
		return e
	}
	return nil
}

func confirmAndReplay(entry *memory.HistoryEntry, cfg *config.Config) {
	fmt.Fprintf(os.Stderr, "📋 Recalling task:\n")
	fmt.Fprintf(os.Stderr, "   Input:    %s\n", truncatePrompt(entry.Input, 80))
	fmt.Fprintf(os.Stderr, "   Agent:    %s\n", entry.Agent)
	fmt.Fprintf(os.Stderr, "   Category: %s\n", entry.Category)
	if entry.Timestamp != "" {
		fmt.Fprintf(os.Stderr, "   Original: %s\n", entry.Timestamp)
	}
	fmt.Fprintf(os.Stderr, "\n")

	input := entry.Input
	if strings.TrimSpace(input) == "" {
		fmt.Fprintln(os.Stderr, "❌ empty input, cannot replay")
		os.Exit(1)
	}

	// This subcommand context has no registry/backend/MLX wiring, so orch
	// can't re-execute the task in-process. Print the original input and
	// let the caller pipe or copy-paste it back into orch themselves.
	fmt.Println(input)
	fmt.Fprintln(os.Stderr, "\n💡 Copy the above and pipe it to orch, or run: orch \""+escapeForShell(input)+"\"")
}

func escapeForShell(s string) string {
	// Simple shell escaping for display
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\"", "\\\"")
	s = strings.ReplaceAll(s, "$", "\\$")
	return s
}
