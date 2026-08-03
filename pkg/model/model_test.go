package model

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOpenAIClient_Chat(t *testing.T) {
	// Mock OpenAI-compatible server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/chat/completions" {
			resp := openAIResponse{
				Choices: []struct {
					Message struct {
						Content string `json:"content"`
					} `json:"message"`
				}{
					{Message: struct {
						Content string `json:"content"`
					}{Content: "test reply"}},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
			return
		}
		if r.URL.Path == "/v1/models" {
			w.WriteHeader(200)
			w.Write([]byte(`{"data":[]}`))
			return
		}
		w.WriteHeader(404)
	}))
	defer server.Close()

	client := NewOpenAIClient(OpenAIClientConfig{
		Endpoint: server.URL,
		Model:    "test-model",
		Backend:  "test",
	})

	// Test Available
	if !client.Available() {
		t.Error("expected client to be available")
	}

	// Test Chat
	reply, err := client.Chat([]Message{
		{Role: "user", Content: "hello"},
	}, nil)
	if err != nil {
		t.Fatalf("chat failed: %v", err)
	}
	if reply != "test reply" {
		t.Errorf("expected 'test reply', got %q", reply)
	}

	// Test ModelName
	if client.ModelName() != "test-model" {
		t.Errorf("expected 'test-model', got %q", client.ModelName())
	}

	// Test Backend
	if client.Backend() != "test" {
		t.Errorf("expected 'test', got %q", client.Backend())
	}
}

func TestOpenAIClient_Unavailable(t *testing.T) {
	client := NewOpenAIClient(OpenAIClientConfig{
		Endpoint: "http://localhost:19999", // nothing here
		Model:    "ghost-model",
		Backend:  "mlx",
	})

	if client.Available() {
		t.Error("expected client to be unavailable")
	}

	_, err := client.Chat([]Message{
		{Role: "user", Content: "hello"},
	}, nil)
	if err == nil {
		t.Error("expected error when server is down")
	}
}

func TestOpenAIClient_ChatOptions(t *testing.T) {
	var receivedBody openAIRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/chat/completions" {
			json.NewDecoder(r.Body).Decode(&receivedBody)
			resp := openAIResponse{
				Choices: []struct {
					Message struct {
						Content string `json:"content"`
					} `json:"message"`
				}{
					{Message: struct {
						Content string `json:"content"`
					}{Content: "ok"}},
				},
			}
			json.NewEncoder(w).Encode(resp)
			return
		}
	}))
	defer server.Close()

	client := NewOpenAIClient(OpenAIClientConfig{
		Endpoint: server.URL,
		Model:    "test-model",
	})

	_, err := client.Chat([]Message{
		{Role: "user", Content: "test"},
	}, &ChatOptions{MaxTokens: 2048, Temperature: 0.7})
	if err != nil {
		t.Fatalf("chat failed: %v", err)
	}

	if receivedBody.MaxTokens != 2048 {
		t.Errorf("expected max_tokens=2048, got %d", receivedBody.MaxTokens)
	}
	if receivedBody.Temperature != 0.7 {
		t.Errorf("expected temperature=0.7, got %f", receivedBody.Temperature)
	}
}

// ══════════════════════════════════════════════════════════════════════════════
// New Coverage Tests
// ══════════════════════════════════════════════════════════════════════════════

// TestOpenAIClient_Unavailable_Explicit verifies Available() returns false for unreachable endpoint.
func TestOpenAIClient_Unavailable_Explicit(t *testing.T) {
	client := NewOpenAIClient(OpenAIClientConfig{
		Endpoint: "http://127.0.0.1:19876", // port nobody listens on
		Model:    "nonexistent-model",
		Backend:  "test",
	})

	if client.Available() {
		t.Error("Available() should return false for unreachable endpoint")
	}
}

// TestOpenAIClient_ChatError verifies Chat() with unreachable server returns error.
func TestOpenAIClient_ChatError(t *testing.T) {
	client := NewOpenAIClient(OpenAIClientConfig{
		Endpoint: "http://127.0.0.1:19876",
		Model:    "nonexistent-model",
		Backend:  "test",
	})

	_, err := client.Chat([]Message{
		{Role: "user", Content: "hello"},
	}, nil)
	if err == nil {
		t.Error("Chat() should return error when server is unreachable")
	}

	// Test with custom options too
	_, err = client.Chat([]Message{
		{Role: "system", Content: "you are helpful"},
		{Role: "user", Content: "test"},
	}, &ChatOptions{MaxTokens: 100, Temperature: 0.5})
	if err == nil {
		t.Error("Chat() with options should still return error when server is unreachable")
	}
}

