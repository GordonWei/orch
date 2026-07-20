# orch Product Roadmap

> 基於 v0.14.0 現狀，聚焦「個人 AI CLI Orchestrator」的實用演進。

---

## 🔴 v0.16 Priority：Bedrock + Vertex AI Stateless API Backends（10/31 AWS Community Day 分享前必須完成）

**背景**：10/31 AWS Community Day Taiwan 投稿標題「用 MLX + Bedrock 打造零成本 AI Agent 調度器」已送出。目前 Layer 3 只有 kiro/claude/gemini 三個 **CLI-based（PTY session）** adapter，沒有真正呼叫 Bedrock/Vertex AI API 的路徑——題目寫「Bedrock」，程式碼裡卻沒有對應實作。這批工作要讓題目字面上的技術主張在程式碼裡真實存在，不是靠「Kiro 底層即 Bedrock」這種代稱蒙混過去。

**交接方式**：Kiro 先依下方規格寫初版（架構設計＋實作），Victoria 之後 code review（比照過往協作模式：Kiro 實作 → Victoria review → 視情況修正 → commit/tag/push）。

### 需求規格

**1. 新 backend 型態：Stateless API Backend**
- 現有 `pkg/backend/backend.go` 的 adapter 都是「spawn CLI process + PTY」模式（session 型，claude/kiro/gemini）
- 新增一種 backend 型態（擴充現有介面或新增介面），支援「一次性 HTTP API 呼叫」：送出 prompt → 拿到 response → 結束，不維持長駐 process/session
- 需要跟現有 `pkg/executor`（DAG executor）、`pkg/planner`（Layer 3 dispatch）相容——呼叫端透過同一介面呼叫，不需要關心目標是 session 型還是 stateless 型

**2. Bedrock adapter**
- 直接呼叫 AWS Bedrock Runtime API（`InvokeModel` 或 `Converse`），不透過 kiro CLI
- 認證：沿用 AWS 標準 credential chain（環境變數 / `~/.aws/credentials` / SSO profile），不要自建認證邏輯
- Model ID 可在 `config.yaml` 設定（具體選型，例如 Claude on Bedrock 或 Amazon Nova 系列，待 Gordon 決定）
- API 呼叫失敗（額度/權限/網路）要有清楚錯誤訊息，不可靜默吞掉

**3. Vertex AI adapter**
- 直接呼叫 Vertex AI `generateContent` API
- 認證：沿用 GCP ADC（Application Default Credentials）或 service account key
- Model ID 可設定（例如 Gemini 系列）

**4. Config 串接**
- `config.yaml` 的 `ai_backend` 與 `route_rules.rules[].target` 需能接受新 backend 名稱（如 `bedrock`/`vertexai`），讓既有 config-driven 路由規則可以直接把 pattern 指到這兩個新 backend，路由邏輯本身不用改
- `pkg/registry/`（本機工具偵測）要能區分「CLI 型 backend 需偵測二進位檔是否存在」vs「API 型 backend 只需偵測是否設定好認證/credential」

**5. Context 傳遞機制（無 session 版的 `/pass`）**
- 現有 `/pass` 是 session-to-session：把某個 PTY session 的最後輸出轉送到另一個 session
- Stateless API 型 backend 沒有 PTY session 可轉送，需要另一套機制：維護「per-backend 最後一次輸出」的輕量快取（可沿用/擴充既有 `session_logs` SQLite 表存 stateless backend 的呼叫紀錄），`/pass` 對 stateless backend 的處理改成「把快取的上一次輸出組進下一次 API request 的 prompt/conversation history」
- 目標體驗：`/pass claude bedrock` 這類指令語法一致，不管目標是 session 型還是 API 型，內部依型態走不同路徑

**6. 成本追蹤（呼應下方 Mid-term #8，這次要優先做）**
- 每次呼叫 Bedrock/Vertex AI API 記錄 token 用量（input/output）+ 依官方定價換算的估計成本，寫入既有 memory/history 層
- `orch history`/`orch briefing` 要能把這些新 backend 的成本數字撈出來，統計格式沿用現有 usage pattern 分類
- **這是 10/31 演講素材的真實數字來源，優先度高於單純功能完整度**——講題裡的成本剖面表不能用 README 的估算數字上台

### 非目標（這次不用做）
- 不需要幫 Bedrock/Vertex AI 做 PTY/互動式 session（設計上就是 stateless 單次呼叫）
- 不需要支援 Bedrock/Vertex AI 以外的其他雲端 API（如 Azure OpenAI），除非之後另外提出

### 時程
建議 9 月底前完成並穩定運行至少 2-3 週，讓成本數據有累積時間、也讓 Gordon 有時間彩排 demo。詳細講題 Agenda 見 `PersonalBrand/docs/_draft_speaking_agendas_0828_1031.md`。

---

## Near-term（v0.15–v0.16，1-2 週）

