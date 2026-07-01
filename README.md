# orch — AI 幕僚長 CLI

一個 CLI 入口，你只說要什麼，它自己規劃、分派、執行、驗證、交付成品。

## 架構

```
使用者輸入
    │
    ▼
┌─────────────────────────────────────┐
│  Workflow Match                       │  ~/.config/orch/workflows/*.yaml
│  觸發詞命中 → 直接載入步驟           │
└────────────┬────────────────────────┘
             │ 不匹配
             ▼
┌─────────────────────────────────────┐
│  Layer 1: Keyword Match（⚡ 0ms）    │  config.yaml → keyword_shortcuts
│  直接指令直接跑                       │
└────────────┬────────────────────────┘
             │ 不匹配
             ▼
┌─────────────────────────────────────┐
│  Layer 2: Local LLM（🍎 ~2-5s）     │  可換模型（MLX/Ollama/OpenAI-compatible）
│  分類 → chat 直接答 / 派給 agent     │
└────────────┬────────────────────────┘
             │ Local 不可用
             ▼
┌─────────────────────────────────────┐
│  Layer 3: Cloud LLM（☁️ ~5-8s）     │  claude -p fallback
└─────────────────────────────────────┘
             │
             ▼
┌─────────────────────────────────────┐
│  Executor（DAG 並行調度）             │
│  goroutine per step → 依賴圖排程     │
│  → context chain 串接（多上游合併）   │
│  → streaming output（逐行即時輸出）  │
│  → structured output（Files/KV）     │
│  → on_failure: retry/skip/re-plan   │
└─────────────────────────────────────┘
             │
             ▼
┌─────────────────────────────────────┐
│  Memory Layer（SQLite）              │
│  history 紀錄 → MLX 壓縮 briefing    │
│  啟動時載入摘要 → 省 cloud token     │
└─────────────────────────────────────┘
             │
             ▼
        交付成品
```

## 專案結構

```
cmd/orch/main.go               CLI 入口（oneshot + REPL + subcommands + streaming）
pkg/
├── config/config.go           設定檔載入（YAML → struct）
├── model/
│   ├── model.go               LLM interface + OpenAI-compatible client
│   └── starter.go             Auto-start MLX/Ollama server
├── memory/
│   └── memory.go              SQLite Store（7 張表 CRUD）
├── registry/registry.go       本機工具掃描 + model 偵測
├── planner/planner.go         三層 Fallback 路由 + plan 生成 + DirectChat
├── executor/
│   ├── executor.go            DAG 並行調度 + context chain + re-plan
│   └── stream.go              串流輸出事件系統
└── workflow/
    └── workflow.go            YAML 工作流模板載入與匹配
launchd/
└── com.orch.mlx-server.plist  macOS LaunchAgent（MLX server 自動啟動）
config.yaml                    設定檔範本
setup.sh                       一鍵安裝腳本（含 launchd 設定）
docs/flow.md                   Mermaid 流程圖
```

## 使用方式

```bash
# Oneshot（一次性任務）
orch "查 S3 bucket 用量"
orch "kubectl get nodes"
orch "整理今天的會議記錄並同步 Notion"

# Dry-run（只看計畫不執行）
orch --dry-run "整合 AWS 和 GCP 用量報告"

# Unix pipe 整合（stdin 自動帶入 context）
kubectl get pods -o json | orch "哪些 pod 不健康？"
cat error.log | orch "分析這個錯誤"

# REPL 模式（持續對話）
orch
# REPL 內建命令：
#   /w          列出可用工作流
#   /w 1        執行第 1 個工作流
#   /h          最近 10 筆歷史
#   /b          顯示 briefing
#   /help       列出所有命令

# 觸發工作流
orch "收工"       # 匹配 ~/.config/orch/workflows/signoff.yaml
orch "週報"       # 匹配 ~/.config/orch/workflows/weekly.yaml

# 子命令
orch history               # 列出最近 20 筆歷史
orch history search kubectl  # 搜尋歷史
orch history clear         # 清除歷史
orch briefing              # 顯示 briefing
orch briefing gen          # MLX 自動生成 briefing
orch briefing set "明天重點：部署 litellm"

# 查看可用工具
orch --tools

# 一般問答（本地 LLM 直接回答）
orch "什麼是 Kubernetes？"
```

