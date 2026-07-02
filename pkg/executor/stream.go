package executor

import (
	"bufio"
	"io"
	"sync"
	"time"
)

// ===== Output Streaming Events (supplements existing EventChan system) =====
//
// The existing StepEvent / EventChan is used for step-level lifecycle notifications (start/done/failed).
// OutputEvent is for real-time output during step execution — allows CLI to display line-by-line shell stdout.

// OutputEventType defines output streaming event types
type OutputEventType string

const (
	OutputLine     OutputEventType = "output"   // shell command line-by-line output
	OutputProgress OutputEventType = "progress" // AI agent periodic progress reports (cannot stream in real-time)
)

// OutputEvent represents a real-time output event during execution
type OutputEvent struct {
	Type      OutputEventType // event type
	StepID    string          // owning step ID (used to distinguish source during parallel execution)
	Message   string          // event message content (one line of output or progress description)
	Timestamp time.Time       // event timestamp
}

// ===== StreamWriter: wraps io.Writer, sends line by line OutputEvent =====

// StreamWriter simultaneously writes to underlying Writer (buffers complete output) and sends events to channel line by line.
// Designed to be goroutine-safe: uses internal mutex to protect shared state.
type StreamWriter struct {
	mu      sync.Mutex
	inner   io.Writer         // underlying writer (usually bytes.Buffer, collects complete output)
	events  chan<- OutputEvent // output event channel (nil means no events sent)
	stepID  string
	lineBuf []byte // line buffer, accumulates until newline before sending
}

// NewStreamWriter creates a StreamWriter.
// inner: underlying writer (complete output still written to underlying writer)
// events: output event channel (nil means only writes to inner without sending events)
// stepID: used to tag event source step
func NewStreamWriter(inner io.Writer, events chan<- OutputEvent, stepID string) *StreamWriter {
	return &StreamWriter{
		inner:  inner,
		events: events,
		stepID: stepID,
	}
}

// Write implements io.Writer interface.
// Writes data on receipt to inner, and splits and sends line by line OutputLine events.
func (sw *StreamWriter) Write(p []byte) (n int, err error) {
	sw.mu.Lock()
	defer sw.mu.Unlock()

	// Write to underlying writer first (ensures complete output is not lost)
	n, err = sw.inner.Write(p)
	if err != nil {
		return n, err
	}

	// If no event channel, no line processing needed
	if sw.events == nil {
		return n, nil
	}

	// Accumulate line buffer byte by byte, send on newline
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

// Flush force-sends remaining incomplete line in buffer (called when step ends).
func (sw *StreamWriter) Flush() {
	sw.mu.Lock()
	defer sw.mu.Unlock()

	if len(sw.lineBuf) > 0 && sw.events != nil {
		sw.emitLine(string(sw.lineBuf))
		sw.lineBuf = sw.lineBuf[:0]
	}
}

// emitLine emits one output line event (mutex already held when called).
func (sw *StreamWriter) emitLine(line string) {
	sw.events <- OutputEvent{
		Type:      OutputLine,
		StepID:    sw.stepID,
		Message:   line,
		Timestamp: time.Now(),
	}
}

// ===== StreamReader: stream line by line from io.Reader =====

// StreamReader reads line by line from reader, each line written to writer and event sent.
// Commonly used for cmd.StdoutPipe() output streaming.
// This function blocks until reader EOF or error.
func StreamReader(reader io.Reader, writer io.Writer, events chan<- OutputEvent, stepID string) error {
	scanner := bufio.NewScanner(reader)
	// Increase buffer for long lines (default 64KB, max 1MB)
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()

		// Write to writer including newline
		if _, err := writer.Write([]byte(line + "\n")); err != nil {
			return err
		}

		// Send event
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

// ===== EmitProgress: convenience function for sending progress events =====

// EmitProgress safely sends progress event to channel (no-op if channel is nil).
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
