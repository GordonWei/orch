# orch — AI 幕僚長 CLI

一個 CLI 入口，你只說要什麼，它自己規劃、分派、執行、驗證、交付成品。

## 快速開始

```bash
git clone https://github.com/GordonWei/orch.git
cd orch
make install
```

完成後執行：

```bash
orch "你好"
```

## 系統需求

- macOS (Apple Silicon M1+)
- Go 1.22+
- Python 3.10+（MLX LM 推理用）
- kiro-cli 或 claude CLI（AI agent backend）

## 安裝

### 方式一：make install（推薦）

```bash
make install    # build binary + 初始化 config + MLX env + launchd daemon
```

### 方式二：只編譯 binary

```bash
make build      # → ~/go/bin/orch
```

### 方式三：setup.sh

```bash
./setup.sh              # 完整安裝（含 MLX daemon）
./setup.sh --no-daemon  # 不裝 launchd daemon
```

### 確認安裝

```bash
orch --version
orch --tools
```

## 使用方式

```bash
# Oneshot（一次性任務）
orch "查 S3 bucket 用量"
orch "kubectl get nodes"

# Dry-run（只看計畫不執行）
orch --dry-run "整合 AWS 和 GCP 用量報告"

# Unix pipe
kubectl get pods -o json | orch "哪些 pod 不健康？"
cat error.log | orch "分析這個錯誤"

# REPL 模式
orch

# 子命令
orch history                 # 最近 20 筆歷史
orch history search kubectl  # 搜尋歷史
orch briefing                # 顯示 briefing
orch briefing gen            # MLX 自動生成 briefing
```

### REPL 內建命令

| 命令 | 說明 |
|------|------|
| `/w` | 列出可用工作流 |
| `/w 1` | 執行第 1 個工作流 |
| `/h` | 最近 10 筆歷史 |
| `/b` | 顯示 briefing |
| `/help` | 列出所有命令 |

## 架構

```
使用者輸入
    ▼
┌─────────────────────────────────────┐
│  Workflow Match                       │  ~/.config/orch/workflows/*.yaml
└────────────┬────────────────────────┘
             ▼ 不匹配
┌─────────────────────────────────────┐
│  Layer 1: Keyword Match（⚡ 0ms）    │  直接 shell 指令
└────────────┬────────────────────────┘
             ▼ 不匹配
┌─────────────────────────────────────┐
│  Layer 2: Local LLM（🍎 ~2-5s）     │  MLX / Ollama
└────────────┬────────────────────────┘
             ▼ 不可用
┌─────────────────────────────────────┐
│  Layer 3: Cloud LLM（☁️ ~5-8s）     │  claude -p fallback
└─────────────────────────────────────┘
             ▼
┌─────────────────────────────────────┐
│  Executor（DAG 並行調度）             │
│  goroutine per step + streaming      │
└─────────────────────────────────────┘
             ▼
┌─────────────────────────────────────┐
│  Memory Layer（SQLite）              │
└─────────────────────────────────────┘
```

## 專案結構

```
cmd/orch/
├── main.go          CLI 入口 + signal handler
├── repl.go          REPL 互動模式
├── printer.go       事件輸出格式化
└── dag.go           ASCII DAG rendering

pkg/
├── config/          設定檔載入（YAML → struct）
├── model/           LLM interface + auto-start server
├── memory/          SQLite 記憶層（7 張表）
├── registry/        本機工具掃描
├── planner/         三層路由 + plan 生成
├── executor/        DAG 並行執行 + streaming + re-plan
└── workflow/        YAML 工作流模板

launchd/             macOS LaunchAgent（MLX daemon）
config.yaml          設定檔範本
setup.sh             安裝腳本
Makefile             build/test/install
```

## 設定

位置：`~/.config/orch/config.yaml`（`ORCH_CONFIG` env 可覆蓋）

```yaml
# 模型設定（支援 MLX / Ollama / 任何 OpenAI-compatible API）
models:
  - name: "qwen-1.5b"
    backend: "mlx"
    endpoint: "http://localhost:8080"
    model: "mlx-community/Qwen2.5-1.5B-Instruct-4bit"
    default: true

# 記憶層
memory:
  db_path: "~/.config/orch/orch.db"
  briefing_on_boot: true
  auto_summarize: true

# 工作流目錄
workflows:
  dir: "~/.config/orch/workflows"
```

## 開發

```bash
make build     # 編譯
make test      # 跑測試
make lint      # go vet
make clean     # 清除產物
```

## License

MIT
