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
| `/auto [on\|off]` | Toggle auto-route mode (strong keywords auto-switch sessions) |
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

**Streaming Output (v0.11.1+)**: Session responses now stream in real-time as chunks arrive from the backend, instead of waiting until the backend goes idle and dumping all output at once. This dramatically improves UX for long responses.

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

### Route Rules (v0.11+)

Control how orch suggests and auto-switches between backends in session mode:

```yaml
route_rules:
  cooldown: 3          # min inputs between route hints
  auto_route: false    # set true to enable auto-switching
  history_size: 5      # sliding window for context momentum
  rules:
    # Phrase rules (checked first, multi-word patterns)
    - pattern: "terraform plan"
      target: kiro
      strength: 3        # 1=weak, 2=medium, 3=strong
      type: phrase
    - pattern: "會議記錄"
      target: claude
      strength: 3
      type: phrase
    # Keyword rules (single-word patterns)
    - pattern: notion
      target: claude
      strength: 3
      type: keyword
    # CLI detection rules (first-word matching for Classify)
    - pattern: kubectl
      target: shell
      strength: 3
      type: cli
    # Chat pattern rules
    - pattern: 你好
      target: ""
      strength: 0
      type: chat
```

The default config includes ~100 rules. Add your own domain-specific patterns without code changes.

## Project Structure

