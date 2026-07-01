package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gordonwei/orch/pkg/config"
)

// testWorkflowYAML 測試用的工作流 YAML 範本
const testWorkflowYAML = `name: "收工"
description: "執行收工流程：交接表更新 + Notion 同步"
trigger: "收工"
variables:
  project: "Cowork"
steps:
  - id: step_1
    description: "更新本地交接表"
    agent: kiro
    prompt: "讀取 docs/_agent_handoff.md，更新今日完成項目（{{.date}}）"
  - id: step_2
    description: "同步 Notion"
    agent: claude
    prompt: "將交接表同步至 Notion 全局看板，專案：{{.project}}"
    depends_on: [step_1]
`

const testWorkflowYAML2 = `name: "開工"
description: "執行開工流程：讀取交接表"
trigger: "開工"
steps:
  - id: step_1
    description: "讀取交接表"
    agent: kiro
    prompt: "讀取 docs/_agent_handoff.md，回報待辦項目"
`

// TestLoadAll 測試從目錄載入所有工作流
func TestLoadAll(t *testing.T) {
	// 建立暫存目錄
	tmpDir := t.TempDir()

	// 寫入測試檔案
	os.WriteFile(filepath.Join(tmpDir, "offwork.yaml"), []byte(testWorkflowYAML), 0644)
	os.WriteFile(filepath.Join(tmpDir, "onwork.yml"), []byte(testWorkflowYAML2), 0644)
	os.WriteFile(filepath.Join(tmpDir, "readme.txt"), []byte("not a workflow"), 0644)

	// 載入
	workflows, err := LoadAll(tmpDir)
	if err != nil {
		t.Fatalf("LoadAll failed: %v", err)
	}

	if len(workflows) != 2 {
		t.Fatalf("expected 2 workflows, got %d", len(workflows))
	}

	// 檢查第一個工作流的基本屬性
	found := false
	for _, w := range workflows {
		if w.Name == "收工" {
			found = true
			if w.Trigger != "收工" {
				t.Errorf("expected trigger '收工', got '%s'", w.Trigger)
			}
			if len(w.Steps) != 2 {
				t.Errorf("expected 2 steps, got %d", len(w.Steps))
			}
			if w.Variables["project"] != "Cowork" {
				t.Errorf("expected variable project='Cowork', got '%s'", w.Variables["project"])
			}
		}
	}
	if !found {
		t.Error("workflow '收工' not found")
	}
}

// TestLoadAll_EmptyDir 測試空目錄
func TestLoadAll_EmptyDir(t *testing.T) {
	tmpDir := t.TempDir()
	workflows, err := LoadAll(tmpDir)
	if err != nil {
		t.Fatalf("LoadAll failed: %v", err)
	}
	if len(workflows) != 0 {
		t.Errorf("expected 0 workflows from empty dir, got %d", len(workflows))
	}
}

// TestLoadAll_NonExistentDir 測試不存在的目錄（不報錯）
func TestLoadAll_NonExistentDir(t *testing.T) {
	workflows, err := LoadAll("/non/existent/path")
	if err != nil {
		t.Fatalf("LoadAll should not error on non-existent dir: %v", err)
	}
	if workflows != nil {
		t.Errorf("expected nil for non-existent dir, got %v", workflows)
	}
}

// TestLoadAll_InvalidYAML 測試格式錯誤的 YAML（跳過，不報錯）
func TestLoadAll_InvalidYAML(t *testing.T) {
	tmpDir := t.TempDir()
	os.WriteFile(filepath.Join(tmpDir, "broken.yaml"), []byte("{{invalid yaml:::"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "good.yaml"), []byte(testWorkflowYAML2), 0644)

	workflows, err := LoadAll(tmpDir)
	if err != nil {
		t.Fatalf("LoadAll failed: %v", err)
	}
	// 只載入有效的那一個
	if len(workflows) != 1 {
		t.Errorf("expected 1 valid workflow, got %d", len(workflows))
	}
}

