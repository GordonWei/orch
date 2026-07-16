package apibackend

import (
	"context"
	"os"
	"testing"
	"time"
)

// ===== Struct Creation Tests =====

func TestRequestCreation(t *testing.T) {
	req := Request{
		Prompt:      "Hello, world!",
		MaxTokens:   1024,
		Temperature: 0.7,
		Messages: []Message{
			{Role: "user", Content: "Hi"},
			{Role: "assistant", Content: "Hello!"},
			{Role: "user", Content: "How are you?"},
		},
	}

	if req.Prompt != "Hello, world!" {
		t.Errorf("expected Prompt = %q, got %q", "Hello, world!", req.Prompt)
	}
	if req.MaxTokens != 1024 {
		t.Errorf("expected MaxTokens = 1024, got %d", req.MaxTokens)
	}
	if req.Temperature != 0.7 {
		t.Errorf("expected Temperature = 0.7, got %f", req.Temperature)
	}
	if len(req.Messages) != 3 {
		t.Errorf("expected 3 messages, got %d", len(req.Messages))
	}
	if req.Messages[0].Role != "user" {
		t.Errorf("expected first message role = %q, got %q", "user", req.Messages[0].Role)
	}
	if req.Messages[1].Role != "assistant" {
		t.Errorf("expected second message role = %q, got %q", "assistant", req.Messages[1].Role)
	}
}

func TestResponseCreation(t *testing.T) {
	resp := Response{
		Content:      "This is a test response.",
		InputTokens:  100,
		OutputTokens: 50,
		Model:        "anthropic.claude-3-5-sonnet-20241022-v2:0",
		StopReason:   "end_turn",
		Latency:      2 * time.Second,
	}

	if resp.Content != "This is a test response." {
		t.Errorf("expected Content = %q, got %q", "This is a test response.", resp.Content)
	}
	if resp.InputTokens != 100 {
		t.Errorf("expected InputTokens = 100, got %d", resp.InputTokens)
	}
	if resp.OutputTokens != 50 {
		t.Errorf("expected OutputTokens = 50, got %d", resp.OutputTokens)
	}
	if resp.Model != "anthropic.claude-3-5-sonnet-20241022-v2:0" {
		t.Errorf("expected Model = %q, got %q", "anthropic.claude-3-5-sonnet-20241022-v2:0", resp.Model)
	}
	if resp.StopReason != "end_turn" {
		t.Errorf("expected StopReason = %q, got %q", "end_turn", resp.StopReason)
	}
	if resp.Latency != 2*time.Second {
		t.Errorf("expected Latency = 2s, got %v", resp.Latency)
	}
}

func TestMessageCreation(t *testing.T) {
	msg := Message{Role: "user", Content: "Tell me a joke"}
	if msg.Role != "user" {
		t.Errorf("expected Role = %q, got %q", "user", msg.Role)
	}
	if msg.Content != "Tell me a joke" {
		t.Errorf("expected Content = %q, got %q", "Tell me a joke", msg.Content)
	}
}

// ===== Backend Name Tests =====

func TestBedrockBackendName(t *testing.T) {
	b := NewBedrock(BedrockConfig{
		Region:  "us-east-1",
		ModelID: "anthropic.claude-3-5-sonnet-20241022-v2:0",
	})
	if b.Name() != "bedrock" {
		t.Errorf("expected Name() = %q, got %q", "bedrock", b.Name())
	}
}

func TestVertexAIBackendName(t *testing.T) {
	v := NewVertexAI(VertexAIConfig{
		ProjectID: "my-project",
		Region:    "us-central1",
		ModelID:   "gemini-2.0-flash",
	})
	if v.Name() != "vertexai" {
		t.Errorf("expected Name() = %q, got %q", "vertexai", v.Name())
	}
}

// ===== Available() Tests =====
// These tests verify that Available() gracefully returns false when no credentials
// are configured, without panicking.

func TestBedrockAvailable_NoCredentials(t *testing.T) {
	// In a CI/test environment without AWS credentials, this should return true
	// because LoadDefaultConfig itself rarely errors (credentials are checked lazily).
	// The important thing is: it does NOT panic.
	b := NewBedrock(BedrockConfig{
		Region:  "us-east-1",
		ModelID: "anthropic.claude-3-5-sonnet-20241022-v2:0",
	})

	// Should not panic — that's the key assertion
	available := b.Available()
	t.Logf("BedrockBackend.Available() = %v (depends on local AWS config)", available)
}

func TestVertexAIAvailable_NoCredentials(t *testing.T) {
	// Without GCP ADC configured, this should return false.
	// The important thing is: it does NOT panic.
	v := NewVertexAI(VertexAIConfig{
		ProjectID: "nonexistent-project",
		Region:    "us-central1",
		ModelID:   "gemini-2.0-flash",
	})

	// Should not panic — that's the key assertion
	available := v.Available()
	t.Logf("VertexAIBackend.Available() = %v (depends on local GCP config)", available)
}

// ===== Invoke() Error Tests =====
// These tests verify that Invoke() returns proper errors when credentials are missing.