## 設定檔

位置：`~/.config/orch/config.yaml`（可用 `ORCH_CONFIG` env 覆蓋）

### 可換模型

```yaml
models:
  - name: "qwen-1.5b"          # 預設，~1 GB
    backend: "mlx"
    endpoint: "http://localhost:8080"
    model: "mlx-community/Qwen2.5-1.5B-Instruct-4bit"
    python_path: "~/mlx-env/bin/python3"
    auto_start: true
    port: "8080"
    default: true
  - name: "llama-8b"           # 需要更好品質時切換
    backend: "ollama"
    endpoint: "http://localhost:11434"
    model: "llama3.1:8b"
    auto_start: true
    port: "11434"
    default: false
```

支援的 backend：
- `mlx` — Apple Silicon 原生推理（mlx-lm）
- `ollama` — Ollama server
- 任何 OpenAI-compatible API（LM Studio、vLLM 等）

### 記憶層

```yaml
memory:
  db_path: "~/.config/orch/orch.db"
  briefing_on_boot: true     # 啟動時顯示上次 briefing
  auto_summarize: true       # 任務完成後自動壓縮摘要
  history_limit: 0           # 歷史紀錄上限（0=無限）
```

### 工作流目錄

```yaml
workflows:
  dir: "~/.config/orch/workflows"
```

### Step 失敗策略

```yaml
# Plan 裡的每個 step 可設定 on_failure：
# - retry（預設）：重試最多 3 次
# - skip：跳過繼續執行下一步
# - re-plan：帶失敗 context 回 planner 重新規劃（最多 2 次）
# - abort：立即停止
```

## 並行執行（DAG 調度）

Executor 根據 `depends_on` 建立有向無環圖（DAG），自動並行化無依賴關係的步驟。

```yaml
# 範例：A 和 B 並行執行，C 等 A+B 都完成
steps:
  - id: step_a
    description: "查 AWS 用量"
    agent: shell
    command: "aws s3 ls"
  - id: step_b
    description: "查 GCP 用量"
    agent: shell
    command: "gcloud storage ls"
  - id: step_c
    description: "整合報告"
    agent: claude
    prompt: "整合以下兩份用量資料..."
    depends_on: [step_a, step_b]
```

特性：
- 每個 step 一個 goroutine，channel-based 依賴等待
- 無環偵測（DFS 三色標記法）
- 多上游 context 自動合併（所有依賴的 output/KV/Files 串接傳入）
- 支援向下相容：`"depends_on": "step_1"` (舊字串格式) 仍可用

## YAML 工作流模板

定義可重複使用的固定流程，透過觸發詞跳過 AI 規劃階段直接執行。

### 範例工作流 `~/.config/orch/workflows/signoff.yaml`

```yaml
name: "收工"
description: "執行收工流程：更新交接表 + 摘要"
trigger: "收工"
variables:
  project: "momo"
steps:
  - id: step_1
    description: "更新本地交接表"
    agent: kiro
    prompt: "讀取 docs/_agent_handoff.md，更新今日 {{.date}} 完成項目"
  - id: step_2
    description: "同步 Notion 交接看板"
    agent: claude
    prompt: "將交接表同步至 Notion 全局交接看板"
    depends_on: [step_1]
  - id: step_3
    description: "生成今日 briefing"
    agent: kiro
    prompt: "根據今日完成項目生成明日 briefing 摘要"
    depends_on: [step_1]
```

### 內建模板變數

| 變數 | 值 |
|------|---|
| `{{.date}}` | 今日日期（2006-01-02） |
| `{{.time}}` | 當前時間（15:04:05） |
| `{{.user}}` | 設定檔 persona.owner |

自定義變數在 `variables:` 區塊定義，Prompt/Command/Description 皆可使用。

## Streaming Output

長時間執行的步驟會即時輸出進度：

```
⏳ [step_a] Starting: 查 AWS 用量
   │ [step_a] 2026-07-01 mybucket-prod
   │ [step_a] 2026-06-29 mybucket-staging
✅ [step_a] Done (1.2s)

⏳ [step_b] Starting: 查 GCP 用量
   ⋯ [step_b] AI agent working... (5s elapsed)
✅ [step_b] Done (6.1s)
```

