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

// ══════════════════════════════════════════════════════════════════════════════
// classifyInputType edge cases
// ══════════════════════════════════════════════════════════════════════════════

func TestClassifyInputType_ChineseEnglishMix(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  inputType
	}{
		{"check_s3", "幫我 check S3 bucket", inputTypeNaturalLanguage},
		{"deploy_gke", "deploy 到 GKE 上", inputTypeNaturalLanguage},
		{"terraform_vpc", "用 terraform 建 VPC", inputTypeNaturalLanguage},
		{"fix_bug", "fix 一下那個 bug", inputTypeNaturalLanguage},
		{"monitor_cpu", "monitor CPU 使用率超過 80%", inputTypeNaturalLanguage},
		{"sync_notion", "同步到 Notion 上", inputTypeNaturalLanguage},
		{"helm_upgrade", "幫我 helm upgrade litellm", inputTypeNaturalLanguage},
		{"check_pod", "看一下 pod 的 log", inputTypeNaturalLanguage},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyInputType(tc.input)
			if got != tc.want {
				t.Errorf("classifyInputType(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestClassifyInputType_Emoji(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  inputType
	}{
		{"single_emoji", "👍", inputTypeChat},
		{"emoji_thanks", "🙏", inputTypeChat},
		{"emoji_with_text", "🚀 deploy to prod", inputTypeNaturalLanguage},
		{"emoji_greeting", "😊 hello", inputTypeChat},
		{"emoji_with_tech", "⚡ kubectl get pods", inputTypeNaturalLanguage},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyInputType(tc.input)
			if got != tc.want {
				t.Errorf("classifyInputType(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestClassifyInputType_MultiLine(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  inputType
	}{
		{
			"multiline_command",
			"kubectl get pods\nkubectl get svc",
			inputTypeCommand, // first word is kubectl → command
		},
		{
			"multiline_task",
			"help me with the following:\n1. check S3\n2. deploy to ECS",
			inputTypeNaturalLanguage,
		},
		{
			"multiline_nl",
			"I need to configure\nthe load balancer\nfor our cluster",
			inputTypeNaturalLanguage,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyInputType(tc.input)
			if got != tc.want {
				t.Errorf("classifyInputType(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestClassifyInputType_VeryShort(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  inputType
	}{
		{"single_char_a", "a", inputTypeChat},
		{"single_char_x", "x", inputTypeChat},
		{"two_chars", "ok", inputTypeChat},
		{"three_chars_yes", "yes", inputTypeChat},
		{"three_chars_no", "no", inputTypeChat},
		{"empty_string", "", inputTypeChat},
		{"whitespace", "  ", inputTypeChat},
		{"single_chinese", "好", inputTypeChat},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyInputType(tc.input)
			if got != tc.want {
				t.Errorf("classifyInputType(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestClassifyInputType_VeryLong(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  inputType
	}{
		{
			"long_english",
			"I need help setting up a comprehensive monitoring solution for our production Kubernetes cluster running on GKE. The solution should include Prometheus for metrics collection, Grafana for visualization, and alerting rules for CPU usage, memory pressure, and pod restart counts. We also need to configure PagerDuty integration for on-call notifications and ensure all dashboards are accessible via our internal VPN. Additionally, please set up log aggregation with Loki and create retention policies that comply with our data governance requirements which mandate 90 days of hot storage and 1 year of cold storage in GCS buckets.",
			inputTypeNaturalLanguage,
		},
		{
			"long_chinese",
			"我需要你幫我設計一個完整的 CI/CD pipeline，包含以下步驟：首先從 Gitea repository 拉取最新代碼，然後執行單元測試和整合測試，接著建構 Docker image 並推送到 Harbor registry，最後透過 Helm chart 部署到 RKE2 cluster 的 staging 環境。部署完成後需要自動執行 health check，如果失敗要自動 rollback 到上一個版本。整個流程需要有 Slack 通知，成功和失敗都要通知到 #deploy-alerts channel。",
			inputTypeNaturalLanguage,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyInputType(tc.input)
			if got != tc.want {
				t.Errorf("classifyInputType(%q) = %v, want %v", tc.input[:50]+"...", got, tc.want)
			}
		})
	}
}

func TestClassifyInputType_CodeSnippets(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  inputType
	}{
		{
			"go_function",
			"func main() {\n\tfmt.Println(\"world\")\n\tos.Exit(0)\n}",
			inputTypeNaturalLanguage, // long content, no chat keywords
		},
		{
			"yaml_snippet",
			"apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: nginx",
			inputTypeNaturalLanguage, // multi-word, contains namespace/deploy keywords
		},
		{
			"json_snippet",
			`{"name": "test", "version": "1.0.0", "dependencies": {"express": "^4.18.0"}}`,
			inputTypeNaturalLanguage, // long enough
		},
		{
			"python_import",
			"import boto3\ns3 = boto3.client('s3')",
			inputTypeNaturalLanguage, // contains "s3"
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyInputType(tc.input)
			if got != tc.want {
				t.Errorf("classifyInputType(%q) = %v, want %v", tc.input[:30]+"...", got, tc.want)
			}
		})
	}
}

func TestClassifyInputType_Ambiguous(t *testing.T) {
	// These are edge cases that are difficult to classify definitively.
	// We test that they DON'T crash and produce a reasonable result.
	cases := []struct {
		name      string
		input     string
		wantOneOf []inputType
	}{
		{
			"question_mark_only",
			"?",
			[]inputType{inputTypeChat},
		},
		{
			"help_standalone",
			"help",
			[]inputType{inputTypeNaturalLanguage, inputTypeChat},
		},
		{
			"numbers_only",
			"12345",
			[]inputType{inputTypeChat}, // short, no words
		},
		{
			"url_only",
			"https://github.com/gordonwei/orch",
			[]inputType{inputTypeNaturalLanguage, inputTypeChat}, // could be either
		},
		{
			"mixed_punctuation",
			"... ok then ...",
			[]inputType{inputTypeChat, inputTypeNaturalLanguage},
		},
		{
			"just_a_path",
			"/var/log/nginx/access.log",
			[]inputType{inputTypeCommand}, // starts with /
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyInputType(tc.input)
			valid := false
			for _, want := range tc.wantOneOf {
				if got == want {
					valid = true
					break
				}
			}
			if !valid {
				t.Errorf("classifyInputType(%q) = %v, want one of %v", tc.input, got, tc.wantOneOf)
			}
		})
	}
}
