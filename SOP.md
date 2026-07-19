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

### Dry-Run DAG Visualization

```bash
orch --dry-run "consolidate AWS and GCP billing"
```

Shows an ASCII tree with dependency arrows:
```
📋 Execution Plan (dry-run):
   ┌─ [step_1] Query AWS billing (agent: kiro)
   ├─ [step_2] Query GCP billing (agent: kiro)
   └─ [step_3] Merge reports (agent: claude) ← depends on: step_1, step_2
```

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

**Startup briefing freshness (v0.16.2+)**: `orch briefing gen` only summarizes
orch's own task history, and only when you remember to manually re-run it — the
cached result shown by `briefing_on_boot` can silently go stale for weeks. If you
maintain a project status/handoff document by hand, set `memory.briefing_source_file`
(prompted during `orch init`, or add it directly to `config.yaml`):

```yaml
memory:
  briefing_on_boot: true
  briefing_source_file: "~/path/to/your/status-or-handoff.md"
```

When set, every startup re-reads and re-summarizes that file fresh via the local
model (also saved via `SetBriefing`, so `orch briefing` reflects it too) — instead
of trusting a cache. Falls back silently to the last cached briefing if the file is
missing or MLX is unreachable (never blocks startup); re-run with `--verbose` to see
why a fallback happened. Note: `memory.auto_summarize` is a reserved config key with
no implementation behind it yet — `briefing_source_file` is the supported way to
keep the startup briefing current.

### API Backends (v0.16+)

Stateless HTTP API backends (Bedrock, Vertex AI) for direct cloud model invocation without spawning CLI processes.

#### Setup

```yaml
# config.yaml
api_backends:
  bedrock:
    enabled: true
    region: "us-east-1"
    model_id: "us.anthropic.claude-sonnet-4-20250514-v1:0"
  vertexai:
    enabled: true
    project_id: "my-gcp-project"
    region: "us-central1"
    model_id: "gemini-2.0-flash"
```

#### Credential Requirements

| Backend | Credential | How to configure |
|---------|-----------|-----------------|
| Bedrock | AWS standard chain | `aws configure` / env vars / SSO profile |
| Vertex AI | GCP ADC | `gcloud auth application-default login` |

#### Routing to API Backends

Three ways to invoke API backends:

1. **Route rules** — Add rules in `config.yaml` targeting `bedrock`/`vertexai`:
   ```yaml
   route_rules:
     rules:
       - pattern: "用 bedrock"
         target: bedrock
         strength: 3
         type: phrase
       - pattern: "vertex 翻譯"
         target: vertexai
         strength: 3
         type: phrase
   ```

2. **YAML workflow** — Specify `agent: bedrock` or `agent: vertexai` in a workflow step.

3. **MLX classification** — If configured, MLX can route to `bedrock:api` or `vertexai:api` for explicit requests.

#### Context Passing (`/pass` with API backends)

```bash
/pass claude bedrock    # pass Claude's last output as context for next Bedrock call
/pass kiro vertexai     # pass Kiro's last output as context for next Vertex AI call
```

Unlike PTY session targets, API backend context is stored in `session_logs` and injected as multi-turn conversation history on the next API call.

### Cost Tracking (v0.16+)

Every API backend invocation (Bedrock/Vertex AI) automatically records token usage and estimated cost.

```bash
orch cost               # all-time summary
orch cost recent        # last 20 API calls (detailed)
orch cost today         # today's usage
orch cost week          # last 7 days
orch cost month         # last 30 days
```

Output format:
```
BACKEND    MODEL                  CALLS  INPUT TOKENS  OUTPUT TOKENS  COST (USD)
-------    -----                  -----  ------------  -------------  ----------
bedrock    claude-sonnet-4...     42     125.3K        89.7K          $1.7205
vertexai   gemini-2.0-flash      18     45.2K         12.1K          $0.0070

TOTAL                             60                                  $1.7275
```

**Pricing**: Built-in pricing table covers Bedrock Claude/Nova and Vertex AI Gemini models. Unknown models use conservative fallback estimates. Prices are per 1M tokens based on public pricing.

#### Direct SQLite access

```bash
# Recent API calls
sqlite3 ~/.config/orch/orch.db "SELECT timestamp, backend, model, input_tokens, output_tokens, cost_usd FROM api_usage ORDER BY id DESC LIMIT 10;"

# Total cost by backend
sqlite3 ~/.config/orch/orch.db "SELECT backend, SUM(cost_usd) as total FROM api_usage GROUP BY backend;"
```

### REPL Mode

