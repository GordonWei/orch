# orch 操作手冊

## 日常使用

### 路由行為

| 輸入類型 | 路由 | 範例 |
|---------|------|------|
| 工作流觸發詞 | 📋 YAML workflow | `orch "收工"` |
| CLI 指令 | ⚡ shell 直接跑 | `orch "kubectl get pods"` |
| 一般問答 | 🍎 本地 LLM | `orch "什麼是 K8s？"` |
| 任務分派 | 🍎→agent | `orch "查 AWS 帳單"` |

### 管理記憶

```bash
orch history                    # 最近 20 筆
orch history search "kubectl"   # 搜尋
orch history clear              # 清除全部

orch briefing                   # 顯示
orch briefing set "重點：..."    # 手動設定
orch briefing gen               # MLX 自動生成
```

## 設定修改

設定檔：`~/.config/orch/config.yaml`

### 切換模型

把 `default: true` 移到你要的模型，改完重啟 MLX server：

```bash
pkill -f "mlx_lm.server"
orch "hello"   # 自動啟動新模型
```

### 用 Ollama

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

需先 `brew install ollama && ollama pull llama3.1:8b`。

### 記憶層

```yaml
memory:
  briefing_on_boot: false    # 關閉啟動 briefing
  auto_summarize: false      # 關閉自動摘要
  history_limit: 5000        # 超過自動清理（0=無限）
```

## 工作流模板

放在 `~/.config/orch/workflows/*.yaml`，觸發詞命中時跳過 AI 規劃直接執行。

```yaml
name: "收工"
trigger: "收工"
steps:
  - id: step_1
    agent: kiro
    prompt: "更新交接表"
  - id: step_2
    agent: claude
    prompt: "同步 Notion"
    depends_on: [step_1]
```

內建模板變數：`{{.date}}`、`{{.time}}`、`{{.user}}`

## 故障排除

### MLX server 啟動失敗

```bash
# 確認 venv 存在
ls ~/mlx-env/bin/python3

# 手動啟動看錯誤
~/mlx-env/bin/python3 -m mlx_lm.server \
  --model mlx-community/Qwen2.5-1.5B-Instruct-4bit --port 8080

# port 被佔
lsof -i :8080 && kill $(lsof -ti :8080)
```

### 所有請求都走 cloud

```bash
curl http://localhost:8080/v1/models   # 應回 200
```

若失敗：確認 `auto_start: true` + `~/mlx-env/bin/python3` 存在。

### LaunchAgent（daemon）管理

```bash
# 狀態
launchctl list | grep orch

# 停止
launchctl unload ~/Library/LaunchAgents/com.orch.mlx-server.plist

# 啟動
launchctl load ~/Library/LaunchAgents/com.orch.mlx-server.plist

# 日誌
tail -f ~/Library/Logs/orch-mlx.log
```

### 直接操作 SQLite

```bash
sqlite3 ~/.config/orch/orch.db "SELECT timestamp, input, category FROM history ORDER BY id DESC LIMIT 10;"
```

## 換機器

```bash
# 新機器
git clone https://github.com/GordonWei/orch.git && cd orch
make install

# 遷移設定和記憶（選用）
scp old-mac:~/.config/orch/config.yaml ~/.config/orch/
scp old-mac:~/.config/orch/orch.db ~/.config/orch/

# 確認 agents
which kiro-cli && which claude

# 測試
orch "你好"
```
