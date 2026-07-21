# orch 使用者體驗實測報告（含修正紀錄）

> 測試日期：2026-07-20 深夜～2026-07-21 凌晨（第一輪）、2026-07-21 凌晨（第二輪，因 10/31 AWS Community Day 要拿這個專案演講，補測更多功能）
> 測試機器：桌機（家用），非平常做 MLX/7B 測試的那台筆電
> 測試方式：`go build` 重新編譯當前 `main` 後，用真實指令實際跑過一輪日常會用到的功能，不是讀 code 猜行為
> 狀態：**第一輪 3 個發現已修正、驗證、commit+tag（v0.17.1）+ push GitHub/Gitea + 建 Release。第二輪測了 REPL/hooks/approval gate/workflow/cost tracking，另外抓到並直接修正一個部署層級的問題（5 個 workflow 觸發詞跟 repo 脫節），其餘記錄但未修**

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

`go build`/`go vet`/`go test -race ./...` 全綠，三項發現都用真實指令重新實測驗證通過。

**發布**：你確認後已照專案慣例走完整流程——2 commits（`b5176b9` fix+README changelog、`c813418` docs/SOP troubleshooting entries+本報告）+ tag `v0.17.1` + push GitHub/Gitea + 建 GitHub Release：https://github.com/GordonWei/orch/releases/tag/v0.17.1

---

# 第二輪測試（因 10/31 要拿這個專案演講，補測第一輪沒覆蓋到的功能）

第一輪只測了 oneshot 模式的幾種 prompt。這輪把「REPL 互動、hooks、approval gate、workflow 觸發、cost tracking、session mode」都實際跑過一次。

## ✅ REPL 互動模式——正常，且第一輪的 ANSI 修法在這裡也生效

用 pty 驅動（真實分配一個虛擬終端機餵指令進去，不是 pipe）測了 `/help`、`你好`、`/history`、`/briefing`、`/w`。

- Banner、`/help` 指令列表、`/history`、`/briefing` 全部正常顯示
- 「你好」在 REPL 裡一樣落到 kiro fallback，這次輸出**乾淨無 ANSI 碼**——確認第一輪的 `runCmd()` ANSI strip 修法在 REPL 這條路徑也有效（REPL 跟 oneshot 共用同一個 `KiroBackend.Execute()`）
- 🟡 **小發現，沒修**：輸入 `/w` 之後會進入「輸入編號執行第幾個 workflow」的巢狀提示（`Enter number to execute, or press Enter to cancel:`）。如果這時候手滑打了別的指令（例如打算輸入下一個任務或 `exit`），會被這個巢狀提示當成「無效的 workflow 編號」直接報錯（`❌ invalid workflow number: exit`），不會被辨識成「使用者想取消、輸入新指令」再退回正常模式重新處理一次——體驗上是「白白噴一次錯誤」，不是當機，但如果你在台上示範 `/w` 忘記按 Enter 取消就直接打下一句，觀眾會看到一行看起來像 bug 的錯誤訊息。建議：demo 時記得先按 Enter 取消再打下一句；要徹底修的話，`/w` 的編號輸入應該偵測「輸入不是純數字」就直接當取消處理並把這句話送回正常指令流程。

## ✅ Approval Gate + Hook 並存——v0.17.0 headline 安全修正實測通過

這是 v0.17.0 review 時修的最大一個安全性 bug（Kiro 原本寫成 hook 存在會靜默停用高風險指令確認），這次找一個真實的高風險場景驗證：

1. 先測沒有任何 hook 設定時，對一個真實存在的暫存資料夾下 `rm -rf` 指令（非互動環境）——orch 正確跳出 `⚠️ HIGH-RISK COMMAND DETECTED`，非互動環境下自動判定拒絕，資料夾確認毫髮無傷保留下來
2. 加一個完全跟安全無關的 `pre_execute` hook（只是把一行字寫進 log 檔）後重測同一個高風險指令——**hook 正常觸發，而且高風險確認機制依然正常跳出並擋下**，不會像修復前那樣被 hook 的存在靜默關掉。資料夾一樣毫髮無傷。

這個修正如果 10/31 要在台上講「AI agent 的安全性設計」，這是一個可以真實 demo 給觀眾看的橋段：故意示範一個危險指令被攔下來，而且攔截機制不會被自己加的 hook 意外繞過。

