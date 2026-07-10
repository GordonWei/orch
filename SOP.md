# orch Operations Manual

## Daily Usage

### Routing Behavior

| Input Type | Route | Example |
|-----------|-------|---------|
| Workflow trigger | 📋 YAML workflow | `orch "signoff"` |
| Known CLI (70+) | ⚡ shell direct (0ms) | `orch "docker ps"`, `orch "git status"` |
| Configured prefix | ⚡ shell direct (0ms) | `orch "kubectl get pods"` |
| General Q&A / chat | 🍎 local LLM | `orch "hello"` |
| NL task dispatch | 🍎→agent classification | `orch "check AWS billing"` |
| Complex multi-step | ☁️ cloud planning | `orch "整理今天會議記錄到 Notion"` |

### Managing Memory

```bash
orch history                    # last 20 entries
orch history search "kubectl"   # search
orch history clear              # clear all

orch briefing                   # show
orch briefing set "focus: ..."  # set manually
orch briefing gen               # auto-generate via MLX
```

**Auto-pruning**: History is automatically pruned when exceeding `history_limit` (default: 1000 entries). Oldest entries are removed first.

### REPL Mode

```bash
orch                            # enter REPL
```

Features:
- **Session context**: 5-turn sliding window, backends see prior conversation
- **Slash commands**: `/w` workflows, `/h` history, `/b` briefing, `/help`
- **No stdout capture hack**: `runTask` no longer redirects `os.Stdout` through an `os.Pipe()`.
  `runTask` is the single place that prints task output — right after execution and *before*
  event-bus chains run, so the result is never delayed behind a slow cloud chain. The output
  is also returned as a value, but only so the REPL can store it in session context —
  callers must not print it (doing so is exactly the double-output bug this replaced).

### Session Mode (v0.9+)

Persistent interactive PTY sessions with AI backends. Instead of one-shot planner calls, attach to a live session and have multi-turn conversations directly through orch.

```bash
› /session claude         # spawn (or attach if already running)
claude› help me refactor  # forwarded directly to claude
claude› /back             # return to normal mode (session alive in background)
› /session kiro           # start a kiro session
kiro› /switch claude      # switch back to claude
claude› /kill all         # terminate everything
›                         # back to normal
```

Shorthand: `c` = claude, `k` = kiro (e.g., `/session c`)

**Route Hints (v0.10+)**: When in a session, orch detects if your input matches another backend's domain and suggests switching:

```
claude› terraform plan for litellm-gke
💡 "terraform" → might be better in kiro (/switch kiro)
```

