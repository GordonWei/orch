# orch 使用者體驗實測報告（含修正紀錄）

> 測試日期：2026-07-20 深夜～2026-07-21 凌晨
> 測試機器：桌機（家用），非平常做 MLX/7B 測試的那台筆電
> 測試方式：`go build` 重新編譯當前 `main` 後，用真實指令實際跑過一輪日常會用到的功能，不是讀 code 猜行為
> 狀態：**3 個發現全部已動手修正並重新實測驗證通過**，待你 review 後決定要不要 commit/tag/push（目前尚未動）

---

## ⚠️ 先更正一件事：發現 1 原本的診斷有一半是我自己看錯，不是真的 bug

原始報告寫「kiro 回覆裡混著沒清乾淨的 markdown 星號」，証據是用 `od -c` 拆出來看到一堆 `**`。回頭要修的時候用 `hexdump -C` 重新核對原始 bytes，發現**那些 `**` 根本不存在於真實資料裡**——是這台機器 BSD `od -c` 在顯示多位元組 UTF-8 字元（中文字）時，會用 `**` 當作續位元組的佔位符，純粹是我這邊的除錯工具在騙我，不是 kiro-cli 真的輸出壞掉的 markdown。已經拿一段乾淨的純中文字串（`嗨！你好`，跟 kiro 完全無關）重現同款「假 `**`」現象，確認是 `od -c` 本身的問題，不是 orch 或 kiro 的 bug。

**真正存在的問題只有 ANSI 顏色碼**（`\033[38;5;141m> \033[0m` 這段，用 `hexdump -C` 確認過是真實 bytes，不是誤讀），這個已經修正並在下面驗證過。抱歉這個環節前面判斷得不夠嚴謹，先更正，再往下看修法。

---

## 先講結論（更新版）

修了半個月的路由/分類 bug（v0.16.1～v0.17.0）方向都是對的，這次實測也驗證了分類本身現在幾乎都對：「你好」判 chat、「台中明天天氣」判 `claude:query`、「列出檔案」判 `shell`、「那讀交接」判 `docs`，全部命中。

但分類對了不等於使用者拿到的東西是好的。抓到 3 個問題，**現在都已經動手修完，並且逐項重新實測驗證通過**：

| # | 問題 | 嚴重度 | 狀態 |
|---|------|--------|------|
| 1 | kiro 執行結果的 ANSI 顏色碼原封不動印出來 | 🔴 | ✅ 已修正並驗證 |
| 2 | `--backend gemini` / `/session gemini` 完全打不通（gemini CLI 升級拿掉了 orch 寫死的 `--skip-trust`） | 🔴 | ✅ 已修正並驗證 |
| 3 | 「那讀交接」不再誤判成聊天，但實際是把整份交接檔原文吐到終端機，不是摘要 | 🟡 | ✅ 已修正並驗證 |

---

## 🔴 發現 1：kiro 回覆混著沒清乾淨的 ANSI 顏色碼 → 已修正

**怎麼測的**：`./orch "greet me in one short sentence"` 之後用 `hexdump -C` 看真實 bytes（不是 `od -c`，見上面更正）。

**修正前**：輸出開頭是 `1b 5b 33 38 3b 35 3b 31 34 31 6d 3e 20 1b 5b 30 6d`，也就是 `\033[38;5;141m> \033[0m`——這是 kiro-cli 自己的顏色 prompt（`> `），設計給人坐在真的終端機前看，`exec.Command` 非互動 capture 沒有 PTY，這些顏色碼全部被當純文字收下來，原樣印給使用者。

**根因**：`pkg/backend/backend.go` 的 `runCmd()` 直接把 `cmd.Stdout` 的 bytes 原封不動回傳，完全沒有清洗這一步。專案裡其實已經有現成、UTF-8-safe 的 ANSI 清洗邏輯（`pkg/session/strip.go` 的 `StripState.Strip()`，v0.10.1 review 時修過），只是 PTY session 那條路徑在用，這條 batch capture 的路徑完全沒套用。

