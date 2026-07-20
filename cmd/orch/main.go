package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gordonwei/orch/pkg/apibackend"
	"github.com/gordonwei/orch/pkg/backend"
	"github.com/gordonwei/orch/pkg/config"
	"github.com/gordonwei/orch/pkg/eventbus"
	"github.com/gordonwei/orch/pkg/hooks"
	"github.com/gordonwei/orch/pkg/memory"
	"github.com/gordonwei/orch/pkg/model"
	"github.com/gordonwei/orch/pkg/registry"
)

// version is set at build time via -ldflags "-X main.version=v0.4"
var version = "dev"
var verbose bool

// hookRunner is the global hook runner, initialized from config in main().
var hookRunner *hooks.Runner

// shutdownFunc is a hook for the signal handler to call Shutdown() on the session manager.
var (
	shutdownMu   sync.Mutex
	shutdownFunc func()
)

// RegisterShutdown sets the function to call on SIGINT/SIGTERM.
func RegisterShutdown(fn func()) {
	shutdownMu.Lock()
	defer shutdownMu.Unlock()
	shutdownFunc = fn
}

func main() {
	args := parseArgs()
	verbose = args.verbose

	// --version flag
	if args.showVersion {
		fmt.Printf("orch %s\n", version)
		return
	}

	// Load config
	cfg := config.Load()

	// Initialize hook runner from config
	if len(cfg.Hooks) > 0 {
		hooksCfg := make(hooks.HooksConfig, len(cfg.Hooks))
		for trigger, defs := range cfg.Hooks {
			hDefs := make([]hooks.HookDef, len(defs))
			for i, d := range defs {
				hDefs[i] = hooks.HookDef{
					Name:           d.Name,
					Command:        d.Command,
					Timeout:        d.Timeout,
					BlockOnFailure: d.BlockOnFailure,
				}
			}
			hooksCfg[trigger] = hDefs
		}
		hookRunner = hooks.NewRunner(hooksCfg)
		if hookRunner != nil {
			fmt.Fprintf(os.Stderr, "🪝 hooks: %d trigger(s) configured\n", len(cfg.Hooks))
		}
	}

	// Scan tools
	reg := registry.Scan()

	// Open memory layer (SQLite)
	store, err := memory.Open(cfg.Memory.DBPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "⚠️  memory db open failed: %v (running without memory)\n", err)
	}
	if store != nil {
		defer store.Close()
	}

	// Signal handler (graceful shutdown)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		fmt.Fprintf(os.Stderr, "\n⚡ received %v, shutting down...\n", sig)
		cancel()

		// Run registered shutdown hooks (e.g. session manager) in the
		// background so a second Ctrl+C can still force an immediate exit
		// even if the hook (session Shutdown() can take up to ~8s) hangs.
		shutdownMu.Lock()
		fn := shutdownFunc
		shutdownMu.Unlock()

		done := make(chan struct{})
		go func() {
			if fn != nil {
				fn()
			} else {
				// No shutdown hook registered (one-shot/subcommand mode
				// never enters the REPL, so RegisterShutdown was never
				// called) — give in-flight work a brief grace period,
				// matching the previous unconditional behavior.
				time.Sleep(500 * time.Millisecond)
			}
			close(done)
		}()

		select {
		case <-done:
		case <-sigCh:
			fmt.Fprintf(os.Stderr, "\n⚡ received second signal, forcing exit...\n")
		}
		os.Exit(130)
	}()

	// Handle subcommands (no MLX server needed)
	if args.subcommand != "" {
		handleSubcommand(args, cfg, store)
		return
	}

	// Determine backend override: --backend flag > ORCH_BACKEND env > config ai_backend.primary
	backendOverride := args.backend
	if backendOverride == "" {
		backendOverride = os.Getenv("ORCH_BACKEND")
	}
	if backendOverride == "" {
		backendOverride = cfg.AIBackend.Primary
	}

	// Initialize backend registry (auto-detect available CLIs)
	br := backend.NewRegistry(backendOverride)
	if len(br.Available()) > 0 {
		fmt.Fprintf(os.Stderr, "🤖 %s\n", br.Summary())
	} else {
		fmt.Fprintf(os.Stderr, "⚠️  no AI backends detected (install kiro-cli, claude, or gemini for cloud planning)\n")
	}

	// Initialize stateless API backends (Bedrock, Vertex AI)
	apiBackends := make(map[string]apibackend.APIBackend)
	if cfg.APIBackends.Bedrock.Enabled {
		ab := apibackend.NewBedrock(apibackend.BedrockConfig{
			Region:  cfg.APIBackends.Bedrock.Region,
			ModelID: cfg.APIBackends.Bedrock.ModelID,
		})
		apiBackends["bedrock"] = ab
		if ab.Available() {
			fmt.Fprintf(os.Stderr, "   ☁️  bedrock: %s (%s) ✓\n", cfg.APIBackends.Bedrock.ModelID, cfg.APIBackends.Bedrock.Region)
		} else {
			fmt.Fprintf(os.Stderr, "   ⚠️  bedrock: enabled but credentials not found\n")
		}
	}
	if cfg.APIBackends.VertexAI.Enabled {
		vab := apibackend.NewVertexAI(apibackend.VertexAIConfig{
			ProjectID: cfg.APIBackends.VertexAI.ProjectID,
			Region:    cfg.APIBackends.VertexAI.Region,
			ModelID:   cfg.APIBackends.VertexAI.ModelID,
		})
		apiBackends["vertexai"] = vab
		if vab.Available() {
			fmt.Fprintf(os.Stderr, "   ☁️  vertexai: %s (%s/%s) ✓\n", cfg.APIBackends.VertexAI.ModelID, cfg.APIBackends.VertexAI.ProjectID, cfg.APIBackends.VertexAI.Region)
		} else {
			fmt.Fprintf(os.Stderr, "   ⚠️  vertexai: enabled but ADC not found\n")
		}
	}

	// Auto-start MLX server (if not running)
	activeModel := cfg.ActiveModel()
	if activeModel.AutoStart {
		starter := model.NewStarter(model.StarterConfig{
			Backend:    activeModel.Backend,
			PythonPath: activeModel.PythonPath,
			Model:      activeModel.Model,
			Port:       activeModel.Port,
			Endpoint:   activeModel.Endpoint,
		})
		llmClient := model.NewOpenAIClient(model.OpenAIClientConfig{
			Endpoint: activeModel.Endpoint,
			Model:    activeModel.Model,
			Backend:  activeModel.Backend,
		})
		if err := starter.EnsureRunning(llmClient); err != nil {
			fmt.Fprintf(os.Stderr, "   ⚠️  %v\n", err)
		}
	}

	// Show briefing on boot
	if store != nil && cfg.Memory.BriefingOnBoot {
		brief, t, err := store.GetBriefing()
		if cfg.Memory.BriefingSourceFile != "" {
			// A source file is configured — always re-summarize it fresh rather
			// than trust whatever's cached, so this can never silently go stale
			// the way a manually-triggered `orch briefing gen` does. Falls back
			// to the last cached briefing (if any) on any failure (file missing,
			// MLX down) instead of blocking startup.
			if fresh, genErr := generateBriefingFromFile(cfg, store); genErr == nil {
				brief, t, err = fresh, time.Now(), nil
			} else if verbose {
				fmt.Fprintf(os.Stderr, "   ⚠️  briefing_source_file: %v (showing last cached briefing)\n", genErr)
			}
		}
		if err == nil && brief != "" {
			fmt.Fprintf(os.Stderr, "📋 briefing (generated %s):\n   %s\n\n", t.Format("01/02 15:04"), brief)
		}
	}

	if args.showTools {
		fmt.Println(reg.ToJSON())
		return
	}

	// ===== stdin pipe integration =====
	stdinIsPipe := false
	var pipeData []byte
	stat, _ := os.Stdin.Stat()
	if (stat.Mode() & os.ModeCharDevice) == 0 {
		stdinIsPipe = true
		pipeData, _ = io.ReadAll(os.Stdin)
	}

	prompt := args.prompt

	if stdinIsPipe && len(pipeData) > 0 {
		pipeStr := truncateStr(string(pipeData), 50000)
		if prompt == "" {
			prompt = strings.TrimSpace(string(pipeData))
			if len(prompt) > 50000 {
				prompt = prompt[:50000]
			}
		} else {
			prompt = fmt.Sprintf("Context (from stdin pipe, %d bytes):\n```\n%s\n```\n\nTask: %s", len(pipeData), pipeStr, prompt)
		}
	}

	if prompt != "" {
		// Load reactive trigger rules
		rules, err := eventbus.LoadRules(cfg.Workflows.Dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "⚠️  reactive rules load failed: %v\n", err)
		}
		var bus *eventbus.Bus
		if len(rules) > 0 {
			bus = eventbus.New(rules)
			fmt.Fprintf(os.Stderr, "🔗 reactive rules: %d loaded\n", len(rules))
		}

		// runTask prints the output itself; the returned string is only for REPL session context.
		ok, _ := runTask(ctx, reg, cfg, store, br, apiBackends, bus, prompt, args.dryRun)
		if !ok {
			os.Exit(1)
		}
		return
	}

	// If stdin is a pipe, skip REPL mode
	if stdinIsPipe {
		fmt.Fprintf(os.Stderr, "⚠️  stdin is a pipe but no input received. Use: echo 'task' | orch\n")
		return
	}

	// Load reactive trigger rules for REPL
	replRules, _ := eventbus.LoadRules(cfg.Workflows.Dir)
	var replBus *eventbus.Bus
	if len(replRules) > 0 {
		replBus = eventbus.New(replRules)
	}

	runREPL(reg, cfg, store, br, apiBackends, replBus)
}