```
cmd/orch/
├── main.go              CLI entry + signal handler + task execution
├── repl.go              REPL interactive mode (session mode, slash commands, /auto)
├── session_manager.go   Multi-session lifecycle manager (spawn/switch/kill/watch/auto-restart/shutdown)
├── init.go              Interactive setup wizard
├── printer.go           Event output formatting
└── dag.go               ASCII DAG rendering

pkg/
├── backend/         AI CLI backend interface + adapters (kiro/claude/gemini) + timeout
├── config/          Config loader (YAML → struct, including route rules)
├── model/           Local LLM interface (OpenAI-compatible) + MLX auto-start with progress
├── memory/          SQLite memory layer (history + briefing + auto-prune)
├── registry/        Local tool scanner (which CLIs are on this machine)
├── router/          Unified routing: keyword/phrase hints, CLI detection, chat matching, history momentum, auto-route
├── planner/         3-layer routing: keyword/CLI detect → MLX classification → cloud
├── executor/        DAG parallel execution engine (goroutines + streaming)
├── eventbus/        Reactive workflow chaining (trigger rules + MLX gate + summarize)
├── session/         PTY-based interactive session (spawn/send/read/kill + idle detection + ANSI strip with alt screen awareness)
└── workflow/        YAML workflow template loader (trigger synonyms via []string, word-boundary matching)

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

### v0.16.4 (2026-07-19)

**Real root cause found for orch's local chat feeling "stuck": every MLX HTTP call had no timeout at all. Plus the collection of quality-of-life bugs that surfaced while chasing it — dynamic routing label, real-time-info routing, date/briefing awareness in `DirectChat()`.**

- **🔴 Fixed: `mlxAvailable()`/`tryMLX()`/`DirectChat()` used the bare `http.Get`/`http.Post` package functions, which go through `http.DefaultClient` — `Timeout: 0`, i.e. no timeout at all.** `mlx_lm.server` on Apple Silicon was confirmed (via direct curl testing, bypassing orch entirely) to have occasional, unpredictable slow windows on `/v1/chat/completions` — the exact same request took 6s once and didn't return within 60s another time, with nothing else changed. With no client-side timeout, hitting one of those slow windows hung the entire orch process forever: no error, no fallback, indistinguishable from a crash. New `mlxPingClient` (5s) and `mlxChatClient` (60s) package-level clients replace every bare call. Two regression tests: one asserts both timeouts are non-zero, one drives a real hanging mock server end-to-end and confirms `DirectChat()` returns an error within ~2s instead of hanging (temporarily shortens the client timeout so the test itself doesn't take a minute).
- **🔴 Fixed: `DirectChat()`'s system prompt never included today's date or any project context.** The `briefing_source_file` feature (v0.16.2) re-summarized the handoff doc fresh on every boot, but that summary was only ever printed to the terminal for the human to read — `DirectChat()` itself never received it, so questions like "what's still pending" got an answer invented from nothing. New `SetBriefing()` on `Planner` threads the current briefing (and today's date) into `DirectChat()`'s system prompt. Also fixed: `generateBriefingFromFile`'s truncation (`truncateStr`) sliced by byte length, not runes — for CJK-heavy handoff docs this could cut a multi-byte character in half, feeding invalid UTF-8 into the MLX request JSON.
- **🔴 Fixed: `DirectChat()`'s `max_tokens: 2048` meant a reply that never hit a natural stop token could legitimately take up to ~2 minutes** at this hardware's measured ~16 tokens/sec local decode speed — indistinguishable from a hang before the timeout fix above existed. Reduced to 400, which comfortably covers a normal chat-length reply.
- **🔴 Fixed: the routing debug line hardcoded `"MLX local (Qwen 2.5 3B)"` regardless of the actually configured model** — stayed wrong even after the default model was upgraded to 7B. New `modelDisplayName()` derives the label from the configured model string generically (works for any org/model/quant naming, not just Qwen).
- **🔴 Fixed: the classification prompt had no rule for real-time/external information** (weather, news, stock prices, current events) — the local model would guess `local:chat` and fabricate an answer from stale training data rather than routing to a backend that can actually look things up. Added an explicit `claude:query` rule with an example.
- Bumped `generateBriefingFromFile`'s prompt to explicitly ask for pending/to-do items to be listed individually rather than merged into one narrative sentence, since that's what callers actually need from it now that `DirectChat()` reads it directly.

### v0.16.2 (2026-07-17)

**Two more real bugs found from actual daily use, both about the gap between what orch's small local model can reliably decide on its own vs. what routing logic was blindly assuming.**

- **🔴 Fixed: short Chinese input was unconditionally guessed as chat.** `router.Classify()` treated any Chinese input at or under 10 runes as chat — including task references like `那讀交接` ("then read the handoff"), which got routed straight to the local 3B model's tool-less `DirectChat()` and produced an unhelpful, ungrounded answer. This length heuristic was redundant with (and less precise than) the existing curated `type: "chat"` rules for actual greetings — none of those tests relied on it. Now config-driven via `route_rules.chat_short_input_max_len` (default `1`, just enough for bare acknowledgments like `好`); everything else falls through to keyword/CLI checks and then real MLX classification, letting the local model make an actual judgment call instead of a blind length guess. Two regression tests added, plus one confirming the config knob can restore the old behavior for anyone who wants more input to skip MLX.
- **🔴 Fixed: the startup briefing could silently go stale indefinitely.** `memory.auto_summarize: true` implied automatic re-summarization "after each task," but no code anywhere ever called `SetBriefing()` outside the manual `orch briefing set`/`orch briefing gen` commands — a fully dead config flag. New `memory.briefing_source_file` config option (prompted during `orch init`) points at a status/handoff document you maintain by hand; when set, every startup re-reads and re-summarizes it fresh via the local model instead of trusting a cache that only updates when you remember to run `orch briefing gen`. Falls back to the last cached briefing on any failure (missing file, MLX down) rather than blocking startup. `auto_summarize` is now documented as reserved/unimplemented rather than silently doing nothing.

### v0.16.1 (2026-07-17)

**Two real bugs found and fixed by dogfooding v0.16.0 with a real user config for the first time, plus a small workflow feature.**

- **🔴 Fixed: MLX misclassification could route natural language to a literal shell exec.** `tryMLX()`'s fallback safety net (`fixPlan()`) existed and was tested, but was never actually called from `GeneratePlan()` — pure dead code. On top of that, its own condition (`isNaturalLanguage && step.Command != userInput`) could never fire for the exact case `tryMLX()` produces (it copies the raw input verbatim into `step.Command`, so the two are always equal). Net effect: when the 3B classifier guessed `agent: shell` for something like `讀取 /path/to/file`, orch executed the literal Chinese sentence as a shell command and failed with `bash: 讀取: command not found`. Now `fixPlan()` is wired in after every MLX classification and reroutes to `claude` whenever the router's own (deterministic) classifier says the input is natural language — regardless of what the small model guessed. Two regression tests added, one of which (`TestGeneratePlan_MLXShellMisclassification_Rerouted`) spins up a mock MLX server and drives the real `GeneratePlan()` path end-to-end specifically to catch this class of "the fix exists but isn't wired up" bug in the future.
- **🔴 Fixed: `pkg/router`/`pkg/planner` tests read the real user's `~/.config/orch/config.yaml`.** Test helpers and a package-level `var defaultRouter` all called `config.Load()`, which follows the same `ORCH_CONFIG` → `~/.config/orch/config.yaml` → built-in defaults resolution used in production — meaning `go test ./...` was never actually hermetic, it just took until a machine had a real user config (enabling Bedrock/Vertex AI, see v0.16.0 below) to surface. New exported `config.DefaultRouteRules()` gives tests (and the `classifyInputType` backward-compat shim) a pure, file-system-free fixture.
- **`file_context` for workflow steps** — a step can now declare `file_context: [path, ...]`; the file contents are read and prepended to that step's prompt before dispatch, instead of relying on the agent to have (or use) its own file-read tool for a known path. Used to simplify `workflows/morning.yaml`'s handoff-reading step.

### v0.16.0 (2026-07-17)

**Bedrock + Vertex AI Stateless API Backends — direct cloud model invocation with real cost tracking.**

The architecture now supports a second backend type: stateless HTTP API backends. Unlike the existing CLI-based PTY adapters (kiro/claude/gemini that spawn interactive processes), API backends make one-shot HTTP calls to cloud model APIs and return structured responses with token usage metadata.

- **New `pkg/apibackend/` package** — Defines the `APIBackend` interface (`Name()`, `Available()`, `Invoke(ctx, Request) (*Response, error)`), with `Request` (prompt, messages, max_tokens, temperature), `Response` (content, input/output tokens, model, stop_reason, latency), and `Message` structs for multi-turn context.
- **Bedrock adapter** — Calls AWS Bedrock Runtime `Converse` API using AWS SDK v2. Supports the standard credential chain (env vars / `~/.aws/credentials` / SSO profile). Model ID configurable in `config.yaml` (default: `anthropic.claude-3-5-sonnet-20241022-v2:0`). `Available()` validates credentials without making a billable API call.
- **Vertex AI adapter** — Calls Google Vertex AI `generateContent` REST endpoint with ADC (Application Default Credentials) via `golang.org/x/oauth2/google`. Model ID configurable (default: `gemini-2.0-flash`). Lightweight: no heavy GCP client libraries, just HTTP + ADC token.
- **Config-driven activation** — New `api_backends:` section in `config.yaml` with `bedrock:` and `vertexai:` sub-blocks (enabled, region, model_id, project_id). Disabled by default; enable by setting `enabled: true` and ensuring credentials are available. Bedrock default model: `us.anthropic.claude-sonnet-4-20250514-v1:0` (inference profile format, required for newer Bedrock models).
- **Executor integration** — `runStep()` now handles `agent: "bedrock"` and `agent: "vertexai"` in the DAG step definition. Routes to stateless API call instead of PTY session, with automatic token usage recording.
- **Cost tracking (`orch cost`)** — New subcommand that queries the `api_usage` SQLite table and displays aggregated statistics. Sub-commands: `orch cost` (all-time summary), `orch cost recent` (last 20 calls), `orch cost today/week/month` (time-filtered). Shows backend, model, call count, input/output tokens, and estimated USD cost. Pricing table covers Bedrock Claude/Nova and Vertex AI Gemini models; unknown models use conservative fallback estimates.
- **API usage recording** — Every API backend invocation automatically records: backend name, model ID, input/output token counts, estimated cost (USD), latency (ms), and a prompt preview (first 100 chars). New `api_usage` table in SQLite with `AddAPIUsage()`, `GetUsageSummary()`, `GetUsageSince()`, `RecentAPIUsage()` methods.
- **`/pass` extended for API backends** — `/pass claude bedrock` or `/pass kiro vertexai` now works. When the target is a stateless API backend, context is stored in `session_logs` (not sent via PTY). The next API call picks up this stored context as conversation history.
- **Registry API backend detection** — New `CheckBedrockCredentials(region)` and `CheckVertexAICredentials()` in `pkg/registry` that validate credentials without making billable calls. Startup prints availability status for enabled API backends.
- **Route rule targeting** — Existing `route_rules` can now use `target: bedrock` or `target: vertexai` to route patterns to API backends directly.
- **Interactive streaming session mode (`/session bedrock`, `/session vertexai`)** — API backends can now be used in session mode just like CLI backends. Type `/session bedrock` to get a `bedrock›` prompt with real-time streaming responses (Bedrock uses `ConverseStream` API, Vertex AI uses SSE `streamGenerateContent`). Conversation history accumulates across turns within the session. Internally uses a new `APISession` adapter that implements the same `SessionLike` interface as PTY sessions, so all session commands (`/sessions`, `/switch`, `/kill`, `/pass`) work uniformly.
- **Race-safe APISession** — The streaming session's concurrent Send/Kill handling was reviewed and hardened: each stream goroutine exclusively owns its output channel (only it closes it), cancellation propagates via `context.Context`, and 5 regression tests (`-race` verified) cover the exact scenarios that historically caused panics in PTY sessions.

### v0.15.0 (2026-07-16)

**Session persistence, cross-session context passing, and approval gate — plus a same-day review that found and fixed 9 real bugs before this shipped.**

Three features that make orch's multi-session mode significantly more useful and safe.

- **Session output persistence** — All session mode conversations (user input + assistant output) are now automatically persisted to SQLite (`session_logs` table). Survives session kills and orch restarts. New `/session-history` (shorthand `/sh`) command shows the last 20 entries for a backend. New `Store.AddSessionLog()`, `RecentSessionLogs()`, `LastSessionOutput()`, and `PruneSessionLogs()` APIs in the memory package. `orch session-history clear [days]` prunes the table (mirrors `orch history clear`) — without this, `session_logs` had no reachable cleanup path and grew unbounded.
- **Cross-session context passing (`/pass`)** — Transfer the last assistant output from one session to another with a single command. Two forms: `/pass <target>` (current → target) and `/pass <from> <to>` (explicit source → target). The context is injected as a prefixed message and the target's acknowledgment is streamed back. This is orch's core differentiator over simply running multiple terminals — sessions can now build on each other's work. Truncation of long outputs is now rune-safe (`executor.TruncateWithSuffix`) instead of a raw byte slice, so it can't split a multi-byte UTF-8 character (this codebase is CJK-heavy).
- **Approval Gate for high-risk commands** — The DAG executor checks shell commands against 40+ high-risk patterns (terraform apply/destroy, kubectl delete, rm -rf, git push --force, docker system prune, AWS/GCP delete operations, SQL DROP/TRUNCATE, etc.) before execution, applied to **both** workflow execution (`/w`) and normal task execution. The pattern list is now config-driven (`high_risk_patterns` in `config.yaml`, defaults to the built-in list) instead of a hardcoded package-level table. Concurrent high-risk steps in the same DAG no longer race on stdin (approval prompts are serialized). Non-interactive/piped invocations get a clear "denied by default, cannot prompt" message instead of silently blocking on a drained stdin; set `ORCH_AUTO_APPROVE=1` to bypass the gate in CI/scripts. Ctrl+C now interrupts a pending approval prompt instead of hanging.
- **`/session-history` (`/sh`) command** — View persisted session conversation history for any backend. Shows timestamped entries with role indicators (👤 user / 🤖 assistant).
- **Gemini routing false-positive fix** — the "drive" keyword rule (added in v0.14.0) matched as a bare substring, so "hard drive"/"test drive" would auto-route to gemini. Narrowed to the "google drive" phrase, and ASCII keyword rules generally now require a whole-word match (`pkg/config.ContainsWholeWord`, shared with the workflow trigger matcher instead of duplicated).

**🔴 Same-day review, 9 bugs found and fixed before release** (see `Study/docs/_agent_handoff.md` 2026-07-16 for the full review):
1. `/w` workflow execution completely bypassed the approval gate (`ApprovalFunc` was never set on that code path) — most severe, contradicted this very changelog's original claim.
2. Non-interactive/CI invocations had no real opt-out and silently denied every high-risk command.
3. Concurrent high-risk steps raced on stdin for approval input.
4. `/pass` and `/session-history` truncated with a raw byte slice, risking invalid UTF-8 on Chinese text.
5. The "drive" keyword rule false-positived on common phrases (see above).
6. `PruneSessionLogs` was dead code with no caller.
7. Approval prompts didn't respect Ctrl+C.
8. `AddSessionLog` errors were silently discarded at all 4 call sites.
9. `isHighRiskCommand`'s pattern list was hardcoded and non-configurable (altitude issue — duplicated the shape of the config-driven rule engine `pkg/router` already exists to replace).

### v0.14.0 (2026-07-15)

**Gemini routing rules — automatic session switching for long-context, multimodal, and Google ecosystem tasks.**

The route rules now include Gemini as a first-class routing target. When you're in a claude or kiro session and type something that's better suited for Gemini (e.g., "summarize this 200-page PDF", "看這張圖", "google drive"), orch suggests (or auto-switches if `/auto` is on) to the gemini session.

- **31 new Gemini route rules** — 16 phrase rules (strength 3) and 15 keyword rules (3 strong + 12 medium) covering:
  - **Explicit invocation**: "用 gemini", "交給 gemini"
  - **Long-context analysis**: "summarize this", "long context", "長文分析", "深度分析", "摘要"
  - **Google ecosystem**: "google drive", "google docs", "google sheets"
  - **Multimodal**: "analyze image", "analyze video", "看這張圖", "看這個影片", "分析這張"
  - **Research**: "研究", "research", "調研", "評估", "比較", "compare"
  - **Media & documents**: "pdf", "video", "image", "影片", "圖片"
- **Gemini hint domain label** — `buildHintReason()` now shows "長文分析/研究/Google 生態" for Gemini suggestions (previously fell through to raw target string).
- **No conflicts with existing rules** — Gemini rules occupy the whitespace between claude (Notion/文件/管理) and kiro (基礎設施/程式碼/部署). Keywords like "notion", "terraform", "deploy" remain untouched.

### v0.13.0 (2026-07-13)

**Trigger synonyms ([]string) + Gemini session backend.**

- **Workflow trigger: multiple synonyms** — The `trigger` field in workflow YAML now accepts both a single string (backward-compatible) and an array of strings. Any synonym match fires the workflow. Custom `Triggers` type with `UnmarshalYAML` handles both YAML scalar and sequence nodes transparently.
  ```yaml
  # Before (still works):
  trigger: "收工"
  # After (new):
  trigger: ["收工", "下班", "下線", "晚安"]
  ```
- **All 5 workflow YAMLs updated** — morning (開工/早安/上線/上班), signoff (收工/下班/下線/晚安), status (status/狀態), weekly-report (週報/weekly report/weekly), handoff-victoria (交給 Victoria/叫 Claude Code/丟給 CC). Aligns with the trigger-keywords table in `.kiro/steering/`.
- **Gemini session backend** — Session mode now supports `gemini` as a third backend alongside `claude` and `kiro`. Spawns `gemini --skip-trust --yolo` via PTY. Shorthand: `/session g` or `/session gemini`. Kill sends `/quit`.
- **Router display names** — Gemini now shows proper display name in auto-route suggestions.

### v0.12.0 (2026-07-12)

**Workflow trigger matching + shell integration.**

- **Workflow trigger auto-match** — `runTask()` now checks `workflow.Match(input, workflows)` before AI planning. If the user's input matches a workflow trigger keyword, the workflow executes directly (bypasses MLX/cloud planning entirely). First-match wins.
- **5 workflow templates** — morning (開工), signoff (收工), status, weekly-report (週報), handoff-victoria (交給 Victoria). Each with Go template variable substitution (`{{.date}}`, `{{.user}}`, etc.).
- **Shell integration** — `shell/orch.zsh` provides 5 aliases and zsh completion for common orch commands.
- **ASCII trigger word-boundary** — Prevents short ASCII triggers (like "status") from false-matching inside compound words ("statusbar", "gitstatus"). CJK triggers retain substring matching (no reliable word boundary in CJK text).

### v0.11.1 (2026-07-12)

**Streaming output, DAG visualization, dead code cleanup, and test coverage boost.**

- **Wire SuggestBackend/history momentum into auto-route** — When `/auto` is enabled, ambiguous inputs now check `rt.SuggestBackend(input)` for history momentum. Conservative policy: momentum-only switches only fire if the target session already exists (no auto-spawn on momentum alone).
- **Streaming output in session mode** — Session `Send()` now exposes a `ReadStream() <-chan string` that delivers real-time output chunks as they arrive from the backend process. The REPL prints chunks immediately instead of blocking until idle. Backward-compatible `Read()` API preserved.
- **ASCII DAG visualization for `--dry-run`** — `printDryRun` now renders a tree-style DAG with `┌─ ├─ └─` connectors, dependency annotations (`← depends on:`), and truncated command/prompt detail lines.
- **Remove dead code** — Deleted `cfg.Routing` field (unused `map[string][]string`), `looksLikeNaturalLanguage()`, `salvagePlan()`, and `tryMLXOnce()` from planner. Removed stale `routing:` section from `config.yaml`.
- **Test coverage improvements** — Added 22 new test functions across `pkg/planner/` (62.9%, +35pp), `pkg/backend/` (63.3%, +32pp), and `pkg/model/` (57.9%, +15pp). New `pkg/session/stream_test.go` with 5 streaming tests.

### v0.11.0 (2026-07-12)

**Unified routing package — config-driven rules, auto-route, history-aware suggestions.**

The route hint system has been fully extracted from a hardcoded 73-rule function (`route_hint.go`) into a configurable, testable package (`pkg/router/`). Routing decisions are now data-driven via `config.yaml`, thread-safe, and enhanced with context-aware history momentum.

- **New: `pkg/router/` unified routing package** — Consolidates keyword→backend hints, CLI detection (`ClassCommand`), chat pattern matching (`ClassChat`), and natural language classification (`ClassNaturalLanguage`) into a single component. Replaces both `RouteHinter` (route_hint.go) and the inline `classifyInputType()` heuristics. Note: CLI detection and chat-pattern matching are config-driven via `route_rules.rules[]`; the technical-keyword list that decides NaturalLanguage vs. Chat (`Classify()` step 2) is still a hardcoded slice in `router.go`, not yet moved to config.
- **New: `/auto` command for auto-route mode** — Toggle automatic session switching in REPL: when enabled, strong keyword matches (strength ≥ 3) auto-switch to the appropriate backend without requiring manual `/switch`. Both the switch-existing-session and spawn-new-session failure paths now report an error to the user instead of failing silently.
- **Built but not yet wired in: `SuggestBackend()` / history momentum** — Combines keyword matching with a history-momentum signal (≥ 3 of the last N inputs to the same backend boosts ambiguous matches toward that backend). Fully implemented, tested, and thread-safe, but not yet called from the `/auto` hint flow in `repl.go` — currently reachable only via direct `Router.SuggestBackend()` calls (e.g. from tests). Wiring it into live auto-routing is a deliberate follow-up, not done here. Fixed a related bug in `historyMomentum()`: the dominant-backend lookup iterated a Go map, which is a non-deterministic order — with the default `history_size: 5` a tie is mathematically impossible so this never surfaced, but a larger configured `history_size` could let two backends both cross the threshold and get a random "winner" each call. Now iterates in first-seen order for a deterministic result.
- **Refactor: Route rules moved from hardcoded to config-driven** — Phrase/keyword/cli/chat rules (except the step-2 tech-keyword list noted above) now live in `config.yaml` under `route_rules.rules[]`. Each rule has `type` (phrase/keyword/cli/chat), `pattern`, `target`, and `strength`.
- **Refactor: Removed `route_hint.go`** — All functionality consolidated into `pkg/router/`. The `RouteHinter`, `NewRouteHinter()`, `RouteHint()`, and `routeRule` types are gone. `repl.go` and `main.go` now use `router.New(cfg.RouteRules)` exclusively. Also removed `planner.NewWithRouter()`, a wrapper added in this same change that had zero callers — `planner.New()` already accepts a `*router.Router` directly.
- **Not actually resolved: `cfg.Routing` (`map[string][]string`, yaml `routing`)** — This field was named alongside `route_hint.go` in the v0.10.1 "not changed" note below as part of the same duplication concern. It is unrelated to this refactor's scope and remains completely unused by any production code path (confirmed via `git grep` against the pre-refactor commit) — this change even adds a `config_test.go` assertion on its default population, which pins the dead field down further rather than removing it. `KeywordShortcuts` (the other field named in that note) turned out to serve a distinct purpose — a shell-command category shortcut in `planner.tryKeywordPlan()`, unrelated to backend routing — so it was correctly left alone. Left `cfg.Routing` as-is pending a decision on whether to delete it outright.
  **Update**: `cfg.Routing` was deleted in v0.11.1.
- **Tests: Comprehensive router coverage** — 12 test functions in `pkg/router/router_test.go` covering CLI classification (35 subtests), chat detection (27 subtests), natural language (17 subtests), cross-domain hints, cooldown, strength filtering, phrase priority, history momentum, and thread safety (220 concurrent goroutines). Additional edge-case tests in `pkg/config/config_test.go` (7 tests) and `pkg/planner/planner_test.go` (7 new tests for classifyInputType with emoji, multi-line, very short/long inputs, code snippets, and ambiguous cases).

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

**Not changed** (partially resolved in v0.11.0): `route_hint.go`'s 73-rule keyword table duplicated keyword-routing logic that already lived in `pkg/planner` (`classifyInputType`) — that half was consolidated into `pkg/router/` in v0.11.0. The `pkg/config` half (`cfg.Routing`) turned out to be dead code with zero production callers rather than a true duplicate, and is still there — see the v0.11.0 entry above.

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
