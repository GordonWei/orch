package session

import (
	"context"
	"sync"
	"testing"
	"time"
)

// mockStreamingBackend is a test helper that simulates a streaming API backend.
type mockStreamingBackend struct {
	name      string
	delay     time.Duration // delay between chunks
	chunks    []string      // text chunks to emit
	available bool
}

func (m *mockStreamingBackend) Name() string    { return m.name }
func (m *mockStreamingBackend) Available() bool  { return m.available }

func (m *mockStreamingBackend) InvokeStream(ctx context.Context, req StreamRequest) (<-chan StreamChunk, error) {
	ch := make(chan StreamChunk, 16)
	go func() {
		defer close(ch)
		for _, text := range m.chunks {
			select {
			case <-ctx.Done():
				ch <- StreamChunk{Error: ctx.Err()}
				return
			case <-time.After(m.delay):
				ch <- StreamChunk{Text: text}
			}
		}
		ch <- StreamChunk{Done: true}
	}()
	return ch, nil
}

// TestAPISession_SendWhileStreaming verifies that calling Send() a second time
// while the first stream is still producing chunks does NOT panic (send on
// closed channel). This is the exact race condition Victoria's review identified.
func TestAPISession_SendWhileStreaming(t *testing.T) {
	backend := &mockStreamingBackend{
		name:      "test",
		delay:     50 * time.Millisecond, // slow chunks so stream is still active when 2nd Send arrives
		chunks:    []string{"hello ", "world ", "this ", "is ", "a ", "long ", "response"},
		available: true,
	}

	sess := NewAPISession(backend, "test")

	// First Send — starts streaming
	if err := sess.Send("first message"); err != nil {
		t.Fatalf("first Send failed: %v", err)
	}

	// Wait a bit so the goroutine starts producing chunks
	time.Sleep(80 * time.Millisecond)

	// Second Send while first stream is still running — this used to panic
	if err := sess.Send("second message"); err != nil {
		t.Fatalf("second Send failed: %v", err)
	}

	// Drain the second stream
	ch := sess.ReadStream()
	var got []string
	for chunk := range ch {
		got = append(got, chunk)
	}

	// Should have received chunks from the second invocation
	if len(got) == 0 {
		t.Error("expected output from second Send, got nothing")
	}

	// Session should be idle after stream completes
	time.Sleep(100 * time.Millisecond)
	if !sess.IsIdle() {
		t.Error("expected session to be idle after stream completes")
	}
}

// TestAPISession_KillWhileStreaming verifies that Kill() during an active
// stream doesn't panic or deadlock.
func TestAPISession_KillWhileStreaming(t *testing.T) {
	backend := &mockStreamingBackend{
		name:      "test",
		delay:     100 * time.Millisecond,
		chunks:    []string{"a", "b", "c", "d", "e", "f", "g", "h"},
		available: true,
	}

	sess := NewAPISession(backend, "test")

	if err := sess.Send("hello"); err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	// Let streaming start
	time.Sleep(50 * time.Millisecond)

	// Kill while streaming
	if err := sess.Kill(); err != nil {
		t.Fatalf("Kill failed: %v", err)
	}

	// Session should not be alive
	if sess.Alive() {
		t.Error("expected session to be dead after Kill")
	}

	// Done channel should be closed
	select {
	case <-sess.Done():
		// good
	case <-time.After(time.Second):
		t.Error("Done() channel not closed after Kill")
	}

	// Send after Kill should error
	if err := sess.Send("after kill"); err == nil {
		t.Error("expected error from Send after Kill")
	}
}

// TestAPISession_ConcurrentSends runs multiple concurrent Sends to stress-test
// the race protection. Run with -race to verify no data races.
func TestAPISession_ConcurrentSends(t *testing.T) {
	backend := &mockStreamingBackend{
		name:      "test",
		delay:     10 * time.Millisecond,
		chunks:    []string{"x", "y", "z"},
		available: true,
	}

	sess := NewAPISession(backend, "test")

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			sess.Send("concurrent message")
			// Drain whatever stream we get
			ch := sess.ReadStream()
			for range ch {
			}
		}(i)
	}

	wg.Wait()

	// Should still be functional
	if !sess.Alive() {
		t.Error("session died during concurrent sends")
	}

	sess.Kill()
}

// TestAPISession_ConversationHistory verifies that multi-turn messages accumulate.
func TestAPISession_ConversationHistory(t *testing.T) {
	backend := &mockStreamingBackend{
		name:      "test",
		delay:     0,
		chunks:    []string{"response"},
		available: true,
	}

	sess := NewAPISession(backend, "test")

	// First turn
	sess.Send("hello")
	output, _ := sess.Read()
	if output == "" {
		t.Error("expected non-empty response from first Send")
	}

	// Second turn — history should contain first exchange
	sess.Send("follow up")
	output2, _ := sess.Read()
	if output2 == "" {
		t.Error("expected non-empty response from second Send")
	}

	// Verify history length (2 user messages + 2 assistant responses = 4)
	sess.mu.Lock()
	histLen := len(sess.history)
	sess.mu.Unlock()

	if histLen != 4 {
		t.Errorf("expected 4 history entries (2 user + 2 assistant), got %d", histLen)
	}
}

// TestAPISession_ReadStreamBeforeSend returns a closed channel (doesn't block).
func TestAPISession_ReadStreamBeforeSend(t *testing.T) {
	backend := &mockStreamingBackend{
		name:      "test",
		available: true,
	}

	sess := NewAPISession(backend, "test")

	ch := sess.ReadStream()
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("expected closed channel from ReadStream before Send")
		}
	case <-time.After(time.Second):
		t.Error("ReadStream before Send should return immediately-closed channel")
	}
}
