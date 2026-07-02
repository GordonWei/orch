# orch — AI Task Orchestration CLI

A single CLI entry point: describe what you need, and it plans, dispatches, executes, verifies, and delivers.

**MLX on Apple Silicon as the primary engine** — local inference handles most tasks without cloud calls. Cloud AI backends (kiro/claude/gemini) serve as fallback for complex planning.

## Origin

This project was inspired by [ORCH](https://github.com/oxgeneral/ORCH) (a TypeScript-based multi-agent orchestrator) and [Microsoft Conductor](https://github.com/microsoft/conductor) (Python, YAML-driven workflow engine). Both solve the same problem: **coordinating multiple AI agents from a single interface**.

The core idea: instead of manually switching between different AI CLI tools (kiro, claude, gemini), why not have a single entry point that **routes tasks automatically**?

What makes this version different:

1. **Local-first with MLX** — A small LLM (Qwen 2.5 1.5B) running locally on Apple Silicon handles routing and simple tasks in ~2-5 seconds. No cloud calls, no API costs, no latency for everyday use.
2. **Cloud as fallback** — Only when the local model can't handle complex multi-step planning does it escalate to a subscribed AI CLI (kiro, claude, or gemini — whichever you have).
3. **Single binary, zero runtime dependencies** — Written in Go. No Node.js, no Python venv to manage (except for MLX inference itself). Just build and run.
4. **CLI-native** — Works with Unix pipes, integrates into existing shell workflows, and respects the terminal as the primary interface.

The 3-layer routing architecture:

```
User Input
    ▼
┌─────────────────────────────────────┐
│  Layer 1: Keyword Match (⚡ 0ms)     │  kubectl, terraform, helm → run directly
└────────────┬────────────────────────┘
             ▼ no match
┌─────────────────────────────────────┐
│  Layer 2: MLX Local LLM (🍎 ~2-5s)  │  plan generation + simple Q&A
└────────────┬────────────────────────┘
             ▼ complex task / MLX fails
┌─────────────────────────────────────┐
│  Layer 3: Cloud AI Backend (☁️)      │  kiro / claude / gemini (any one)
└─────────────────────────────────────┘
             ▼
┌─────────────────────────────────────┐
│  DAG Executor (parallel goroutines)  │
└─────────────────────────────────────┘
             ▼
┌─────────────────────────────────────┐
│  Memory Layer (SQLite)               │
└─────────────────────────────────────┘
```

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
make build    # compiles → ~/go/bin/orch
```

If `~/go/bin` isn't in your PATH, add to `~/.zshrc`:

```bash
export PATH="$HOME/go/bin:$PATH"
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
go build -o ~/go/bin/orch ./cmd/orch

# Start MLX server manually (in another terminal)
~/mlx-env/bin/python3 -m mlx_lm.server \
  --model mlx-community/Qwen2.5-1.5B-Instruct-4bit --port 8080

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
| `/w` | List available workflows |
| `/w 1` | Execute workflow #1 |
| `/h` | Last 10 history entries |
| `/b` | Show briefing |
| `/help` | List all commands |

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
  - name: "qwen-1.5b"
    backend: "mlx"
    endpoint: "http://localhost:8080"
    model: "mlx-community/Qwen2.5-1.5B-Instruct-4bit"
    python_path: "~/mlx-env/bin/python3"
    auto_start: true
    port: "8080"
    default: true
```

To use a larger model (better accuracy, slower):

```yaml
  - name: "qwen-3b"
    model: "mlx-community/Qwen2.5-3B-Instruct-4bit"
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
├── main.go          CLI entry + signal handler
├── repl.go          REPL interactive mode
├── init.go          Interactive setup wizard
├── printer.go       Event output formatting
└── dag.go           ASCII DAG rendering

pkg/
├── backend/         AI CLI backend interface + adapters (kiro/claude/gemini)
├── config/          Config loader (YAML → struct)
├── model/           Local LLM interface (OpenAI-compatible) + MLX auto-start
├── memory/          SQLite memory layer (history + briefing)
├── registry/        Local tool scanner (which CLIs are on this machine)
├── planner/         3-layer routing + JSON plan generation
├── executor/        DAG parallel execution engine (goroutines)
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
  --model mlx-community/Qwen2.5-1.5B-Instruct-4bit --port 8080

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

## License

MIT
