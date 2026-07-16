package apibackend

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/gordonwei/orch/pkg/session"
	"golang.org/x/oauth2/google"
)

// Ensure both backends implement StreamingBackend at compile time.
var (
	_ session.StreamingBackend = (*BedrockBackend)(nil)
	_ session.StreamingBackend = (*VertexAIBackend)(nil)
)

// InvokeStream calls Bedrock ConverseStream API and returns chunks via channel.
func (b *BedrockBackend) InvokeStream(ctx context.Context, req session.StreamRequest) (<-chan session.StreamChunk, error) {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(b.region))
	if err != nil {
		return nil, fmt.Errorf("bedrock: failed to load AWS config: %w", err)
	}

	client := bedrockruntime.NewFromConfig(cfg)

	// Build messages
	var messages []types.Message
	for _, m := range req.Messages {
		messages = append(messages, types.Message{
			Role: types.ConversationRole(m.Role),
			Content: []types.ContentBlock{
				&types.ContentBlockMemberText{Value: m.Content},
			},
		})
	}

	if len(messages) == 0 {
		return nil, fmt.Errorf("bedrock: stream request must contain at least one message")
	}

	// Build input
	input := &bedrockruntime.ConverseStreamInput{
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

	output, err := client.ConverseStream(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("bedrock: ConverseStream API call failed: %w", err)
	}

	ch := make(chan session.StreamChunk, 64)

	go func() {
		defer close(ch)
		stream := output.GetStream()
		defer stream.Close()

		for event := range stream.Events() {
			switch v := event.(type) {
			case *types.ConverseStreamOutputMemberContentBlockDelta:
				if delta, ok := v.Value.Delta.(*types.ContentBlockDeltaMemberText); ok {
					ch <- session.StreamChunk{Text: delta.Value}
				}
			case *types.ConverseStreamOutputMemberMessageStop:
				ch <- session.StreamChunk{Done: true}
				return
			}
		}

		if err := stream.Err(); err != nil {
			ch <- session.StreamChunk{Error: err}
		} else {
			ch <- session.StreamChunk{Done: true}
		}
	}()

	return ch, nil
}

// InvokeStream calls Vertex AI generateContent with streaming (SSE) and returns chunks.
func (v *VertexAIBackend) InvokeStream(ctx context.Context, req session.StreamRequest) (<-chan session.StreamChunk, error) {
	creds, err := google.FindDefaultCredentials(ctx, "https://www.googleapis.com/auth/cloud-platform")
	if err != nil {
		return nil, fmt.Errorf("vertexai: failed to find default credentials: %w", err)
	}

	token, err := creds.TokenSource.Token()
	if err != nil {
		return nil, fmt.Errorf("vertexai: failed to get access token: %w", err)
	}

	// Build request body
	vReq := vertexRequest{}
	for _, m := range req.Messages {
		vReq.Contents = append(vReq.Contents, vertexContent{
			Role:  vertexRole(m.Role),
			Parts: []vertexPart{{Text: m.Content}},
		})
	}
	if req.MaxTokens > 0 || req.Temperature > 0 {
		vReq.GenerationConfig = &vertexGenerationConfig{
			MaxOutputTokens: req.MaxTokens,
			Temperature:     req.Temperature,
		}
	}

	bodyJSON, err := json.Marshal(vReq)
	if err != nil {
		return nil, fmt.Errorf("vertexai: failed to marshal request: %w", err)
	}

	// Use streamGenerateContent endpoint
	endpoint := fmt.Sprintf(
		"https://%s-aiplatform.googleapis.com/v1/projects/%s/locations/%s/publishers/google/models/%s:streamGenerateContent?alt=sse",
		v.region, v.projectID, v.region, v.modelID,
	)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bodyJSON))
	if err != nil {
		return nil, fmt.Errorf("vertexai: failed to create HTTP request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+token.AccessToken)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("vertexai: HTTP request failed: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		body := make([]byte, 1024)
		n, _ := resp.Body.Read(body)
		resp.Body.Close()
		return nil, fmt.Errorf("vertexai: API returned status %d: %s", resp.StatusCode, string(body[:n]))
	}

	ch := make(chan session.StreamChunk, 64)

	go func() {
		defer close(ch)
		defer resp.Body.Close()

		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := scanner.Text()

			// SSE format: "data: {json}"
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := strings.TrimPrefix(line, "data: ")

			var sseResp vertexResponse
			if err := json.Unmarshal([]byte(data), &sseResp); err != nil {
				continue // skip malformed chunks
			}

			// Extract text from candidates
			if len(sseResp.Candidates) > 0 {
				for _, part := range sseResp.Candidates[0].Content.Parts {
					if part.Text != "" {
						ch <- session.StreamChunk{Text: part.Text}
					}
				}
				if sseResp.Candidates[0].FinishReason != "" && sseResp.Candidates[0].FinishReason != "STOP" {
					// Non-normal stop
					ch <- session.StreamChunk{Done: true}
					return
				}
			}
		}

		if err := scanner.Err(); err != nil {
			ch <- session.StreamChunk{Error: err}
		} else {
			ch <- session.StreamChunk{Done: true}
		}
	}()

	return ch, nil
}