// TestStarter_EnsureRunning_AlreadyRunning verifies EnsureRunning is a no-op when Available() is true.
func TestStarter_EnsureRunning_AlreadyRunning(t *testing.T) {
	// Create a mock server that responds to /v1/models and /v1/chat/completions
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.WriteHeader(200)
			w.Write([]byte(`{"data":[]}`))
			return
		}
		if r.URL.Path == "/v1/chat/completions" {
			w.WriteHeader(200)
			w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
			return
		}
		w.WriteHeader(404)
	}))
	defer server.Close()

	client := NewOpenAIClient(OpenAIClientConfig{
		Endpoint: server.URL,
		Model:    "test-model",
		Backend:  "test",
	})

	// Verify client is available
	if !client.Available() {
		t.Fatal("test client should be available")
	}

	// Create starter
	starter := NewStarter(StarterConfig{
		Backend:  "mlx",
		Endpoint: server.URL,
		Model:    "test-model",
	})

	// EnsureRunning should be a no-op (client is already available)
	err := starter.EnsureRunning(client)
	if err != nil {
		t.Errorf("EnsureRunning should succeed (no-op) when server is already running, got: %v", err)
	}
}

// TestStarter_EnsureRunning_UnsupportedBackend verifies error for unknown backend.
func TestStarter_EnsureRunning_UnsupportedBackend(t *testing.T) {
	// Client that reports unavailable
	client := NewOpenAIClient(OpenAIClientConfig{
		Endpoint: "http://127.0.0.1:19876",
		Model:    "ghost",
		Backend:  "test",
	})

	starter := NewStarter(StarterConfig{
		Backend:  "unsupported-backend",
		Endpoint: "http://127.0.0.1:19876",
		Model:    "ghost",
	})

	err := starter.EnsureRunning(client)
	if err == nil {
		t.Error("EnsureRunning should return error for unsupported backend")
	}
}

// fakeLLM is a minimal LLM implementation for exercising EnsureRunning's
// retry logic without a real HTTP server.
type fakeLLM struct {
	availableAfter int // Available() returns false until this many calls have been made
	calls          int
}

func (f *fakeLLM) Chat(messages []Message, opts *ChatOptions) (string, error) { return "", nil }
func (f *fakeLLM) Available() bool {
	f.calls++
	return f.calls > f.availableAfter
}
func (f *fakeLLM) ModelName() string { return "fake" }
func (f *fakeLLM) Backend() string   { return "fake" }

// TestStarter_EnsureRunning_RetriesTransientUnavailable is a regression test
// for a real incident (2026-08-01): a single failed Available() check isn't
// reliable enough to conclude the server is down. mlx_lm.server handles one
// request at a time, so a busy-but-healthy server can fail one health check
// — which used to make EnsureRunning spawn a competing mlx_lm.server via the
// deprecated `python -m mlx_lm.server` invocation, crashing immediately on
// the port the real, healthy server already holds. Verifies 2 transient
// failures followed by a success are treated as "already running" (no-op),
// not "start a new one".
func TestStarter_EnsureRunning_RetriesTransientUnavailable(t *testing.T) {
	client := &fakeLLM{availableAfter: 2}
	starter := NewStarter(StarterConfig{
		Backend:  "mlx",
		Endpoint: "http://127.0.0.1:19876",
		Model:    "ghost",
	})

	err := starter.EnsureRunning(client)
	if err != nil {
		t.Errorf("EnsureRunning should recover from transient Available() failures, got: %v", err)
	}
	if client.calls != 3 {
		t.Errorf("expected exactly 3 Available() calls (2 failures + 1 success), got %d", client.calls)
	}
}

// TestStarter_EnsureRunning_GivesUpAfterPersistentUnavailable verifies the
// retry is bounded: if Available() never succeeds, EnsureRunning still
// proceeds to attempt starting the server rather than retrying forever.
func TestStarter_EnsureRunning_GivesUpAfterPersistentUnavailable(t *testing.T) {
	client := &fakeLLM{availableAfter: 999} // never becomes available
	starter := NewStarter(StarterConfig{
		Backend:    "mlx",
		Endpoint:   "http://127.0.0.1:19876",
		Model:      "ghost",
		PythonPath: "/nonexistent/path/to/python",
	})

	_ = starter.EnsureRunning(client) // startMLX fails on the bad python path; only the call count matters here
	if client.calls != 3 {
		t.Errorf("expected exactly 3 Available() attempts before giving up, got %d", client.calls)
	}
}

// TestOpenAIClient_ServerError verifies Chat() handles non-200 responses.
func TestOpenAIClient_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		w.Write([]byte("internal server error"))
	}))
	defer server.Close()

	client := NewOpenAIClient(OpenAIClientConfig{
		Endpoint: server.URL,
		Model:    "test-model",
		Backend:  "test",
	})

	_, err := client.Chat([]Message{
		{Role: "user", Content: "hello"},
	}, nil)
	if err == nil {
		t.Error("Chat() should return error on 500 response")
	}
}

// TestOpenAIClient_EmptyChoices verifies Chat() handles empty choices array.
func TestOpenAIClient_EmptyChoices(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[]}`))
	}))
	defer server.Close()

	client := NewOpenAIClient(OpenAIClientConfig{
		Endpoint: server.URL,
		Model:    "test-model",
		Backend:  "test",
	})

	_, err := client.Chat([]Message{
		{Role: "user", Content: "hello"},
	}, nil)
	if err == nil {
		t.Error("Chat() should return error when choices is empty")
	}
}
