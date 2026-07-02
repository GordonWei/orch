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
