// Package apibackend provides a unified interface for stateless HTTP API backends
// (Bedrock, Vertex AI, etc.) as opposed to the CLI-based PTY backends in pkg/backend.
//
// Each APIBackend adapter wraps the HTTP/SDK invocation details for a cloud AI service.
// The system can use these for direct model invocation without spawning CLI processes.
package apibackend

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"golang.org/x/oauth2/google"
)

// APIBackend defines the interface for a stateless HTTP API backend.
type APIBackend interface {
	// Name returns the backend identifier (e.g., "bedrock", "vertexai").
	Name() string

	// Available checks if credentials and configuration are valid.
	Available() bool

	// Invoke sends a request to the backend API and returns the response.
	Invoke(ctx context.Context, req Request) (*Response, error)
}

// Request represents a prompt request to an API backend.
type Request struct {
	Prompt      string    // Single-turn prompt (convenience)
	Messages    []Message // Multi-turn conversation history
	MaxTokens   int
	Temperature float64
}

// Message represents a single message in a conversation.
type Message struct {
	Role    string // "user" or "assistant"
	Content string
}

// Response represents the result from an API backend invocation.
type Response struct {
	Content      string
	InputTokens  int
	OutputTokens int
	Model        string
	StopReason   string
	Latency      time.Duration
}

// ===== Bedrock Backend =====

// BedrockConfig holds configuration for the Bedrock backend.
type BedrockConfig struct {
	Region  string // AWS region (e.g., "us-east-1")
	ModelID string // Model identifier (e.g., "anthropic.claude-3-5-sonnet-20241022-v2:0")
}

// BedrockBackend implements APIBackend using AWS Bedrock Runtime Converse API.
type BedrockBackend struct {
	region  string
	modelID string
}

// NewBedrock creates a new BedrockBackend with the given configuration.
func NewBedrock(cfg BedrockConfig) *BedrockBackend {
	return &BedrockBackend{
		region:  cfg.Region,
		modelID: cfg.ModelID,
	}
}

func (b *BedrockBackend) Name() string { return "bedrock" }

// Available checks if AWS credentials can be loaded for the configured region.
func (b *BedrockBackend) Available() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := config.LoadDefaultConfig(ctx, config.WithRegion(b.region))
	return err == nil
}

// Invoke sends a request to the Bedrock Converse API and returns the response.
func (b *BedrockBackend) Invoke(ctx context.Context, req Request) (*Response, error) {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(b.region))
	if err != nil {
		return nil, fmt.Errorf("bedrock: failed to load AWS config: %w", err)
	}

	client := bedrockruntime.NewFromConfig(cfg)

	// Build messages for the Converse API
	messages := buildBedrockMessages(req)
	if len(messages) == 0 {
		return nil, fmt.Errorf("bedrock: request must contain at least one message or a prompt")
	}

	// Build inference config
	input := &bedrockruntime.ConverseInput{
		ModelId:  aws.String(b.modelID),
		Messages: messages,
	}

	if req.MaxTokens > 0 || req.Temperature > 0 {
		inferCfg := &types.InferenceConfiguration{}
		if req.MaxTokens > 0 {
			inferCfg.MaxTokens = aws.Int32(int32(req.MaxTokens))
		}
		if req.Temperature > 0 {
			t := float32(req.Temperature)
			inferCfg.Temperature = &t
		}
		input.InferenceConfig = inferCfg
	}

	start := time.Now()
	output, err := client.Converse(ctx, input)
	latency := time.Since(start)

	if err != nil {
		return nil, fmt.Errorf("bedrock: Converse API call failed: %w", err)
	}

	// Extract response content
	content := extractBedrockContent(output)

	// Extract token usage
	var inputTokens, outputTokens int
	if output.Usage != nil {
		inputTokens = int(aws.ToInt32(output.Usage.InputTokens))
		outputTokens = int(aws.ToInt32(output.Usage.OutputTokens))
	}

	// Extract stop reason
	stopReason := string(output.StopReason)

	return &Response{
		Content:      content,
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		Model:        b.modelID,
		StopReason:   stopReason,
		Latency:      latency,
	}, nil
}

// buildBedrockMessages converts our Request into Bedrock Converse message format.
func buildBedrockMessages(req Request) []types.Message {
	var messages []types.Message

	// If Messages are provided, use them
	if len(req.Messages) > 0 {
		for _, m := range req.Messages {
			msg := types.Message{
				Role: types.ConversationRole(m.Role),
				Content: []types.ContentBlock{
					&types.ContentBlockMemberText{Value: m.Content},
				},
			}
			messages = append(messages, msg)
		}
		return messages
	}

	// Otherwise, use Prompt as a single user message
	if req.Prompt != "" {
		messages = append(messages, types.Message{
			Role: types.ConversationRoleUser,
			Content: []types.ContentBlock{
				&types.ContentBlockMemberText{Value: req.Prompt},
			},
		})
	}

	return messages
}

// extractBedrockContent extracts text content from the Converse API response.
func extractBedrockContent(output *bedrockruntime.ConverseOutput) string {
	if output == nil || output.Output == nil {
		return ""
	}

	// The output is a union type; extract the message variant
	msgOutput, ok := output.Output.(*types.ConverseOutputMemberMessage)
	if !ok {
		return ""
	}

	var parts []string
	for _, block := range msgOutput.Value.Content {
		if textBlock, ok := block.(*types.ContentBlockMemberText); ok {
			parts = append(parts, textBlock.Value)
		}
	}

	if len(parts) == 1 {
		return parts[0]
	}
	return fmt.Sprintf("%s", joinStrings(parts))
}

// ===== Vertex AI Backend =====

