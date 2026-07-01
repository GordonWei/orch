package executor

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestStreamWriter_BasicWrite 測試基本寫入：inner writer 收到完整資料，events 收到逐行事件
func TestStreamWriter_BasicWrite(t *testing.T) {
	var buf bytes.Buffer
	events := make(chan OutputEvent, 100)

	sw := NewStreamWriter(&buf, events, "step_1")

	input := "line1\nline2\nline3\n"
	n, err := sw.Write([]byte(input))
	if err != nil {
		t.Fatalf("Write error: %v", err)
	}
	if n != len(input) {
		t.Fatalf("expected %d bytes written, got %d", len(input), n)
	}

	// 驗證 inner writer 收到完整資料
	if buf.String() != input {
		t.Fatalf("inner buffer mismatch: got %q, want %q", buf.String(), input)
	}

	// 驗證收到 3 個 OutputLine 事件
	close(events)
	var received []OutputEvent
	for ev := range events {
		received = append(received, ev)
	}

	if len(received) != 3 {
		t.Fatalf("expected 3 events, got %d", len(received))
	}

	expectedLines := []string{"line1", "line2", "line3"}
	for i, ev := range received {
		if ev.Type != OutputLine {
			t.Errorf("event[%d] type: got %q, want %q", i, ev.Type, OutputLine)
		}
		if ev.StepID != "step_1" {
			t.Errorf("event[%d] stepID: got %q, want %q", i, ev.StepID, "step_1")
		}
		if ev.Message != expectedLines[i] {
			t.Errorf("event[%d] message: got %q, want %q", i, ev.Message, expectedLines[i])
		}
		if ev.Timestamp.IsZero() {
			t.Errorf("event[%d] timestamp is zero", i)
		}
	}
}

// TestStreamWriter_PartialLine 測試不完整行：Flush 時才發送最後一段
func TestStreamWriter_PartialLine(t *testing.T) {
	var buf bytes.Buffer
	events := make(chan OutputEvent, 100)

	sw := NewStreamWriter(&buf, events, "step_2")

	// 寫入沒有換行的資料
	sw.Write([]byte("partial"))

	// 此時不應該有事件
	select {
	case ev := <-events:
		t.Fatalf("unexpected event before flush: %+v", ev)
	default:
		// 正確：沒有事件
	}

	// 繼續寫入含換行的資料
	sw.Write([]byte(" data\nline2"))

	// 應該收到一個事件（"partial data"）
	ev := <-events
	if ev.Message != "partial data" {
		t.Fatalf("expected 'partial data', got %q", ev.Message)
	}

	// "line2" 還在 buffer 中
	sw.Flush()

	ev = <-events
	if ev.Message != "line2" {
		t.Fatalf("expected 'line2' after flush, got %q", ev.Message)
	}

	// 驗證 inner buffer 完整
	if buf.String() != "partial data\nline2" {
		t.Fatalf("inner buffer: got %q", buf.String())
	}
}

// TestStreamWriter_NilEvents 測試 events 為 nil 時不 panic，僅寫入 inner
func TestStreamWriter_NilEvents(t *testing.T) {
	var buf bytes.Buffer
	sw := NewStreamWriter(&buf, nil, "step_3")

	input := "hello\nworld\n"
	n, err := sw.Write([]byte(input))
	if err != nil {
		t.Fatalf("Write error: %v", err)
	}
	if n != len(input) {
		t.Fatalf("expected %d bytes, got %d", len(input), n)
	}
	if buf.String() != input {
		t.Fatalf("inner buffer mismatch")
	}

	// Flush 也不應該 panic
	sw.Flush()
}

// TestStreamWriter_ConcurrentWrite 測試多個 goroutine 同時寫入的安全性
func TestStreamWriter_ConcurrentWrite(t *testing.T) {
	var buf bytes.Buffer
	events := make(chan OutputEvent, 1000)

	sw := NewStreamWriter(&buf, events, "step_concurrent")

	var wg sync.WaitGroup
	numWriters := 10
	linesPerWriter := 50

	for i := 0; i < numWriters; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < linesPerWriter; j++ {
				sw.Write([]byte("data\n"))
			}
		}(i)
	}

	wg.Wait()
	sw.Flush()
	close(events)

	// 驗證收到正確數量的事件
	count := 0
	for range events {
		count++
	}

	expected := numWriters * linesPerWriter
	if count != expected {
		t.Fatalf("expected %d events, got %d", expected, count)
	}
}

