# orch — AI Chief of Staff CLI

A single CLI entry point: describe what you need, and it plans, dispatches, executes, verifies, and delivers.

## Quick Start

```bash
git clone https://github.com/GordonWei/orch.git
cd orch
make install
```

Then run:

```bash
orch "hello"
```

## Requirements

- macOS (Apple Silicon M1+)
- Go 1.22+
- Python 3.10+ (for MLX LM inference)
- kiro-cli or claude CLI (AI agent backend)

## Installation

### Option 1: make install (recommended)

```bash
make install    # build binary + init config + MLX env + launchd daemon
```

### Option 2: build only

```bash
make build      # → ~/go/bin/orch
```

### Option 3: setup.sh

```bash
./setup.sh              # full install (includes MLX daemon)
./setup.sh --no-daemon  # skip launchd daemon
```

### Verify

```bash
orch --version
orch --tools
```

## Usage

```bash
# Oneshot (single task)
orch "check S3 bucket usage"
orch "kubectl get nodes"

# Dry-run (plan only, no execution)
orch --dry-run "consolidate AWS and GCP usage report"

# Unix pipe
kubectl get pods -o json | orch "which pods are unhealthy?"
cat error.log | orch "analyze this error"

# REPL mode
orch

# Subcommands
orch history                 # last 20 entries
orch history search kubectl  # search history
orch briefing                # show briefing
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

## Architecture

```
User Input
    ▼
┌─────────────────────────────────────┐
│  Workflow Match                       │  ~/.config/orch/workflows/*.yaml
└────────────┬────────────────────────┘
             ▼ no match
┌─────────────────────────────────────┐
│  Layer 1: Keyword Match (⚡ 0ms)     │  direct shell commands
└────────────┬────────────────────────┘
             ▼ no match
┌─────────────────────────────────────┐
│  Layer 2: Local LLM (🍎 ~2-5s)      │  MLX / Ollama
└────────────┬────────────────────────┘
             ▼ unavailable
┌─────────────────────────────────────┐
│  Layer 3: Cloud LLM (☁️ ~5-8s)      │  claude -p fallback
└─────────────────────────────────────┘
             ▼
┌─────────────────────────────────────┐
│  Executor (DAG parallel scheduling)  │
│  goroutine per step + streaming      │
└─────────────────────────────────────┘
             ▼
┌─────────────────────────────────────┐
│  Memory Layer (SQLite)               │
└─────────────────────────────────────┘
```

## Project Structure

```
cmd/orch/
├── main.go          CLI entry + signal handler
├── repl.go          REPL interactive mode
├── printer.go       Event output formatting
└── dag.go           ASCII DAG rendering

pkg/
├── config/          Config loader (YAML → struct)
├── model/           LLM interface + auto-start server
├── memory/          SQLite memory layer (7 tables)
├── registry/        Local tool scanner
├── planner/         3-layer routing + plan generation
├── executor/        DAG parallel execution + streaming + re-plan
└── workflow/        YAML workflow templates

launchd/             macOS LaunchAgent (MLX daemon)
config.yaml          Config template
setup.sh             Installation script
Makefile             build/test/install
```

## Configuration

Location: `~/.config/orch/config.yaml` (override with `ORCH_CONFIG` env)

```yaml
# Models (supports MLX / Ollama / any OpenAI-compatible API)
models:
  - name: "qwen-1.5b"
    backend: "mlx"
    endpoint: "http://localhost:8080"
    model: "mlx-community/Qwen2.5-1.5B-Instruct-4bit"
    default: true

# Memory layer
memory:
  db_path: "~/.config/orch/orch.db"
  briefing_on_boot: true
  auto_summarize: true

# Workflow directory
workflows:
  dir: "~/.config/orch/workflows"
```

## Development

```bash
make build     # compile
make test      # run tests
make lint      # go vet
make cover     # coverage report
make clean     # remove artifacts
```

## License

MIT
