// Package executor 提供並行 DAG 執行引擎。
//
// 步驟依照 DependsOn 欄位形成有向無環圖（DAG），
// 無依賴的步驟會立即以 goroutine 並行啟動，
// 有依賴的步驟則等待所有上游步驟成功完成後再開始。
package executor

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gordonwei/orch/pkg/config"
	"github.com/gordonwei/orch/pkg/planner"
)

// StepResult 記錄單一步驟的執行結果。
type StepResult struct {
	StepID      string
	Description string
	Agent       string
	Output      string
	Err         error
	Took        time.Duration
	Verified    bool
	Files       map[string]string // path → description (files produced by this step)
	KV          map[string]string // structured key-value data for downstream steps
}

// Result 記錄整個 Plan 的執行結果。
type Result struct {
	Steps       []StepResult
	Success     bool
	Took        time.Duration
	Err         error
	RePlanCount int
}

// ===== 步驟生命週期事件（EventChan） =====

// EventType 定義步驟生命週期事件的類別。
type EventType int

const (
	// EventStepStart 步驟開始執行。
	EventStepStart EventType = iota
	// EventStepDone 步驟執行完成（成功）。
	EventStepDone
	// EventStepFailed 步驟執行失敗。
	EventStepFailed
	// EventStepSkipped 因上游失敗且策略為 skip，步驟被跳過但下游可繼續。
	EventStepSkipped
	// EventStepCancelled 因上游失敗且策略為 abort，步驟被取消。
	EventStepCancelled
)

// StepEvent 是透過 EventChan 發送的步驟生命週期事件。
type StepEvent struct {
	Type   EventType
	StepID string
	Result *StepResult // EventStepDone / EventStepFailed 時填入
	Err    error       // EventStepCancelled 時的取消原因
}

// ===== Executor =====

// Executor 是並行 DAG 執行引擎。
type Executor struct {
	timeout    time.Duration
	maxRetries int
	maxRePlans int
	cfg        *config.Config
	rePlanFunc func(failedContext string) error // callback to trigger re-plan

	// EventChan 若設定（非 nil），執行過程中會發送步驟生命週期事件（start/done/failed）。
	// 使用者需在呼叫 Execute() 前建立 channel，Execute() 完成後會關閉它。
	EventChan chan StepEvent

	// OutputEvents 若設定（非 nil），shell 指令會逐行串流 stdout，
	// AI agent 呼叫則定期發送 progress 事件。
	// 此 channel 不由 Executor 關閉——由呼叫端負責管理生命週期。
	OutputEvents chan<- OutputEvent
}

// New 建立新的 Executor 實例。
func New(cfg *config.Config) *Executor {
	return &Executor{
		timeout:    10 * time.Minute,
		maxRetries: 3,
		maxRePlans: 2,
		cfg:        cfg,
	}
}

// SetRePlanFunc 設定步驟失敗後觸發重新規劃的回呼函式。
// 當步驟的 on_failure=re-plan 時，此函式會被呼叫並傳入失敗上下文。
func (e *Executor) SetRePlanFunc(fn func(failedContext string) error) {
	e.rePlanFunc = fn
}

// emit 安全地發送步驟生命週期事件到 EventChan（若已設定）。
func (e *Executor) emit(ev StepEvent) {
	if e.EventChan != nil {
		e.EventChan <- ev
	}
}

// ===== DAG 建構與驗證 =====

// dagNode 代表 DAG 中的一個節點，包含步驟本身與依賴關係。
type dagNode struct {
	step        planner.Step
	upstreams   []string // 此步驟依賴的上游步驟 ID
	downstreams []string // 依賴此步驟的下游步驟 ID
}

// buildDAG 從步驟清單建構 DAG 圖並驗證。
func buildDAG(steps []planner.Step) (map[string]*dagNode, error) {
	nodes := make(map[string]*dagNode, len(steps))

	// 建立所有節點
	for _, step := range steps {
		if _, exists := nodes[step.ID]; exists {
			return nil, fmt.Errorf("重複的步驟 ID: %q", step.ID)
		}
		nodes[step.ID] = &dagNode{
			step:      step,
			upstreams: step.DependsOn,
		}
	}

	// 驗證依賴是否存在，並建立 downstream 指標
	for id, node := range nodes {
		for _, dep := range node.upstreams {
			upstream, exists := nodes[dep]
			if !exists {
				return nil, fmt.Errorf("步驟 %q 依賴不存在的步驟 %q", id, dep)
			}
			upstream.downstreams = append(upstream.downstreams, id)
		}
	}

	// 循環偵測（DFS）
	if err := detectCycle(nodes); err != nil {
		return nil, err
	}

	return nodes, nil
}

