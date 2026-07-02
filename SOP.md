# orch Operations Manual

## Daily Usage

### Routing Behavior

| Input Type | Route | Example |
|-----------|-------|---------|
| Workflow trigger | 📋 YAML workflow | `orch "signoff"` |
| CLI command | ⚡ shell direct | `orch "kubectl get pods"` |
| General Q&A | 🍎 local LLM | `orch "what is K8s?"` |
| Task dispatch | 🍎→agent | `orch "check AWS billing"` |

### Managing Memory

```bash
orch history                    # last 20 entries
orch history search "kubectl"   # search
orch history clear              # clear all

orch briefing                   # show
orch briefing set "focus: ..."  # set manually
orch briefing gen               # auto-generate via MLX
```

## Configuration

Config file: `~/.config/orch/config.yaml`

### Switching Models

Move `default: true` to the desired model, then restart MLX server:

```bash
pkill -f "mlx_lm.server"
orch "hello"   # auto-starts with new model
```

### Using Ollama

```yaml
models:
  - name: "llama-8b"
    backend: "ollama"
    endpoint: "http://localhost:11434"
    model: "llama3.1:8b"
    auto_start: true
    port: "11434"
    default: true
```

Requires: `brew install ollama && ollama pull llama3.1:8b`

### Memory Layer

```yaml
memory:
  briefing_on_boot: false    # disable boot briefing
  auto_summarize: false      # disable auto-summarize
  history_limit: 5000        # auto-prune above limit (0=unlimited)
```

## Workflow Templates

Place YAML files in `~/.config/orch/workflows/`. Matching triggers bypass AI planning and execute directly.

```yaml
name: "signoff"
trigger: "signoff"
steps:
  - id: step_1
    agent: kiro
    prompt: "update handoff notes"
  - id: step_2
    agent: claude
    prompt: "sync to Notion"
    depends_on: [step_1]
```

Built-in template variables: `{{.date}}`, `{{.time}}`, `{{.user}}`

## Troubleshooting

### MLX server fails to start

```bash
# verify venv exists
ls ~/mlx-env/bin/python3

# start manually to see errors
~/mlx-env/bin/python3 -m mlx_lm.server \
  --model mlx-community/Qwen2.5-1.5B-Instruct-4bit --port 8080

# port conflict
lsof -i :8080 && kill $(lsof -ti :8080)
```

### All requests go to cloud

```bash
curl http://localhost:8080/v1/models   # should return 200
```

If it fails: verify `auto_start: true` and `~/mlx-env/bin/python3` exists.

### LaunchAgent (daemon) management

```bash
# status
launchctl list | grep orch

# stop
launchctl unload ~/Library/LaunchAgents/com.orch.mlx-server.plist

# start
launchctl load ~/Library/LaunchAgents/com.orch.mlx-server.plist

# logs
tail -f ~/Library/Logs/orch-mlx.log
```

### Direct SQLite access

```bash
sqlite3 ~/.config/orch/orch.db "SELECT timestamp, input, category FROM history ORDER BY id DESC LIMIT 10;"
```

## Migration (New Machine)

```bash
# new machine
git clone https://github.com/GordonWei/orch.git && cd orch
make install

# migrate config and memory (optional)
scp old-mac:~/.config/orch/config.yaml ~/.config/orch/
scp old-mac:~/.config/orch/orch.db ~/.config/orch/

# verify agents
which kiro-cli && which claude

# test
orch "hello"
```