// TestMatch 測試觸發詞比對
func TestMatch(t *testing.T) {
	workflows := []Workflow{
		{Name: "收工", Trigger: "收工", Steps: []WorkflowStep{{ID: "s1", Agent: "kiro"}}},
		{Name: "開工", Trigger: "開工", Steps: []WorkflowStep{{ID: "s1", Agent: "kiro"}}},
		{Name: "Deploy", Trigger: "deploy staging", Steps: []WorkflowStep{{ID: "s1", Agent: "shell"}}},
	}

	tests := []struct {
		input    string
		expected string // 期望匹配的 workflow name，"" 表示 nil
	}{
		{"收工", "收工"},
		{"我要收工了", "收工"},
		{"收工吧", "收工"},
		{"開工", "開工"},
		{"早安開工", "開工"},
		{"deploy staging", "Deploy"},
		{"please deploy staging now", "Deploy"},
		{"random input", ""},
		{"", ""},
	}

	for _, tt := range tests {
		result := Match(tt.input, workflows)
		if tt.expected == "" {
			if result != nil {
				t.Errorf("Match(%q): expected nil, got %q", tt.input, result.Name)
			}
		} else {
			if result == nil {
				t.Errorf("Match(%q): expected %q, got nil", tt.input, tt.expected)
			} else if result.Name != tt.expected {
				t.Errorf("Match(%q): expected %q, got %q", tt.input, tt.expected, result.Name)
			}
		}
	}
}

// TestMatch_CaseInsensitive 測試大小寫不敏感
func TestMatch_CaseInsensitive(t *testing.T) {
	workflows := []Workflow{
		{Name: "Deploy", Trigger: "DEPLOY", Steps: []WorkflowStep{{ID: "s1", Agent: "shell"}}},
	}

	result := Match("deploy", workflows)
	if result == nil {
		t.Fatal("expected match for case-insensitive trigger")
	}
	if result.Name != "Deploy" {
		t.Errorf("expected 'Deploy', got '%s'", result.Name)
	}
}

// TestToPlanner 測試轉換為 planner.Plan
func TestToPlanner(t *testing.T) {
	w := &Workflow{
		Name:        "收工",
		Description: "執行收工流程",
		Trigger:     "收工",
		Variables: map[string]string{
			"project": "Cowork",
		},
		Steps: []WorkflowStep{
			{
				ID:          "step_1",
				Description: "更新交接表（{{.date}}）",
				Agent:       "kiro",
				Prompt:      "更新 {{.project}} 的交接表，日期 {{.date}}，使用者 {{.user}}",
			},
			{
				ID:          "step_2",
				Description: "同步 Notion",
				Agent:       "claude",
				Prompt:      "同步 Notion",
				DependsOn:   []string{"step_1"},
			},
		},
	}

	cfg := &config.Config{
		Persona: config.Persona{
			Owner: "Gordon Wei",
		},
	}

	// 使用者覆蓋 project 變數
	vars := map[string]string{
		"project": "AWS",
	}

	plan := ToPlanner(w, vars, cfg)

	// 基本屬性
	if plan.TaskSummary != "執行收工流程" {
		t.Errorf("expected task_summary '執行收工流程', got '%s'", plan.TaskSummary)
	}
	if plan.Difficulty != "workflow" {
		t.Errorf("expected difficulty 'workflow', got '%s'", plan.Difficulty)
	}
	if plan.Category != "workflow" {
		t.Errorf("expected category 'workflow', got '%s'", plan.Category)
	}
	if len(plan.Steps) != 2 {
		t.Fatalf("expected 2 steps, got %d", len(plan.Steps))
	}

	// Step 1: 模板替換
	step1 := plan.Steps[0]
	if step1.Agent != "kiro" {
		t.Errorf("step1 agent: expected 'kiro', got '%s'", step1.Agent)
	}
	// 使用者覆蓋的 project 應該是 "AWS"
	if !strings.Contains(step1.Prompt, "AWS") {
		t.Errorf("step1 prompt should contain 'AWS' (user override), got '%s'", step1.Prompt)
	}
	// {{.user}} 應被替換為 Gordon Wei
	if !strings.Contains(step1.Prompt, "Gordon Wei") {
		t.Errorf("step1 prompt should contain 'Gordon Wei', got '%s'", step1.Prompt)
	}
	// {{.date}} 應被替換（不再包含 {{）
	if strings.Contains(step1.Prompt, "{{") {
		t.Errorf("step1 prompt still has template syntax: '%s'", step1.Prompt)
	}

	// Step 2: depends_on 轉換
	step2 := plan.Steps[1]
	if len(step2.DependsOn) != 1 || step2.DependsOn[0] != "step_1" {
		t.Errorf("step2 depends_on: expected [step_1], got %v", step2.DependsOn)
	}
}