**修法**：
- `runCmd()` 回傳前套用 `session.StripState{}.Strip()`（複用既有邏輯，不重寫）
- 額外幫 `KiroBackend.Execute()` 加一層 `cleanKiroChatOutput()`，把 ANSI 清乾淨後殘留的純文字 `"> "` prompt 前綴也去掉（這段是真實存在的純文字，不是誤讀，`hexdump` 有驗證到）

**驗證**：重新編譯後跑同一句話，`hexdump -C` 確認整段輸出完全沒有 `0x1b` bytes，也沒有殘留 `"> "` 前綴，開頭直接是乾淨的回覆內容「嗨！週二凌晨了還沒睡，你也辛苦 👋 有什麼我能幫忙的嗎？」。`go test -race ./...` 全綠。

**影響範圍**：這是 `runCmd()` 的共用邏輯，`kiro`/`claude`/`gemini` 三種 agent 都會受惠，不只是 chat fallback 這條路徑。

---

## 🔴 發現 2：gemini backend 完全打不通 → 已修正

**怎麼測的**：`./orch --dry-run --backend gemini "整理這篇文章重點"`

**修正前**：orch 沒印出 dry-run 計畫，反而是 `gemini` CLI 自己的 yargs 錯誤噴了整頁 help，結尾 `Unknown arguments: skip-trust, skipTrust`。

**根因**：`pkg/backend/backend.go`（`GeminiBackend.Execute`/`CLIArgs`）與 `pkg/session/session.go`（`/session gemini` 的 PTY spawn）都寫死 `gemini --skip-trust ...`。查這台機器裝的 `gemini --help`，現在只有 `-y`/`--yolo` 跟 `--approval-mode`，**`--skip-trust` 已經被 Google 拿掉了**，是上游 CLI 改版，不是 orch 邏輯錯。README 裡 `/session gemini` 的說明跟 `--help` 範例（`orch --backend gemini "summarize this doc"`）兩處都是照著這個已失效的參數寫的。

**修法**：兩個檔案的 `--skip-trust` 都換成 `--yolo`（效果一樣：自動接受所有動作，不用再手動確認）：
- `pkg/backend/backend.go`：`GeminiBackend.Execute()`/`CLIArgs()`
- `pkg/session/session.go`：`/session gemini` 的 PTY spawn

**驗證**：① 直接測 `gemini --yolo -p "reply with exactly PONG"` 正常回 `PONG` ② 透過 orch 跑 `./orch --backend gemini --dry-run "summarize test"`，這次順利印出 `routed by: gemini (cloud)` 跟正常的 dry-run 計畫，不再噴 CLI 錯誤。`go test -race ./...` 全綠。

**沒動的部分**：README.md v0.13.0 那則舊 changelog 提到 `--skip-trust --yolo` 的文字**沒有改**——那是當時（v0.13.0 發布時）真實正確的用法，事後改寫歷史紀錄不合適，已經在新的 changelog 條目另外記錄這次修正。

---

## 🟡 發現 3：「那讀交接」分類對了，但吐的是原文不是摘要 → 已修正

**怎麼測的**：`echo "那讀交接" | ./orch`（這是 7 月中那一整輪 v0.16.1～v0.16.3 路由 bug 追查的起點案例）

**修正前**：分類正確（`category: docs`），但路由到 `agent: shell`、`command: cat ...`，把整份 200＋ 行交接檔原文原樣印到終端機，等同 `cat docs/_agent_handoff.md`，使用者要自己在一長串 markdown 裡找重點。

**根因**：`pkg/planner/planner.go` 的 `buildSystemPrompt()`（Layer 3 cloud 規劃時餵給 kiro/claude 的系統提示）完全沒有教模型區分「使用者想要原文」vs「使用者想要理解內容/摘要重點」，模型看到「讀」這個字最直覺的選擇就是 `shell: cat`。

**修法**：`buildSystemPrompt()` 新增一條規則（第 8 條）：使用者說「讀/看一下/check」文件、log、交接檔是為了了解內容或現況（不是明確要求看原文/完整文字），要用 `kiro`/`claude` agent 帶著「讀取該路徑的檔案並摘要重點」的 prompt，而不是純 `shell cat`；只有使用者明確要求原文/完整內容時才用 shell。