## 🔴 發現並直接修正：這台機器部署的 5 個 workflow 全部跟 repo 脫節，中文/多語同義詞觸發詞完全失效

測 `/w` 列出的 5 個 workflow（status/morning/signoff/weekly-report/handoff-victoria）時，先測中文觸發詞「狀態」——**沒有命中 workflow**，直接落到一般規劃流程（雖然因為第一輪的修法 3，還是給出了還算合理的摘要，只是沒有真的跑 workflow 定義的邏輯）。查證後發現：**這台機器 `~/.config/orch/workflows/*.yaml` 是舊版檔案，跟 repo 裡 `workflows/*.yaml` 的內容不同步**——v0.13.0 加的「一個 workflow 可以有多個同義詞觸發詞」功能（`trigger: ["開工","早安","上線","上班"]`），這台機器部署的版本全部還停在舊的單一字串格式（`trigger: "開工"`），也就是「早安」「上線」「上班」「下班」「下線」「晚安」「狀態」「weekly」「叫 Claude Code」「丟給 CC」這些後來加的同義詞在這台機器完全無法觸發對應 workflow，只有最早的那個詞（開工/收工/status/週報/交給 Victoria）還有效。`morning.yaml` 甚至還缺 v0.16.1 加的 `file_context` 功能（自動注入交接檔內容，改用舊版手動指示文字）。

**這是這次測試中最直接跟你 10/31 演講相關的發現**：orch 本身沒有任何機制會提醒使用者「你部署的 workflow 檔案跟 repo 版本不同步」，git commit 只更新 repo 裡的範本，不會自動同步到 `~/.config/orch/workflows/`。如果你在台上示範某個你以為已經支援的同義詞觸發（例如講到一半說「早安」示範 morning workflow），在沒同步過的機器上會安靜失效，觀眾不會看到明顯錯誤，只會覺得「怎麼沒有觸發預期的 workflow」。

**已直接處理**：核對 5 個檔案的 diff，確認差異都是單純的版本落後（多同義詞、file_context），沒有這台機器自己客製化的內容會被覆蓋掉，於是直接把 repo 的 5 個 workflow 檔案複製覆蓋過去，重新測過「狀態」「早安」都能正確命中 workflow 了。**這個修正只影響這台機器的本機部署檔案，不是 code 變更，不需要 commit**——但值得記錄，因為你另一台平常用的筆電，或是 10/31 demo 真正會用的那台機器，很可能也有同樣的落後情況，上台前建議比照這次的做法（`diff -r` 這個 repo 的 `workflows/` 跟 `~/.config/orch/workflows/`）手動確認一次。

## 🟡 發現、記錄但沒修：「狀態」workflow 自己的文字處理邏輯，遇到這個專案真實的交接檔格式會出包

同步好 workflow 後，實際跑一次「狀態」（在 `Cowork/` 根目錄下跑，因為這個 workflow 的 shell 指令用的是相對路徑）：

- `GCP`、`Salesforce` 兩個子專案完全沒印出任何內容（`grep -E "更新時間|狀態|摘要"` 對 `head -20` 抓到的前 20 行沒命中，靜默跳過，沒有錯誤訊息）
- `momo` 印出一行完全文不對題的內容（「這份檔案是所有 Agent...的共享狀態」），只是因為這句話裡剛好有「狀態」兩個字被 grep 命中，不是真的摘要
- `Study` 印出來的不是「摘要」，而是**整個一行、好幾千字的完整交接紀錄原文**——因為這個 repo 的交接檔慣例是把整段任務摘要寫成 markdown 表格裡「一行」（沒有真正的換行），`head -20` 數的是「20 行」，但這一行本身可以長達數千字，等於 `head -20` 完全沒有發揮限制長度的效果

這個 workflow 的 shell script 是寫死的 `grep`/`head`，跟第一輪發現 3（「那讀交接」該摘要不該吐原文）根因類似但是不同的程式碼路徑——這個是 workflow YAML 裡的固定 shell 指令，不是 AI 規劃出來的。**這次沒有動手修**，因為怎麼改牽涉到你想要的呈現方式（例如：改用 `kiro`/`claude` agent 真的讀取摘要，而不是 grep 硬撈；或是把交接檔慣例改成真正的多行格式），比較像是設計決定，不是我能直接判斷的單一修法。如果 10/31 想在台上示範這個 workflow，目前的樣子還不適合直接上台。