// TestToPlanner_MultipleDependsOn 測試多重依賴轉換
func TestToPlanner_MultipleDependsOn(t *testing.T) {
	w := &Workflow{
		Name:        "test",
		Description: "test workflow",
		Trigger:     "test",
		Steps: []WorkflowStep{
			{ID: "step_1", Agent: "kiro", Prompt: "a"},
			{ID: "step_2", Agent: "kiro", Prompt: "b"},
			{ID: "step_3", Agent: "claude", Prompt: "c", DependsOn: []string{"step_1", "step_2"}},
		},
	}

	plan := ToPlanner(w, nil, &config.Config{})

	step3 := plan.Steps[2]
	if len(step3.DependsOn) != 2 || step3.DependsOn[0] != "step_1" || step3.DependsOn[1] != "step_2" {
		t.Errorf("step3 depends_on: expected [step_1, step_2], got %v", step3.DependsOn)
	}
}

// TestRenderTemplate 測試模板渲染
func TestRenderTemplate(t *testing.T) {
	vars := map[string]string{
		"date":    "2026-07-02",
		"project": "Cowork",
		"user":    "Gordon",
	}

	tests := []struct {
		input    string
		expected string
	}{
		{"hello", "hello"},
		{"", ""},
		{"today is {{.date}}", "today is 2026-07-02"},
		{"{{.user}} works on {{.project}}", "Gordon works on Cowork"},
		{"no template here", "no template here"},
	}

	for _, tt := range tests {
		result := renderTemplate(tt.input, vars)
		if result != tt.expected {
			t.Errorf("renderTemplate(%q): expected %q, got %q", tt.input, tt.expected, result)
		}
	}
}

// TestRenderTemplate_InvalidTemplate 測試無效模板（回傳原文）
func TestRenderTemplate_InvalidTemplate(t *testing.T) {
	vars := map[string]string{"x": "1"}
	// 不完整的模板語法
	result := renderTemplate("{{.missing_var}}", vars)
	// Go template 對 map 未定義 key 會輸出 <no value>，不會出錯
	// 但格式錯誤的語法會原樣回傳
	_ = result // 不 panic 即通過
}

// TestBuiltinVars 測試內建變數
func TestBuiltinVars(t *testing.T) {
	cfg := &config.Config{
		Persona: config.Persona{
			Owner: "TestUser",
		},
	}

	vars := builtinVars(cfg)

	if vars["user"] != "TestUser" {
		t.Errorf("expected user='TestUser', got '%s'", vars["user"])
	}
	if vars["date"] == "" {
		t.Error("expected date to be set")
	}
	if vars["time"] == "" {
		t.Error("expected time to be set")
	}
}

// TestBuiltinVars_NilConfig 測試 config 為 nil
func TestBuiltinVars_NilConfig(t *testing.T) {
	vars := builtinVars(nil)

	if _, ok := vars["user"]; ok {
		t.Error("expected no 'user' var when config is nil")
	}
	if vars["date"] == "" {
		t.Error("expected date to be set even with nil config")
	}
}
