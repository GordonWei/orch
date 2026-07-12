package planner

import (
	"encoding/json"
	"testing"

	"github.com/gordonwei/orch/pkg/backend"
	"github.com/gordonwei/orch/pkg/config"
	"github.com/gordonwei/orch/pkg/registry"
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

// ══════════════════════════════════════════════════════════════════════════════
// New Coverage Tests: keyword plan, fixPlan, truncateRepetition, extractJSON, fixJSON, parseClassification, looksInvalid
// ══════════════════════════════════════════════════════════════════════════════

// TestTryKeywordPlan_CLIDetection verifies that known CLI commands route to agent="shell"
// via GeneratePlan (which calls tryKeywordPlan as Layer 1 — no MLX/cloud needed).
func TestTryKeywordPlan_CLIDetection(t *testing.T) {
	p := newTestPlanner(t)

	cases := []struct {
		input    string
		category string
	}{
		{"kubectl get pods -n default", "infra"},
		{"k get pods", "infra"},
		{"terraform plan", "infra"},
		{"tf apply", "infra"},
		{"helm install nginx bitnami/nginx", "deploy"},
		{"docker ps -a", "infra"},
		{"docker-compose up -d", "infra"},
		{"git status", "code"},
		{"git log --oneline -5", "code"},
		{"ls -la", "query"},
		{"cat /etc/hosts", "query"},
		{"grep -r TODO .", "query"},
		{"find . -name '*.go'", "query"},
		{"aws s3 ls", "query"},
		{"gcloud compute instances list", "query"},
		{"curl -s http://localhost:8080/health", "query"},
		{"ssh user@host", "infra"},
		{"brew install jq", "infra"},
		{"go test ./...", "code"},
		{"npm install express", "code"},
		{"make build", "code"},
		{"ping 8.8.8.8", "query"},
		{"whoami", "query"},
		{"date", "query"},
	}

	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			plan, err := p.GeneratePlan(tc.input)
			if err != nil {
				t.Fatalf("GeneratePlan(%q) error: %v", tc.input, err)
			}
			if plan == nil {
				t.Fatalf("GeneratePlan(%q) returned nil plan", tc.input)
			}
			if len(plan.Steps) == 0 {
				t.Fatalf("GeneratePlan(%q) returned no steps", tc.input)
			}
			if plan.Steps[0].Agent != "shell" {
				t.Errorf("GeneratePlan(%q): agent=%q, want 'shell'", tc.input, plan.Steps[0].Agent)
			}
			if plan.Category != tc.category {
				t.Errorf("GeneratePlan(%q): category=%q, want %q", tc.input, plan.Category, tc.category)
			}
			if plan.Steps[0].Command != tc.input {
				t.Errorf("GeneratePlan(%q): command=%q, want %q", tc.input, plan.Steps[0].Command, tc.input)
			}
		})
	}
}

// TestTryKeywordPlan_ChatDetection verifies that greetings return plan with agent="local", category="chat"
func TestTryKeywordPlan_ChatDetection(t *testing.T) {
	p := newTestPlanner(t)

	cases := []string{
		"你好",
		"嗨",
		"hello",
		"謝謝",
		"再見",
		"hi",
	}

	for _, input := range cases {
		t.Run(input, func(t *testing.T) {
			plan, err := p.GeneratePlan(input)
			if err != nil {
				t.Fatalf("GeneratePlan(%q) error: %v", input, err)
			}
			if plan == nil {
				t.Fatalf("GeneratePlan(%q) returned nil", input)
			}
			if plan.Category != "chat" {
				t.Errorf("GeneratePlan(%q): category=%q, want 'chat'", input, plan.Category)
			}
			if len(plan.Steps) == 0 || plan.Steps[0].Agent != "local" {
				t.Errorf("GeneratePlan(%q): agent=%q, want 'local'", input, plan.Steps[0].Agent)
			}
		})
	}
}

// TestTryKeywordPlan_NoMatch verifies random English sentences return nil from keyword matching
// (they would need MLX/cloud to classify, so GeneratePlan will proceed to Layer 2/3).
// We test via the private method indirectly: if MLX is not running, GeneratePlan falls to cloud,
// so we just verify the plan doesn't get keyword-matched by checking it's NOT category "chat" and agent != "shell".
func TestTryKeywordPlan_NoMatch(t *testing.T) {
	p := &Planner{cfg: &config.Config{}}

	cases := []string{
		"explain the difference between microservices and monolith",
		"write a Python script to parse CSV files",
		"how does garbage collection work in Go",
		"analyze the performance of our API endpoints",
		"what is the capital of France",
	}

	for _, input := range cases {
		t.Run(input, func(t *testing.T) {
			plan := p.tryKeywordPlan(input)
			if plan != nil {
				t.Errorf("tryKeywordPlan(%q) should return nil (no keyword match), got agent=%q category=%q",
					input, plan.Steps[0].Agent, plan.Category)
			}
		})
	}
}

