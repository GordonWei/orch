package executor

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestStreamWriter_BasicWrite tests basic write: inner writer gets full data, events get line-by-line
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

	// Verify inner writer received complete data
	if buf.String() != input {
		t.Fatalf("inner buffer mismatch: got %q, want %q", buf.String(), input)
	}

	// Verify 3 OutputLine events received
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

// TestStreamWriter_PartialLine tests partial line: last segment sent on Flush
func TestStreamWriter_PartialLine(t *testing.T) {
	var buf bytes.Buffer
	events := make(chan OutputEvent, 100)

	sw := NewStreamWriter(&buf, events, "step_2")

	// Write data without newline
	sw.Write([]byte("partial"))

	// Should be no events at this point
	select {
	case ev := <-events:
		t.Fatalf("unexpected event before flush: %+v", ev)
	default:
		// Correct: no events
	}

	// Continue writing data with newline
	sw.Write([]byte(" data\nline2"))

	// Should receive one event ("partial data")
	ev := <-events
	if ev.Message != "partial data" {
		t.Fatalf("expected 'partial data', got %q", ev.Message)
	}

	// "line2" is still in buffer
	sw.Flush()

	ev = <-events
	if ev.Message != "line2" {
		t.Fatalf("expected 'line2' after flush, got %q", ev.Message)
	}

	// Verify inner buffer is complete
	if buf.String() != "partial data\nline2" {
		t.Fatalf("inner buffer: got %q", buf.String())
	}
}

// TestStreamWriter_NilEvents tests no panic when events is nil, only writes to inner
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

	// Flush should also not panic
	sw.Flush()
}

// TestStreamWriter_ConcurrentWrite tests concurrent write safety from multiple goroutines
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

	// Verify correct number of events received
	count := 0
	for range events {
		count++
	}

	expected := numWriters * linesPerWriter
	if count != expected {
		t.Fatalf("expected %d events, got %d", expected, count)
	}
}

// TestStreamReader_Basic tests StreamReader reading line by line and sending events
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

	// Verify writer received complete content
	if writer.String() != input {
		t.Fatalf("writer mismatch: got %q, want %q", writer.String(), input)
	}

	// Verify events
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

// TestStreamReader_NilEvents tests StreamReader still writes to writer when events is nil
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

// TestEmitProgress_Nil tests EmitProgress does not panic with nil channel
func TestEmitProgress_Nil(t *testing.T) {
	// Should not panic
	EmitProgress(nil, "step_x", "hello")
}

// TestEmitProgress_Send tests EmitProgress normal send
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

// TestOutputEventTypes verifies OutputEventType constant values
func TestOutputEventTypes(t *testing.T) {
	if string(OutputLine) != "output" {
		t.Errorf("OutputLine: got %q, want 'output'", OutputLine)
	}
	if string(OutputProgress) != "progress" {
		t.Errorf("OutputProgress: got %q, want 'progress'", OutputProgress)
	}
}

// TestStreamWriter_WithOutputEvents integration test: simulate executor streaming shell output
func TestStreamWriter_WithOutputEvents(t *testing.T) {
	outputEvents := make(chan OutputEvent, 100)
	var buf bytes.Buffer

	sw := NewStreamWriter(&buf, outputEvents, "shell_step")

	// Simulate shell command incremental output
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

	// Confirm buf has complete output
	if buf.String() != "compiling...\nlinking...\ndone!\n" {
		t.Fatalf("buffer mismatch: %q", buf.String())
	}
}
