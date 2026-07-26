package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// fileContext manages files attached to the REPL session for context injection.
type fileContext struct {
	files []contextFile // ordered list of attached files
}

type contextFile struct {
	path    string // as provided by user (for display)
	absPath string // resolved absolute path
	content string // file content at time of attach
}

// add reads and attaches a file to the context.
func (fc *fileContext) add(path string) error {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve path: %w", err)
	}

	// Check if already attached
	for _, f := range fc.files {
		if f.absPath == absPath {
			return fmt.Errorf("already attached: %s", path)
		}
	}

	content, err := os.ReadFile(absPath)
	if err != nil {
		return fmt.Errorf("read file: %w", err)
	}

	// Limit file size (256KB)
	if len(content) > 256*1024 {
		return fmt.Errorf("file too large: %s (%d bytes, max 256KB)", path, len(content))
	}

	fc.files = append(fc.files, contextFile{
		path:    path,
		absPath: absPath,
		content: string(content),
	})
	return nil
}

// remove detaches a file by path (supports both original path and absolute).
func (fc *fileContext) remove(path string) bool {
	absPath, _ := filepath.Abs(path)
	for i, f := range fc.files {
		if f.path == path || f.absPath == absPath {
			fc.files = append(fc.files[:i], fc.files[i+1:]...)
			return true
		}
	}
	return false
}

// clear removes all attached files.
func (fc *fileContext) clear() {
	fc.files = nil
}

// buildContext generates the context block to inject into the prompt.
func (fc *fileContext) buildContext() string {
	if len(fc.files) == 0 {
		return ""
	}

	var parts []string
	for _, f := range fc.files {
		parts = append(parts, fmt.Sprintf("--- File: %s ---\n%s\n--- End: %s ---", f.path, f.content, f.path))
	}
	return strings.Join(parts, "\n\n")
}

// list returns a formatted string of all attached files.
func (fc *fileContext) list() string {
	if len(fc.files) == 0 {
		return "No files attached."
	}
	var lines []string
	for i, f := range fc.files {
		lines = append(lines, fmt.Sprintf("  %d. %s (%s)", i+1, f.path, formatBytes(len(f.content))))
	}
	return strings.Join(lines, "\n")
}

// count returns the number of attached files.
func (fc *fileContext) count() int {
	return len(fc.files)
}

// handleContextCmd handles /context slash commands.
func handleContextCmd(fc *fileContext, args []string) {
	if len(args) == 0 {
		// Show current context
		fmt.Fprintf(os.Stderr, "📎 Attached files (%d):\n%s\n", fc.count(), fc.list())
		return
	}

	subcmd := args[0]
	switch subcmd {
	case "add":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "Usage: /context add <file> [file2 ...]")
			return
		}
		for _, path := range args[1:] {
			if err := fc.add(path); err != nil {
				fmt.Fprintf(os.Stderr, "❌ %s: %v\n", path, err)
			} else {
				fmt.Fprintf(os.Stderr, "📎 attached: %s\n", path)
			}
		}

	case "rm", "remove":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "Usage: /context rm <file>")
			return
		}
		for _, path := range args[1:] {
			if fc.remove(path) {
				fmt.Fprintf(os.Stderr, "🗑️  removed: %s\n", path)
			} else {
				fmt.Fprintf(os.Stderr, "❌ not found: %s\n", path)
			}
		}

	case "clear":
		fc.clear()
		fmt.Fprintln(os.Stderr, "🧹 all context files cleared")

	case "list":
		fmt.Fprintf(os.Stderr, "📎 Attached files (%d):\n%s\n", fc.count(), fc.list())

	case "refresh":
		// Re-read all files (in case they changed on disk)
		refreshed := 0
		for i := range fc.files {
			content, err := os.ReadFile(fc.files[i].absPath)
			if err != nil {
				fmt.Fprintf(os.Stderr, "⚠️  %s: %v\n", fc.files[i].path, err)
				continue
			}
			fc.files[i].content = string(content)
			refreshed++
		}
		fmt.Fprintf(os.Stderr, "🔄 refreshed %d file(s)\n", refreshed)

	default:
		// Treat as file path (shorthand for /context add <path>)
		for _, path := range args {
			if err := fc.add(path); err != nil {
				fmt.Fprintf(os.Stderr, "❌ %s: %v\n", path, err)
			} else {
				fmt.Fprintf(os.Stderr, "📎 attached: %s\n", path)
			}
		}
	}
}

func formatBytes(n int) string {
	if n >= 1024*1024 {
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	}
	if n >= 1024 {
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	}
	return fmt.Sprintf("%d B", n)
}