// TestFixPlan_ShellToCloud verifies that when agent="shell" but the command is natural language,
// fixPlan reroutes to "claude".
func TestFixPlan_ShellToCloud(t *testing.T) {
	p := newTestPlanner(t)

	plan := &Plan{
		TaskSummary: "help me check cluster health",
		Difficulty:  "simple",
		Category:    "infra",
		Steps: []Step{
			{
				ID:          "step_1",
				Description: "check cluster health",
				Agent:       "shell",
				Command:     "check the health of my kubernetes cluster",
			},
		},
	}

	fixed := p.fixPlan(plan, "help me check cluster health")
	if fixed.Steps[0].Agent == "shell" {
		t.Errorf("fixPlan should reroute natural language command from shell to cloud, got agent=%q", fixed.Steps[0].Agent)
	}
	if fixed.Steps[0].Command != "" {
		t.Errorf("fixPlan should clear command when rerouting, got command=%q", fixed.Steps[0].Command)
	}
	if fixed.Steps[0].Prompt == "" {
		t.Error("fixPlan should set prompt when rerouting")
	}
}

// TestFixPlan_InvalidCommand verifies that commands with placeholders get rerouted.
func TestFixPlan_InvalidCommand(t *testing.T) {
	p := newTestPlanner(t)

	cases := []struct {
		name    string
		command string
	}{
		{"angle_bracket_region", "aws ec2 describe-instances --region <region>"},
		{"your_prefix", "kubectl get pods -n your-namespace"},
		{"placeholder_project", "gcloud compute instances list --project <project>"},
		{"ellipsis", "terraform plan -var 'name=...'"},
		{"parentheses", "docker run (image name here)"},
		{"example_domain", "curl https://example.com/api"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := &Plan{
				TaskSummary: "deploy",
				Difficulty:  "simple",
				Category:    "infra",
				Steps: []Step{
					{
						ID:      "step_1",
						Agent:   "shell",
						Command: tc.command,
					},
				},
			}

			fixed := p.fixPlan(plan, "deploy the application")
			if fixed.Steps[0].Agent == "shell" {
				t.Errorf("fixPlan should reroute invalid command %q, but agent is still 'shell'", tc.command)
			}
		})
	}
}

// TestFixPlan_SchemaJunk verifies that plans with task_summary="one line" get converted to chat.
func TestFixPlan_SchemaJunk(t *testing.T) {
	p := newTestPlanner(t)

	junkSummaries := []string{"one line", "what to do", "task description", "one line summary"}
	for _, junk := range junkSummaries {
		t.Run(junk, func(t *testing.T) {
			plan := &Plan{
				TaskSummary: junk,
				Difficulty:  "simple",
				Category:    "infra",
				Steps: []Step{
					{
						ID:    "step_1",
						Agent: "kiro",
					},
				},
			}

			fixed := p.fixPlan(plan, "what is kubernetes")
			if fixed.Category != "chat" {
				t.Errorf("fixPlan should convert schema junk %q to chat, got category=%q", junk, fixed.Category)
			}
			if fixed.Steps[0].Agent != "local" {
				t.Errorf("fixPlan should set agent=local for schema junk, got %q", fixed.Steps[0].Agent)
			}
		})
	}
}

// TestTruncateRepetition tests the repetition truncation logic.
func TestTruncateRepetition(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		expected string
	}{
		{
			"no_repetition",
			"This is a normal sentence without any issues.",
			"This is a normal sentence without any issues.",
		},
		{
			"single_word_repeat",
			"Hello world world world world world world",
			"Hello",
		},
		{
			"phrase_repeat_3x",
			"The answer is yes. The answer is yes. The answer is yes. The answer is yes.",
			"The answer is yes.",
		},
		{
			"short_input",
			"hi there",
			"hi there",
		},
		{
			"empty",
			"",
			"",
		},
		{
			"repetition_at_start",
			"ok ok ok ok ok more text",
			"ok ok ok ok ok more text", // cutAt=0 fails the > 0 check; ngram doesn't hit 3x either
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := truncateRepetition(tc.input)
			if result != tc.expected {
				t.Errorf("truncateRepetition(%q) = %q, want %q", tc.input, result, tc.expected)
			}
		})
	}
}

