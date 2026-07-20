package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/chzyer/readline"
	"github.com/gordonwei/orch/pkg/apibackend"
	"github.com/gordonwei/orch/pkg/backend"
	"github.com/gordonwei/orch/pkg/config"
	"github.com/gordonwei/orch/pkg/eventbus"
	"github.com/gordonwei/orch/pkg/executor"
	"github.com/gordonwei/orch/pkg/hooks"
	"github.com/gordonwei/orch/pkg/memory"
	"github.com/gordonwei/orch/pkg/registry"
	"github.com/gordonwei/orch/pkg/router"
	"github.com/gordonwei/orch/pkg/session"
	"github.com/gordonwei/orch/pkg/workflow"
)

func runREPL(reg *registry.Registry, cfg *config.Config, store *memory.Store, br *backend.Registry, apiBackends map[string]apibackend.APIBackend, bus *eventbus.Bus) {
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

	// Create router instance for route hints and auto-routing
	rt := router.New(cfg.RouteRules)

	// Session context: keeps recent conversation turns for backend context injection
	replSession := &sessionContext{maxTurns: 5}

	// Session manager for interactive PTY sessions
	sm := NewSessionManager()
	// Wire API backends for /session bedrock and /session vertexai
	if len(apiBackends) > 0 {
		sm.SetAPIBackendFactory(func(backend session.Backend) (session.StreamingBackend, error) {
			ab, ok := apiBackends[string(backend)]
			if !ok {
				return nil, fmt.Errorf("API backend %s not configured", backend)
			}
			// apibackend.BedrockBackend and VertexAIBackend implement session.StreamingBackend
			sb, ok := ab.(session.StreamingBackend)
			if !ok {
				return nil, fmt.Errorf("API backend %s does not support streaming", backend)
			}
			if !ab.Available() {
				return nil, fmt.Errorf("API backend %s credentials not available", backend)
			}
			return sb, nil
		})
	}
	sm.WatchSessions()
	defer sm.Shutdown()

	// Register shutdown hook so signal handler can gracefully close sessions
	RegisterShutdown(sm.Shutdown)

	// Listen for session death events in background
	go func() {
		for event := range sm.Events() {
			switch event.Type {
			case "died":
				fmt.Fprintf(os.Stderr, "\n⚠️  session %s died unexpectedly", event.Backend)
				if event.Err != nil {
					fmt.Fprintf(os.Stderr, ": %v", event.Err)
				}
				fmt.Fprintf(os.Stderr, "\n")
				fmt.Fprintf(os.Stderr, "   💡 use /session %s to restart, or /kill %s to clean up\n", event.Backend, event.Backend)
			case "restarted":
				fmt.Fprintf(os.Stderr, "\n🔄 session %s auto-restarted\n", event.Backend)
			case "killed":
				// Silent — user initiated
			}
		}
	}()

	fmt.Fprintf(rl.Stdout(), "🟢 orch %s — AI Chief of Staff\n", version)
	fmt.Fprintf(rl.Stdout(), "   tools: %s\n", toolNames(reg))
	fmt.Fprintf(rl.Stdout(), "   type your request, /help for commands, ctrl+d to quit\n\n")

	// --- on_session_start hooks ---
	if hookRunner != nil && hookRunner.HasHooks(hooks.OnSessionStart) {
		cwd, _ := os.Getwd()
		hookResults, err := hookRunner.Run(context.Background(), hooks.HookEvent{
			Trigger: hooks.OnSessionStart,
			Cwd:     cwd,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "⚠️  on_session_start hook error: %v\n", err)
		}
		if stdout := hooks.CombinedStdout(hookResults); stdout != "" {
			fmt.Fprintf(os.Stderr, "📎 %s\n\n", stdout)
		}
	}

	for {
		// Update prompt based on session mode
		if sm.HasActive() {
			rl.SetPrompt(fmt.Sprintf("%s› ", sm.ActiveBackend()))
		} else {
			rl.SetPrompt("› ")
		}

		line, err := rl.Readline()
		if err == readline.ErrInterrupt {
			// Ctrl+C in session mode → back to normal
			if sm.HasActive() {
				sm.Back()
				fmt.Fprintf(os.Stderr, "⏎ back to normal mode\n")
				continue
			}
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

		// Slash commands are handled in both modes
		if strings.HasPrefix(input, "/") {
			handleSlashCommand(rl, reg, cfg, store, br, apiBackends, bus, sm, rt, input)
			continue
		}

		// === Session mode: forward input to active session ===
		if sm.HasActive() {
			sess := sm.Active()
			if sess == nil {
				fmt.Fprintf(os.Stderr, "⚠️  session died unexpectedly, back to normal mode\n")
				sm.Back()
				continue
			}

			// Route hint: suggest switching if input matches cross-domain keywords
			hint := rt.Hint(input, sm.ActiveBackend())
			if hint.Suggested != "" {
				fmt.Fprintf(os.Stderr, "💡 \"%s\" → might be better in %s (/switch %s)\n", hint.Keyword, hint.Suggested, hint.Suggested)

				// Auto-route: if enabled and strong signal, switch automatically
				if rt.AutoRoute() && hint.Strength >= 3 {
					if sm.Get(hint.Suggested) != nil {
						// Session already exists → just switch
						if err := sm.Switch(hint.Suggested); err == nil {
							fmt.Fprintf(os.Stderr, "🔀 auto-routed to %s (matched: \"%s\")\n", hint.Suggested, hint.Keyword)
							sess = sm.Active()
						} else {
							fmt.Fprintf(os.Stderr, "⚠️  auto-route switch failed: %v\n", err)
						}
					} else {
						// No session → spawn + switch + forward
						fmt.Fprintf(os.Stderr, "🔀 auto-routed to %s (matched: \"%s\")\n", hint.Suggested, hint.Keyword)
						if err := sm.SpawnOrSwitch(hint.Suggested); err == nil {
							time.Sleep(2 * time.Second)
							sess = sm.Active()
							if sess != nil {
								banner := sess.ReadRaw()
								if banner != "" {
									fmt.Print(banner)
									if !strings.HasSuffix(banner, "\n") {
										fmt.Println()
									}
								}
							}
						} else {
							fmt.Fprintf(os.Stderr, "⚠️  auto-route spawn failed: %v\n", err)
						}
					}
					// Update sess to the newly-active session
					sess = sm.Active()
					if sess == nil {
						fmt.Fprintf(os.Stderr, "⚠️  session not available after auto-route\n")
						continue
					}
				} else if rt.AutoRoute() {
					// Secondary: context-aware history momentum
					if suggested, kw := rt.SuggestBackend(input); suggested != "" && suggested != sm.ActiveBackend() && kw == "(history momentum)" {
						// Only auto-switch on pure momentum if target session already exists (don't spawn)
						if sm.Get(suggested) != nil {
							if err := sm.Switch(suggested); err == nil {
								fmt.Fprintf(os.Stderr, "🔀 auto-routed to %s (context momentum)\n", suggested)
								sess = sm.Active()
							}
						}
					}
				}
			}

			// Record input for history analysis
			rt.RecordInput(input, sm.ActiveBackend())

			// Persist user input to session logs
			if store != nil {
				if err := store.AddSessionLog(string(sm.ActiveBackend()), "user", input); err != nil {
					fmt.Fprintf(os.Stderr, "⚠️  failed to persist session log: %v\n", err)
				}
			}

			if err := sess.Send(input); err != nil {
				fmt.Fprintf(os.Stderr, "❌ send failed: %v\n", err)
				continue
			}

			// Stream response chunks as they arrive (real-time output)
			stream := sess.ReadStream()
			var sessionOutputBuf strings.Builder
			for chunk := range stream {
				if chunk != "" {
					fmt.Print(chunk)
					sessionOutputBuf.WriteString(chunk)
				}
			}
			if sessionOutputBuf.Len() > 0 {
				// Ensure trailing newline
				raw := sess.ReadRaw()
				if raw != "" && !strings.HasSuffix(raw, "\n") {
					fmt.Println()
				}
				sm.TouchOutput(sm.ActiveBackend())
				// Persist assistant output to session logs
				if store != nil {
					if err := store.AddSessionLog(string(sm.ActiveBackend()), "assistant", sessionOutputBuf.String()); err != nil {
						fmt.Fprintf(os.Stderr, "⚠️  failed to persist session log: %v\n", err)
					}
				}
			}
			continue
		}

		// === Normal mode: existing planner behavior ===

		// --- pre_route hooks ---
		if hookRunner != nil && hookRunner.HasHooks(hooks.PreRoute) {
			cwd, _ := os.Getwd()
			hookResults, hookErr := hookRunner.Run(context.Background(), hooks.HookEvent{
				Trigger: hooks.PreRoute,
				Input:   input,
				Cwd:     cwd,
			})
			if hookErr != nil {
				fmt.Fprintf(os.Stderr, "⚠️  pre_route hook error: %v\n", hookErr)
			}
			if hooks.Blocked(hookResults) {
				fmt.Fprintf(os.Stderr, "🚫 blocked by hook: %s\n", hooks.BlockReason(hookResults))
				continue
			}
		}

		sessionCtx := replSession.buildContext()
		enrichedInput := input
		if sessionCtx != "" {
			enrichedInput = fmt.Sprintf("[Prior conversation for context]\n%s\n[End prior conversation]\n\nCurrent request: %s", sessionCtx, input)
		}

		_, output := runTask(nil, reg, cfg, store, br, apiBackends, bus, enrichedInput, false)
		replSession.add(input, output)
		fmt.Fprintln(os.Stderr)
	}

	// --- on_session_end hooks ---
	if hookRunner != nil && hookRunner.HasHooks(hooks.OnSessionEnd) {
		cwd, _ := os.Getwd()
		hookResults, err := hookRunner.Run(context.Background(), hooks.HookEvent{
			Trigger: hooks.OnSessionEnd,
			Cwd:     cwd,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "⚠️  on_session_end hook error: %v\n", err)
		}
		for _, hr := range hookResults {
			if hr.Err != nil {
				fmt.Fprintf(os.Stderr, "⚠️  hook %q: %v\n", hr.Name, hr.Err)
			}
		}
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

func handleSlashCommand(rl *readline.Instance, reg *registry.Registry, cfg *config.Config, store *memory.Store, br *backend.Registry, apiBackends map[string]apibackend.APIBackend, bus *eventbus.Bus, sm *SessionManager, rt *router.Router, input string) {
	parts := strings.Fields(input)
	cmd := strings.ToLower(parts[0])
	args := parts[1:]

	switch cmd {
	case "/help":
		printREPLHelp(sm)

	case "/session":
		handleSessionCmd(sm, args)

	case "/switch":
		handleSwitchCmd(sm, args)

	case "/sessions":
		handleSessionsCmd(sm)

	case "/back":
		handleBackCmd(sm)

	case "/kill":
		handleKillCmd(sm, args)

	case "/auto":
		handleAutoCmd(rt, args)

	case "/pass":
		handlePassCmd(sm, store, args)

	case "/session-history", "/sh":
		handleSessionHistoryCmd(store, sm, args)

	case "/w", "/workflows":
		if len(args) > 0 {
			handleWorkflowExec(rl, reg, cfg, store, br, apiBackends, args[0])
		} else {
			handleWorkflowMenu(rl, reg, cfg, store, br, apiBackends)
		}

	case "/h", "/history":
		replHistory(store)

	case "/b", "/briefing":
		replBriefing(store)

	default:
		fmt.Fprintf(os.Stderr, "❓ unknown command: %s (type /help for available commands)\n", cmd)
	}
}

// --- Session commands ---

func handleSessionCmd(sm *SessionManager, args []string) {
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "Usage: /session <claude|kiro>\n")
		return
	}

	backend := parseBackend(args[0])
	if backend == "" {
		fmt.Fprintf(os.Stderr, "❌ unsupported backend: %s (use: claude, kiro)\n", args[0])
		return
	}

	fmt.Fprintf(os.Stderr, "🔌 connecting to %s...\n", backend)
	if err := sm.SpawnOrSwitch(backend); err != nil {
		fmt.Fprintf(os.Stderr, "❌ %v\n", err)
		return
	}

	// Wait a moment for the session to initialize
	time.Sleep(2 * time.Second)

	// Drain initial output (startup banner)
	sess := sm.Active()
	if sess != nil {
		banner := sess.ReadRaw()
		if banner != "" {
			fmt.Print(banner)
			if !strings.HasSuffix(banner, "\n") {
				fmt.Println()
			}
		}
	}

	fmt.Fprintf(os.Stderr, "✅ session active: %s (type /back to return to orch)\n", backend)
}

func handleSwitchCmd(sm *SessionManager, args []string) {
	if len(args) == 0 {
		// If no arg, list available and prompt
		infos := sm.List()
		if len(infos) == 0 {
			fmt.Fprintf(os.Stderr, "❌ no sessions running. Use /session <claude|kiro> to start one\n")
			return
		}
		fmt.Fprintf(os.Stderr, "Usage: /switch <claude|kiro>\nRunning sessions:\n")
		for _, info := range infos {
			marker := "  "
			if info.IsActive {
				marker = "→ "
			}
			fmt.Fprintf(os.Stderr, "  %s%s (up %s)\n", marker, info.Backend, time.Since(info.StartedAt).Round(time.Second))
		}
		return
	}

	backend := parseBackend(args[0])
	if backend == "" {
		fmt.Fprintf(os.Stderr, "❌ unsupported backend: %s\n", args[0])
		return
	}

	if err := sm.Switch(backend); err != nil {
		fmt.Fprintf(os.Stderr, "❌ %v\n", err)
		return
	}
	fmt.Fprintf(os.Stderr, "✅ switched to %s\n", backend)
}

func handleSessionsCmd(sm *SessionManager) {
	infos := sm.List()
	if len(infos) == 0 {
		fmt.Fprintf(os.Stderr, "📋 no sessions running\n")
		return
	}

	fmt.Fprintf(os.Stderr, "📋 Sessions:\n")
	for _, info := range infos {
		marker := "  "
		if info.IsActive {
			marker = "→ "
		}
		idle := ""
		if info.IsIdle {
			idle = " (idle)"
		}
		uptime := time.Since(info.StartedAt).Round(time.Second)
		fmt.Fprintf(os.Stderr, "  %s%s — up %s%s\n", marker, info.Backend, uptime, idle)
	}
}

func handleBackCmd(sm *SessionManager) {
	if !sm.HasActive() {
		fmt.Fprintf(os.Stderr, "ℹ️  already in normal mode\n")
		return
	}
	backend := sm.ActiveBackend()
	sm.Back()
	fmt.Fprintf(os.Stderr, "⏎ back to normal mode (session %s still alive in background)\n", backend)
}

func handleKillCmd(sm *SessionManager, args []string) {
	if len(args) == 0 {
		// Kill active session if any
		if !sm.HasActive() {
			fmt.Fprintf(os.Stderr, "Usage: /kill <claude|kiro|all>\n")
			return
		}
		backend := sm.ActiveBackend()
		if err := sm.Kill(backend); err != nil {
			fmt.Fprintf(os.Stderr, "❌ %v\n", err)
			return
		}
		fmt.Fprintf(os.Stderr, "💀 killed %s session\n", backend)
		return
	}

	target := strings.ToLower(args[0])
	if target == "all" {
		sm.KillAll()
		fmt.Fprintf(os.Stderr, "💀 killed all sessions\n")
		return
	}

	backend := parseBackend(target)
	if backend == "" {
		fmt.Fprintf(os.Stderr, "❌ unsupported backend: %s\n", target)
		return
	}

	if err := sm.Kill(backend); err != nil {
		fmt.Fprintf(os.Stderr, "❌ %v\n", err)
		return
	}
	fmt.Fprintf(os.Stderr, "💀 killed %s session\n", backend)
}

// parseBackend converts a user string to a session.Backend constant.
func parseBackend(s string) session.Backend {
	switch strings.ToLower(s) {
	case "claude", "c":
		return session.BackendClaude
	case "kiro", "k":
		return session.BackendKiro
	case "gemini", "g":
		return session.BackendGemini
	case "bedrock", "br":
		return session.BackendBedrock
	case "vertexai", "vertex", "va":
		return session.BackendVertexAI
	default:
		return ""
	}
}

// --- Auto-route command ---

func handleAutoCmd(rt *router.Router, args []string) {
	if len(args) == 0 {
		// Toggle
		current := rt.AutoRoute()
		rt.SetAutoRoute(!current)
		if !current {
			fmt.Fprintf(os.Stderr, "🔀 auto-route: ON (strong matches will auto-switch sessions)\n")
		} else {
			fmt.Fprintf(os.Stderr, "🔀 auto-route: OFF (hints only, no auto-switch)\n")
		}
		return
	}

	switch strings.ToLower(args[0]) {
	case "on":
		rt.SetAutoRoute(true)
		fmt.Fprintf(os.Stderr, "🔀 auto-route: ON (strong matches will auto-switch sessions)\n")
	case "off":
		rt.SetAutoRoute(false)
		fmt.Fprintf(os.Stderr, "🔀 auto-route: OFF (hints only, no auto-switch)\n")
	default:
		fmt.Fprintf(os.Stderr, "Usage: /auto [on|off]\n")
	}
}

// --- Pass command (cross-session context transfer) ---

func handlePassCmd(sm *SessionManager, store *memory.Store, args []string) {
	if store == nil {
		fmt.Fprintf(os.Stderr, "❌ memory store not available\n")
		return
	}

	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "Usage: /pass <backend> — pass last output from current session to target\n")
		fmt.Fprintf(os.Stderr, "       /pass <from> <to> — pass last output from <from> to <to>\n")
		return
	}

	var fromBackend, toBackend session.Backend

	if len(args) == 1 {
		// /pass <target> — current session → target
		if !sm.HasActive() {
			fmt.Fprintf(os.Stderr, "❌ no active session. Use /pass <from> <to> in normal mode\n")
			return
		}
		fromBackend = sm.ActiveBackend()
		toBackend = parseBackend(args[0])
	} else {
		// /pass <from> <to>
		fromBackend = parseBackend(args[0])
		toBackend = parseBackend(args[1])
	}

	if fromBackend == "" {
		fmt.Fprintf(os.Stderr, "❌ invalid source backend: %s\n", args[0])
		return
	}
	if toBackend == "" {
		target := args[0]
		if len(args) > 1 {
			target = args[1]
		}
		fmt.Fprintf(os.Stderr, "❌ invalid target backend: %s\n", target)
		return
	}
	if fromBackend == toBackend {
		fmt.Fprintf(os.Stderr, "❌ source and target are the same\n")
		return
	}

	// Get last output from source
	lastOutput, err := store.LastSessionOutput(string(fromBackend))
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ failed to read session log: %v\n", err)
		return
	}
	if lastOutput == "" {
		fmt.Fprintf(os.Stderr, "❌ no output from %s to pass\n", fromBackend)
		return
	}

	// Truncate if too long (rune-safe — avoid splitting multi-byte UTF-8 chars)
	if len(lastOutput) > 8000 {
		lastOutput = executor.Truncate(lastOutput, 8000)
	}

	// Stateless API backends (bedrock, vertexai): store context for next Invoke call
	if toBackend == "bedrock" || toBackend == "vertexai" {
		contextMsg := fmt.Sprintf("[Context passed from %s]\n%s\n[End of passed context]", fromBackend, lastOutput)
		if err := store.AddSessionLog(string(toBackend), "user", contextMsg); err != nil {
			fmt.Fprintf(os.Stderr, "❌ failed to persist context for %s: %v\n", toBackend, err)
			return
		}
		fmt.Fprintf(os.Stderr, "✅ passed %d chars from %s → %s (stored as context for next API call)\n",
			len(lastOutput), fromBackend, toBackend)
		return
	}

	// Ensure target session exists
	targetSess := sm.Get(toBackend)
	if targetSess == nil {
		fmt.Fprintf(os.Stderr, "⚠️  %s session not running, spawning...\n", toBackend)
		if err := sm.SpawnOrSwitch(toBackend); err != nil {
			fmt.Fprintf(os.Stderr, "❌ failed to spawn %s: %v\n", toBackend, err)
			return
		}
		time.Sleep(2 * time.Second)
		targetSess = sm.Active()
		if targetSess == nil {
			fmt.Fprintf(os.Stderr, "❌ %s session not available\n", toBackend)
			return
		}
		// Drain banner
		banner := targetSess.ReadRaw()
		if banner != "" {
			fmt.Print(banner)
			if !strings.HasSuffix(banner, "\n") {
				fmt.Println()
			}
		}
	} else {
		// Switch to target
		sm.Switch(toBackend)
	}

	// Send context as a prefixed message
	contextMsg := fmt.Sprintf("[Context passed from %s session]\n%s\n[End of passed context]\n\nPlease acknowledge you received the above context.", fromBackend, lastOutput)
	if err := targetSess.Send(contextMsg); err != nil {
		fmt.Fprintf(os.Stderr, "❌ failed to send context to %s: %v\n", toBackend, err)
		return
	}

	// Persist the pass action
	if err := store.AddSessionLog(string(toBackend), "user", contextMsg); err != nil {
		fmt.Fprintf(os.Stderr, "⚠️  failed to persist session log: %v\n", err)
	}

	fmt.Fprintf(os.Stderr, "✅ passed %d chars from %s → %s (now in %s session)\n",
		len(lastOutput), fromBackend, toBackend, toBackend)

	// Stream the target's response
	stream := targetSess.ReadStream()
	var buf strings.Builder
	for chunk := range stream {
		if chunk != "" {
			fmt.Print(chunk)
			buf.WriteString(chunk)
		}
	}
	if buf.Len() > 0 {
		raw := targetSess.ReadRaw()
		if raw != "" && !strings.HasSuffix(raw, "\n") {
			fmt.Println()
		}
		sm.TouchOutput(toBackend)
		if err := store.AddSessionLog(string(toBackend), "assistant", buf.String()); err != nil {
			fmt.Fprintf(os.Stderr, "⚠️  failed to persist session log: %v\n", err)
		}
	}
}

