# orch — AI Task Orchestration CLI

A single CLI entry point: describe what you need, and it plans, dispatches, executes, verifies, and delivers.

**MLX on Apple Silicon as the primary engine** — local inference handles routing and simple chat without cloud calls. Cloud AI backends (kiro/claude/gemini) serve as fallback for complex tasks.

## Origin

This project was inspired by [ORCH](https://github.com/oxgeneral/ORCH) (a TypeScript-based multi-agent orchestrator) and [Microsoft Conductor](https://github.com/microsoft/conductor) (Python, YAML-driven workflow engine). Both solve the same problem: **coordinating multiple AI agents from a single interface**.

The core idea: instead of manually switching between different AI CLI tools (kiro, claude, gemini), why not have a single entry point that **routes tasks automatically**?

What makes this version different:

1. **Local-first with MLX** — A small LLM (Qwen 2.5 3B) running locally on Apple Silicon handles task classification and simple chat in <1 second. No cloud calls, no API costs, no latency for everyday use.
2. **Cloud as fallback** — Only when the local model can't handle complex multi-step planning does it escalate to a subscribed AI CLI (kiro, claude, or gemini — whichever you have).
3. **Single binary, zero runtime dependencies** — Written in Go. No Node.js, no Python venv to manage (except for MLX inference itself). Just build and run.
4. **CLI-native** — Works with Unix pipes, integrates into existing shell workflows, and respects the terminal as the primary interface.

The 3-layer routing architecture:

```
User Input
    ▼
┌──────────────────────────────────────────────┐
│  Layer 1: Keyword + CLI Detection (⚡ 0ms)    │  70+ known CLIs → shell direct
└────────────┬─────────────────────────────────┘
             ▼ no match
┌──────────────────────────────────────────────┐
│  Layer 2: MLX Local LLM (🍎 <1s)             │  classification routing + chat
└────────────┬─────────────────────────────────┘
             ▼ complex task / MLX fails
┌──────────────────────────────────────────────┐
│  Layer 3: Cloud AI Backend (☁️ 5min timeout)  │  kiro / claude / gemini
└──────────────────────────────────────────────┘
             ▼
┌──────────────────────────────────────────────┐
│  DAG Executor (parallel goroutines)           │
└──────────────────────────────────────────────┘
             ▼
┌──────────────────────────────────────────────┐
│  Event Bus (reactive chaining)                │
└──────────────────────────────────────────────┘
             ▼
┌──────────────────────────────────────────────┐
│  Memory Layer (SQLite, auto-prune)            │
└──────────────────────────────────────────────┘
```

### Layer 1 Design: Zero-Latency CLI Detection

Layer 1 uses three strategies to route commands instantly (no AI involved):

1. **Configured keyword shortcuts** — Prefix matching from `config.yaml` (e.g., `kubectl`, `terraform plan`)
2. **First-word CLI detection** — If the first token is a known CLI binary (70+ registered: `k`, `tf`, `docker`, `git`, `npm`, `brew`, etc.), route directly to shell
3. **Chat detection with tech exclusion** — Greetings and social chat are caught here, but only if they don't contain technical keywords (prevents "幫我查 GKE pod 狀態" from being misrouted as chat)

### Layer 2 Design: Classification, Not Generation

Small models (≤3B) are unreliable at generating structured output (JSON). Instead of asking the local LLM to produce a full execution plan, Layer 2 uses a **classification-only approach**:

1. **Chat detection** — Common greetings and Q&A patterns are caught by keyword matching before even calling MLX
2. **MLX classification** — The model outputs only `agent:category` (e.g., `kiro:infra`), a single token pair that's nearly impossible to corrupt
3. **Plan assembly** — The program constructs the execution plan from the classification result

For direct chat (category=chat), the local model generates a free-text response with **repetition truncation**: if the output degenerates into loops (common with small models), it's automatically cut at the last coherent point.

## Design Philosophy: Lightweight Router vs Heavy Framework

Projects like [ORCH](https://github.com/oxgeneral/ORCH) (TypeScript) take a **monolithic** approach: the framework itself manages agents, defines roles, tracks state machines, handles inter-agent messaging, and isolates work via git worktrees. It's an "AI company simulator" — you set a goal, deploy a team of 5 agents, go to sleep, and wake up to pull requests.

Our orch takes the **microservices** approach: each AI CLI (kiro, claude, gemini) is already a fully capable agent with its own skills, tools, and domain knowledge. orch is just the **API gateway** — it routes, dispatches, and chains results.

```
ORCH (TypeScript):
  Heavy framework → manages everything internally
  Agent intelligence lives inside ORCH's role prompts
  Good for: overnight autonomous runs, multi-agent code generation

Our orch (Go):
  Lightweight router → each endpoint is self-contained
  Agent intelligence lives in each CLI's own config (CLAUDE.md, .kiro/steering/)
  Good for: daily CLI workflow, local-first, cost-efficient
```

### Why this works in practice

If you've already invested in configuring your AI CLIs — giving them personas, skills, MCP tool connections, routing rules — then you don't need a heavy framework re-implementing all that. You need a dispatcher that knows **which tool to call** and **how to chain their outputs**.

| ORCH concept | Our equivalent |
|-------------|----------------|
| Agent + Role definition | `CLAUDE.md` / `.kiro/steering/` persona & rules |
| Agent skills | MCP tools (Notion, GCal, AWS), built-in skills |
| Team template | Agent routing table in steering config |
| Inter-agent messaging | Shared `_agent_handoff.md` + DAG output chaining |
| State machine | Workflow YAML + handoff protocol |
| CTO task decomposition | orch planner (MLX → DAG plan) |

### When to use which

| Scenario | Better choice |
|----------|--------------|
| "Build an entire SaaS overnight with 5 agents" | ORCH (TypeScript) |
| "I have kiro + claude configured with MCP tools, just route my daily tasks" | orch (this project) |
| "Run agents 24/7 on a server, zero human intervention" | ORCH (headless daemon) |
| "Quick local inference, pipe-friendly, $0/day for routine work" | orch (MLX Layer 2) |

## Real-World Use Case

This is how orch is actually used in a multi-AI workspace with kiro-cli and claude (Victoria):

```
┌─────────────────────────────────────────────────────┐
│  orch (Go binary — router + MLX local inference)     │
└───────────┬─────────────────────────┬───────────────┘
            │                         │
            ▼                         ▼
┌───────────────────────┐   ┌───────────────────────────┐
│  kiro-cli             │   │  claude -p (Victoria)      │
│  ├─ .kiro/steering/   │   │  ├─ CLAUDE.md persona      │
│  │  ├─ agent-registry │   │  │  ├─ Sub Agent routing   │
│  │  ├─ global-rules   │   │  │  ├─ 3-layer handoff     │
│  │  └─ handoff proto  │   │  │  └─ Notion/GCal skills  │
│  ├─ AWS/GCP/infra ops │   │  ├─ MCP: Notion, Gmail     │
│  └─ code gen, terraform│   │  └─ meeting notes, writing │
└───────────────────────┘   └───────────────────────────┘
```

### Daily workflow

```bash
# Morning: orch routes to MLX for quick tasks
orch "kubectl get pods"              # Layer 1: keyword → shell direct
orch "昨天那個 PR 改了什麼"           # Layer 2: MLX answers locally

# Complex: falls through to cloud backend
orch "幫我整理今天三場會議記錄到 Notion"  # Layer 3: → claude (has Notion MCP)
orch "terraform plan for litellm-gke"    # Layer 3: → kiro (has AWS/GCP skills)

# Workflow: pre-defined DAG, no AI planning needed
orch "signoff"                           # YAML workflow: kiro handoff → claude Notion sync
```

### Cost profile

| Usage pattern | Daily cost |
|---------------|-----------|
| 80% routine (keyword + MLX) | $0 |
| 15% moderate (single cloud call) | ~$0.50 |
| 5% complex (multi-step DAG) | ~$1-2 |
| **vs. ORCH with 5 agents** | **$4-20/run** |

The key insight: most daily work is simple enough for a 3B parameter model running locally. You only pay for cloud when you genuinely need it.

## Requirements

| Requirement | Why |
|-------------|-----|
| **macOS (Apple Silicon M1+)** | MLX inference requires Apple Silicon |
| **Go 1.22+** | To build the binary |
| **Python 3.10+** | For MLX local LLM server |
| **At least one AI CLI** | Cloud fallback — pick one: `kiro-cli`, `claude`, or `gemini` |

## Step-by-Step Setup

### 1. Install Go (if not installed)

```bash
brew install go
```

Verify: `go version` should show 1.22+.

### 2. Clone and build

```bash
git clone https://github.com/GordonWei/orch.git
cd orch
make build    # compiles → ./orch
make install  # installs → /usr/local/bin/orch (requires sudo)
```

### 3. Set up MLX environment

```bash
# Create a dedicated Python venv for MLX
python3 -m venv ~/mlx-env
source ~/mlx-env/bin/activate

# Install MLX LM
pip install mlx-lm

# Verify
python3 -m mlx_lm.server --help
deactivate
```

### 4. Run the setup wizard

```bash
orch init
```

This will:
- Detect which AI CLI backends you have installed (kiro-cli, claude, gemini)
- Ask your preferred language and name
- Let you choose the MLX model
- Write `~/.config/orch/config.yaml`

### 5. Install the MLX LaunchAgent (auto-start on login)

```bash
make install    # or: ./setup.sh
```

This creates a macOS LaunchAgent that keeps the MLX server running in the background.

### 6. Verify

```bash
# Should print version
orch --version

# Should show detected tools and backend
orch --tools

# Try it — should route to MLX locally
orch "hello"

# Should show the routing layer used
orch --dry-run "check disk usage"
```

### Minimal Setup (no make install)

If you just want to try it quickly without the daemon:

```bash
# Build
go build -o orch ./cmd/orch
sudo cp orch /usr/local/bin/orch

# Start MLX server manually (in another terminal)
~/mlx-env/bin/python3 -m mlx_lm.server \
  --model mlx-community/Qwen2.5-3B-Instruct-4bit --port 8080

# Run
orch init
orch "hello"
```

## Usage

```bash
# Oneshot (single task)
orch "check S3 bucket usage"
orch "kubectl get nodes"

# Override backend for one command
orch --backend gemini "summarize this 200-page doc"

# Verbose mode (show MLX debug output)
orch --verbose "check system status"

# Dry-run (plan only, no execution)
orch --dry-run "consolidate AWS and GCP usage report"

# Unix pipe
kubectl get pods -o json | orch "which pods are unhealthy?"
cat error.log | orch "analyze this error"

# REPL mode (continuous interaction)
orch

# Subcommands
orch init                    # interactive setup wizard
orch history                 # last 20 entries
orch history search kubectl  # search history
orch briefing                # show daily briefing
orch briefing gen            # auto-generate briefing via MLX
```

### REPL Commands

| Command | Description |
|---------|-------------|
| `/session <claude\|kiro>` | Start or attach to a backend session (enters session mode) |
| `/switch <claude\|kiro>` | Switch between running sessions |
| `/sessions` | List all running sessions (uptime, idle status) |
| `/back` | Return to normal mode (session stays alive in background) |
| `/kill [backend\|all]` | Terminate a session |
| Ctrl+C | Same as `/back` when in session mode |
| `/w` | List available workflows |
| `/w 1` | Execute workflow #1 |
| `/h` | Last 10 history entries |
| `/b` | Show briefing |
| `/help` | List all commands |

### Session Mode

Session mode provides **persistent interactive sessions** with AI CLI backends. Instead of one-shot calls through the planner, your input is directly forwarded to the backend's live PTY session.

```bash
› /session claude        # spawn a claude session
🔌 connecting to claude...
✅ session active: claude (type /back to return to orch)

claude› help me refactor this function    # forwarded directly to claude
[claude's response appears here]

claude› /back            # return to orch normal mode
⏎ back to normal mode (session claude still alive in background)

› /session kiro          # start a kiro session too
kiro› deploy the terraform changes
[kiro's response]

kiro› /switch claude     # switch back to claude (still alive)
✅ switched to claude

claude› /sessions        # see all running sessions
📋 Sessions:
  → claude — up 5m32s
    kiro — up 2m18s (idle)

claude› /kill all        # terminate everything
💀 killed all sessions
›                        # back to normal mode
```

Shorthand: `c` = claude, `k` = kiro.

### REPL Session Context

The REPL maintains a **5-turn sliding window** of conversation history. This context is:
- Injected into cloud backend prompts (so agents know what you discussed earlier)
- Used by DirectChat for local model responses (no cross-turn amnesia)
- Stripped before classification (so routing decisions aren't polluted by prior turns)

## How Backend Fallback Works

If you only have **one** AI CLI installed (say, just `gemini`):

1. orch detects only gemini is available → sets it as primary
2. When a plan says "use kiro for this step" → orch **automatically routes to gemini instead**
3. No errors, no broken workflows — everything just works with your single backend

Override priority: `--backend` flag > `ORCH_BACKEND` env > config file > auto-detect

## Configuration

Generated by `orch init` at `~/.config/orch/config.yaml`.

### AI Backend

```yaml
ai_backend:
  primary: "kiro"    # kiro | claude | gemini | "" (auto-detect)
```

### MLX Model

```yaml
models:
  - name: "mlx-default"
    backend: "mlx"
    endpoint: "http://localhost:8080"
    model: "mlx-community/Qwen2.5-3B-Instruct-4bit"
    python_path: "~/mlx-env/bin/python3"
    auto_start: true
    port: "8080"
    default: true
```

To use a smaller model (less RAM, faster but less accurate):

```yaml
  - name: "qwen-1.5b"
    model: "mlx-community/Qwen2.5-1.5B-Instruct-4bit"
```

### Workspace Routing (optional)

```yaml
workspace:
  root: "~/projects"
  subdirs:
    - name: backend
      keywords: [api, server, database]
    - name: frontend
      keywords: [react, css, ui]
```

### YAML Workflows

Place workflow files in `~/.config/orch/workflows/`:

```yaml
name: "daily-signoff"
trigger: "signoff"
description: "End of day handoff routine"
steps:
  - id: gather
    agent: kiro
    prompt: "read docs/_agent_handoff.md and summarize today's progress"
  - id: sync
    agent: claude
    prompt: "sync the summary to Notion"
    depends_on: [gather]
```

## Project Structure

```
cmd/orch/
├── main.go              CLI entry + signal handler + task execution
├── repl.go              REPL interactive mode (session mode, slash commands)
├── session_manager.go   Multi-session lifecycle manager (spawn/switch/kill/watch/auto-restart/shutdown)
├── route_hint.go        Intelligent cross-domain route suggestions (73 rules, 3-tier confidence)
├── init.go              Interactive setup wizard
├── printer.go           Event output formatting
└── dag.go               ASCII DAG rendering

pkg/
├── backend/         AI CLI backend interface + adapters (kiro/claude/gemini) + timeout
├── config/          Config loader (YAML → struct)
├── model/           Local LLM interface (OpenAI-compatible) + MLX auto-start with progress
├── memory/          SQLite memory layer (history + briefing + auto-prune)
├── registry/        Local tool scanner (which CLIs are on this machine)
├── planner/         3-layer routing: keyword/CLI detect → MLX classification → cloud
├── executor/        DAG parallel execution engine (goroutines + streaming)
├── eventbus/        Reactive workflow chaining (trigger rules + MLX gate + summarize)
├── session/         PTY-based interactive session (spawn/send/read/kill + idle detection + ANSI strip with alt screen awareness)
└── workflow/        YAML workflow template loader

launchd/             macOS LaunchAgent for MLX daemon
config.yaml          Config template
setup.sh             Full installation script
Makefile             build/test/install targets
```

## Development

```bash
make build     # compile
make test      # run tests
make lint      # go vet
make cover     # coverage report
make clean     # remove artifacts
```

## Troubleshooting

### MLX server won't start

```bash
# Check if python venv exists
ls ~/mlx-env/bin/python3

# Start manually to see errors
~/mlx-env/bin/python3 -m mlx_lm.server \
  --model mlx-community/Qwen2.5-3B-Instruct-4bit --port 8080

# Port conflict
lsof -i :8080
```

### All requests go to cloud (Layer 3)

MLX server isn't responding:
```bash
curl http://localhost:8080/v1/models   # should return 200
```

### No AI backend available

Install at least one:
```bash
# Option A: Kiro
npm install -g @anthropic-ai/kiro

# Option B: Claude Code
npm install -g @anthropic-ai/claude-code

# Option C: Gemini CLI
npm install -g @anthropic-ai/gemini  # or: brew install gemini
```

## Changelog

### v0.10.1 (2026-07-10)

**Session mode hardening, round 2 — fixing bugs the "hardening" release introduced.**

An 8-angle code review of the v0.10.0 diff found 9 confirmed bugs (all verified against actual code behavior, not just read) — several of them directly contradicting v0.10.0's "no more orphan processes" / "crash resilience" claims. All fixed, with regression tests added for the two that were pure-function-testable.

- **ANSI strip: UTF-8 safety** — `Strip()` checked C1 control bytes (0x90/0x98/0x9B/0x9E/0x9F) by raw byte value with no UTF-8 awareness. Traditional Chinese text routinely hits these exact byte values as continuation bytes (e.g. 記 → `E8 A8 98`), so ordinary CJK session output was getting silently truncated. Now decodes with `utf8.DecodeRuneInString` first; valid multi-byte runes pass through untouched, and only genuinely invalid/stray high bytes are checked against the C1 table.
- **ANSI strip: sequences split across PTY reads** — `StripState` tracked `InAltScreen` across chunked `Read()` calls but never buffered a truncated escape sequence, so a CSI/OSC/DCS sequence landing on a 4096-byte chunk boundary got silently dropped instead of completed — in the worst case (alt-screen leave sequence split) a session would go permanently blank with no error. `StripState` now buffers the incomplete tail in a `pending` field and prepends it to the next `Strip()` call.
- **Idle detection starved during alt-screen** — `idle.Ping()` only fired when cleaned output was non-blank; since alt-screen suppresses all output, a TUI backend rendering for longer than `IdleTime` (default 5s) would let the idle timer expire mid-render, `Read()` would return early, and the real answer (written once alt-screen exits) was lost — the next `Send()` resets the output buffer before anyone read it. `Ping()` now fires on any PTY read activity, not just non-blank cleaned output.
- **`Session.Kill()` deadlock** — held its mutex across the blocking wait on `<-s.done`, but `readLoop`'s cleanup (the only code that closes `s.done`) needs that same mutex first. Killing a live session would hang forever. Lock is now released before waiting.
- **`checkSessions()` auto-restart races** — unlocked between deleting a dead session and writing back its replacement, with no protection against a concurrent user `Spawn()`/`SpawnOrSwitch()` (silently overwritten, orphaning the user's process) or a concurrent `Shutdown()`/`KillAll()` (restarted session written into the map *after* shutdown believed everything was torn down). Added a `generation` counter (bumped by `KillAll()`) plus a permanent `shutdown` flag (`Shutdown()`) that `checkSessions()` checks before writing back — on conflict it kills its own redundant restart instead of overwriting.
- **`stopWatch` signal could be dropped** — the non-blocking `select { case stopWatch <- struct{}{}: default: }` silently dropped the stop signal if the watcher goroutine was mid-`checkSessions()`, leaking the ticker goroutine forever (and, post-`KillAll()`, letting it resurrect a backend later). Changed to `close(stopWatch)` behind a `sync.Once` — closing a channel can't be dropped or missed regardless of timing.
- **`Shutdown()` mislabeled graceful exits as "killed"** — the final event-emission loop iterated over the full original session set instead of just the subset that actually needed force-killing, so sessions that exited cleanly within the 5s deadline got an incorrect `killed` event anyway.
- **Ctrl+C had zero grace period outside session mode** — v0.10.0 replaced the old unconditional 500ms shutdown sleep with a hook that's only registered inside `runREPL()`; one-shot (`orch "prompt"`) and subcommand (`orch history`/`orch briefing`) invocations got zero grace period on Ctrl+C. Restored the 500ms fallback when no hook is registered.
- **Ctrl+C had no escape hatch during a slow shutdown** — the signal handler read `sigCh` exactly once; a second impatient Ctrl+C during session mode's up-to-8s `Shutdown()` sequence did nothing. Shutdown now runs in a background goroutine while the handler keeps listening — a second signal forces an immediate exit.

**Not changed**: `route_hint.go`'s 73-rule keyword table duplicates keyword-routing logic that already lives in `pkg/config` (`Routing`/`KeywordShortcuts`) and `pkg/planner` (`classifyInputType`) — flagged as a design/maintainability concern (not a bug), left as-is pending a deliberate decision on whether to consolidate.

- **Test coverage**: 5 new regression tests (CJK text integrity, CJK+ANSI mixed, alt-screen sequence split across chunks, known two-char ESC sequence split across chunks). `go build`/`go vet`/`go test -race ./...` all clean.

### v0.10.0 (2026-07-10)

**Session mode hardening — ANSI strip, intelligent route hints, crash resilience.**

Completes the "second round" of session mode improvements. The session experience is now production-quality with proper TUI handling, smart routing, and fault tolerance.

- **ANSI strip: alternate screen buffer awareness** — New `StripState` struct tracks alternate screen buffer mode across chunked reads. When a TUI app enters alt screen (`ESC[?1049h`, `ESC[?47h`, `ESC[?1047h`), all content is discarded until the leave sequence arrives. This eliminates TUI chrome (menus, progress bars, status lines) from session output while preserving the meaningful text. `stripANSI` moved to its own file (`pkg/session/strip.go`) for clarity.
- **Route hints v2: 73 rules, 3-tier confidence, cooldown** — Complete rewrite of `route_hint.go`. Rules expanded from 25 → 73, covering multi-word phrases (matched first to avoid false positives), single keywords, and both Chinese and English patterns. Three confidence levels (strong/medium/weak) — only medium+ triggers suggestions. Built-in cooldown (3 inputs between hints) prevents nagging. Contextual reason messages explain WHY the switch is suggested.
- **Session crash detection & auto-restart** — `WatchSessions()` background goroutine polls session health every 2 seconds. Dead sessions emit `SessionEvent` notifications to the REPL. Optional `SetAutoRestart(backend, true)` automatically respawns crashed sessions. `SessionInfo` now includes `RestartCount` and `LastOutput` timestamp.
- **Graceful shutdown** — New `Shutdown()` method: sends exit commands → waits 5s → force kills remaining. Signal handler (`SIGINT`/`SIGTERM`) now calls `Shutdown()` ensuring all PTY file descriptors are properly closed on exit. No more orphan processes.
- **Test coverage** — 17 new tests for session manager (lifecycle, cooldown, route hints, stateful strip). Total: 38 tests across session + cmd packages.

### v0.9.0 (2026-07-08)

**Interactive Session Mode — persistent PTY sessions with AI backends.**

The biggest UX shift since v0.1: instead of one-shot planner calls, you can now **attach to a live backend session** and have a multi-turn conversation directly through orch.

- **Session mode**: `/session claude` or `/session kiro` spawns a persistent PTY session. All subsequent input is forwarded directly — no planning, no routing, no overhead.
- **Multi-session support**: Run claude and kiro sessions simultaneously. `/switch` between them; `/sessions` to see what's alive; `/kill` to terminate.
- **Prompt indicator**: When in session mode, the prompt shows the active backend (`claude›` or `kiro›`). Normal mode shows `›`.
- **Graceful lifecycle**: Sessions survive `/back` (return to normal mode). They run in background until explicitly `/kill`ed or orch exits. `Ctrl+C` in session mode = `/back`.
- **Shorthand**: `c` = claude, `k` = kiro (e.g., `/session c`).
- **New package `pkg/session/`**: PTY-based session management (macOS native `/dev/ptmx`, no third-party deps). Includes idle detection, ANSI stripping, and graceful kill (sends `/exit` or `/quit`, waits 3s, then SIGKILL).
- **New file `session_manager.go`**: Manages multiple sessions with single-active-pointer pattern.

This solves the original pain point: "I still have to switch between kiro and claude terminals manually." Now orch is the single interface for everything — quick local tasks (normal mode) and deep interactive work (session mode).

### v0.8.0 (2026-07-06)

**Routing accuracy & robustness overhaul.**

- **Layer 1 expanded**: First-word CLI detection (70+ known binaries including `k`, `tf`, `docker`, `git`, `npm`, `make`, `brew`, `echo`, `cd`). Commands route in 0ms without hitting MLX.
- **Chat detection tightened**: Added ~50 technical keyword exclusions. "幫我查 S3 bucket" no longer misroutes as chat. Chat patterns narrowed (removed overly broad "how to", "can you", "請問").
- **Unified input classifier**: `classifyInputType()` is now the single classification function — the Layer 1 chat short-circuit (`tryKeywordPlan`) and the plan-fixup reroute check (`fixPlan`) both call it directly, with one shared keyword list. (An earlier pass left the old `looksLikeChat` helper in place alongside it with a different keyword list, so the two call sites could silently disagree on the same input — that helper has been removed.)
- **REPL stability**: Removed `os.Pipe()` stdout capture hack. `runTask` remains the single place that prints task output — immediately after execution, *before* event-bus chains run (chains can block on cloud backends for minutes, so printing must not wait for them). The returned output value exists solely so the REPL can feed it into session context; callers must never print it. (This took three passes to get right: the first had both `runTask` and the REPL print, so every REPL reply appeared twice — caught via a pty-based test. The second moved printing to the callers, which deferred the main output until after all chains completed and inverted the main/chain output order. Final design: `runTask` prints, callers don't.) Verified with a pty-driven REPL session (reply appears exactly once) and a oneshot `echo` run (stdout contains exactly one copy, routed via Layer 1); see SOP "REPL replies appear twice" for the reusable check.
- **Backend timeout**: All AI CLI calls (kiro/claude/gemini) now have a 5-minute timeout with automatic process kill. No more infinite hangs.
- **MLX startup UX**: Progress indicator during MLX server startup (`⏳ waiting for mlx server... 5s`).
- **Session context fix**: REPL session context now properly flows to DirectChat (local model no longer amnesiac across turns). Classification uses stripped input to avoid routing pollution.
- **History auto-prune**: New `Store.AutoPrune()` keeps SQLite bounded (default limit: 1000 entries).
- **Event Bus observability**: Chain failures are recorded in history with retry command hint. Successful chains also logged.
- **Config alignment**: Default model in template changed to 3B (matches README and actual usage).
- **Test coverage**: Added `TestClassifyInputType_*` covering command/chat/NL classification and a `TestClassifyInputType_SingleSourceOfTruth` test that fails if `tryKeywordPlan`'s chat routing and `classifyInputType` ever diverge again. This was the one part of the rewrite that shipped with zero tests initially.

### v0.7.2 (2026-07-03)

- REPL session context (5-turn sliding window)
- REPL stability: failure no longer exits process

### v0.7.1 (2026-07-03)

- Bypass permission for all backends (claude `--dangerously-skip-permissions`, gemini `--skip-trust`)

### v0.7.0 (2026-07-03)

- MLX architecture rewrite: classification-only routing (no JSON generation)
- Model upgrade: Qwen 2.5 1.5B → 3B
- Chat detection (`looksLikeChat`) skips MLX planning
- DirectChat repetition truncation
- `--verbose` flag

### v0.6.0 (2026-07-02)

- Event Bus reactive workflow chaining (`pkg/eventbus/`)
- MLX gate (YES/NO decision before cloud dispatch)
- Output compression for downstream prompts

### v0.5.0 (2026-07-02)

- Backend abstraction (`pkg/backend/` — 3 adapters + Registry + auto-detect/fallback)
- `orch init` interactive setup wizard
- `--backend` flag override

### v0.3.0 (2026-07-02)

- `orch history` / `orch briefing` subcommands
- `--dry-run` with ASCII DAG visualization
- REPL slash commands (`/w`, `/h`, `/b`)
- stdin pipe integration
- MLX launchd daemon

### v0.2.1 (2026-07-01)

- DAG parallel execution (goroutines + DFS cycle detection)
- YAML workflow templates
- Streaming output (line-by-line for shell, progress for AI)

### v0.2.0 (2026-07-01)

- SQLite memory layer (history + briefing)
- Configurable models
- Re-plan loop (up to 2 retries)

### v0.1.0 (2026-06-30)

- Initial release: 3-layer routing + MLX + REPL + 15 unit tests

## License

MIT