**驗證**：重新編譯後跑同一句話：
- `--dry-run` 顯示 task summary 變成「讀取交接文件並摘要目前狀態」，`agent: kiro`，prompt 是「請讀取 ...，摘要目前的專案狀態、進行中任務、待辦事項、以及任何需要注意的阻塞點」
- 拿掉 `--dry-run` 實際跑，真的印出一份結構化的繁體中文摘要（已完成的重大工作 / 進行中待辦 / 阻塞點 / 跨專案快照四段），內容也確實正確反映了截至今晚的最新狀態（含這次 session 剛做完的 Gitea 補推）

這是三個發現裡改動幅度最大的一個（改 prompt 邏輯而非單純清資料），值得你重點 review 一下這條規則會不會誤傷「我就是要看原文」的情境——目前規則寫的是「明確要求原文/完整文字才用 shell」，實測沒有明顯反例，但這種 prompt-level 規則本質上不是 100% 確定性的，之後如果又遇到分類跑偏，這裡是第一個要回頭檢查的地方。

---

## 🟡 附帶觀察（環境因素，不是 code bug，這次沒動，記錄留待你決定要不要處理）

- **這台機器完全沒裝 MLX**（`/Users/wei/mlx-env/bin/python3` 不存在），Layer 2（本機分類/本機聊天）整段沒被測到，這次測的其實是「MLX 掛掉時的降級體驗」。MLX 掛掉時目前完全沒有事前警告，只有每次聊天訊息單獨噴一行「local chat failed, falling back to executor」，使用者會覺得莫名其妙每次打招呼要等 7 秒。
- 這台機器的 `~/.config/orch/config.yaml` 從沒被 `orch init` 更新過：`models[0].model` 還停在 `qwen-3b`，`memory.briefing_source_file` 也沒設（`orch briefing` 印出來還是 7/1 那筆測試假資料）——呼應根目錄交接檔一直在講的「跨機器 config 沒同步」問題，這次沒有動手改這台機器的 config（不確定這台機器你平常要不要真的拿來跑 orch，怕改錯方向）。

---

## 順手記的小問題（沒深究，優先度低，這次沒動）

- **AWS S3 指令品質**：`--dry-run "check S3 bucket usage"` 產生的指令是 `aws s3 ls --summarize --human-readable --recursive s3://`——`s3://` 後面沒接 bucket 名稱，語意模糊，這類 cloud LLM 生成指令目前沒有任何 sanity check。
- `orch history` 裡兩筆 bedrock/vertexai 呼叫先 ❌ 後 ✅（同一句話重打就過），沒深挖錯誤訊息，可能只是憑證/連線暖機偶發問題。

---

## 這次沒測到的（誠實列出）

- **REPL 互動模式**與 `/session claude|kiro|gemini`——需要真實 TTY，這次非互動腳本沒辦法覆蓋，只能從 code 推論行為，`/session gemini` 那行程式碼有跟著修（跟發現 2 同一組參數），但沒有真的 spawn 驗證過。
- **hooks（v0.17.0 新功能）**、**workflow 觸發**——沒有刻意去湊觸發條件，沒測。
- **claude backend**（`claude -p --dangerously-skip-permissions`）——沒有實際跑（遞迴呼叫自己的風險），只讀 code 確認參數語法沒有像 gemini 那樣過期。

---

## 修改的檔案

- `pkg/backend/backend.go`：`runCmd()` 套用 ANSI strip；新增 `cleanKiroChatOutput()`；`GeminiBackend` 的 `--skip-trust` → `--yolo`
- `pkg/session/session.go`：`/session gemini` 的 `--skip-trust` → `--yolo`
- `pkg/planner/planner.go`：`buildSystemPrompt()` 新增「讀文件要摘要不要純 cat」規則

`go build`/`go vet`/`go test -race ./...` 全綠，三項發現都用真實指令重新實測驗證通過。**目前程式碼還沒 commit**，等你 review 這份報告沒問題後再走 commit/tag/push 流程。