// TestStreamReader_Basic 測試 StreamReader 逐行讀取並發送事件
func TestStreamReader_Basic(t *testing.T) {
	input := "line_a\nline_b\nline_c\n"
	reader := strings.NewReader(input)
	var writer bytes.Buffer
	events := make(chan OutputEvent, 100)

	err := StreamReader(reader, &writer, events, "step_r")
	if err != nil {
		t.Fatalf("StreamReader error: %v", err)
	}

	close(events)

	// 驗證 writer 收到完整內容
	if writer.String() != input {
		t.Fatalf("writer mismatch: got %q, want %q", writer.String(), input)
	}

	// 驗證事件
	var received []OutputEvent
	for ev := range events {
		received = append(received, ev)
	}
	if len(received) != 3 {
		t.Fatalf("expected 3 events, got %d", len(received))
	}
	for _, ev := range received {
		if ev.Type != OutputLine {
			t.Errorf("expected OutputLine, got %q", ev.Type)
		}
		if ev.StepID != "step_r" {
			t.Errorf("expected step_r, got %q", ev.StepID)
		}
	}
}

// TestStreamReader_NilEvents 測試 StreamReader 在 events 為 nil 時仍正常寫入 writer
func TestStreamReader_NilEvents(t *testing.T) {
	input := "hello\nworld\n"
	reader := strings.NewReader(input)
	var writer bytes.Buffer

	err := StreamReader(reader, &writer, nil, "step_nil")
	if err != nil {
		t.Fatalf("StreamReader error: %v", err)
	}

	if writer.String() != input {
		t.Fatalf("writer mismatch: got %q, want %q", writer.String(), input)
	}
}

// TestEmitProgress_Nil 測試 EmitProgress 在 nil channel 時不 panic
func TestEmitProgress_Nil(t *testing.T) {
	// 不應該 panic
	EmitProgress(nil, "step_x", "hello")
}

// TestEmitProgress_Send 測試 EmitProgress 正常發送
func TestEmitProgress_Send(t *testing.T) {
	events := make(chan OutputEvent, 10)
	EmitProgress(events, "step_y", "working...")

	select {
	case ev := <-events:
		if ev.Type != OutputProgress {
			t.Errorf("type: got %q, want %q", ev.Type, OutputProgress)
		}
		if ev.StepID != "step_y" {
			t.Errorf("stepID: got %q, want %q", ev.StepID, "step_y")
		}
		if ev.Message != "working..." {
			t.Errorf("message: got %q, want %q", ev.Message, "working...")
		}
		if ev.Timestamp.IsZero() {
			t.Error("timestamp should not be zero")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for event")
	}
}

// TestOutputEventTypes 驗證 OutputEventType 常數值正確
func TestOutputEventTypes(t *testing.T) {
	if string(OutputLine) != "output" {
		t.Errorf("OutputLine: got %q, want 'output'", OutputLine)
	}
	if string(OutputProgress) != "progress" {
		t.Errorf("OutputProgress: got %q, want 'progress'", OutputProgress)
	}
}

// TestStreamWriter_WithOutputEvents 整合測試：模擬 executor 使用 StreamWriter 串流 shell 輸出
func TestStreamWriter_WithOutputEvents(t *testing.T) {
	outputEvents := make(chan OutputEvent, 100)
	var buf bytes.Buffer

	sw := NewStreamWriter(&buf, outputEvents, "shell_step")

	// 模擬 shell 指令逐步輸出
	sw.Write([]byte("compiling...\n"))
	sw.Write([]byte("linking...\n"))
	sw.Write([]byte("done!\n"))
	sw.Flush()

	close(outputEvents)

	var messages []string
	for ev := range outputEvents {
		messages = append(messages, ev.Message)
	}

	expected := []string{"compiling...", "linking...", "done!"}
	if len(messages) != len(expected) {
		t.Fatalf("expected %d messages, got %d: %v", len(expected), len(messages), messages)
	}
	for i, msg := range messages {
		if msg != expected[i] {
			t.Errorf("message[%d]: got %q, want %q", i, msg, expected[i])
		}
	}

	// 確認 buf 有完整輸出
	if buf.String() != "compiling...\nlinking...\ndone!\n" {
		t.Fatalf("buffer mismatch: %q", buf.String())
	}
}
