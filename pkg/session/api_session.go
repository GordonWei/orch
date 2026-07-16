// api_session.go implements a session-like wrapper around stateless streaming API backends.
// It exposes the same Send/ReadStream/Kill/Alive interface as the PTY-based Session,
// allowing SessionManager to treat API backends (Bedrock, Vertex AI) identically to CLI backends.
package session

import (
	"context"
	"strings"
	"sync"
	"time"
)

// SessionLike is the common interface for both PTY sessions and API sessions.
// SessionManager uses this interface to manage sessions uniformly.
type SessionLike interface {
	Send(input string) error
	ReadStream() <-chan string
	Read() (string, error)
	ReadRaw() string
	IsIdle() bool
	Kill() error
	Done() <-chan struct{}
	Alive() bool
}

// StreamingBackend is the interface that API backends must implement
// to be usable in session mode. It extends the basic Invoke with streaming.
type StreamingBackend interface {
	Name() string
	Available() bool
	InvokeStream(ctx context.Context, req StreamRequest) (<-chan StreamChunk, error)
}

// StreamRequest is the request payload for streaming invocation.
type StreamRequest struct {
	Messages    []StreamMessage
	MaxTokens   int
	Temperature float64
}

// StreamMessage represents a single message in a conversation.
type StreamMessage struct {
	Role    string // "user" or "assistant"
	Content string
}

// StreamChunk represents a single chunk of streaming output.
type StreamChunk struct {
	Text  string // text delta
	Done  bool   // true when stream is complete
	Error error  // non-nil if an error occurred
}

// APISession wraps a StreamingBackend to behave like a PTY Session.
// It maintains conversation history and streams responses chunk-by-chunk.
type APISession struct {
	backend  StreamingBackend
	bname    Backend // "bedrock" or "vertexai"
	mu       sync.Mutex
	history  []StreamMessage
	streamCh chan string
	done     chan struct{}
	alive    bool
	idle     bool
	cancel   context.CancelFunc // cancel the current streaming call
}

// NewAPISession creates a new API-backed session.
func NewAPISession(backend StreamingBackend, name Backend) *APISession {
	return &APISession{
		backend: backend,
		bname:   name,
		done:    make(chan struct{}),
		alive:   true,
		idle:    true,
	}
}

// Send sends input to the API backend (triggers a streaming invocation).
func (s *APISession) Send(input string) error {
	s.mu.Lock()
	if !s.alive {
		s.mu.Unlock()
		return ErrSessionExited
	}

	// Cancel any in-flight request — this causes the old goroutine to stop
	// writing and close its own channel via defer.
	if s.cancel != nil {
		s.cancel()
	}

	// Wait briefly for old goroutine to finish closing its channel
	oldCh := s.streamCh
	s.streamCh = nil
	s.mu.Unlock()

	// If there was an old channel, drain it to unblock the old goroutine
	if oldCh != nil {
		for range oldCh {
		}
	}

	s.mu.Lock()
	// Append user message to history
	s.history = append(s.history, StreamMessage{Role: "user", Content: input})

	// Create new stream channel
	newCh := make(chan string, 64)
	s.streamCh = newCh
	s.idle = false

	// Build request with conversation history
	req := StreamRequest{
		Messages:    append([]StreamMessage{}, s.history...),
		MaxTokens:   4096,
		Temperature: 0.7,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	s.cancel = cancel
	s.mu.Unlock()

	// Start streaming in background — goroutine owns newCh and will close it
	go s.streamResponse(ctx, req, newCh)

	return nil
}

// streamResponse invokes the streaming API and pipes chunks to the stream channel.
// The goroutine owns `ch` and is the only one that closes it.
func (s *APISession) streamResponse(ctx context.Context, req StreamRequest, ch chan string) {
	defer func() {
		close(ch) // goroutine is sole owner of ch; safe to close
		s.mu.Lock()
		s.idle = true
		// Clear streamCh only if it's still pointing to our channel
		if s.streamCh == ch {
			s.streamCh = nil
		}
		s.mu.Unlock()
	}()

	chunks, err := s.backend.InvokeStream(ctx, req)
	if err != nil {
		select {
		case ch <- "❌ " + err.Error() + "\n":
		case <-ctx.Done():
		}
		return
	}

	var fullResponse strings.Builder
	for chunk := range chunks {
		if ctx.Err() != nil {
			// Context cancelled (new Send() or Kill() happened) — stop writing
			return
		}
		if chunk.Error != nil {
			select {
			case ch <- "\n❌ stream error: " + chunk.Error.Error() + "\n":
			case <-ctx.Done():
			}
			break
		}
		if chunk.Text != "" {
			fullResponse.WriteString(chunk.Text)
			select {
			case ch <- chunk.Text:
			case <-ctx.Done():
				return
			}
		}
		if chunk.Done {
			break
		}
	}

	// Append assistant response to history
	if fullResponse.Len() > 0 {
		s.mu.Lock()
		s.history = append(s.history, StreamMessage{Role: "assistant", Content: fullResponse.String()})
		s.mu.Unlock()
	}
}

// ReadStream returns a channel that emits output chunks as they arrive.
func (s *APISession) ReadStream() <-chan string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.streamCh == nil {
		ch := make(chan string)
		close(ch)
		return ch
	}
	return s.streamCh
}

// Read blocks until the current response is complete, returns full output.
func (s *APISession) Read() (string, error) {
	ch := s.ReadStream()
	var buf strings.Builder
	for chunk := range ch {
		buf.WriteString(chunk)
	}
	return buf.String(), nil
}

// ReadRaw returns current buffered output (empty for API sessions since streaming is direct).
func (s *APISession) ReadRaw() string {
	return ""
}

// IsIdle returns true when no streaming call is in progress.
func (s *APISession) IsIdle() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.idle
}

// Kill terminates the API session.
func (s *APISession) Kill() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.alive {
		return nil
	}

	// Cancel in-flight request — the goroutine will close its channel via defer
	if s.cancel != nil {
		s.cancel()
	}

	// Do NOT close streamCh here — the owning goroutine will close it
	// when it detects context cancellation. Just nil out our reference.
	s.streamCh = nil

	s.alive = false
	close(s.done)
	return nil
}

// Done returns a channel closed when the session ends.
func (s *APISession) Done() <-chan struct{} {
	return s.done
}

// Alive returns true if the session hasn't been killed.
func (s *APISession) Alive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.alive
}

// ErrSessionExited is returned when Send is called on a killed session.
var ErrSessionExited = &sessionExitedError{}

type sessionExitedError struct{}

func (e *sessionExitedError) Error() string { return "session already exited" }