// VertexAIConfig holds configuration for the Vertex AI backend.
type VertexAIConfig struct {
	ProjectID string // GCP project ID
	Region    string // GCP region (e.g., "us-central1")
	ModelID   string // Model identifier (e.g., "gemini-2.0-flash")
}

// VertexAIBackend implements APIBackend using Google Cloud Vertex AI REST API.
type VertexAIBackend struct {
	projectID string
	region    string
	modelID   string
}

// NewVertexAI creates a new VertexAIBackend with the given configuration.
func NewVertexAI(cfg VertexAIConfig) *VertexAIBackend {
	return &VertexAIBackend{
		projectID: cfg.ProjectID,
		region:    cfg.Region,
		modelID:   cfg.ModelID,
	}
}

func (v *VertexAIBackend) Name() string { return "vertexai" }

// Available checks if Application Default Credentials (ADC) can be found.
func (v *VertexAIBackend) Available() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := google.FindDefaultCredentials(ctx, "https://www.googleapis.com/auth/cloud-platform")
	return err == nil
}

// Invoke sends a request to the Vertex AI generateContent REST endpoint.
func (v *VertexAIBackend) Invoke(ctx context.Context, req Request) (*Response, error) {
	// Get ADC token
	creds, err := google.FindDefaultCredentials(ctx, "https://www.googleapis.com/auth/cloud-platform")
	if err != nil {
		return nil, fmt.Errorf("vertexai: failed to find default credentials: %w", err)
	}

	token, err := creds.TokenSource.Token()
	if err != nil {
		return nil, fmt.Errorf("vertexai: failed to get access token: %w", err)
	}

	// Build request body
	body := v.buildRequestBody(req)
	bodyJSON, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("vertexai: failed to marshal request body: %w", err)
	}

	// Build endpoint URL
	endpoint := fmt.Sprintf(
		"https://%s-aiplatform.googleapis.com/v1/projects/%s/locations/%s/publishers/google/models/%s:generateContent",
		v.region, v.projectID, v.region, v.modelID,
	)

	// Create HTTP request
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bodyJSON))
	if err != nil {
		return nil, fmt.Errorf("vertexai: failed to create HTTP request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+token.AccessToken)
	httpReq.Header.Set("Content-Type", "application/json")

	start := time.Now()
	resp, err := http.DefaultClient.Do(httpReq)
	latency := time.Since(start)

	if err != nil {
		return nil, fmt.Errorf("vertexai: HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("vertexai: failed to read response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("vertexai: API returned status %d: %s", resp.StatusCode, string(respBody))
	}

	// Parse response
	return v.parseResponse(respBody, latency)
}

// vertexRequest represents the Vertex AI generateContent request payload.
type vertexRequest struct {
	Contents         []vertexContent         `json:"contents"`
	GenerationConfig *vertexGenerationConfig `json:"generationConfig,omitempty"`
}

type vertexContent struct {
	Role  string       `json:"role"`
	Parts []vertexPart `json:"parts"`
}

type vertexPart struct {
	Text string `json:"text"`
}

type vertexGenerationConfig struct {
	MaxOutputTokens int     `json:"maxOutputTokens,omitempty"`
	Temperature     float64 `json:"temperature,omitempty"`
}

// vertexResponse represents the Vertex AI generateContent response.
type vertexResponse struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
	UsageMetadata struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
	} `json:"usageMetadata"`
}

// buildRequestBody constructs the Vertex AI request payload.
func (v *VertexAIBackend) buildRequestBody(req Request) vertexRequest {
	vReq := vertexRequest{}

	// Build contents from messages or prompt
	if len(req.Messages) > 0 {
		for _, m := range req.Messages {
			vReq.Contents = append(vReq.Contents, vertexContent{
				Role:  vertexRole(m.Role),
				Parts: []vertexPart{{Text: m.Content}},
			})
		}
	} else if req.Prompt != "" {
		vReq.Contents = append(vReq.Contents, vertexContent{
			Role:  "user",
			Parts: []vertexPart{{Text: req.Prompt}},
		})
	}

	// Generation config
	if req.MaxTokens > 0 || req.Temperature > 0 {
		vReq.GenerationConfig = &vertexGenerationConfig{
			MaxOutputTokens: req.MaxTokens,
			Temperature:     req.Temperature,
		}
	}

	return vReq
}

// vertexRole maps our role names to Vertex AI role names.
func vertexRole(role string) string {
	switch role {
	case "assistant":
		return "model"
	default:
		return role // "user" stays "user"
	}
}

// parseResponse parses the Vertex AI JSON response into our Response struct.
func (v *VertexAIBackend) parseResponse(body []byte, latency time.Duration) (*Response, error) {
	var vResp vertexResponse
	if err := json.Unmarshal(body, &vResp); err != nil {
		return nil, fmt.Errorf("vertexai: failed to parse response JSON: %w", err)
	}

	// Extract text content
	var content string
	var stopReason string
	if len(vResp.Candidates) > 0 {
		candidate := vResp.Candidates[0]
		stopReason = candidate.FinishReason
		var parts []string
		for _, p := range candidate.Content.Parts {
			parts = append(parts, p.Text)
		}
		content = joinStrings(parts)
	}

	return &Response{
		Content:      content,
		InputTokens:  vResp.UsageMetadata.PromptTokenCount,
		OutputTokens: vResp.UsageMetadata.CandidatesTokenCount,
		Model:        v.modelID,
		StopReason:   stopReason,
		Latency:      latency,
	}, nil
}

// ===== Helpers =====

// joinStrings joins string slices with no separator (text concatenation).
func joinStrings(parts []string) string {
	var buf bytes.Buffer
	for _, p := range parts {
		buf.WriteString(p)
	}
	return buf.String()
}