// --- Session history command ---

func handleSessionHistoryCmd(store *memory.Store, sm *SessionManager, args []string) {
	if store == nil {
		fmt.Fprintf(os.Stderr, "❌ memory store not available\n")
		return
	}

	var backend string
	if len(args) > 0 {
		b := parseBackend(args[0])
		if b == "" {
			fmt.Fprintf(os.Stderr, "❌ unsupported backend: %s\n", args[0])
			return
		}
		backend = string(b)
	} else if sm.HasActive() {
		backend = string(sm.ActiveBackend())
	} else {
		fmt.Fprintf(os.Stderr, "Usage: /session-history <claude|kiro|gemini>\n")
		return
	}

	entries, err := store.RecentSessionLogs(backend, 20)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ failed to read session logs: %v\n", err)
		return
	}
	if len(entries) == 0 {
		fmt.Fprintf(os.Stderr, "📜 no session logs for %s\n", backend)
		return
	}

	fmt.Fprintf(os.Stderr, "📜 session history (%s, last %d entries):\n", backend, len(entries))
	for _, e := range entries {
		icon := "👤"
		if e.Role == "assistant" {
			icon = "🤖"
		}
		content := executor.TruncateWithSuffix(e.Content, 120, "...")
		// Replace newlines for display
		content = strings.ReplaceAll(content, "\n", " ↵ ")
		fmt.Fprintf(os.Stderr, "  %s [%s] %s\n", icon, e.Timestamp, content)
	}
	fmt.Fprintln(os.Stderr)
}