```bash
orch                            # enter REPL
```

Features:
- **Session context**: 5-turn sliding window, backends see prior conversation
- **Slash commands**: `/w` workflows, `/h` history, `/b` briefing, `/sh` session-history, `/pass` context transfer, `/help`
- **No stdout capture hack**: `runTask` no longer redirects `os.Stdout` through an `os.Pipe()`.
  `runTask` is the single place that prints task output — right after execution and *before*
  event-bus chains run, so the result is never delayed behind a slow cloud chain. The output
  is also returned as a value, but only so the REPL can store it in session context —
  callers must not print it (doing so is exactly the double-output bug this replaced).
  Note: In **session mode** (v0.11.1+), output is handled differently — `ReadStream()` delivers chunks in real-time directly to the terminal, bypassing `runTask` entirely.

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

Shorthand: `c` = claude, `k` = kiro, `br` = bedrock, `va` = vertexai (e.g., `/session c`)

**API Backend Sessions (v0.16+)**: Bedrock and Vertex AI can also be used in session mode:

```bash
› /session bedrock        # start API streaming session
bedrock› translate this to Japanese   # real-time streaming response
bedrock› /pass bedrock claude         # pass context to claude
bedrock› /switch claude
claude› ...
```

Unlike PTY sessions (which spawn a CLI process), API sessions use streaming HTTP calls (`ConverseStream` / `streamGenerateContent`). Conversation history accumulates within the session. Requires `api_backends.bedrock.enabled: true` (or `vertexai`) in config with valid credentials.

**Route Hints (v0.10+)**: When in a session, orch detects if your input matches another backend's domain and suggests switching:

```
claude› terraform plan for litellm-gke
💡 "terraform" → might be better in kiro (/switch kiro)
```

- Config-driven rules (~100 default phrase/keyword patterns in `route_rules.rules[]`)
- 3-tier confidence: strong (3), medium (2), weak (1)
- Only medium+ signals trigger suggestions
- Cooldown: configurable (default 3 inputs between hints)
- Same-domain keywords are ignored
- `/auto [on|off]`: toggle auto-switching (strong signals auto-switch when enabled)

**Streaming Output (v0.11.1+)**: Session responses stream in real-time (no more blocking until idle). If output appears truncated during `Ctrl+C` shutdown, this is expected behavior — the stream channel closes as part of graceful teardown.

**Session Persistence (v0.15+)**: All session conversations are automatically persisted to SQLite (`session_logs` table). Survives session kills and orch restarts.

```
› /sh claude              # show last 20 entries for claude session
› /session-history kiro   # long form

$ orch session-history clear         # wipe all entries
$ orch session-history clear 30      # wipe entries older than 30 days
```

Truncation for display/transfer (`/pass`, `/sh`) is rune-safe (`executor.TruncateWithSuffix`) — it won't split a multi-byte UTF-8 character, which matters given how much Traditional Chinese passes through this tool.

**Cross-Session Context Passing (v0.15+)**: Transfer the last assistant output from one session to another:

```
claude› /pass kiro        # pass claude's last output → kiro session
› /pass claude kiro       # explicit: from claude to kiro (works in normal mode too)
```

The passed context is wrapped in `[Context passed from X session]` markers and the target session streams its acknowledgment. Automatically spawns the target session if not running.

**Approval Gate (v0.15+)**: The DAG executor checks shell commands against high-risk patterns before execution — applied to **both** `/w` workflow execution and normal task execution:

```
⚠️  HIGH-RISK COMMAND DETECTED:
   terraform apply -auto-approve
   Execute? [y/N]:
```

Covers 40+ patterns by default (`terraform apply/destroy`, `kubectl delete/drain`, `rm -rf`, `git push --force`, `docker system prune`, AWS/GCP delete operations, SQL `DROP`/`TRUNCATE`, ...). Set `high_risk_patterns` in `config.yaml` to add/replace patterns without a rebuild.

Concurrent high-risk steps in the same DAG serialize their approval prompts (no stdin race). Ctrl+C during a pending prompt cancels it (denies the command) instead of hanging.

**Non-interactive/CI use**: piped/non-TTY stdin can't be prompted — the gate denies by default with a clear message instead of silently blocking. Set `ORCH_AUTO_APPROVE=1` to bypass the gate entirely (e.g. in CI/cron):

```
ORCH_AUTO_APPROVE=1 orch "terraform apply for litellm-gke"
```

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

