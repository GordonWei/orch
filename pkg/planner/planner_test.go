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
		"task_summary": "check S3 bucket",
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

	// Verify correct unmarshal
	var plan Plan
	err := unmarshalPlan(result, &plan)
	if err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if plan.TaskSummary != "check S3 bucket" {
		t.Errorf("task_summary = %q, want 'check S3 bucket'", plan.TaskSummary)
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

// TestStepDependsOn_StringCompat verifies old format "depends_on": "step_1" correctly deserializes to []string.
func TestStepDependsOn_StringCompat(t *testing.T) {
	raw := `{
		"task_summary": "compat test",
		"difficulty": "simple",
		"category": "infra",
		"steps": [
			{"id": "step_1", "description": "first", "agent": "shell", "command": "echo 1"},
			{"id": "step_2", "description": "second", "agent": "shell", "command": "echo 2", "depends_on": "step_1"}
		]
	}`

	var plan Plan
	if err := json.Unmarshal([]byte(raw), &plan); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if len(plan.Steps) != 2 {
		t.Fatalf("expected 2 steps, got %d", len(plan.Steps))
	}

	// step_1 should have no dependencies
	if len(plan.Steps[0].DependsOn) != 0 {
		t.Errorf("step_1 DependsOn should be empty, got %v", plan.Steps[0].DependsOn)
	}

	// step_2 has old string format depends_on, should be converted to []string{"step_1"}
	if len(plan.Steps[1].DependsOn) != 1 || plan.Steps[1].DependsOn[0] != "step_1" {
		t.Errorf("step_2 DependsOn: expected [step_1], got %v", plan.Steps[1].DependsOn)
	}
}

// TestStepDependsOn_ArrayFormat verifies new format "depends_on": ["step_1", "step_2"] works correctly.
func TestStepDependsOn_ArrayFormat(t *testing.T) {
	raw := `{
		"task_summary": "array test",
		"difficulty": "moderate",
		"category": "infra",
		"steps": [
			{"id": "a", "agent": "shell", "command": "echo a"},
			{"id": "b", "agent": "shell", "command": "echo b"},
			{"id": "c", "agent": "shell", "command": "echo c", "depends_on": ["a", "b"]}
		]
	}`

	var plan Plan
	if err := json.Unmarshal([]byte(raw), &plan); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	step := plan.Steps[2]
	if len(step.DependsOn) != 2 {
		t.Fatalf("step c DependsOn length: expected 2, got %d", len(step.DependsOn))
	}
	if step.DependsOn[0] != "a" || step.DependsOn[1] != "b" {
		t.Errorf("step c DependsOn: expected [a, b], got %v", step.DependsOn)
	}
}

// TestStepDependsOn_NullAndEmpty verifies null and empty string are handled correctly.
func TestStepDependsOn_NullAndEmpty(t *testing.T) {
	raw := `{
		"task_summary": "null test",
		"steps": [
			{"id": "a", "agent": "shell", "command": "echo a", "depends_on": null},
			{"id": "b", "agent": "shell", "command": "echo b", "depends_on": ""},
			{"id": "c", "agent": "shell", "command": "echo c", "depends_on": []}
		]
	}`

	var plan Plan
	if err := json.Unmarshal([]byte(raw), &plan); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	for _, step := range plan.Steps {
		if len(step.DependsOn) != 0 {
			t.Errorf("step %s: expected empty DependsOn, got %v", step.ID, step.DependsOn)
		}
	}
}
