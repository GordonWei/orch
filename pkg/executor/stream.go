package executor

import (
	"bufio"
	"io"
	"sync"
	"time"
)

// ===== 輸出串流事件（補充現有的 EventChan 系統）=====
//
// 現有的 StepEvent / EventChan 用於步驟層級的生命週期通知（start/done/failed）。
// OutputEvent 則用於步驟「執行期間」的即時輸出——讓 CLI 能逐行顯示 shell 指令的 stdout。

// OutputEventType 定義輸出串流事件的種類
type OutputEventType string

const (
	OutputLine     OutputEventType = "output"   // shell 指令逐行輸出
	OutputProgress OutputEventType = "progress" // AI agent 定期回報進度（無法即時串流）
)

// OutputEvent 代表執行過程中產生的即時輸出事件
type OutputEvent struct {
	Type      OutputEventType // 事件類型
	StepID    string          // 所屬步驟 ID（平行執行時用來區分來源）
	Message   string          // 事件訊息內容（一行輸出或進度描述）
	Timestamp time.Time       // 事件發生時間
}

// ===== StreamWriter：包裝 io.Writer，逐行發送 OutputEvent =====

// StreamWriter 同時寫入底層 Writer（緩衝完整輸出）並逐行發送事件到 channel。
// 設計為 goroutine-safe：內部使用 mutex 保護共享狀態。
type StreamWriter struct {
	mu      sync.Mutex
	inner   io.Writer         // 底層 writer（通常是 bytes.Buffer，收集完整輸出）
	events  chan<- OutputEvent // 輸出事件 channel（nil 則不發送）
	stepID  string
	lineBuf []byte // 行緩衝區，累積到換行才發送
}

// NewStreamWriter 建立 StreamWriter。
// inner: 底層 writer（完整輸出仍會寫入）
// events: 輸出事件 channel（nil 則僅寫入 inner，不發送事件）
// stepID: 用來標記事件來源步驟
func NewStreamWriter(inner io.Writer, events chan<- OutputEvent, stepID string) *StreamWriter {
	return &StreamWriter{
		inner:  inner,
		events: events,
		stepID: stepID,
	}
}

// Write 實作 io.Writer 介面。
// 每收到資料就寫入 inner，並逐行切割發送 OutputLine 事件。
func (sw *StreamWriter) Write(p []byte) (n int, err error) {
	sw.mu.Lock()
	defer sw.mu.Unlock()

	// 先寫入底層 writer（保證完整輸出不遺失）
	n, err = sw.inner.Write(p)
	if err != nil {
		return n, err
	}

	// 若沒有事件 channel，不需要逐行處理
	if sw.events == nil {
		return n, nil
	}

	// 逐 byte 累積行緩衝區，遇到換行就發送
	for _, b := range p[:n] {
		if b == '\n' {
			sw.emitLine(string(sw.lineBuf))
			sw.lineBuf = sw.lineBuf[:0]
		} else {
			sw.lineBuf = append(sw.lineBuf, b)
		}
	}

	return n, nil
}

// Flush 將行緩衝區中殘餘的不完整行強制發送（步驟結束時呼叫）。
func (sw *StreamWriter) Flush() {
	sw.mu.Lock()
	defer sw.mu.Unlock()

	if len(sw.lineBuf) > 0 && sw.events != nil {
		sw.emitLine(string(sw.lineBuf))
		sw.lineBuf = sw.lineBuf[:0]
	}
}

// emitLine 發送一行輸出事件（呼叫時 mutex 已持有）。
func (sw *StreamWriter) emitLine(line string) {
	sw.events <- OutputEvent{
		Type:      OutputLine,
		StepID:    sw.stepID,
		Message:   line,
		Timestamp: time.Now(),
	}
}

// ===== StreamReader：從 io.Reader 逐行串流 =====

// StreamReader 從 reader 逐行讀取，每行同時寫入 writer 並發送事件。
// 常用於 cmd.StdoutPipe() 的輸出串流。
// 此函式會阻塞直到 reader EOF 或 error。
func StreamReader(reader io.Reader, writer io.Writer, events chan<- OutputEvent, stepID string) error {
	scanner := bufio.NewScanner(reader)
	// 加大 buffer 以處理長行（預設 64KB，上限 1MB）
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()

		// 寫入 writer（含換行）
		if _, err := writer.Write([]byte(line + "\n")); err != nil {
			return err
		}

		// 發送事件
		if events != nil {
			events <- OutputEvent{
				Type:      OutputLine,
				StepID:    stepID,
				Message:   line,
				Timestamp: time.Now(),
			}
		}
	}

	return scanner.Err()
}

// ===== EmitProgress：發送進度事件的快捷函式 =====

// EmitProgress 安全地向 channel 發送進度事件（若 channel 為 nil 則不操作）。
func EmitProgress(events chan<- OutputEvent, stepID, message string) {
	if events == nil {
		return
	}
	events <- OutputEvent{
		Type:      OutputProgress,
		StepID:    stepID,
		Message:   message,
		Timestamp: time.Now(),
	}
}