**MLX local model calls (v0.16.4+)**: the ping to `/v1/models` gets 5s, classification and `DirectChat()` both get 60s (`mlxPingClient` / `mlxChatClient` in `pkg/planner/planner.go`). Before v0.16.4 these went through Go's bare `http.Get`/`http.Post` — no timeout at all — so a slow `mlx_lm.server` response just hung orch forever. See "orch appears to hang" below.

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

### File Context (v0.16.1+)

A step can declare `file_context` — local file paths whose contents get read and prepended
to the step's prompt before it's sent to the agent. Use this when the agent itself may not
have (or shouldn't need) filesystem tools to read a specific known file:

```yaml
steps:
  - id: read_handoff
    agent: kiro
    file_context:
      - ~/Desktop/Cowork/docs/_agent_handoff.md
    prompt: |
      以上是全局交接儀表板的內容。
      請根據這份交接檔案，回報目前狀態快照：
```

Paths support `~` expansion and template variables (`{{.date}}` etc.). If a file can't be
read, an inline `--- FILE: <path> (read error: ...) ---` marker is injected instead of
failing the step — check the agent's output if content looks missing.

## Troubleshooting

### orch appears to hang (never returns)

Fixed in v0.16.4. `mlxAvailable()`/`tryMLX()`/`DirectChat()` used to call MLX through
Go's bare `http.Get`/`http.Post` — no timeout, ever. `mlx_lm.server` has moments where
it just goes quiet on `/v1/chat/completions` for no obvious reason (same request, same
size, 6s one time and never-returns the next), and with nothing capping the wait on our
side, that took the whole orch process down with it — no error, nothing, looked exactly
like a crash.

Now a slow local model can't do that. You'll see one of:
```
⚠️  MLX routing failed, falling back to cloud          # classification timed out (60s)
⚠️  local chat failed: ... falling back to executor    # DirectChat timed out (60s)
```
and orch falls through to cloud or the executor instead. Still hanging on v0.16.4+?
Check `git log --oneline -1` is at or after v0.16.4 first — if it is, that's a new bug,
not the old one back.

If the local model is *always* slow, not just occasionally, that's a different problem
worth chasing on its own (thermal throttling, memory pressure, a stale `mlx_lm.server`
process). Quickest check: `pkill -f mlx_lm.server`, then `orch "hello"` to spin up a
fresh one.

### MLX server fails to start

```bash
# verify venv exists
ls ~/mlx-env/bin/python3

# start manually to see errors
~/mlx-env/bin/python3 -m mlx_lm.server \
  --model mlx-community/Qwen2.5-7B-Instruct-4bit --port 8080

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

If a technical request gets answered by the local model instead of cloud:
- Use `--verbose` to see MLX classification output
- Add relevant tech keywords to `keyword_shortcuts` in config
- Override with `--backend kiro "your task"` for one-off

**Short Chinese input specifically (fixed in v0.16.2)**: before v0.16.2, any Chinese
input at or under 10 runes was unconditionally classified as chat, even task
references like `那讀交接` ("then read the handoff") — swallowing them into the
local model's free-form (tool-less) chat completion instead of real planning. This
is now `route_rules.chat_short_input_max_len` (default `1` — just enough to catch
bare acknowledgments like `好`). Unrecognized short Chinese input now falls through
to keyword/CLI checks and then MLX classification, which can itself return
`local:chat` if it judges the input is genuinely conversational — a real judgment
call instead of a blind length guess. Raise the config value if you want more short
input to skip MLX and go straight to chat (trades accuracy for speed); set to `0`
to disable the fast path entirely.

Two related things this does **not** fix, since they're not routing bugs:
- The local model's own answer quality/language-instruction-following is a
  model capability limit, not something `fixPlan`/`router.Classify` controls.
- MLX classification only ever sees the current utterance (`classifyInput`, not
  prior conversation) — so a genuine follow-up like `那讀交接` still can't be
  resolved to "read today's briefing" without conversation context, which no
  layer currently passes into MLX classification.

### Task misrouted as a shell command (fixed in v0.16.1)

Before v0.16.1, a natural-language request like `讀取 /path/to/file` could get misclassified
by the MLX 3B model as `agent: shell`, and orch would try to execute the raw sentence as a
literal shell command — failing with `bash: 讀取: command not found` (exit 127). `fixPlan()`
now runs after every MLX classification and reroutes to `claude` whenever the router detects
natural language, regardless of what the small model guessed. If you still see a shell-exec
failure on a natural-language input on v0.16.1+, that's a new bug, not a recurrence — check
`--verbose` output for `MLX classification raw` to see what the model actually returned, and
whether `router.Classify()` on that exact string is returning `ClassNaturalLanguage`.

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