// --- Help ---

func printREPLHelp(sm *SessionManager) {
	fmt.Fprintf(os.Stderr, `
📖 REPL Commands:

  Session Mode:
    /session <claude|kiro|gemini>  — start or attach to a backend session
    /switch <claude|kiro|gemini>   — switch between running sessions
    /sessions                      — list all running sessions
    /back                          — return to normal mode (session stays alive)
    /kill [backend|all]            — terminate a session
    /auto [on|off]                 — toggle auto-route (strong keywords auto-switch)
    /pass <target>                 — pass last output from current session to target
    /pass <from> <to>             — pass last output between sessions
    /sh, /session-history [backend] — show recent session conversation
    Ctrl+C                         — same as /back

  Normal Mode:
    /w, /workflows          — list all available workflows
    /w <number>             — execute workflow by number
    /h, /history            — last 10 history entries
    /b, /briefing           — show current briefing
    tools                   — list registered tools
    exit, quit, q           — exit (kills all sessions)

`)
	if sm.HasActive() {
		fmt.Fprintf(os.Stderr, "  Current mode: SESSION (%s)\n", sm.ActiveBackend())
		fmt.Fprintf(os.Stderr, "  All non-slash input is forwarded to %s\n\n", sm.ActiveBackend())
	} else {
		fmt.Fprintf(os.Stderr, "  Current mode: NORMAL (input goes to orch planner)\n\n")
	}
}

// ===== Workflow / History / Briefing (unchanged) =====

func handleWorkflowMenu(rl *readline.Instance, reg *registry.Registry, cfg *config.Config, store *memory.Store, br *backend.Registry, apiBackends map[string]apibackend.APIBackend) {
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

	handleWorkflowExec(rl, reg, cfg, store, br, apiBackends, choice)
}

func handleWorkflowExec(rl *readline.Instance, reg *registry.Registry, cfg *config.Config, store *memory.Store, br *backend.Registry, apiBackends map[string]apibackend.APIBackend, numStr string) {
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
	e.ApprovalFunc = promptApproval
	e.APIBackends = apiBackends
	e.MemoryStore = store

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