// detectCycle 使用 DFS 三色標記法偵測 DAG 中的循環依賴。
func detectCycle(nodes map[string]*dagNode) error {
	const (
		white = 0 // 未造訪
		gray  = 1 // 造訪中（在當前 DFS 路徑上）
		black = 2 // 已完成
	)

	color := make(map[string]int, len(nodes))
	for id := range nodes {
		color[id] = white
	}

	var dfs func(id string) error
	dfs = func(id string) error {
		color[id] = gray
		for _, downstream := range nodes[id].downstreams {
			switch color[downstream] {
			case gray:
				return fmt.Errorf("偵測到循環依賴：%s → %s", id, downstream)
			case white:
				if err := dfs(downstream); err != nil {
					return err
				}
			}
		}
		color[id] = black
		return nil
	}

	for id := range nodes {
		if color[id] == white {
			if err := dfs(id); err != nil {
				return err
			}
		}
	}
	return nil
}

// ===== 並行 DAG 執行引擎 =====

// Execute 執行計畫中的所有步驟，以 DAG 並行方式排程。
//
// 無依賴的步驟會立即並行啟動；有依賴的步驟會等到所有上游成功後才開始。
// 若 EventChan 已設定，會在開始前發送事件，執行完畢後關閉 channel。
func (e *Executor) Execute(plan *planner.Plan) Result {
	start := time.Now()

	// 若有 EventChan，結束後關閉
	if e.EventChan != nil {
		defer close(e.EventChan)
	}

	// 建構 DAG
	nodes, err := buildDAG(plan.Steps)
	if err != nil {
		return Result{
			Steps:   nil,
			Success: false,
			Took:    time.Since(start),
			Err:     err,
		}
	}

	// 執行環境
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var (
		mu         sync.Mutex
		results    = make(map[string]*StepResult)
		completed  = make(map[string]chan struct{}) // 每個步驟完成時關閉
		wg         sync.WaitGroup
		globalErr  error
		rePlanFlag bool
	)

	// 為每個步驟建立完成信號 channel
	for id := range nodes {
		completed[id] = make(chan struct{})
	}

	// buildContext 收集所有上游步驟的輸出，組成此步驟的先驗上下文。
	buildContext := func(deps []string) string {
		mu.Lock()
		defer mu.Unlock()

		var buf strings.Builder
		for _, dep := range deps {
			sr := results[dep]
			if sr == nil || sr.Err != nil {
				continue
			}
			if sr.Output != "" {
				buf.WriteString(fmt.Sprintf("\n--- Output from %s ---\n%s\n", dep, truncate(sr.Output, 4000)))
			}
			if len(sr.KV) > 0 {
				buf.WriteString(fmt.Sprintf("--- KV from %s ---\n", dep))
				for k, v := range sr.KV {
					buf.WriteString(fmt.Sprintf("  %s=%s\n", k, v))
				}
			}
			if len(sr.Files) > 0 {
				buf.WriteString(fmt.Sprintf("--- Files from %s ---\n", dep))
				for path, desc := range sr.Files {
					buf.WriteString(fmt.Sprintf("  %s: %s\n", path, desc))
				}
			}
		}
		return buf.String()
	}

	// 啟動每個步驟的 goroutine
	for _, node := range nodes {
		wg.Add(1)
		go func(n *dagNode) {
			defer wg.Done()
			defer close(completed[n.step.ID])

			// 等待所有上游步驟完成
			for _, dep := range n.upstreams {
				select {
				case <-completed[dep]:
					// 上游已完成
				case <-ctx.Done():
					// 全局取消
					e.emit(StepEvent{Type: EventStepCancelled, StepID: n.step.ID, Err: ctx.Err()})
					mu.Lock()
					results[n.step.ID] = &StepResult{
						StepID:      n.step.ID,
						Description: n.step.Description,
						Agent:       n.step.Agent,
						Err:         fmt.Errorf("cancelled: %w", ctx.Err()),
					}
					mu.Unlock()
					return
				}
			}

			// 檢查全局 context 是否已取消
			select {
			case <-ctx.Done():
				e.emit(StepEvent{Type: EventStepCancelled, StepID: n.step.ID, Err: ctx.Err()})
				mu.Lock()
				results[n.step.ID] = &StepResult{
					StepID:      n.step.ID,
					Description: n.step.Description,
					Agent:       n.step.Agent,
					Err:         fmt.Errorf("cancelled: %w", ctx.Err()),
				}
				mu.Unlock()
				return
			default:
			}

			// 檢查上游是否有失敗（且策略非 skip）
			mu.Lock()
			upstreamFailed := false
			for _, dep := range n.upstreams {
				sr := results[dep]
				if sr != nil && sr.Err != nil {
					// 查看上游步驟的 on_failure 策略
					upNode := nodes[dep]
					if upNode != nil && upNode.step.OnFailure != "skip" {
						upstreamFailed = true
						break
					}
				}
			}
			mu.Unlock()

			if upstreamFailed {
				e.emit(StepEvent{Type: EventStepCancelled, StepID: n.step.ID, Err: fmt.Errorf("upstream failed")})
				mu.Lock()
				results[n.step.ID] = &StepResult{
					StepID:      n.step.ID,
					Description: n.step.Description,
					Agent:       n.step.Agent,
					Err:         fmt.Errorf("cancelled due to upstream failure"),
				}
				mu.Unlock()
				return
			}

			// 組建此步驟的 context（來自所有上游步驟的輸出）
			priorContext := buildContext(n.upstreams)

			// 發送開始事件
			fmt.Fprintf(os.Stderr, "\n📋 [%s] %s\n", n.step.ID, n.step.Description)
			fmt.Fprintf(os.Stderr, "   agent: %s\n", n.step.Agent)
			e.emit(StepEvent{Type: EventStepStart, StepID: n.step.ID})

			// 執行步驟（含重試邏輯）
			sr := e.executeStep(n.step, priorContext)

			// 儲存結果
			mu.Lock()
			results[n.step.ID] = &sr
			mu.Unlock()

			if sr.Err != nil {
				fmt.Fprintf(os.Stderr, "   ❌ failed: %v\n", sr.Err)
				e.emit(StepEvent{Type: EventStepFailed, StepID: n.step.ID, Result: &sr})

				// 根據 on_failure 策略處理
				switch n.step.OnFailure {
				case "skip":
					fmt.Fprintf(os.Stderr, "   ⏭️  skipping (on_failure=skip)\n")
					e.emit(StepEvent{Type: EventStepSkipped, StepID: n.step.ID, Result: &sr})
					return

				case "re-plan":
					fmt.Fprintf(os.Stderr, "   🔁 requesting re-plan...\n")
					mu.Lock()
					rePlanFlag = true
					if e.rePlanFunc != nil {
						failCtx := fmt.Sprintf("Step [%s] failed: %v\nPrior context:\n%s", n.step.ID, sr.Err, priorContext)
						_ = e.rePlanFunc(failCtx)
					}
					mu.Unlock()
					cancel()
					return

				case "abort":
					fmt.Fprintf(os.Stderr, "   🛑 aborting all (on_failure=abort)\n")
					mu.Lock()
					globalErr = fmt.Errorf("aborted at step %s: %w", n.step.ID, sr.Err)
					mu.Unlock()
					cancel()
					return

				default: // "retry" 已在 executeStep 中處理完畢，仍然失敗則 abort
					fmt.Fprintf(os.Stderr, "   🛑 all retries exhausted, aborting\n")
					mu.Lock()
					globalErr = fmt.Errorf("step %s failed after retries: %w", n.step.ID, sr.Err)
					mu.Unlock()
					cancel()
					return
				}
			}

			fmt.Fprintf(os.Stderr, "   ✅ done (%s)\n", sr.Took.Round(100*time.Millisecond))
			e.emit(StepEvent{Type: EventStepDone, StepID: n.step.ID, Result: &sr})
		}(node)
	}

	// 等待所有 goroutine 完成
	wg.Wait()

	// 收集結果（按原始步驟順序）
	mu.Lock()
	var orderedResults []StepResult
	allSuccess := true
	for _, step := range plan.Steps {
		if sr, ok := results[step.ID]; ok {
			orderedResults = append(orderedResults, *sr)
			if sr.Err != nil {
				allSuccess = false
			}
		}
	}
	mu.Unlock()

	if rePlanFlag {
		return Result{
			Steps:       orderedResults,
			Success:     false,
			Took:        time.Since(start),
			Err:         fmt.Errorf("re-plan triggered"),
			RePlanCount: 1,
		}
	}

	if globalErr != nil {
		return Result{
			Steps:   orderedResults,
			Success: false,
			Took:    time.Since(start),
			Err:     globalErr,
		}
	}

	return Result{
		Steps:   orderedResults,
		Success: allSuccess,
		Took:    time.Since(start),
	}
}