// TestExtractJSON tests JSON extraction from various formats.
func TestExtractJSON_VariousFormats(t *testing.T) {
	cases := []struct {
		name  string
		input string
		valid bool // whether extracted string is valid JSON
	}{
		{
			"bare_json",
			`{"task_summary":"test","steps":[]}`,
			true,
		},
		{
			"markdown_json_block",
			"Here is the plan:\n```json\n{\"task_summary\":\"test\",\"steps\":[]}\n```\nDone!",
			true,
		},
		{
			"markdown_no_lang_block",
			"```\n{\"task_summary\":\"test\",\"steps\":[]}\n```",
			true,
		},
		{
			"garbage_wrapped",
			"Sure! I'll help you.\n\nThe plan is: {\"task_summary\":\"deploy\",\"steps\":[]} Hope this helps!",
			true,
		},
		{
			"nested_json",
			`{"task_summary":"test","steps":[{"id":"step_1","agent":"shell","command":"ls"}]}`,
			true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := extractJSON(tc.input)
			if tc.valid {
				var js json.RawMessage
				if err := json.Unmarshal([]byte(result), &js); err != nil {
					t.Errorf("extractJSON(%q) produced invalid JSON: %v\nresult: %s", tc.name, err, result)
				}
			}
		})
	}
}

// TestFixJSON tests single quotes, trailing commas, unquoted keys.
func TestFixJSON(t *testing.T) {
	cases := []struct {
		name   string
		input  string
		valid  bool
	}{
		{
			"single_quotes",
			"{'task_summary': 'test', 'steps': []}",
			true,
		},
		{
			"trailing_comma_object",
			`{"task_summary": "test", "steps": [],}`,
			true,
		},
		{
			"trailing_comma_array",
			`{"steps": ["a", "b",]}`,
			true,
		},
		{
			"unquoted_keys",
			"{\ntask_summary: \"test\",\nsteps: []\n}",
			true,
		},
		{
			"mixed_issues",
			"{\ntask_summary: 'hello',\nsteps: [],\n}",
			true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := fixJSON(tc.input)
			if tc.valid {
				var js json.RawMessage
				if err := json.Unmarshal([]byte(result), &js); err != nil {
					t.Errorf("fixJSON(%q) produced invalid JSON: %v\nresult: %s", tc.name, err, result)
				}
			}
		})
	}
}

// TestParseClassification tests the parseClassification function.
func TestParseClassification(t *testing.T) {
	cases := []struct {
		input        string
		wantAgent    string
		wantCategory string
	}{
		{"kiro:infra", "kiro", "infra"},
		{"claude:docs", "claude", "docs"},
		{"shell:query", "shell", "query"},
		{"local:chat", "local", "chat"},
		{"gemini:docs", "gemini", "docs"},
		{"KIRO:INFRA", "kiro", "infra"},
		{"`kiro:infra`", "kiro", "infra"},
		{"\"claude:docs\"", "claude", "docs"},
		// Garbage input falls back to local:chat
		{"garbage text no colon", "local", "chat"},
		{"", "local", "chat"},
		{"invalid:invalid", "local", "chat"},
		{"kiro:invalid_cat", "kiro", "chat"},
		{"bad_agent:infra", "local", "infra"},
		// Multi-line (only first line matters)
		{"shell:infra\nsome garbage", "shell", "infra"},
	}

	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			agent, category := parseClassification(tc.input)
			if agent != tc.wantAgent {
				t.Errorf("parseClassification(%q): agent=%q, want %q", tc.input, agent, tc.wantAgent)
			}
			if category != tc.wantCategory {
				t.Errorf("parseClassification(%q): category=%q, want %q", tc.input, category, tc.wantCategory)
			}
		})
	}
}

// TestLooksInvalid tests the looksInvalid function for detecting placeholder/garbage commands.
func TestLooksInvalid(t *testing.T) {
	cases := []struct {
		input string
		want  bool
	}{
		// Invalid cases (should return true)
		{"aws ec2 describe-instances --region <region>", true},
		{"kubectl get pods -n your-namespace", true},
		{"gcloud compute list --project <project>", true},
		{"terraform plan -var '...'", true},
		{"docker run (image name)", true},
		{"curl https://example.com/api", true},
		{"kubectl apply -f <your-file>.yaml", true},
		{"ssh user@placeholder-host", true},
		// Valid cases (should return false)
		{"kubectl get pods -n kube-system", false},
		{"aws s3 ls", false},
		{"terraform plan -out=plan.tf", false},
		{"docker ps -a", false},
		{"git status", false},
		{"ls -la /tmp", false},
	}

	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			got := looksInvalid(tc.input)
			if got != tc.want {
				t.Errorf("looksInvalid(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

// newTestPlanner creates a Planner instance for testing with minimal dependencies.
func newTestPlanner(t *testing.T) *Planner {
	t.Helper()
	cfg := config.Load()
	reg := registry.Scan()
	br := backend.NewRegistry("")
	p := New(reg, cfg, br, nil)
	return p
}