- 73 keyword/phrase rules, 3-tier confidence (strong/medium/weak)
- Only medium+ signals trigger suggestions
- Cooldown: 3 inputs between hints (no nagging)
- Same-domain keywords are ignored (e.g., "notion" in claude won't trigger)

**Session Health (v0.10+)**:
- `WatchSessions()` monitors session health in background (2s interval)
- If a session dies unexpectedly: `⚠️ session X died unexpectedly`
- Auto-restart available: sessions can be configured to respawn on crash
- `/sessions` shows uptime, idle status, and restart count
- v0.10.1: auto-restart is now race-safe against a concurrent `/session <backend>` or `/kill all` happening mid-restart — no more silently overwritten or orphaned processes

**Graceful Shutdown (v0.10+)**:
- `Ctrl+C` or `SIGTERM` triggers 3-phase shutdown: send `/exit` → wait 5s → force kill
- No orphan processes left behind
- All PTY file descriptors properly closed
- v0.10.1: a second `Ctrl+C` while shutdown is in progress now forces an immediate exit instead of waiting out the full (up to ~8s) sequence; one-shot/subcommand mode (outside session mode) also has its grace period restored

### ANSI Strip (v0.10+)

Session output is automatically cleaned:
- All ANSI escape sequences (SGR colors, cursor movement, erase) stripped
- **Alternate screen buffer awareness**: TUI apps entering alt screen (`ESC[?1049h`) have their chrome completely discarded. Only real output after leaving alt screen is shown.
- Stateful across chunked reads (handles sequences split across multiple PTY reads, including a sequence split mid-way through the chunk boundary — fixed in v0.10.1, was previously silently dropped)
- **UTF-8 safe (v0.10.1+)**: high bytes are decoded as UTF-8 before being checked against the C1 control-code table, so multi-byte characters (Traditional Chinese, etc.) are never misidentified as escape sequences. Before v0.10.1, ordinary CJK session output could get silently truncated.

### Reactive Event Bus

When a step completes, trigger rules in `~/.config/orch/workflows/*.yaml` can automatically dispatch follow-up actions:

```yaml
name: "auto-sync-notion"
on: "step.done"
agent: "claude"
condition: "category==meeting"
then:
  agent: claude
  prompt: "Sync the following to Notion:\n{{.Output}}"
  gate_with_mlx: true    # MLX decides if dispatch is worthwhile
  summarize: true         # compress large output before passing
  max_context: 5000       # truncate at N chars
```

**Chain failure handling** (v0.8+):
- Failed chains are recorded in history with tag `chain/failed`
- A retry command is printed: `💡 to retry: orch "..."`
- Successful chains are also logged for traceability

## Configuration

Config file: `~/.config/orch/config.yaml`

### Switching Models

Move `default: true` to the desired model, then restart MLX server:

```bash
pkill -f "mlx_lm.server"
orch "hello"   # auto-starts with new model (shows progress: ⏳ waiting... Ns)
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
  history_limit: 1000        # auto-prune above limit (0=unlimited, default: 1000)
```

### Backend Timeout

All AI CLI backend calls (kiro/claude/gemini) have a **5-minute timeout**. If a backend process hangs (waiting for input, rate-limited), it is automatically killed after 5 minutes with error output preserved.

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
  --model mlx-community/Qwen2.5-3B-Instruct-4bit --port 8080

# port conflict
lsof -i :8080 && kill $(lsof -ti :8080)
```

### All requests go to cloud

```bash
curl http://localhost:8080/v1/models   # should return 200
```

If it fails: verify `auto_start: true` and `~/mlx-env/bin/python3` exists.

### Backend hangs / timeout

If you see `timed out after 5m0s`:
- Check if the backend CLI requires interactive input (kiro/claude may prompt for auth)
- Run the backend directly to verify: `claude -p "hello"`
- Check rate limits on the API

### Session won't start

If `/session claude` or `/session kiro` fails:

```bash
# Verify the CLI is available
which claude && claude --version
which kiro-cli && kiro-cli --version

# Test direct PTY spawn (bypass orch)
python3 -c "import pty; pty.spawn(['claude', '--dangerously-skip-permissions'])"
```

Common issues:
- CLI not installed or not in PATH
- Auth expired (claude needs `claude auth`, kiro needs login)
- Another instance already running with lock file

### Session output is garbled / shows escape codes

If you see raw ANSI codes in session output:
- This shouldn't happen in v0.10.1+ (StripState handles alt screen, buffers sequences split across chunk boundaries, and is UTF-8 safe)
- If it does: the TUI app may be using non-standard escape sequences
- Check with `--verbose`: `orch --verbose` then `/session claude`
- File a bug with the raw output

### Session goes silent / stops responding mid-conversation

If a session (especially claude/kiro rendering a spinner or full-screen UI) stops producing any output and `orch` never prints a reply:
- Fixed in v0.10.1 — idle detection previously only reset while the backend was already inside alt-screen chrome-suppression, so a long-running render could trip the idle timeout early and drop the eventual answer
- If you're on an older build: `/kill` the session and `/session <backend>` again
- Confirm your build includes the fix: `git log --oneline -1` should be at or after `v0.10.1`

### Route hints not appearing

Route hints only trigger when:
1. You're in session mode (not normal mode)
2. The keyword matches a DIFFERENT backend's domain
3. The match strength is ≥ 2 (medium or strong)
4. Cooldown elapsed (at least 3 inputs since last hint)

To debug: weak signals (strength 1) like "test" or "會議" are intentionally suppressed.

### Session died unexpectedly

When you see `⚠️ session X died unexpectedly`:
- The backend process crashed or was killed externally
- Use `/session X` to restart
- If it keeps dying, run the backend directly to check for errors
- Consider enabling auto-restart: (currently code-level, future: config flag)

### Task misrouted as chat

If a technical request gets answered by the local 3B model instead of cloud:
- Use `--verbose` to see MLX classification output
- Add relevant tech keywords to `keyword_shortcuts` in config
- Override with `--backend kiro "your task"` for one-off

### REPL replies appear twice

Output printing lives in exactly one place: `runTask` (cmd/orch/main.go) prints task/chat
output right after execution. Callers (`main()` oneshot, the REPL loop) receive the output
as a return value for session context only and must NOT print it. If replies start appearing
twice, someone reintroduced a `fmt.Print(output)` at a call site.

To verify, you need a real terminal (piping stdin into `orch` triggers pipe mode and
bypasses the REPL entirely — this is why the bug escaped plain-pipe testing):

```bash
# oneshot: stdout must contain exactly one copy
orch "echo dup-check" | grep -c "dup-check"   # → 1

# REPL: drive through a pty (python3 one-liner)
python3 -c "
import pty,os,time,select,sys
pid,fd=pty.fork()
if pid==0: os.execvp('orch',['orch'])
time.sleep(1); os.read(fd,4096)
os.write(fd,'你好\n'.encode()); time.sleep(12)
buf=b''
while select.select([fd],[],[],2)[0]: buf+=os.read(fd,4096)
os.write(fd,b'exit\n')
sys.stdout.write(str(buf.decode(errors='replace').count('可以幫你')))
"   # → 1
```

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

# Check chain failures
sqlite3 ~/.config/orch/orch.db "SELECT timestamp, input, output_summary FROM history WHERE tags LIKE '%failed%' ORDER BY id DESC LIMIT 5;"
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