// ===== 單步驟執行（含重試與驗證）=====

// executeStep 執行單一步驟，包含重試邏輯。
func (e *Executor) executeStep(step planner.Step, priorContext string) StepResult {
	start := time.Now()
	var output string
	var err error

	for attempt := 0; attempt <= e.maxRetries; attempt++ {
		if attempt > 0 {
			fmt.Fprintf(os.Stderr, "   🔄 retry %d/%d\n", attempt, e.maxRetries)
		}

		output, err = e.runStep(step, priorContext)
		if err != nil {
			continue
		}

		// 驗證指令
		if step.VerifyCmd != "" {
			if verifyErr := e.verify(step.VerifyCmd); verifyErr != nil {
				err = fmt.Errorf("verification failed: %w", verifyErr)
				continue
			}
		}

		// 成功
		return StepResult{
			StepID:      step.ID,
			Description: step.Description,
			Agent:       step.Agent,
			Output:      output,
			Took:        time.Since(start),
			Verified:    step.VerifyCmd != "",
			Files:       parseFiles(output),
			KV:          parseKV(output),
		}
	}

	return StepResult{
		StepID:      step.ID,
		Description: step.Description,
		Agent:       step.Agent,
		Output:      output,
		Err:         err,
		Took:        time.Since(start),
	}
}

