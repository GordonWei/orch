# orch — AI 幕僚長 CLI

一個 CLI 入口，你只說要什麼，它自己規劃、分派、執行、驗證、交付成品。

## 架構

```
使用者輸入
    │
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
│  Executor                            │
│  逐步執行 → context chain 串接       │
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
Study/projects/multi-agent-orchestrator/dev/
├── cmd/orch/main.go           CLI 入口（oneshot + REPL + auto-start + memory）
├── pkg/
│   ├── config/config.go       設定檔載入（YAML → struct，支援 Models[] + Memory）
│   ├── model/
│   │   ├── model.go           LLM interface + OpenAI-compatible client
│   │   └── starter.go         Auto-start MLX/Ollama server
│   ├── memory/
│   │   └── memory.go          SQLite Store（7 張表 CRUD）
│   ├── registry/registry.go   本機工具掃描 + model 偵測
│   ├── planner/planner.go     三層 Fallback 路由 + plan 生成 + DirectChat
│   └── executor/executor.go   逐步執行 + context chain + re-plan + structured output
├── config.yaml                設定檔範本（含可換模型 + memory 設定）
├── setup.sh                   一鍵安裝腳本
├── docs/flow.md               Mermaid 流程圖
└── README.md                  本文件
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

### Step 失敗策略

```yaml
# Plan 裡的每個 step 可設定 on_failure：
# - retry（預設）：重試最多 3 次
# - skip：跳過繼續執行下一步
# - re-plan：帶失敗 context 回 planner 重新規劃（最多 2 次）
# - abort：立即停止
```

## 使用方式

```bash
# Oneshot（一次性任務）
orch "查 S3 bucket 用量"
orch "kubectl get nodes"
orch "整理今天的會議記錄並同步 Notion"

# REPL 模式（持續對話）
orch

# 查看可用工具
orch --tools

# 一般問答（本地 LLM 直接回答）
orch "什麼是 Kubernetes？"
```

## 路由行為

| 輸入類型 | 路由 | 處理方式 |
|---------|------|---------|
| 直接指令（`kubectl get pods`） | ⚡ keyword match | shell 直接執行 |
| 一般問答（`介紹你自己`） | 🍎 Local → category: chat | 本地 LLM 直接回答 |
| 自然語言任務（`列出 IP`） | 🍎 Local → agent 分派 | 交給 kiro/claude 處理 |
| Notion/日曆相關 | 🍎 Local → claude | claude 處理 |
| 寫程式/部署 | 🍎 Local → kiro | kiro 處理 |

## 記憶流程

```
啟動
  → SQLite 撈 briefing → 顯示「今日重點：...」
  → 使用者下指令 → plan → execute
  → 完成後寫 history（input + category + output_summary + took_ms）
  → 下次啟動時 briefing 提供 context（省 cloud token）
```

## 封裝產物

```
~/go/bin/orch                    ~10 MB（Go binary）
~/.config/orch/
├── config.yaml                  ~3 KB（設定）
└── orch.db                      初始 50 KB → 半年 ~6 MB（SQLite）
~/mlx-env/ + model               ~1.5 GB（Python venv + Qwen 1.5B 4-bit）
```

## 環境需求

- macOS (Apple Silicon)
- Go 1.22+
- Python 3.10+（for MLX LM）
- kiro-cli / claude CLI（AI agents）

## Phase 2 新增功能

| 功能 | 說明 |
|------|------|
| 可換模型 | config 定義多個 backend，標記 default 切換 |
| SQLite 記憶 | 7 張表（history/prompts/agents/skills/shortcuts/workspaces/briefing） |
| Briefing on boot | 啟動時載入上次摘要，提供 context |
| Re-plan loop | 失敗時帶 context 回 planner 重新規劃（最多 2 次） |
| Structured output | StepResult 包含 Files/KV，step 間傳遞結構化資料 |
| On-failure 策略 | retry / skip / re-plan / abort 四種模式 |
