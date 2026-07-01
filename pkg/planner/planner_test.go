package planner

import (
	"encoding/json"
	"testing"
)

// test helper
func unmarshalPlan(raw string, plan *Plan) error {
	return json.Unmarshal([]byte(raw), plan)
}

func TestExtractJSON_Plain(t *testing.T) {
	input := `{"task_summary":"test","difficulty":"simple","category":"query","steps":[]}`
	result := extractJSON(input)
	if result != input {
		t.Errorf("expected plain JSON passthrough, got: %s", result)
	}
}

func TestExtractJSON_MarkdownWrapped(t *testing.T) {
	input := "Here's the plan:\n```json\n{\"task_summary\":\"test\",\"difficulty\":\"simple\",\"category\":\"query\",\"steps\":[]}\n```\nDone."
	result := extractJSON(input)
	expected := `{"task_summary":"test","difficulty":"simple","category":"query","steps":[]}`
	if result != expected {
		t.Errorf("expected %s, got: %s", expected, result)
	}
}

func TestExtractJSON_CodeBlockNoLang(t *testing.T) {
	input := "```\n{\"task_summary\":\"t\",\"difficulty\":\"simple\",\"category\":\"q\",\"steps\":[]}\n```"
	result := extractJSON(input)
	expected := `{"task_summary":"t","difficulty":"simple","category":"q","steps":[]}`
	if result != expected {
		t.Errorf("expected %s, got: %s", expected, result)
	}
}

func TestExtractJSON_WithSurroundingText(t *testing.T) {
	input := "Sure! Here is the plan:\n{\"task_summary\":\"deploy\",\"difficulty\":\"complex\",\"category\":\"infra\",\"steps\":[{\"id\":\"step_1\",\"description\":\"do it\",\"agent\":\"kiro\"}]}\nLet me know!"
	result := extractJSON(input)
	if result == "" {
		t.Fatal("expected JSON extracted, got empty")
	}
	if result[0] != '{' {
		t.Errorf("expected JSON object, got: %c...", result[0])
	}
}

func TestPlanParsing(t *testing.T) {
	raw := `{
		"task_summary": "查 S3 bucket",
		"difficulty": "simple",
		"category": "query",
		"steps": [
			{
				"id": "step_1",
				"description": "list buckets",
				"agent": "aws",
				"command": "aws s3 ls"
			}
		]
	}`

	result := extractJSON(raw)
	if result == "" {
		t.Fatal("extractJSON returned empty")
	}

	// 驗證能正確 unmarshal
	var plan Plan
	err := unmarshalPlan(result, &plan)
	if err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if plan.TaskSummary != "查 S3 bucket" {
		t.Errorf("task_summary = %q, want '查 S3 bucket'", plan.TaskSummary)
	}
	if plan.Difficulty != "simple" {
		t.Errorf("difficulty = %q, want 'simple'", plan.Difficulty)
	}
	if len(plan.Steps) != 1 {
		t.Fatalf("expected 1 step, got %d", len(plan.Steps))
	}
	if plan.Steps[0].Agent != "aws" {
		t.Errorf("step agent = %q, want 'aws'", plan.Steps[0].Agent)
	}
}