// runStep 執行步驟的具體指令。
// 若 OutputEvents 已設定：
//   - shell 指令：使用 StdoutPipe 逐行串流 stdout
//   - AI agent（kiro/claude/gemini）：定期發送 progress 事件
//
// 若 OutputEvents 為 nil：行為完全向後相容（全部緩衝）。
func (e *Executor) runStep(step planner.Step, priorContext string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), e.timeout)
	defer cancel()

	var cmd *exec.Cmd

	switch step.Agent {
	case "kiro":
		prompt := step.Prompt
		if prompt == "" {
			prompt = step.Description
		}
		if priorContext != "" {
			prompt = fmt.Sprintf("Context from prior steps:\n%s\n\nTask: %s", priorContext, prompt)
		}
		cmd = exec.CommandContext(ctx, "kiro-cli", "chat", "--trust-all-tools", prompt)

	case "claude":
		prompt := step.Prompt
		if prompt == "" {
			prompt = step.Description
		}
		if priorContext != "" {
			prompt = fmt.Sprintf("Context from prior steps:\n%s\n\nTask: %s", priorContext, prompt)
		}
		cmd = exec.CommandContext(ctx, "claude", "-p", prompt)

	case "gemini":
		prompt := step.Prompt
		if prompt == "" {
			prompt = step.Description
		}
		cmd = exec.CommandContext(ctx, "gemini", "-p", prompt)

	case "shell":
		if step.Command == "" {
			return "", fmt.Errorf("shell step has no command")
		}
		cmd = exec.CommandContext(ctx, "bash", "-c", step.Command)

	default:
		// 直接當 shell command 跑（terraform, kubectl, helm, aws, gcloud）
		if step.Command != "" {
			cmd = exec.CommandContext(ctx, "bash", "-c", step.Command)
		} else {
			return "", fmt.Errorf("agent %q has no command or prompt", step.Agent)
		}
	}

	cmd.Dir = e.findWorkDir(step)

	// 根據 agent 類型與 OutputEvents 是否啟用，選擇執行策略
	isShellLike := step.Agent == "shell" || step.Command != ""

	if e.OutputEvents != nil && isShellLike {
		// Shell 指令：使用 StdoutPipe 逐行串流
		return e.runShellStreaming(cmd, step)
	}

	if e.OutputEvents != nil && !isShellLike {
		// AI agent 呼叫：定期發送 progress 事件
		return e.runWithProgress(cmd, step)
	}

	// 無串流（向後相容）：緩衝全部輸出
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("%s failed: %w\nstderr: %s", step.Agent, err, stderr.String())
	}

	return stdout.String(), nil
}