## ✅ Cost Tracking——`orch cost` 正常，帳實測看得到真實數字

`orch cost` 印出一個乾淨的表格（backend / model / calls / input tokens / output tokens / cost USD），這台機器上有一筆之前測 bedrock 跟 vertexai 留下的真實記錄，金額算得出來（$0.0020 / $0.0000）。這個指令沒有安全疑慮、輸出乾淨，適合示範。

## 🟡 附帶觀察：kiro-cli 每次呼叫後會留下孤兒背景程序，沒被清掉

這次測試過程中用 `ps aux` 順手看了一下，發現每次 orch 透過 `KiroBackend.Execute()` 呼叫 `kiro-cli chat --trust-all-tools` 之後，即使那次呼叫本身已經正常結束回傳答案，`kiro-cli` 自己額外開的 `tui.js`／`acp` 背景程序有時候不會跟著退出，會一直留在 `ps aux` 裡累積。粗略數過這次測試累積下來已經有 10 隻左右的孤兒程序。這看起來是 `kiro-cli` 自己的程序模型（可能是刻意設計成常駐加速下次啟動），不是 orch 這邊 `exec.Command`/`runCmd()` 的問題（orch 呼叫的那個進程確實有正常 `Wait()` 返回），但如果 10/31 demo 要連續示範很多次 orch 呼叫 kiro，機器上可能會慢慢堆一堆背景程序，值得知道這件事、示範前後可以留意一下要不要清理（`pkiro-cli` 或直接關機重開）。這次測試累積的孤兒程序已經一併清掉，不留給你。

## 沒測到的（第二輪，誠實列出）

- **`/session claude`／`claude` backend 本體**——同樣因為遞迴呼叫自己的風險，沒有實際 spawn。`/session gemini` 也沒有實際 spawn（改用發現 2 的方式驗證程式碼邏輯，沒測真的互動）。
- **`/pass`**（session 間傳遞輸出）、**reactive event bus**（`eventbus`，`orch --tools` 或 config 裡有提到但這次沒特地去湊觸發條件）——沒測。
- **`orch init` 互動精靈**——沒有實際跑過，因為會覆蓋/詢問這台機器的既有設定，這次選擇不動。如果你要在 10/31 demo 一台全新機器的初始化流程，建議提前用一台乾淨環境跑過一次 `orch init`，不要臨場才第一次跑。

## 這台機器如果要拿去demo，目前已知會不夠漂亮的地方（彙整，不分先後）

1. `orch briefing` 顯示的是 2026-07-01 的測試假資料，不是真的交接摘要——這台機器的 `~/.config/orch/config.yaml` 沒設定 `memory.briefing_source_file`（v0.16.2 加的功能，需要手動指定才會生效）。上台前建議先設定好並跑一次 `orch briefing gen` 確認顯示真實內容。
2. MLX 完全沒裝（`~/mlx-env/bin/python3` 不存在），所有分類/聊天都走 cloud fallback，跟你平常在其他機器上展示的「本機 7B 模型」體驗不一樣，延遲也明顯更高（chat 大約 7-12 秒）。如果要展示「MLX 本機推論、不用等雲端」這個賣點，這台機器目前展示不出來。
3. ~~「狀態」workflow 目前的輸出品質不適合直接上台~~ **第三輪已修正，見下方。**

---

# 第三輪測試（因 10/31 要「務必順利使用」，把第二輪列出的缺口逐一補測/修正）

第二輪列了 3 個「沒測到」、1 個「記錄但沒修」。這輪把它們全部處理掉，並且對每個修正都用重新編譯的 binary 實測驗證，不是改完就假設沒問題。

## 🔴 發現並修正：`/w` 巢狀提示吃掉使用者下一句話，不只是「噴一次錯誤」那麼輕微

第二輪把這個列為「小發現，沒修」。這輪重新驗證後發現比原本描述的更值得修：使用者在 `Enter number to execute, or press Enter to cancel:` 這個巢狀提示下打的任何非數字內容，不只是印一行 `❌ invalid workflow number`，那句話會被**直接丟棄、不會被執行**——使用者得再打一次。如果台上你示範 `/w` 忘記按 Enter 取消就接著喊出下一句指令，觀眾會看到一行錯誤，而且你剛剛講的那句話完全沒生效，得再講一次。

