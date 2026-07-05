package planner

import (
	"encoding/json"
	"testing"

	"github.com/gordonwei/orch/pkg/config"
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

// TestClassifyInputType_Command verifies direct CLI invocations are routed as commands,
// including the first-word aliases (k, tf) and the echo/cd entries added alongside the
// Layer 1 knownCLIs map so the two lists stay consistent.
func TestClassifyInputType_Command(t *testing.T) {
	cases := []string{
		"kubectl get pods",
		"k get pods",
		"terraform plan",
		"tf apply",
		"echo hello",
		"cd /tmp",
		"git status",
		"./deploy.sh",
		"sudo systemctl restart nginx",
	}
	for _, in := range cases {
		if got := classifyInputType(in); got != inputTypeCommand {
			t.Errorf("classifyInputType(%q) = %v, want inputTypeCommand", in, got)
		}
	}
}

// TestClassifyInputType_Chat verifies greetings/social chat is detected — both short
// inputs caught by the length heuristic and longer explicit greeting phrases that the
// length heuristic alone would miss.
func TestClassifyInputType_Chat(t *testing.T) {
	cases := []string{
		"你好",
		"嗨",
		"謝謝",
		"再見",
		"hello",
		"thank you",
		"who are you, tell me about yourself", // long, but matches an explicit chat pattern
	}
	for _, in := range cases {
		if got := classifyInputType(in); got != inputTypeChat {
			t.Errorf("classifyInputType(%q) = %v, want inputTypeChat", in, got)
		}
	}
}

// TestClassifyInputType_NaturalLanguage verifies technical requests are never misrouted
// as chat, even when short or containing a greeting-like word — this is the exact
// regression the v0.8.0 "chat detection tightened" changelog entry was meant to fix.
func TestClassifyInputType_NaturalLanguage(t *testing.T) {
	cases := []string{
		"幫我查 S3 bucket",
		"查 GKE pod 狀態",
		"請幫我執行 terraform plan for litellm-gke", // tech keyword mid-sentence, not the first word
		"整理今天三場會議記錄到 Notion",
		"幫我看一下 kubectl pod 狀態", // "kubectl" mid-sentence must still win over any chat pattern
	}
	for _, in := range cases {
		if got := classifyInputType(in); got != inputTypeNaturalLanguage {
			t.Errorf("classifyInputType(%q) = %v, want inputTypeNaturalLanguage", in, got)
		}
	}
}

// TestClassifyInputType_SingleSourceOfTruth guards against a second classifier being
// reintroduced: tryKeywordPlan's chat short-circuit must agree with classifyInputType
// directly, since a prior version had them diverge (looksLikeChat vs classifyInputType
// carried different keyword lists).
func TestClassifyInputType_SingleSourceOfTruth(t *testing.T) {
	p := &Planner{cfg: &config.Config{}}
	cases := map[string]inputType{
		"你好":               inputTypeChat,
		"幫我查 S3 bucket":    inputTypeNaturalLanguage,
		"kubectl get pods": inputTypeCommand,
	}
	for in, want := range cases {
		plan := p.tryKeywordPlan(in)
		got := classifyInputType(in)
		if got != want {
			t.Fatalf("classifyInputType(%q) = %v, want %v (test setup issue)", in, got, want)
		}
		switch want {
		case inputTypeChat:
			if plan == nil || plan.Category != "chat" {
				t.Errorf("tryKeywordPlan(%q): expected chat plan, got %+v", in, plan)
			}
		case inputTypeCommand:
			if plan == nil || plan.Steps[0].Agent != "shell" {
				t.Errorf("tryKeywordPlan(%q): expected shell plan, got %+v", in, plan)
			}
		}
	}
}