- Shell 指令：逐行串流 stdout
- AI Agent 呼叫：定期進度通知（5 秒間隔）
- 並行執行時以 `[step_id]` 前綴區分來源

## 路由行為

| 輸入類型 | 路由 | 處理方式 |
|---------|------|---------|
| 工作流觸發詞（`收工`、`週報`） | 📋 workflow match | 載入 YAML 直接執行 |
| 直接指令（`kubectl get pods`） | ⚡ keyword match | shell 直接執行 |
| 一般問答（`介紹你自己`） | 🍎 Local → category: chat | 本地 LLM 直接回答 |
| 自然語言任務（`列出 IP`） | 🍎 Local → agent 分派 | 交給 kiro/claude 處理 |
| Notion/日曆相關 | 🍎 Local → claude | claude 處理 |
| 寫程式/部署 | 🍎 Local → kiro | kiro 處理 |

## 記憶流程

```
啟動
  → SQLite 撈 briefing → 顯示「今日重點：...」
  → 使用者下指令 → workflow match / plan → execute（DAG 並行）
  → 完成後寫 history（input + category + output_summary + took_ms）
  → 下次啟動時 briefing 提供 context（省 cloud token）
```

## 封裝產物

```
~/go/bin/orch                    ~10 MB（Go binary）
~/.config/orch/
├── config.yaml                  ~3 KB（設定）
├── orch.db                      初始 50 KB → 半年 ~6 MB（SQLite）
└── workflows/                   YAML 工作流模板
    ├── signoff.yaml
    └── weekly.yaml
~/mlx-env/ + model               ~1.5 GB（Python venv + Qwen 1.5B 4-bit）
```

## 環境需求

- macOS (Apple Silicon)
- Go 1.22+
- Python 3.10+（for MLX LM）
- kiro-cli / claude CLI（AI agents）

## 版本歷史

### v0.3 — Phase 3（CLI 完善 + daemon + pipe）

| 功能 | 說明 |
|------|------|
| `orch history` 子命令 | 列出/搜尋/清除任務歷史（SQLite） |
| `orch briefing` 子命令 | 顯示/設定/自動生成（MLX summarize）每日摘要 |
| `--dry-run` 模式 | 只生成計畫 + ASCII DAG 圖視覺化，不執行 |
| REPL 斜線命令 | `/w` 工作流選單、`/h` 歷史、`/b` briefing、`/help` |
| stdin pipe 整合 | `cmd \| orch "分析"` 自動讀 stdin 作為 context |
| MLX launchd daemon | 登入自動啟動 MLX server，零冷啟動延遲 |

### v0.2.1 — Phase 2 完成（並行執行 + 工作流 + 串流）

| 功能 | 說明 |
|------|------|
| 並行執行（DAG） | goroutine per step + channel-based 依賴等待 + 無環偵測 |
| YAML 工作流模板 | ~/.config/orch/workflows/ 定義固定流程，觸發詞直接執行 |
| Streaming output | shell 逐行串流 + AI agent 進度通知 + 並行步驟前綴區分 |
| DependsOn 升級 | string → []string，支援多上游依賴 + 向下相容 |

### v0.2 — Phase 2（記憶 + 模型 + 失敗策略）

| 功能 | 說明 |
|------|------|
| 可換模型 | config 定義多個 backend，標記 default 切換 |
| SQLite 記憶 | 7 張表（history/prompts/agents/skills/shortcuts/workspaces/briefing） |
| Briefing on boot | 啟動時載入上次摘要，提供 context |
| Re-plan loop | 失敗時帶 context 回 planner 重新規劃（最多 2 次） |
| Structured output | StepResult 包含 Files/KV，step 間傳遞結構化資料 |
| On-failure 策略 | retry / skip / re-plan / abort 四種模式 |

### v0.1 — Phase 1（MVP）

| 功能 | 說明 |
|------|------|
| 三層路由 | Keyword → MLX Local → Cloud fallback |
| Executor | 逐步執行 + context chain + verify_cmd |
| REPL | readline 互動模式 |
| Tool Registry | 本機工具掃描 |