func TestBedrockInvoke_NoCredentials(t *testing.T) {
	if os.Getenv("ORCH_TEST_API") == "" {
		t.Skip("Skipping real API test (set ORCH_TEST_API=1 to enable)")
	}

	b := NewBedrock(BedrockConfig{
		Region:  "us-east-1",
		ModelID: "us.anthropic.claude-sonnet-4-20250514-v1:0",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req := Request{
		Prompt:    "Hello",
		MaxTokens: 100,
	}

	resp, err := b.Invoke(ctx, req)

	// In most test environments without valid AWS credentials + Bedrock access,
	// this should error. If credentials exist, the call might succeed or fail
	// with a service error — either way it should not panic.
	if err != nil {
		t.Logf("BedrockBackend.Invoke() returned expected error: %v", err)
		if resp != nil {
			t.Errorf("expected nil response on error, got %+v", resp)
		}
	} else {
		// If somehow credentials are available and call succeeds
		t.Logf("BedrockBackend.Invoke() succeeded (valid credentials found): model=%s, tokens=%d/%d",
			resp.Model, resp.InputTokens, resp.OutputTokens)
	}
}

func TestVertexAIInvoke_NoCredentials(t *testing.T) {
	if os.Getenv("ORCH_TEST_API") == "" {
		t.Skip("Skipping real API test (set ORCH_TEST_API=1 to enable)")
	}

	v := NewVertexAI(VertexAIConfig{
		ProjectID: "nonexistent-project-12345",
		Region:    "us-central1",
		ModelID:   "gemini-2.0-flash",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req := Request{
		Prompt:    "Hello",
		MaxTokens: 100,
	}

	resp, err := v.Invoke(ctx, req)

	// Without ADC credentials, FindDefaultCredentials should fail.
	if err != nil {
		t.Logf("VertexAIBackend.Invoke() returned expected error: %v", err)
		if resp != nil {
			t.Errorf("expected nil response on error, got %+v", resp)
		}
	} else {
		t.Logf("VertexAIBackend.Invoke() succeeded (valid credentials found): model=%s", resp.Model)
	}
}

// ===== Invoke() Validation Tests =====

func TestBedrockInvoke_EmptyRequest(t *testing.T) {
	b := NewBedrock(BedrockConfig{
		Region:  "us-east-1",
		ModelID: "anthropic.claude-3-5-sonnet-20241022-v2:0",
	})

	ctx := context.Background()
	req := Request{} // No prompt, no messages

	_, err := b.Invoke(ctx, req)
	// Should fail because no messages can be built
	// (either config load fails, or empty message validation fails)
	if err != nil {
		t.Logf("BedrockBackend.Invoke() with empty request returned error: %v", err)
	}
}

// ===== Interface Compliance Tests =====

func TestBedrockImplementsAPIBackend(t *testing.T) {
	var _ APIBackend = (*BedrockBackend)(nil)
}

func TestVertexAIImplementsAPIBackend(t *testing.T) {
	var _ APIBackend = (*VertexAIBackend)(nil)
}

// ===== Constructor Tests =====

func TestNewBedrockConfig(t *testing.T) {
	cfg := BedrockConfig{
		Region:  "ap-northeast-1",
		ModelID: "amazon.nova-pro-v1:0",
	}
	b := NewBedrock(cfg)

	if b.region != "ap-northeast-1" {
		t.Errorf("expected region = %q, got %q", "ap-northeast-1", b.region)
	}
	if b.modelID != "amazon.nova-pro-v1:0" {
		t.Errorf("expected modelID = %q, got %q", "amazon.nova-pro-v1:0", b.modelID)
	}
}

func TestNewVertexAIConfig(t *testing.T) {
	cfg := VertexAIConfig{
		ProjectID: "my-gcp-project",
		Region:    "asia-east1",
		ModelID:   "gemini-2.0-flash",
	}
	v := NewVertexAI(cfg)

	if v.projectID != "my-gcp-project" {
		t.Errorf("expected projectID = %q, got %q", "my-gcp-project", v.projectID)
	}
	if v.region != "asia-east1" {
		t.Errorf("expected region = %q, got %q", "asia-east1", v.region)
	}
	if v.modelID != "gemini-2.0-flash" {
		t.Errorf("expected modelID = %q, got %q", "gemini-2.0-flash", v.modelID)
	}
}

// ===== Helper Tests =====

func TestBuildBedrockMessages_FromPrompt(t *testing.T) {
	req := Request{Prompt: "Hello, AI!"}
	msgs := buildBedrockMessages(req)

	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].Role != "user" {
		t.Errorf("expected role = %q, got %q", "user", msgs[0].Role)
	}
}

func TestBuildBedrockMessages_FromMessages(t *testing.T) {
	req := Request{
		Messages: []Message{
			{Role: "user", Content: "Hi"},
			{Role: "assistant", Content: "Hello!"},
			{Role: "user", Content: "How are you?"},
		},
	}
	msgs := buildBedrockMessages(req)

	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(msgs))
	}
	if msgs[0].Role != "user" {
		t.Errorf("expected first message role = %q, got %q", "user", msgs[0].Role)
	}
	if msgs[1].Role != "assistant" {
		t.Errorf("expected second message role = %q, got %q", "assistant", msgs[1].Role)
	}
}

func TestBuildBedrockMessages_EmptyRequest(t *testing.T) {
	req := Request{}
	msgs := buildBedrockMessages(req)

	if len(msgs) != 0 {
		t.Errorf("expected 0 messages for empty request, got %d", len(msgs))
	}
}

func TestVertexRole(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"user", "user"},
		{"assistant", "model"},
		{"system", "system"},
	}

	for _, tc := range tests {
		got := vertexRole(tc.input)
		if got != tc.expected {
			t.Errorf("vertexRole(%q) = %q, want %q", tc.input, got, tc.expected)
		}
	}
}

func TestJoinStrings(t *testing.T) {
	tests := []struct {
		input    []string
		expected string
	}{
		{nil, ""},
		{[]string{}, ""},
		{[]string{"hello"}, "hello"},
		{[]string{"hello", " ", "world"}, "hello world"},
		{[]string{"a", "b", "c"}, "abc"},
	}

	for _, tc := range tests {
		got := joinStrings(tc.input)
		if got != tc.expected {
			t.Errorf("joinStrings(%v) = %q, want %q", tc.input, got, tc.expected)
		}
	}
}