// ===== Arg Parsing =====

type cliArgs struct {
	prompt      string
	showTools   bool
	showVersion bool
	dryRun      bool
	verbose     bool
	backend     string // --backend override
	subcommand  string
	subArgs     []string
}

func parseArgs() cliArgs {
	args := cliArgs{}

	if len(os.Args) < 2 {
		return args
	}

	// Check for subcommands first
	switch os.Args[1] {
	case "history", "session-history", "briefing", "cost", "init":
		args.subcommand = os.Args[1]
		if len(os.Args) > 2 {
			args.subArgs = os.Args[2:]
		}
		return args
	}

	// Normal arg parsing (flags + prompt)
	var prompts []string
	for i := 1; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--tools":
			args.showTools = true
		case "--dry-run":
			args.dryRun = true
		case "--verbose":
			args.verbose = true
		case "--version", "-v":
			args.showVersion = true
		case "--backend":
			if i+1 < len(os.Args) {
				i++
				args.backend = os.Args[i]
			}
		case "--help", "-h":
			printUsage()
			os.Exit(0)
		default:
			prompts = append(prompts, os.Args[i])
		}
	}

	args.prompt = strings.Join(prompts, " ")
	return args
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `orch %s - AI Task Orchestration CLI

Usage:
  orch <prompt>              Oneshot: plan → execute → deliver
  orch                       REPL mode: continuous interaction
  orch --tools               Show available tools
  orch --dry-run <prompt>    Plan only, don't execute
  orch --verbose <prompt>    Show detailed MLX debug output
  orch --backend <name>      Override AI backend (kiro/claude/gemini)
  orch --version             Show version

Subcommands:
  orch init                  Interactive setup wizard
  orch history               List last 20 task history entries
  orch history search <kw>   Search history
  orch history clear         Clear all history

  orch session-history clear [days]   Clear session mode conversation logs
                                       (all, or older than N days)

  orch briefing              Show current briefing
  orch briefing set <text>   Manually set briefing
  orch briefing gen          Auto-generate briefing from recent history via MLX

Environment:
  ORCH_BACKEND               Override AI backend (same as --backend)
  ORCH_CONFIG                Override config file path

Examples:
  orch init
  orch "check S3 bucket usage"
  orch --backend gemini "summarize this doc"
  orch --dry-run "consolidate AWS and GCP usage report"
  kubectl get pods -o json | orch "which pods are unhealthy?"
  cat error.log | orch "analyze this error"
  orch history search "kubectl"
  orch briefing gen
`, version)
}