| # | 功能 | 描述 | 為什麼做 | 複雜度 |
|---|------|------|---------|--------|
| 1 | **techIndicators config-driven** | 將 `router.go` Classify() step 2 的 70+ 技術關鍵字搬到 `config.yaml` | 目前是唯一還 hardcode 的路由邏輯，用戶無法自訂「什麼算技術任務」 | S |
| 2 | **Session output 持久化** | session mode 的對話歷史自動寫入 SQLite（或 `~/.config/orch/sessions/`） | 目前 session 結束就沒了，無法回顧「剛才跟 claude 聊了什麼」 | M |
| 3 | **`orch replay <id>`** | 從 history 重新執行某次任務（含原始 plan + re-execute） | `orch history` 能看但不能重跑，失敗任務需要手動重打 | S |
| 4 | **Session 間 context 傳遞** | `/pass <backend>` 把當前 session 最後一輪 output 注入另一個 session 作為 context | 目前 multi-session 之間是隔離的，手動 copy-paste 很痛苦。這是 orch 相比直接開多 terminal 的核心差異化 | M |
| 5 | **`--json` output mode** | one-shot 模式輸出結構化 JSON（plan + result + timing） | 方便跟其他腳本/CI 串接，Unix philosophy | S |

---

## Mid-term（v0.17–v0.20，1-2 月）

| # | 功能 | 描述 | 為什麼做 | 複雜度 |
|---|------|------|---------|--------|
| 6 | **Approval Gate（人工確認）** | 高風險 step（`terraform apply`、`rm -rf`）自動暫停等使用者確認才繼續 | 目前 DAG executor 一路衝到底，沒有 human-in-the-loop。對比 Claude Code 的權限模型，orch 作為 orchestrator 更需要這個 | M |
| 7 | **Multi-step Workflow 編輯器** | `orch workflow new` 互動式建立 YAML workflow（不需要手寫） | 降低 workflow 建立門檻，目前必須懂 YAML schema | M |
| 8 | **Task cost tracking** | 追蹤每次 cloud backend 呼叫的 token 用量/時長，定期報表 | README 強調 cost-efficient，但目前沒有數據證明。讓用戶真正看到「本地處理了 80%」 | M |
| 9 | **Conditional routing by time-of-day** | 工作時間走 cloud（需要品質），深夜/凌晨走 local-only（省錢、低延遲） | 個人使用者有明顯的模式：上班嚴肅任務 vs 下班隨便問問 | S |
| 10 | ~~**Plugin system（hook 腳本）**~~ ✅ | `pkg/hooks` 實作完成：5 trigger（pre_route/pre_execute/post_execute/on_session_start/on_session_end）、JSON via STDIN、exit 2 block、timeout。已串接 executor + repl。 | ~~Event Bus 目前只支援 AI agent 作為 action~~ | M |
| 11 | **模型熱切換** | REPL 內 `/model qwen-8b` 即時切換 MLX 推理模型，不重啟 server | 目前切模型要改 config + 重啟 server，不方便 A/B 測試不同模型的路由準確度 | L |
| 12 | **Session 複合指令** | `/all "status"` 同時送指令到所有 active session，收集回覆並比較 | 一次問多個 AI 同樣問題再選最好的回答——這是 orchestrator 獨有的能力 | M |

---

## Long-term（v1.0 Vision）

| # | 功能 | 描述 | 為什麼做 | 複雜度 |
|---|------|------|---------|--------|
| 13 | **Learning Router** | 基於歷史 success/fail 數據，自動調整 route rule strength | 目前規則是靜態的——用戶打了 100 次 terraform 都成功走 kiro，但規則不會因此變更自信 | L |
| 14 | **Cross-device sync** | briefing/history/config 同步（iCloud/git-based） | 你有桌機和筆電，兩邊的 orch 記憶目前完全獨立 | L |
| 15 | **MCP Server mode** | orch 本身暴露為 MCP server，讓 Claude/Kiro 能反向呼叫 orch 的能力（子任務分派） | 現在 orch 呼叫 backend，但 backend 不能反向調度。若 Claude 在處理一個大任務時想「順便叫 kiro 跑個 terraform plan」，目前做不到 | L |
| 16 | **Autonomous mode（agent loop）** | `orch --auto "完成這個 PR 的 code review 到 merge"` → 自動迭代 plan/execute/verify 直到完成 | 目前是 one-shot plan+execute，沒有 retry loop 或 goal-driven iteration。這是往 ORCH (TypeScript) 的「sleep and wake up to PRs」靠攏 | L |
| 17 | **可視化 Dashboard** | `orch dashboard` 開 local web UI 顯示 session 狀態、routing 統計、cost breakdown | CLI 友善但有時需要全局鳥瞰。不做 SaaS，只做本地 HTTP server | L |
| 18 | **Ollama/vLLM backend 支援** | Layer 2 不只 MLX，也支援 Ollama（Linux）和 vLLM（GPU server） | 目前鎖死 macOS Apple Silicon，加入 Ollama 就能跑在 Linux dev machine 上 | M |

---

## 優先建議

如果只做 3 件事，推薦順序：

1. **#4 Session 間 context 傳遞** — 這是 orch 最核心的差異化（「我為什麼不直接開兩個 terminal？」的答案）
2. **#6 Approval Gate** — 安全性，尤其你會拿它跑 terraform/kubectl
3. **#2 Session output 持久化** — 實用性，每次 session 結束都丟失對話很痛

---

## 不做清單（Anti-Roadmap）

| 方向 | 為什麼不做 |
|------|-----------|
| Multi-user / team collaboration | 這是個人工具，不是 platform |
| Web UI first | CLI-native 是核心哲學 |
| 自建 LLM fine-tune pipeline | 用現成的 MLX community 模型就好 |
| Agent memory graph / RAG | 讓 backend 自己管（Claude 有 memory，Kiro 有 steering） |
| 支援 Windows | Apple Silicon + MLX 是核心優勢，Windows 沒有等效本地推理 |