// runShellStreaming 使用 StdoutPipe 逐行串流 shell 指令輸出，
// 同時保留完整 output 供後續步驟 context chain 使用。
func (e *Executor) runShellStreaming(cmd *exec.Cmd, step planner.Step) (string, error) {
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	// 取得 stdout pipe
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		// fallback：回退到 buffered 模式
		var stdout bytes.Buffer
		cmd.Stdout = &stdout
		if runErr := cmd.Run(); runErr != nil {
			return stdout.String(), fmt.Errorf("%s failed: %w\nstderr: %s", step.Agent, runErr, stderr.String())
		}
		return stdout.String(), nil
	}

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("%s failed to start: %w", step.Agent, err)
	}

	// 逐行讀取 stdout 並發送 OutputEvent，同時收集完整輸出
	var stdout bytes.Buffer
	streamErr := StreamReader(pipe, &stdout, e.OutputEvents, step.ID)

	// 等待指令結束
	cmdErr := cmd.Wait()

	if streamErr != nil {
		return stdout.String(), fmt.Errorf("stream read error: %w", streamErr)
	}
	if cmdErr != nil {
		return stdout.String(), fmt.Errorf("%s failed: %w\nstderr: %s", step.Agent, cmdErr, stderr.String())
	}

	return stdout.String(), nil
}

// runWithProgress 用於 AI agent 呼叫（kiro/claude/gemini）。
// 因為無法即時串流它們的輸出，改為定期發送 progress 事件表示仍在執行。
func (e *Executor) runWithProgress(cmd *exec.Cmd, step planner.Step) (string, error) {
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("%s failed to start: %w", step.Agent, err)
	}

	// 背景 goroutine 定期發送 progress 事件
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		elapsed := 0
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				elapsed += 5
				EmitProgress(e.OutputEvents, step.ID,
					fmt.Sprintf("%s 執行中... (%ds)", step.Agent, elapsed))
			}
		}
	}()

	err := cmd.Wait()
	close(done)

	if err != nil {
		return stdout.String(), fmt.Errorf("%s failed: %w\nstderr: %s", step.Agent, err, stderr.String())
	}

	return stdout.String(), nil
}

// verify 執行步驟驗證指令。
func (e *Executor) verify(verifyCmd string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bash", "-c", verifyCmd)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("verify cmd failed: %w\n%s", err, stderr.String())
	}
	return nil
}

// ===== 輔助函式 =====

// findWorkDir 根據步驟的描述、指令等內容判斷工作目錄。
func (e *Executor) findWorkDir(step planner.Step) string {
	text := strings.ToLower(step.Description + " " + step.Prompt + " " + step.Command)

	base := e.cfg.Workspace.Root
	if base == "" {
		base = "."
	}

	for _, sd := range e.cfg.Workspace.Subdirs {
		for _, kw := range sd.Keywords {
			if strings.Contains(text, strings.ToLower(kw)) {
				fullPath := filepath.Join(base, sd.Name)
				if info, err := os.Stat(fullPath); err == nil && info.IsDir() {
					return fullPath
				}
			}
		}
	}

	return ""
}

// truncate 截斷過長的字串。
func truncate(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max] + "\n... (truncated)"
}

// parseFiles 從步驟輸出中擷取檔案宣告。
// 格式：以 "FILE:" 開頭的行，內容為 "path|description"。
func parseFiles(output string) map[string]string {
	files := make(map[string]string)
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "FILE:") {
			parts := strings.SplitN(strings.TrimPrefix(line, "FILE:"), "|", 2)
			if len(parts) == 2 {
				files[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
			} else if len(parts) == 1 {
				files[strings.TrimSpace(parts[0])] = ""
			}
		}
	}
	if len(files) == 0 {
		return nil
	}
	return files
}

// parseKV 從步驟輸出中擷取鍵值對。
// 格式：以 "KV:" 開頭的行，內容為 "key=value"。
func parseKV(output string) map[string]string {
	kv := make(map[string]string)
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "KV:") {
			parts := strings.SplitN(strings.TrimPrefix(line, "KV:"), "=", 2)
			if len(parts) == 2 {
				kv[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
			}
		}
	}
	if len(kv) == 0 {
		return nil
	}
	return kv
}