// ===== Helpers =====

func toolNames(reg *registry.Registry) string {
	var names []string
	for _, t := range reg.Available() {
		names = append(names, t.Name)
	}
	return strings.Join(names, ", ")
}

// promptApprovalMu serializes concurrent approval prompts (DAG steps run in
// their own goroutines; without this, two high-risk steps triggered at the
// same time would interleave their prompts and race on stdin).
var promptApprovalMu sync.Mutex

// promptApproval asks the user to confirm execution of a high-risk command.
// Returns true if user approves, false if denied.
//
// Non-interactive sessions (stdin is a pipe/redirect, e.g. CI/cron) cannot be
// prompted — that case is detected explicitly and denied with a clear message
// rather than silently blocking on a Scanln that will hit EOF. Set
// ORCH_AUTO_APPROVE=1 to bypass the gate entirely in those environments.
func promptApproval(command string) bool {
	promptApprovalMu.Lock()
	defer promptApprovalMu.Unlock()

	if os.Getenv("ORCH_AUTO_APPROVE") == "1" {
		fmt.Fprintf(os.Stderr, "⚠️  ORCH_AUTO_APPROVE=1: auto-approving high-risk command: %s\n", command)
		return true
	}

	stat, _ := os.Stdin.Stat()
	if stat != nil && (stat.Mode()&os.ModeCharDevice) == 0 {
		fmt.Fprintf(os.Stderr, "\n⚠️  HIGH-RISK COMMAND DETECTED (non-interactive session, cannot prompt):\n")
		fmt.Fprintf(os.Stderr, "   %s\n", command)
		fmt.Fprintf(os.Stderr, "   Denied by default. Set ORCH_AUTO_APPROVE=1 to allow high-risk commands in CI/scripts.\n")
		return false
	}

	fmt.Fprintf(os.Stderr, "\n⚠️  HIGH-RISK COMMAND DETECTED:\n")
	fmt.Fprintf(os.Stderr, "   %s\n", command)
	fmt.Fprintf(os.Stderr, "   Execute? [y/N]: ")

	// Read the response off the main goroutine so Ctrl+C can interrupt a
	// pending prompt instead of blocking forever on Scanln.
	responseCh := make(chan string, 1)
	go func() {
		var response string
		fmt.Scanln(&response)
		responseCh <- response
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT)
	defer signal.Stop(sigCh)

	select {
	case response := <-responseCh:
		response = strings.TrimSpace(strings.ToLower(response))
		return response == "y" || response == "yes"
	case <-sigCh:
		fmt.Fprintf(os.Stderr, "\n⚡ cancelled, denying command\n")
		return false
	}
}

// truncateStr truncates s to at most max runes (not bytes) so CJK text isn't
// cut mid-character into invalid UTF-8 — this codebase is CJK-heavy (handoff
// docs, task summaries, prompts) and a byte-slice truncation here previously
// risked feeding a broken multi-byte sequence straight into MLX request JSON.
func truncateStr(s string, max int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "..."
}