**修法**（`cmd/orch/repl.go`）：`handleWorkflowMenu` 收到非數字輸入時，判定為「取消」，並把這句話透過新的 `pendingInput` 機制交還給主迴圈，在下一輪當成正常指令重新處理——不管是純文字（會被聊天/規劃邏輯接住）還是 `/xxx` 指令都會被正確執行，不會遺失。

**驗證**（PTY 實測，兩種情況都測過）：
- `/w` → 打 `exit` → 印出「(cancelled — "exit" doesn't look like a workflow number, treating it as your next command)」，接著 REPL 真的執行 `exit` 並印出 `👋 bye`（修正前：這句話會被吃掉，REPL 停在原地等你重打）
- `/w` → 打 `/history` → 同樣先印取消訊息，接著真的執行 `/history` 並印出歷史紀錄清單
- 空白輸入（單純按 Enter）維持原本「(cancelled)」行為不變，沒有動到這個既有路徑

## 🔴 發現並修正：「狀態」workflow 用 shell grep/head 從各子專案交接檔擷取重點，對半數子專案完全失效

第二輪記錄了這個問題但沒修（判斷是設計決定）。這輪決定動手修，理由：這是 10/31 最直接會被拿來 demo 的指令之一（「叫它看一下狀態」是最自然的開場），維持現狀等於直接把一個會出包的功能端上台。

**根因回顧**：`workflows/status.yaml` 原本寫死 `head -20 file | grep -E "更新時間|狀態|摘要" | head -3`，但各子專案交接檔開頭格式差異很大——GCP 表格欄位名稱是「日期」不是「更新時間」（grep 完全落空）、Salesforce 前 20 行是天條規則不是狀態表（同樣落空）、momo/Study 把整段摘要塞進表格單一儲存格、長達數千字的一整「行」，`head -20` 對「限制長度」完全無效（等於整段吐出來）。

**修法**：改成跟第一輪發現 3（「讀文件要摘要不要 cat」）同一種思路，但套用在 workflow 層級——`agent: shell` 改成 `agent: kiro`，prompt 明確要求對 6 個路徑逐一 `test -f` 確認存在、`head -20` 讀取開頭、每個子專案輸出「一行」摘要。

**修的過程中自己抓到一個新 bug，順手修掉**：第一版 prompt 讓 kiro 自由發揮怎麼找「最新」內容，結果它自己想出「grep 搜尋 `2026-07` 這個日期樣式」的策略——這個策略對 AWS 完全誤判：AWS 最後一次更新是 6 月（`2026-06-15`），kiro 搜不到 7 月的日期就直接回報「(無交接檔)」，但那個檔案明明存在、內容也完全正常。這是一個「看起來像沒問題、但答案是錯的」的假陰性，比原本的 grep 失效更隱蔽。修法：prompt 明確要求先用 `test -f` 判斷檔案是否存在，並且明講「不要用日期範圍篩選、不要假設最新內容一定在特定月份」，只有 `test -f` 回報 MISSING 才能寫「(無交接檔)」。

**驗證**：重新編譯後跑了兩次「狀態」（LLM 輸出本質上非 100% 決定性，所以刻意跑兩次看穩定度），兩次六個子專案全部正確列出（含 AWS 正確顯示 6 月的內容，不再誤判成無交接檔），摘要內容也確實反映各專案最新狀態，沒有輸出原文或整段變更紀錄。

## 🟡 發現並修正（安全網，非確認過的實際問題）：`kiro-cli` 孤兒背景程序

第二輪觀察到 kiro-cli 呼叫後偶爾留下背景程序。這輪在 `pkg/backend/backend.go` 的 `runCmd()` 加上防禦性修法：呼叫時用 `Setpgid` 讓子行程獨立成一個 process group，指令結束（不管正常結束或逾時）後主動 kill 整個 group，清掉該次呼叫可能留下的任何子行程。

**誠實記錄**：這台桌機本身背景常駐了 Kiro IDE（Electron 桌面 App）跟 `kiro_cli_desktop --is-startup` 常駐服務，桌面環境本身的程序數在 830-840 之間浮動，雜訊大到没辦法用簡單的「呼叫前後 ps 數量差」乾淨驗證這次修法確實把孤兒程序歸零——沒有刻意誇大驗證結果。但這個修法本身（呼叫結束後清理自己那個 process group）是防禦性正確且低風險的通用做法，`go test -race ./...` 全綠，不影響任何既有行為（approval gate/hooks 走的是 `pkg/executor`/`pkg/hooks` 各自獨立的 `exec.CommandContext`，完全沒有共用這段程式碼，這次修改不會影響它們）。

## ✅ 新驗證：`/session gemini`（第二輪標記「沒測到」）

用 PTY 實際 spawn，確認 v0.17.1 的 `--yolo` 修法在互動 session 這條路徑（不只是 batch `Execute()`）也生效——成功進入 gemini 的全螢幕 TUI，畫面上看得到「YOLO mode (ctrl + y to toggle)」，沒有再噴 `Unknown arguments: skip-trust` 錯誤。Ctrl+C 乾淨退回正常模式。（測試過程中有一次因為我自己的測試腳本按鍵時序沒抓好，看到 gemini 重啟了兩三次、印出「received interrupt, shutting down...」，重新用更乾淨的腳本单獨測 Ctrl+C 沒有重現，判定是我自己測試腳本的問題，不是 orch 的 bug，誠實記錄不要誤植。）

## ✅ 新驗證：`claude` backend（第二輪因遞迴風險沒測）

透過 `orch --backend claude --dry-run` 而非直接呼叫 `claude` CLI 本體，藉助 orch 自己內建的 5 分鐘逾時保護，安全驗證了 claude backend 的 cloud planning 路徑。正常任務提示詞（「列出目前資料夾有哪些檔案」）規劃結果正確、指令合理。附帶測試發現：如果故意給一個要求「原封不動回覆某個字串」的對抗性提示詞，claude 會照字面回覆導致 JSON 解析失敗——但這是我刻意構造的邊界情況（沒有正常使用者會這樣打），不是真實 bug；同時也確認 `pkg/planner` 既有的 JSON 擷取邏輯（```json 圍籬、大括號配對、小模型常見錯誤修正）本來就相當健壯。

## ✅ 新驗證：`/pass`（第二輪沒測）

`/session kiro` 聊一句話 → Ctrl+C 回正常模式 → `/pass kiro gemini`，正確印出「✅ passed 7052 chars from kiro → gemini (now in gemini session)」並自動 spawn+切換到 gemini session，過程無錯誤。

## ✅ 新驗證：Event Bus / reactive workflow chaining（第二輪沒測，而且這次才發現：專案裡完全沒有任何一個 reactive workflow 範例可以拿來測）

`pkg/eventbus` 有完整單元測試、README 架構圖也把它列為主打功能，但 `workflows/` 目錄裡實際的 5 個範例全部是普通 DAG workflow，沒有一個標 `mode: "reactive"`——也就是說在今天之前，這個功能從來沒有被端到端驗證過，也沒有任何可以照抄的範例設定。

在獨立的 scratch 目錄（不影響任何正式設定）手寫一個最小 reactive workflow（`on: step.done` + `agent: shell` 過濾 + 串到 kiro），用 `ORCH_CONFIG` 指向這個隔離設定跑一個真實 oneshot 任務，結果：`🔗 reactive rules: 1 loaded` → shell 步驟完成後 `🔗 trigger: shell_to_kiro_note → kiro` 自動觸發 → 串接呼叫成功，答案正是我在規則裡指定的驗證字串。**這證實了機制本身是真的能動的**，不是空殼功能。

**沒有處理的部分**：我沒有把這個範例加進正式 `workflows/` 目錄或部署到這台機器——因為 Event Bus 的串接是「符合規則就自動執行下一個 agent 呼叫」，沒有像高風險指令那樣的確認機制，如果隨手加一條寬鬆的規則進正式設定，可能會在 demo 其他段落無預警觸發，不是我該自己決定的取捨。如果你想在 10/31 拿這個當亮點橋段，我可以幫你設計一個範圍narrow、上台前刻意排練過的範例；但這次只做到「證明機制本身沒壞」為止。

## ✅ 新驗證 + 順手修正：`orch init`（第二輪因怕覆蓋這台機器設定沒測）

用 `HOME` 環境變數指向隔離的 scratch 目錄（`orch init` 寫死用 `os.UserHomeDir()`，不吃 `ORCH_CONFIG`，所以只能用這個方式隔離，不會動到這台機器真正的 `~/.config/orch/config.yaml`），完整跑過一次互動精靈（選 primary backend、語言、姓名、MLX 模型、workspace、briefing source），設定檔正確寫入隔離路徑。

**過程中發現並修正一個真實的預設值過期問題**：精靈詢問 MLX 模型時預設建議的是 `mlx-community/Qwen2.5-1.5B-Instruct-4bit`，但團隊在 v0.16.4 已經把預設模型正式改成 7B、repo 裡實際出貨的 `config.yaml` 範本也是 7B——`orch init` 這條路徑沒跟著更新，代表在全新機器上跑 `orch init` 會被導向一個已經過期、團隊已經捨棄的小模型選擇。已修正 `cmd/orch/init.go` 三處（提示文字、實際預設值、MLX 停用時的註解範例）與 `README.md` 對應的設定文件段落（原本示範用 3B 當「預設」、1.5B 當「更小」的選項，已改成 7B 為預設、3B/1.5B 皆列為更小的替代選擇），跟 repo 現行 `config.yaml` 一致。

**另外發現、沒有修的相關缺口**：`orch init` 只會建立空的 `~/.config/orch/workflows/` 目錄，不會把 repo 裡的 5 個範例 workflow 複製進去——這其實是第二輪「這台機器 workflow 跟 repo 脫節」問題的根本原因之一：全新機器跑完 `orch init` 之後，`/w` 會直接顯示「no workflows available」，除非使用者自己手動把 repo 的 `workflows/*.yaml` 複製過去。這算是 onboarding 流程的設計缺口（要不要讓 `orch init` 自動複製範本，是一個產品決定），這次沒有動手改，只記錄下來。

## 這次沒有處理、但明確建議你上台前自己確認的事

1. **這台桌機的 `~/.config/orch/config.yaml` 本身也跟 repo 範本脫節**（例如還在用 `qwen-3b` 而非 7B、沒設定 `briefing_source_file`、註解裡還提到已經失效的 `--skip-trust`），我沒有動它——因為不確定這台桌機是不是 10/31 真正會用的那台機器，怕改錯方向。**強烈建議**：不管是這台還是你平常那台筆電，上台前都比照這次修 workflow 的方法，`diff` 一次 repo 的 `config.yaml` 範本跟實際部署的 `~/.config/orch/config.yaml`，確認關鍵設定（backend、模型、bedrock/vertexai 開關）是你要的。
2. **MLX 未裝的機器上，聊天訊息預設會等 7 秒以上才 fallback**，而且沒有開機時的一次性警告（只有每則訊息各自的錯誤行）。如果 demo 機器有裝 MLX，這點不影響你；如果沒裝，建議上台前用實際會用的機器確認過一次真實延遲，不要臨場才發現。
3. **AWS S3 相關指令生成品質**（第二輪「順手記的小問題」提到的 `s3://` 缺 bucket 名稱）這輪沒有再深入，維持原判斷：低優先度，不建議在剩下的時間投入。

## 本輪修改的檔案

- `cmd/orch/repl.go`：`/w` 巢狀提示改用 `pendingInput` 機制，非數字輸入視為取消並交還主迴圈重新處理
- `workflows/status.yaml`：`agent: shell`（grep/head）→ `agent: kiro`（讀取+摘要，含避免日期範圍誤判的明確規則）
- `pkg/backend/backend.go`：`runCmd()` 加上 `Setpgid` + 呼叫結束後清理整個 process group
- `cmd/orch/init.go`：MLX 模型預設值 1.5B → 7B（三處）
- `README.md`：MLX Model 設定範例同步更新為 7B 預設 + 3B/1.5B 替代選項

`go build`/`go vet`/`go test -race ./...` 全綠。所有修正都用重新編譯的 binary 實測驗證，不是只看 code 就假設沒問題。**目前還沒 commit**，等你確認這份報告沒問題後再走 commit/tag/push 流程。
