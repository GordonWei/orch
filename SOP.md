# orch SOP（Standard Operating Procedure）

## 1. 新機器安裝

### 前置條件
- macOS (Apple Silicon M1+)
- Homebrew 已安裝
- kiro-cli 和 claude CLI 已安裝

### 一鍵安裝

```bash
git clone <repo> ~/Desktop/Cowork  # 或 sync 你的工作區
cd ~/Desktop/Cowork/Study/projects/multi-agent-orchestrator/dev
./setup.sh
```

`setup.sh` 自動執行：
1. 安裝 Go（如果沒有）
2. 建立 `~/mlx-env/` Python venv + 安裝 mlx-lm
3. 下載 Qwen 2.5 1.5B 4-bit 模型（~1GB，首次需要網路）
4. `go build` 編譯 orch binary → `~/go/bin/orch`
5. 建立 `~/.config/orch/` 目錄 + 複製 config.yaml

### 手動安裝（如果 setup.sh 有問題）

```bash
# 1. Go
brew install go

# 2. MLX
python3 -m venv ~/mlx-env
source ~/mlx-env/bin/activate
pip install mlx-lm
python3 -c "from mlx_lm import load; load('mlx-community/Qwen2.5-1.5B-Instruct-4bit')"
deactivate

# 3. Build
cd Study/projects/multi-agent-orchestrator/dev
go build -o ~/go/bin/orch ./cmd/orch/

# 4. Config
mkdir -p ~/.config/orch
cp config.yaml ~/.config/orch/config.yaml
```

### 確認 PATH

確保 `~/go/bin` 在你的 PATH 中：

```bash
echo 'export PATH="$HOME/go/bin:$PATH"' >> ~/.zshrc
source ~/.zshrc
```

---

## 2. 日常使用

### 啟動

```bash
# 直接用（MLX server 會自動啟動）
orch "你的任務"

# 或進入 REPL
orch
```

首次執行會看到：
```
🍎 starting MLX server...
   ✅ MLX server ready (pid 12345)
📋 briefing (generated 07/01 08:30):
   今日重點：GKE 3 nodes 正常，昨天部署了 litellm PRD
```

### 常用指令

```bash
# 直接跑 CLI 指令（keyword match，最快）
orch "kubectl get pods -A"
orch "terraform plan"
orch "helm list"

# 問問題（本地 LLM 回答）
orch "什麼是 Gateway API？"
orch "介紹一下 MetalLB"

# 任務分派（自動選 agent）
orch "查一下 AWS 這個月的帳單"
orch "幫我整理今天的會議記錄到 Notion"
orch "寫一個 health check 的 Go function"

# 查看可用工具
orch --tools
```

### REPL 模式操作

| 按鍵 | 功能 |
|------|------|
| ← → | 游標移動 |
| ↑ ↓ | 瀏覽歷史 |
| Ctrl+A / Ctrl+E | 行首 / 行尾 |
| Ctrl+W | 刪除前一個字 |
| Ctrl+C | 清除當前輸入 |
| Ctrl+D | 退出 |
| `exit` / `quit` / `q` | 退出 |
| `tools` | 顯示工具清單 |

---

## 3. 設定修改

### 設定檔位置

```
~/.config/orch/config.yaml
```

### 切換模型

```yaml
models:
  - name: "qwen-1.5b"
    backend: "mlx"
    model: "mlx-community/Qwen2.5-1.5B-Instruct-4bit"
    default: true    # ← 把 default 移到你要的模型
  - name: "qwen-3b"
    backend: "mlx"
    model: "mlx-community/Qwen2.5-3B-Instruct-4bit"
    default: false
```

改完後需要重啟 MLX server：
```bash
pkill -f "mlx_lm.server"
# 下次跑 orch 會自動啟動新模型
```

### 換 Ollama backend

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

需要先安裝 Ollama：`brew install ollama && ollama pull llama3.1:8b`

### 記憶層設定

```yaml
memory:
  db_path: "~/.config/orch/orch.db"
  briefing_on_boot: true     # false = 啟動不顯示 briefing
  auto_summarize: true       # false = 不自動壓縮寫 briefing
  history_limit: 5000        # 超過 5000 筆自動清理（0=無限）
```

### 改 system prompt

```yaml
persona:
  system_prompt: |
    你是 xxx，負責 yyy...
```

改完立即生效（不用重新 compile）。

---

## 4. 故障排除

### MLX server 啟動失敗

```bash
# 確認 Python venv 存在
ls ~/mlx-env/bin/python3

# 手動啟動看錯誤
source ~/mlx-env/bin/activate
mlx_lm.server --model mlx-community/Qwen2.5-1.5B-Instruct-4bit --port 8080

# 常見問題：port 被佔
lsof -i :8080
kill <pid>
```

### 所有請求都走 cloud（claude）

1. 確認 MLX server 在跑：`curl http://localhost:8080/v1/models`
2. 確認 config 的 `auto_start: true`
3. 確認 `~/mlx-env/bin/python3` 存在

### orch command not found

```bash
# 確認 binary 存在
ls ~/go/bin/orch

# 確認 PATH
echo $PATH | tr ':' '\n' | grep go
```

### 查看歷史紀錄

```bash
sqlite3 ~/.config/orch/orch.db "SELECT timestamp, input, category, success FROM history ORDER BY id DESC LIMIT 10;"
```

### 手動更新 briefing

```bash
sqlite3 ~/.config/orch/orch.db "INSERT OR REPLACE INTO briefing (id, content, generated_at) VALUES (1, '你的 briefing 內容', datetime('now'));"
```

### 清除歷史

```bash
sqlite3 ~/.config/orch/orch.db "DELETE FROM history;"
```

---

## 5. 換機器 Checklist

| # | 項目 | 指令 |
|---|------|------|
| 1 | 同步工作區 | `rsync` 或 git clone |
| 2 | 跑 setup | `./setup.sh` |
| 3 | 複製 config | `cp ~/.config/orch/config.yaml` 到新機器 |
| 4 | 複製記憶 | `cp ~/.config/orch/orch.db` 到新機器（選用） |
| 5 | 確認 agents | `which kiro-cli && which claude` |
| 6 | 測試 | `orch "你好"` |

---

## 6. 開發者指南

### Build

```bash
cd Study/projects/multi-agent-orchestrator/dev
go build -o orch ./cmd/orch/
```

### Test

```bash
go test ./...
go vet ./...
```

### 目錄結構

```
cmd/orch/main.go              入口（CLI 解析、REPL、memory 整合、re-plan callback）
pkg/config/config.go          設定檔載入（支援 Models[] + Memory）
pkg/model/model.go            LLM interface + OpenAI-compatible client
pkg/model/starter.go          Auto-start server（MLX/Ollama）
pkg/memory/memory.go          SQLite Store（7 張表）
pkg/registry/registry.go      工具掃描
pkg/planner/planner.go        路由 + planning + DirectChat
pkg/executor/executor.go      執行 + context chain + re-plan + structured output
```

### 加新模型 backend

1. 確認是 OpenAI-compatible API（`/v1/chat/completions`）
2. `config.yaml` 的 `models[]` 加一筆
3. 不需要改 code — `pkg/model/` 的 `OpenAIClient` 是通用的

### 加新工具

1. `pkg/registry/registry.go` 的 `toolDef` 列表加一筆
2. `config.yaml` 的 `routing` 加對應 keywords
3. 如果需要 keyword shortcut，加到 `keyword_shortcuts`
